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

onMounted(() => {
  void load()
  // Images arrive because something was pushed, which happens on a machine
  // somebody else is sitting at.
  stopWatching = watchEvents({
    kinds: ['pipeline.updated', 'job.updated'],
    project: () => props.projectPath,
    onChange: () => void loadImages(),
  })
})

onBeforeUnmount(() => stopWatching?.())
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
  <div>
    <div v-if="loading" class="spinner">Loading images…</div>

    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <div v-else-if="!answer?.registry" class="card empty">
      {{ reasons[answer?.reason ?? ''] ?? 'This instance has no registry to show images from.' }}
    </div>

    <template v-else>
      <div class="repo-head">
        <div class="title">
          <h2 class="section-title" style="margin: 0">Images</h2>
          <p class="page-subtitle">
            <span v-if="tagCount">{{ tagCount }} tag{{ tagCount === 1 ? '' : 's' }} in
              {{ formatBytes(totalBytes) }}</span>
            <span v-else>Nothing pushed yet.</span>
          </p>
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

      <div v-for="repository in repositories" :key="repository.name" class="card">
        <div class="card-header">
          <span class="mono">{{ repository.name }}</span>
          <span class="badge">{{ formatBytes(repository.size_bytes) }}</span>
        </div>

        <table class="table">
          <thead>
            <tr>
              <th>Tag</th>
              <th>Digest</th>
              <th>Built</th>
              <th>Size</th>
              <th v-if="answer.can_delete" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="tag in repository.tags" :key="tag.name">
              <td class="mono">{{ tag.name }}</td>
              <td class="mono muted small">{{ shortDigest(tag.digest) }}</td>
              <td class="muted small">{{ builtAt(tag) }}</td>
              <td class="small">{{ formatBytes(tag.size_bytes) }}</td>
              <td v-if="answer.can_delete" class="right">
                <button
                  class="btn"
                  type="button"
                  :disabled="busyTag === `${repository.name}:${tag.name}`"
                  @click="removeTag(repository.name, tag.name)"
                >
                  Delete
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<style scoped>
.section-title {
  font-size: 18px;
}

.push-hint {
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: 6px;
  margin-bottom: 14px;
}

.push-hint-label {
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 4px;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.table th {
  text-align: left;
  padding: 8px 14px;
  border-bottom: 1px solid var(--border);
  color: var(--text-muted);
  font-weight: 500;
  font-size: 12px;
}

.table td {
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
}

.table tbody tr:last-child td {
  border-bottom: none;
}

.right {
  text-align: right;
}

.small {
  font-size: 12px;
}
</style>