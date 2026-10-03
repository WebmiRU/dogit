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

  try {
    const access = await api.get<{
      reason: string
      address: string
      token: string
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

    const response = await fetch(`${access.address}/catalog`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${access.token}` },
      body: JSON.stringify({}),
    })
    if (!response.ok) {
      const detail = await response.json().catch(() => null)
      throw new Error(detail?.errors?.[0]?.message ?? `the registry answered ${response.status}`)
    }

    const result = await response.json()
    groups.value = arrange(result.repos ?? [], placement.value)
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
    const access = await api.get<{ address: string; token: string }>('/registry/catalog')
    const response = await fetch(`${access.address}/packages/delete`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${access.token}` },
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
  stopWatching = watchEvents({
    kinds: ['pipeline.updated', 'job.updated'],
    onChange: () => void load(true),
  })
})

onBeforeUnmount(() => stopWatching?.())
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h3 class="section-title" style="margin: 0">Images</h3>
        <p class="page-subtitle">
          Everything this registry holds, by the group that owns it.
        </p>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="unavailable" class="alert alert-warning">{{ unavailable }}</div>

    <div v-if="loading && groups.length === 0" class="spinner">Reading the registry…</div>

    <div v-else-if="!loading && groups.length === 0 && !unavailable" class="card empty">
      Nothing has been pushed. The first image a pipeline builds will appear here.
    </div>

    <div v-else class="summary">
      <span class="badge badge-green">online</span>
      <span>{{ groups.length }} {{ groups.length === 1 ? 'group' : 'groups' }}</span>
      <span>{{ images }} {{ images === 1 ? 'image' : 'images' }}</span>
      <span>{{ repositories }} {{ repositories === 1 ? 'repository' : 'repositories' }}</span>
      <span>{{ tags }} {{ tags === 1 ? 'tag' : 'tags' }}</span>
      <strong>{{ size(groups.reduce((sum, group) => sum + group.size, 0)) }}</strong>
    </div>

    <section v-for="group in groups" :key="group.label" class="group">
      <button class="group-head" type="button" @click="toggle(group.label)">
        <span class="chevron" :class="{ open: !collapsed[group.label] }">▸</span>
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

          <table v-for="repo in project.repositories" :key="repo.repository" class="images">
            <thead>
              <tr>
                <th>Tag</th>
                <th>Digest</th>
                <th>Built</th>
                <th class="numeric">Size</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="tag in repo.tags" :key="tag.name">
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

<style scoped>
.section-title {
  font-size: 16px;
}

.summary {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  padding: 10px 12px;
  margin-bottom: 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 13px;
  color: var(--text-muted);
}

.summary strong {
  color: var(--text);
  font-variant-numeric: tabular-nums;
}

.group {
  margin-bottom: 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
}

.group-head,
.project-head {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 12px;
  background: none;
  border: none;
  color: inherit;
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}

.group-head:hover,
.project-head:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.03));
}

.group-name {
  font-weight: 600;
}

.group-head .spacer,
.project-head .spacer {
  flex: 1;
}

.chevron {
  color: var(--text-muted);
  transition: transform 0.12s ease;
}

.chevron.open {
  transform: rotate(90deg);
}

.group-body {
  padding: 0 12px 10px;
}

.project {
  margin-bottom: 10px;
}

.project-head {
  padding: 6px 0;
  cursor: default;
}

.project-name {
  color: inherit;
  text-decoration: none;
}

.project-name:hover {
  text-decoration: underline;
}

.images {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.images th {
  text-align: left;
  font-weight: 500;
  font-size: 11px;
  color: var(--text-muted);
  padding: 4px 8px;
  border-bottom: 1px solid var(--border);
}

.images td {
  padding: 5px 8px;
  border-bottom: 1px solid var(--border);
}

.tag {
  font-weight: 600;
}

.numeric {
  text-align: right;
}

.small {
  font-size: 12px;
}

.delete {
  padding: 3px 8px;
  font: inherit;
  font-size: 12px;
  color: var(--text-muted);
  background: none;
  border: 1px solid var(--border);
  border-radius: 4px;
  cursor: pointer;
}

.delete:hover:not(:disabled) {
  color: var(--danger, #f85149);
  border-color: currentColor;
}

.delete:disabled {
  opacity: 0.4;
  cursor: default;
}
</style>