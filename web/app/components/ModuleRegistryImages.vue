<script setup lang="ts">
/**
 * Every image this instance's registry holds.
 *
 * The module's own page, because the module is the only one that knows: the core
 * stores digests it passed on and has no idea what any of it weighs. So the core
 * decides who may look — an administrator, since the answer spans every project —
 * and mints a short credential; the page then asks the module at the address it
 * published, the same address anybody pushing an image uses.
 *
 * Images are grouped by the group that owns them, and a project outside any group
 * is a group of one. Disk is the thing an operator opens this page about, so the
 * sizes are at the top of every level and the biggest group is first.
 */

interface CatalogTag {
  name: string
  digest: string
  size_bytes: number
  /** When the image was built, as its own config says. Absent when unreadable. */
  created_at?: string
}

interface CatalogRepository {
  repository: string
  project: string
  size_bytes: number
  tag_count: number
  tags: CatalogTag[]
  /**
   * Which registry holds it, when the instance has more than one.
   *
   * Not decoration: a credential is minted for one module and refused by another, so deleting
   * a tag needs the registry it came from. An image whose registry is not named could only be
   * deleted by trying every registry until one accepted, and the one that accepted would be
   * whichever answered first rather than whichever holds the image.
   */
  registry?: string
}

/** One registry on this instance, and a credential for it. */
interface RegistryAccess {
  name: string
  kind: string
  address: string
  token: string
}

interface CatalogProject {
  path: string
  /** Owning group, as the core named it. */
  group: string
  size: number
  tags: number
  repositories: CatalogRepository[]
}

interface CatalogGroup {
  label: string
  size: number
  tags: number
  repositories: number
  projects: CatalogProject[]
}

const props = defineProps<{ moduleKind: string }>()
const { add: notify } = useNotifyPool()

const reasonText: Record<string, string> = {
  no_registry_module: 'No registry module is installed on this instance.',
  registry_forbidden: 'The registry module has been forbidden by an administrator.',
  registry_not_published: 'The registry module registered without saying where it can be reached.',
}

const groups = ref<CatalogGroup[]>([])
const loading = ref(true)
const error = ref('')
const unavailable = ref('')
/** Registries that could not be reached, named. Empty when all of them answered. */
const unreachable = ref<string[]>([])
/** Names of the registries that answered. More than one changes what the page has to show. */
const registryNames = ref<string[]>([])
const busyTag = ref('')
const collapsed = ref<Record<string, boolean>>({})

/**
 * What is here, counted three ways because they are three different things.
 *
 * A repository is a name in the registry. A tag is a name for one image inside it.
 * An image is one digest — and two tags with different digests are two images that
 * happen to share a repository, which is the normal case: the image name has no
 * room for a tag, so every tag of a project lands in one repository.
 *
 * Calling that one image would be the sort of number that is defensible in a
 * footnote and useless in the sentence it appears in.
 */
const repositories = computed(() => groups.value.reduce((sum, group) => sum + group.repositories, 0))
const tags = computed(() => groups.value.reduce((sum, group) => sum + group.tags, 0))
const images = computed(() => {
  const digests = new Set<string>()
  for (const group of groups.value) {
    for (const project of group.projects) {
      for (const repository of project.repositories) {
        for (const tag of repository.tags) digests.add(tag.digest)
      }
    }
  }
  return digests.size
})

/** Projects, as the core named them, grouped by owner. */
interface Placement {
  projects: { path: string; group: string | null }[]
  groupNames: Record<string, string>
  total: number
}

const placement = ref<Placement>({ projects: [], groupNames: {}, total: 0 })

async function load(quiet = false) {
  // A reload that follows an event leaves the open groups open and the reader where
  // they were: rebuilding everything under the pointer is what makes an
  // automatically-updating page annoying.
  if (!quiet) loading.value = true
  error.value = ''
  unavailable.value = ''
  unreachable.value = []

  try {
    const access = await api.get<{
      reason: string
      registries: RegistryAccess[]
      projects: { path: string; group: string | null }[]
      group_names: Record<string, string>
    }>('/registry/catalog')

    if (access.reason) {
      unavailable.value = reasonText[access.reason] ?? 'The registry is not available.'
      groups.value = []
      return
    }

    placement.value = {
      projects: access.projects ?? [],
      groupNames: access.group_names ?? {},
      total: 0,
    }

    // Every registry, and one that fails does not hide the others.
    //
    // An instance with a registry that is down and one that is fine is an ordinary state, and a
    // page that goes blank because of it tells the operator less than a page that lists what it
    // could reach and names what it could not. The failure is remembered and shown; it is not
    // thrown away.
    const collected: CatalogRepository[] = []
    const failed: string[] = []
    for (const registry of access.registries ?? []) {
      try {
        const response = await fetch(`${registry.address}/catalog`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${registry.token}` },
          body: JSON.stringify({}),
        })
        if (!response.ok) {
          const detail = await response.json().catch(() => null)
          throw new Error(detail?.errors?.[0]?.message ?? `the registry answered ${response.status}`)
        }
        const result = await response.json()
        for (const repo of (result.repos ?? []) as CatalogRepository[]) {
          collected.push({ ...repo, registry: registry.name })
        }
      } catch {
        failed.push(registry.name)
      }
    }

    if (collected.length === 0 && failed.length > 0) {
      throw new Error(`the registry could not be reached: ${failed.join(', ')}`)
    }
    unreachable.value = failed
    registryNames.value = (access.registries ?? []).map((one) => one.name)
    groups.value = arrange(collected, placement.value)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the registry could not be reached'
  } finally {
    loading.value = false
  }
}

/**
 * Arranges images under groups and projects.
 *
 * Done here rather than by the core or the module: the core knows which group a
 * project belongs to, the module knows what images exist, and neither knows the
 * other half. This is the one place that has both.
 */
function arrange(repos: CatalogRepository[], where: Placement): CatalogGroup[] {
  const groupOfProject = new Map(where.projects.map((one) => [one.path, one.group]))

  const byGroup = new Map<string, CatalogGroup>()
  const order: string[] = []

  for (const repo of repos) {
    const groupID = groupOfProject.get(repo.project) ?? null
    // A group, or the project itself when it is not in one. An image the core cannot
    // place gets its own bucket rather than being hidden: something is holding disk
    // that nobody claims.
    const key = groupID ? (where.groupNames[groupID] ?? groupID) : (repo.project || 'unattributed')

    let group = byGroup.get(key)
    if (!group) {
      group = { label: key, size: 0, tags: 0, repositories: 0, projects: [] }
      byGroup.set(key, group)
      order.push(key)
    }
    group.size += repo.size_bytes
    group.tags += repo.tag_count
    group.repositories += 1

    let project = group.projects.find((one) => one.path === repo.project)
    if (!project) {
      project = { path: repo.project, group: groupID ?? '', size: 0, tags: 0, repositories: [] }
      group.projects.push(project)
    }
    project.size += repo.size_bytes
    project.tags += repo.tag_count
    project.repositories.push(repo)
  }

  const result = order.map((key) => byGroup.get(key)!)
  for (const group of result) {
    group.projects.sort((a, b) => (b.size - a.size) || a.path.localeCompare(b.path))
    for (const project of group.projects) {
      project.repositories.sort((a, b) => a.repository.localeCompare(b.repository))
    }
  }
  result.sort((a, b) => (b.size - a.size) || a.label.localeCompare(b.label))
  return result
}

/**
 * Removes one tag.
 *
 * A confirmation, and not a second one: the core already decided this caller may
 * when it minted the credential, and asking twice in front of a permission check is
 * theatre.
 */
async function removeTag(repo: CatalogRepository, tag: CatalogTag) {
  const full = `${repo.repository}:${tag.name}`
  if (!confirm(`Delete ${full}? Anything built from this tag will have to be built again.`)) return

  busyTag.value = full
  error.value = ''
  try {
    // The credential belongs to one registry, so the registry the image came from decides
    // which one to present. With a single registry this is the same call as before; with
    // several, presenting the wrong one is refused by the module that holds the image.
    const access = await api.get<{ registries: RegistryAccess[] }>('/registry/catalog')
    const registry = (access.registries ?? []).find((one) => one.name === repo.registry)
      ?? access.registries?.[0]
    if (!registry) {
      throw new Error('this instance has no registry to delete from')
    }
    const response = await fetch(`${registry.address}/packages/delete`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${registry.token}` },
      body: JSON.stringify({ project: repo.project, repository: repo.repository, tag: tag.name }),
    })
    if (!response.ok) {
      const detail = await response.json().catch(() => null)
      throw new Error(detail?.errors?.[0]?.message ?? `the registry answered ${response.status}`)
    }
    notify(`${full} deleted`, { type: 'success' })
    await load()
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : 'the deletion failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    busyTag.value = ''
  }
}

/** A size as something a person can read at a glance. */
function size(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`
}

/** When the image says it was built, or plainly that it does not say. */
function created(tag: CatalogTag): string {
  if (!tag.created_at) return 'not reported'
  return new Date(tag.created_at).toLocaleString()
}

function toggle(key: string) {
  collapsed.value = { ...collapsed.value, [key]: !collapsed.value[key] }
}

let stopWatching: (() => void) | undefined

onMounted(() => {
  void load()

  // Images appear because something was pushed, which happens on a machine
  // somebody else is sitting at. This page says so by itself rather than waiting to
  // be asked whether anything changed.
  // Both directions are covered by this one feed. A push travels through the core
  // and is announced as a job finishing; a deletion goes straight from the browser
  // to this module, and the module reports it afterwards — it holds its own token,
  // so the core can believe it about itself.
  stopWatching = watchEvents({
    kinds: ['pipeline.updated', 'job.updated', 'module.reported'],
    onChange: () => void load(true),
  })
})

onBeforeUnmount(() => stopWatching?.())
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h3 class="images-section-title" style="margin: 0">Images</h3>
        <p class="page-subtitle">
          Everything this registry holds, by the group that owns it.
        </p>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="unavailable" class="alert alert-warning">{{ unavailable }}</div>
    <div v-else-if="unreachable.length" class="alert alert-warning">
      Not reachable: {{ unreachable.join(', ') }}. What the other registries hold is below.
    </div>

    <div v-if="loading && groups.length === 0" class="spinner">Reading the registry…</div>

    <div v-else-if="!loading && groups.length === 0 && !unavailable" class="card empty">
      Nothing has been pushed. The first image a pipeline builds will appear here.
    </div>

    <div v-else class="images-summary">
      <span class="badge badge-green">Online</span>
      <span v-if="registryNames.length > 1">{{ registryNames.length }} registries</span>
      <span>{{ groups.length }} {{ groups.length === 1 ? 'group' : 'groups' }}</span>
      <span>{{ images }} {{ images === 1 ? 'image' : 'images' }}</span>
      <span>{{ repositories }} {{ repositories === 1 ? 'repository' : 'repositories' }}</span>
      <span>{{ tags }} {{ tags === 1 ? 'tag' : 'tags' }}</span>
      <strong>{{ size(groups.reduce((sum, group) => sum + group.size, 0)) }}</strong>
    </div>

    <section v-for="group in groups" :key="group.label" class="group">
      <button class="group-head" type="button" @click="toggle(group.label)">
        <span class="images-chevron" :class="{ open: !collapsed[group.label] }">▸</span>
        <span class="group-name">{{ group.label }}</span>
        <span class="muted small">
          {{ group.projects.length }} {{ group.projects.length === 1 ? 'project' : 'projects' }}
        </span>
        <div class="spacer" />
        <span class="muted small mono">{{ size(group.size) }}</span>
      </button>

      <div v-if="!collapsed[group.label]" class="group-body">
        <div v-for="project in group.projects" :key="project.path" class="project">
          <div class="project-head">
            <NuxtLink :to="`/p/${project.path}/-/packages`" class="project-name mono">
              {{ project.path }}
            </NuxtLink>
            <span class="muted small">
              {{ project.repositories.length }}
              {{ project.repositories.length === 1 ? 'repository' : 'repositories' }}
              · {{ project.tags }} {{ project.tags === 1 ? 'tag' : 'tags' }}
            </span>
            <div class="spacer" />
            <span class="muted small mono">{{ size(project.size) }}</span>
          </div>

          <table v-for="repo in project.repositories" :key="`${repo.registry}/${repo.repository}`" class="images">
            <thead>
              <tr>
                <th>Repository</th>
                <th>Tag</th>
                <th>Digest</th>
                <th>Built</th>
                <th class="numeric">Size</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="tag in repo.tags" :key="tag.name">
                <td class="mono small">
                  {{ repo.repository }}
                  <span
                    v-if="registryNames.length > 1"
                    class="badge"
                    :title="`Held by the registry module ${repo.registry}`"
                  >{{ repo.registry }}</span>
                </td>
                <td class="tag">{{ tag.name }}</td>
                <td class="mono muted small">{{ tag.digest.slice(0, 19) }}</td>
                <td class="muted small">{{ created(tag) }}</td>
                <td class="numeric mono small">{{ size(tag.size_bytes) }}</td>
                <td class="numeric">
                  <button
                    class="delete"
                    type="button"
                    :disabled="busyTag === `${repo.repository}:${tag.name}`"
                    @click="removeTag(repo, tag)"
                  >Delete</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>
  </div>
</template>

