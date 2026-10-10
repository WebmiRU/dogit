<script setup lang="ts">
/**
 * The project's images.
 *
 * The images are not in the core: a registry module holds them, and the core
 * stores hashes, not layers. So this page asks the core whether the caller may
 * look — that is the part that must not be delegated — and the core hands back a
 * short credential and the address the module published. The listing then comes
 * from the module itself, which is the only place that knows what it holds.
 */
import { timeAgo } from '~/utils/format'

interface RegistryAnswer {
  project: string
  registry: {
    url: string
    dedicated_host: boolean
    kind: string
  } | null
  token?: string
  reason?: string
  can_push?: boolean
  can_delete?: boolean
  expires_in?: number
}

interface ImageTag {
  name: string
  digest: string
  size_bytes: number
  /** When the image says it was built. Absent when the registry will not say. */
  created_at?: string
}

interface ImageRepository {
  name: string
  tags: ImageTag[]
  size_bytes: number
}

const props = defineProps<{
  /** The id the API is asked about: a grouped project's path has a slash in it. */
  projectId?: string
  projectPath: string
}>()

const { add: notify } = useNotifyPool()

/** The project as the API is asked about it: its id when there is one. */
const apiRef = computed(() => props.projectId || encodeURIComponent(props.projectPath))


const answer = ref<RegistryAnswer | null>(null)
const repositories = ref<ImageRepository[]>([])
const totalBytes = ref(0)
const loading = ref(true)
const loadingImages = ref(false)
const error = ref('')
const imageError = ref('')
const busyTag = ref('')

/**
 * The view over the listing: what is being looked for, which way the tags are
 * ordered, which repositories are folded shut and which show all their tags.
 *
 * The registry answers with everything it holds, once. It has no paging and
 * giving it some is a contract change in someone else's code, so the reader's
 * half of paging lives here: a repository draws its ten newest tags and offers
 * the rest, because a repository of three hundred tags that grows to fit them
 * is the same wall as before, only taller.
 */
const filter = ref('')
const sort = ref<'newest' | 'oldest' | 'largest'>('newest')
const folded = ref<Set<string>>(new Set())
const opened = ref<Set<string>>(new Set())

/** How many tags a repository draws before offering the rest. */
const SHOWN = 10

const needle = computed(() => filter.value.trim().toLowerCase())

/** A tag answers the needle on its name or on its digest. */
function tagMatches(tag: ImageTag): boolean {
  if (!needle.value) return true
  return (
    tag.name.toLowerCase().includes(needle.value) ||
    tag.digest.toLowerCase().includes(needle.value)
  )
}

/**
 * The repositories worth drawing, with the tags worth drawing under them.
 *
 * A repository stays when its own name answers, or when a tag under it does —
 * an image is usually looked for by its name, and its repository is looked up
 * second, so a search that only matches repositories finds nothing. Tags the
 * registry will not date sort last rather than first: nobody orders images by
 * a date that is not there.
 */
const shown = computed(() => {
  const found = repositories.value
    .map((repository) => ({
      repository,
      tags: repository.tags.filter(tagMatches),
    }))
    .filter(
      (entry) =>
        !needle.value ||
        entry.repository.name.toLowerCase().includes(needle.value) ||
        entry.tags.length > 0,
    )

  for (const entry of found) {
    entry.tags.sort((a, b) => {
      if (sort.value === 'largest') return b.size_bytes - a.size_bytes
      const when = (tag: ImageTag) =>
        tag.created_at ? Date.parse(tag.created_at) : Number.NaN
      const left = when(a)
      const right = when(b)
      if (Number.isNaN(left) && Number.isNaN(right)) return 0
      if (Number.isNaN(left)) return 1
      if (Number.isNaN(right)) return -1
      return sort.value === 'oldest' ? left - right : right - left
    })
  }
  return found
})

/** What the subtitle counts when the needle is in. */
const foundTags = computed(() =>
  shown.value.reduce((total, entry) => total + entry.tags.length, 0),
)

/** The rows a repository draws: all of them when it is opened or being searched. */
function visibleTags(name: string, tags: ImageTag[]): ImageTag[] {
  if (needle.value || opened.value.has(name)) return tags
  return tags.slice(0, SHOWN)
}

/** A folded repository opens anyway while it has something the needle matched. */
function isFolded(name: string): boolean {
  return folded.value.has(name) && !needle.value
}

function toggleFold(name: string) {
  folded.value = toggleIn(folded.value, name)
}

function toggleOpened(name: string) {
  opened.value = toggleIn(opened.value, name)
}

function toggleIn(set: Set<string>, name: string): Set<string> {
  const next = new Set(set)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  return next
}

/**
 * The one tag whose menu is open, and where its panel goes.
 *
 * The same arrangement as the tags page, for the same reason: the card clips
 * its children, and a panel belonging to the last row of a long table would be
 * cut in half by the card it belongs to. In the body nothing clips it, and
 * because it cannot follow the row a scroll closes it — a menu hanging next to
 * the wrong row is worse than no menu.
 */
const open = ref<{ repository: string; tag: string } | null>(null)
const panel = ref({ top: 0, left: 0, up: false })

const openedTag = computed(() => {
  const current = open.value
  if (!current) return null
  const repository = repositories.value.find((one) => one.name === current.repository)
  const tag = repository?.tags.find((one) => one.name === current.tag)
  return repository && tag ? { repository: repository.name, tag } : null
})

function fullTag(repository: string, tag: string): string {
  return `${repository}:${tag}`
}

function showMenu(repository: string, tag: string, event: MouseEvent) {
  // The same ⋯ twice is a toggle: the second press is a look at the row again,
  // not a second menu.
  if (open.value && open.value.repository === repository && open.value.tag === tag) {
    closeMenu()
    return
  }
  const box = (event.currentTarget as HTMLElement).getBoundingClientRect()
  panel.value = {
    top: box.bottom + 6,
    left: box.right - 190,
    up: window.innerHeight - box.bottom < 150,
  }
  open.value = { repository, tag }
}

function closeMenu() {
  open.value = null
}

// Three ways the open menu stops being the thing at hand: a pointer down
// elsewhere, Escape, and the page moving under it. All three do nothing while
// no menu is open, so they are three idle listeners for the life of the page.
function onPointerAway(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as HTMLElement | null
  if (target?.closest('.row-menu-panel') || target?.closest('.row-menu')) return
  closeMenu()
}

function onKeyAway(event: KeyboardEvent) {
  if (event.key === 'Escape') closeMenu()
}

const settings = ref<Record<string, unknown>>({})

/** What to tell somebody who cannot see the page's subject at all. */
const reasons: Record<string, string> = {
  no_registry_module: 'No registry module is installed on this instance.',
  registry_forbidden: 'The registry module has been forbidden by an administrator.',
  registry_not_published: 'The registry module registered without saying where it can be reached.',
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await api.get<RegistryAnswer>(
      `/projects/${apiRef.value}/packages`,
    )
    answer.value = result

    if (!result.registry || !result.token) {
      repositories.value = []
      return
    }
    await loadImages()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

async function loadImages() {
  const registry = answer.value?.registry
  if (!registry || !answer.value?.token) return

  loadingImages.value = true
  imageError.value = ''
  try {
    // Asked of the module directly: it is the one that knows, and the core has
    // already decided whether this caller is allowed to ask.
    const response = await fetch(`${registry.url}/packages`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${answer.value.token}`,
      },
      body: JSON.stringify({ project: props.projectPath }),
    })

    if (!response.ok) {
      const detail = await response.json().catch(() => null)
      throw new Error(detail?.errors?.[0]?.message ?? `the registry answered ${response.status}`)
    }

    const result = await response.json()
    repositories.value = result.repos ?? []
    totalBytes.value = result.total_bytes ?? 0
  } catch (caught) {
    imageError.value = caught instanceof Error ? caught.message : 'the registry could not be reached'
  } finally {
    loadingImages.value = false
  }
}

/**
 * Removes one tag.
 *
 * A maintainer may, and the question is not asked again: the core already decided
 * it when it minted the credential, and a second confirmation dialog would be
 * theatre in front of a permission check.
 */
async function removeTag(repository: string, tag: string) {
  const registry = answer.value?.registry
  if (!registry || !answer.value?.token) return

  // The menu goes before the confirm: a dialog and the panel it came out of
  // are two things on top of each other, and only one of them is a question.
  closeMenu()

  const full = `${repository}:${tag}`
  if (!confirm(`Delete ${full}? Anything that was built from this tag will need rebuilding.`)) return

  busyTag.value = full
  imageError.value = ''
  try {
    const response = await fetch(`${registry.url}/packages/delete`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${answer.value.token}`,
      },
      body: JSON.stringify({ project: props.projectPath, repository, tag }),
    })

    if (!response.ok) {
      const detail = await response.json().catch(() => null)
      throw new Error(detail?.errors?.[0]?.message ?? `the registry answered ${response.status}`)
    }

    notify(`${full} removed`, { type: 'success' })
    await loadImages()
  } catch (caught) {
    imageError.value = caught instanceof Error ? caught.message : 'the image could not be removed'
  } finally {
    busyTag.value = ''
  }
}

let stopWatching: (() => void) | undefined
let stopWatchingModule: (() => void) | undefined

onMounted(() => {
  void load()
  window.addEventListener('pointerdown', onPointerAway, true)
  window.addEventListener('keydown', onKeyAway)
  window.addEventListener('scroll', closeMenu, true)
  // Both directions. A push travels through the core and is announced by a job
  // finishing; a deletion goes from whichever page asked for it straight to the
  // module, and the module reports it afterwards — it holds its own token, so the
  // core can believe it about itself. This page is open whichever one happened.
  stopWatching = watchEvents({
    kinds: ['pipeline.updated', 'job.updated'],
    project: () => props.projectPath,
    onChange: () => void loadImages(),
  })

  // A second look, without the project filter.
  //
  // A tag deleted from the registry module's own page reaches the core as an
  // instance-wide event: a module is not inside any project, so there is no project
  // to file it under. The filtered feed above cannot see those by design — it is
  // what keeps one project's events off another project's page — so this one is
  // asked for separately and only watches that single kind.
  stopWatchingModule = watchEvents({
    kinds: ['module.reported'],
    onChange: () => void loadImages(),
  })
})

onBeforeUnmount(() => {
  stopWatching?.()
  stopWatchingModule?.()
  window.removeEventListener('pointerdown', onPointerAway, true)
  window.removeEventListener('keydown', onKeyAway)
  window.removeEventListener('scroll', closeMenu, true)
})
watch(() => props.projectPath, load)

const tagCount = computed(() =>
  repositories.value.reduce((total, repository) => total + repository.tags.length, 0),
)

/**
 * The host a client signs in to.
 *
 * Worked out here rather than in the template: the host of the registry address is
 * what `docker login` takes, and the port is part of it whenever one is published —
 * which in development it is.
 */
const registryHost = computed(() => {
  const url = answer.value?.registry?.url
  if (!url) return ''
  try {
    return new URL(url).host
  } catch {
    return url
  }
})

/** Short enough to recognise, long enough to compare. */
/**
 * When the image says it was built.
 *
 * Taken from the image's own config, which is the moment the build ran — not when
 * it was pushed, which the registry does not record. An image whose config cannot
 * be read says so rather than showing a date from nowhere.
 */
function builtAt(tag: ImageTag): string {
  if (!tag.created_at) return 'not reported'
  return new Date(tag.created_at).toLocaleString()
}

function shortDigest(digest: string) {
  return digest.replace(/^sha256:/, '').slice(0, 12)
}
</script>

<template>
  <div class="packages-page">
    <div v-if="loading" class="spinner">Loading images…</div>

    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <div v-else-if="!answer?.registry" class="card empty">
      {{ reasons[answer?.reason ?? ''] ?? 'This instance has no registry to show images from.' }}
    </div>

    <template v-else>
      <div class="repo-head">
        <div class="title">
          <h2 class="packages-section-title" style="margin: 0">Images</h2>
          <p class="page-subtitle">
            <span v-if="tagCount">{{ needle ? `${foundTags} of ${tagCount}` : tagCount }} tag{{
              (needle ? foundTags : tagCount) === 1 ? '' : 's'
            }} in {{ formatBytes(totalBytes) }}</span>
            <span v-else>Nothing pushed yet.</span>
          </p>
        </div>
        <div class="field" style="max-width: 280px; margin: 0">
          <input v-model="filter" type="search" placeholder="Filter tags and digests" />
        </div>
        <div class="field" style="max-width: 170px; margin: 0">
          <select v-model="sort" aria-label="Order tags by">
            <option value="newest">Newest first</option>
            <option value="oldest">Oldest first</option>
            <option value="largest">Largest first</option>
          </select>
        </div>
      </div>

      <div v-if="imageError" class="alert alert-error">{{ imageError }}</div>

      <!-- A push instruction, because knowing the address is the difference
           between using a registry and wondering why nothing is there. -->
      <div v-if="answer.registry" class="push-hint">
        <div class="push-hint-label">Push to</div>
        <code class="mono">{{ answer.registry.url }}/{{ projectPath }}:tag</code>
        <div class="muted small">
          Sign in with
          <code>docker login {{ registryHost }}</code>
          using a dogit account with access to this project.
        </div>
      </div>

      <div v-if="repositories.length === 0" class="card empty">
        This project has no images yet.
        <div v-if="!answer.can_push" class="muted small" style="margin-top: 8px">
          Pushing needs developer rights on this project.
        </div>
      </div>

      <template v-else>
        <div v-for="entry in shown" :key="entry.repository.name" class="packages-repository">
          <div class="packages-repository-header">
            <!-- The header folds the repository: a project with a hundred images
                 is read one repository at a time, and a hundred tables in a
                 column is a page nobody scrolls to the end of. -->
            <button
              class="fold"
              type="button"
              :aria-expanded="!isFolded(entry.repository.name)"
              @click="toggleFold(entry.repository.name)"
            >
              <span class="packages-chevron" :class="{ closed: isFolded(entry.repository.name) }">▾</span>
              <span class="mono">{{ entry.repository.name }}</span>
            </button>
            <span class="muted small">
              {{ entry.tags.length }} tag{{ entry.tags.length === 1 ? '' : 's' }}
            </span>
            <span class="badge">{{ formatBytes(entry.repository.size_bytes) }}</span>
          </div>

          <div v-if="!isFolded(entry.repository.name)" class="packages-table-wrap">
            <table class="admin-table repository-flat-table packages-table">
              <thead>
                <tr>
                  <th>Tag</th>
                  <th>Digest</th>
                  <th>Built</th>
                  <th>Size</th>
                  <th v-if="answer.can_delete" class="right">
                    <span class="visually-hidden">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="tag in visibleTags(entry.repository.name, entry.tags)" :key="tag.name">
                  <td class="mono">{{ tag.name }}</td>
                  <td class="mono muted small">{{ shortDigest(tag.digest) }}</td>
                  <!-- A relative date with the exact one behind it: this column is
                       the one that is sorted, and a full date on every row was
                       the widest thing on the page. -->
                  <td class="muted small">
                    <span v-if="tag.created_at" :title="builtAt(tag)">{{ timeAgo(tag.created_at) }}</span>
                    <span v-else>{{ builtAt(tag) }}</span>
                  </td>
                  <td class="small">{{ formatBytes(tag.size_bytes) }}</td>
                  <td v-if="answer.can_delete" class="right">
                    <button
                      class="row-menu"
                      type="button"
                      aria-haspopup="menu"
                      :aria-expanded="open?.repository === entry.repository.name && open?.tag === tag.name"
                      :aria-label="`Actions for ${fullTag(entry.repository.name, tag.name)}`"
                      @click="showMenu(entry.repository.name, tag.name, $event)"
                    >
                      ⋯
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- The rest of the tags, as one decision rather than one scroll: a
               repository of three hundred tags keeps its first ten until the
               reader says otherwise, and the needle ignores the limit — a
               filtered list is already an answer. -->
          <div
            v-if="!isFolded(entry.repository.name) && !needle && entry.tags.length > SHOWN"
            class="more"
          >
            <button class="btn" type="button" @click="toggleOpened(entry.repository.name)">
              {{
                opened.has(entry.repository.name)
                  ? `Show first ${SHOWN}`
                  : `Show all ${entry.tags.length} tags`
              }}
            </button>
          </div>
        </div>

        <div v-if="shown.length === 0" class="card empty">
          No image matches “{{ filter }}”.
        </div>
      </template>

      <!-- The menu is in the body for the same reason as on the tags page: the
           card clips whatever is inside it. -->
      <Teleport to="#teleports">
        <div
          v-if="openedTag"
          class="row-menu-panel"
          :class="{ up: panel.up }"
          :style="{ top: `${panel.top}px`, left: `${panel.left}px` }"
        >
          <button
            class="row-menu-danger"
            type="button"
            :disabled="busyTag === fullTag(openedTag.repository, openedTag.tag.name)"
            @click="removeTag(openedTag.repository, openedTag.tag.name)"
          >
            {{
              busyTag === fullTag(openedTag.repository, openedTag.tag.name)
                ? 'Deleting…'
                : 'Delete'
            }}
          </button>
        </div>
      </Teleport>
    </template>
  </div>
</template>

