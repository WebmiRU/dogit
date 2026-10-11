<script setup lang="ts">
/** Artifact catalog for every image-producing job, including queued and failed builds. */
import { watchEvents } from '~/composables/useEvents'
import { formatDuration, statusClass, statusText } from '~/types/pipeline'

interface BuildArtifact {
  job_id: number
  pipeline_id: number
  pipeline_iid: number
  job_iid: number
  job_name: string
  stage: string
  status: string
  queue_priority: number
  project_path: string
  ref: string
  sha: string
  build: Record<string, unknown>
  created_at: string
  started_at?: string
  finished_at?: string
  duration_ms: number
  error?: string
}

interface ArtifactPage {
  artifacts: BuildArtifact[]
  total: number
  page: number
  pages: number
  per_page: number
}

const props = defineProps<{
  projectId?: string
  projectPath: string
}>()

const apiRef = computed(() => props.projectId || encodeURIComponent(props.projectPath))
const artifacts = ref<BuildArtifact[]>([])
const total = ref(0)
const page = ref(1)
const pages = ref(1)
const perPage = 30
const loading = ref(true)
const error = ref('')
const selectedJobId = ref<number | null>(null)
const logText = ref('')
const consumedLogLength = ref(0)
const logLoading = ref(false)
let selectedLogRequest = 0

const selected = computed(() =>
  artifacts.value.find(one => one.job_id === selectedJobId.value) ?? null,
)

function buildString(one: BuildArtifact, key: string): string {
  const value = one.build?.[key]
  return typeof value === 'string' ? value : ''
}

function imageReference(one: BuildArtifact): string {
  const pushed = buildString(one, 'reference')
  if (pushed) return pushed
  const image = buildString(one, 'image')
  const tag = buildString(one, 'tag')
  return tag ? `${image}:${tag}` : (image || 'Image build')
}

function displayStatus(status: string): string {
  return statusText[status as keyof typeof statusText] ?? status
}

function age(value: string): string {
  const at = new Date(value).getTime()
  if (!Number.isFinite(at)) return value
  const seconds = Math.max(0, Math.floor((Date.now() - at) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

async function loadArtifacts() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<ArtifactPage>(
      `/projects/${apiRef.value}/artifacts?page=${page.value}&per_page=${perPage}`,
    )
    artifacts.value = answer.artifacts ?? []
    total.value = answer.total ?? 0
    pages.value = Math.max(1, answer.pages ?? 1)
    if (page.value > pages.value) {
      page.value = pages.value
      await loadArtifacts()
      return
    }
    if (!artifacts.value.some(one => one.job_id === selectedJobId.value)) {
      selectedJobId.value = artifacts.value[0]?.job_id ?? null
    }
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'Could not load artifacts'
  } finally {
    loading.value = false
  }
}

async function loadSelectedLog() {
  const requestID = ++selectedLogRequest
  const artifact = selected.value
  if (!artifact) {
    logText.value = ''
    consumedLogLength.value = 0
    return
  }
  logLoading.value = true
  try {
    const response = await fetch(
      rawApiUrl(
        `/projects/${apiRef.value}/pipelines/${artifact.pipeline_iid}/jobs/${artifact.job_iid}/log`,
      ),
      { credentials: 'include' },
    )
    if (!response.ok) return
    const whole = await response.text()
    // A response for a previously selected job must not overwrite the current log.
    if (requestID !== selectedLogRequest || selectedJobId.value !== artifact.job_id) return
    if (whole.length < consumedLogLength.value) {
      // A new attempt can replace the old log with a shorter one.
      logText.value = whole
      consumedLogLength.value = whole.length
    } else if (whole.length > consumedLogLength.value) {
      logText.value += whole.slice(consumedLogLength.value)
      consumedLogLength.value = whole.length
    }
  } catch {
    // A transient network problem is corrected by the next socket event or reconnect.
  } finally {
    logLoading.value = false
  }
}

watch(selectedJobId, async () => {
  logText.value = ''
  consumedLogLength.value = 0
  await loadSelectedLog()
})

const stopStateEvents = watchEvents({
  project: () => props.projectPath,
  kinds: ['job.created', 'job.updated', 'pipeline.created', 'pipeline.updated'],
  onChange: () => { void loadArtifacts() },
  onReconnect: () => {
    void loadArtifacts()
    void loadSelectedLog()
  },
})
const stopLogEvents = watchEvents({
  project: () => props.projectPath,
  kinds: ['job.log'],
  onEvent: (event) => {
    if (Number(event.payload?.job_id ?? 0) === selectedJobId.value) {
      void loadSelectedLog()
    }
  },
  onReconnect: () => { void loadSelectedLog() },
})

onMounted(() => { void loadArtifacts() })
onBeforeUnmount(() => {
  stopStateEvents()
  stopLogEvents()
})

function previousPage() {
  if (page.value <= 1) return
  page.value--
  void loadArtifacts()
}
function nextPage() {
  if (page.value >= pages.value) return
  page.value++
  void loadArtifacts()
}
</script>

<template>
  <section class="project-artifacts">
    <header class="project-artifacts-head">
      <div>
        <h2>Artifacts</h2>
        <p class="muted">Images produced by this project's pipelines, including work waiting in the low-priority queue.</p>
      </div>
      <span class="artifact-total">{{ total }} artifact{{ total === 1 ? '' : 's' }}</span>
    </header>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading && artifacts.length === 0" class="spinner">Loading artifacts…</div>
    <div v-else-if="artifacts.length === 0" class="card empty artifact-empty">
      <h3>No artifacts yet</h3>
      <p>When a pipeline has a job with a <code>build:</code> definition, it appears here as soon as the pipeline is created.</p>
    </div>

    <div v-else class="artifacts-layout" :aria-busy="loading">
      <section class="artifacts-table-wrap">
        <table class="admin-table repository-flat-table artifacts-table">
          <thead>
            <tr>
              <th>Status</th>
              <th>Artifact</th>
              <th>Pipeline</th>
              <th>Digest</th>
              <th>Queue</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="one in artifacts"
              :key="one.job_id"
              :class="{ 'artifact-selected': one.job_id === selectedJobId }"
              tabindex="0"
              @click="selectedJobId = one.job_id"
              @keydown.enter="selectedJobId = one.job_id"
            >
              <td>
                <span class="badge badge-label" :class="statusClass[one.status] ?? 'badge-neutral'">
                  {{ displayStatus(one.status) }}
                </span>
              </td>
              <td class="artifact-ref-cell">
                <strong class="mono">{{ imageReference(one) }}</strong>
                <span class="artifact-subline muted">
                  {{ one.ref }} · {{ one.sha.slice(0, 8) }} · {{ one.job_name }}
                </span>
                <span v-if="one.error" class="artifact-error">{{ one.error }}</span>
              </td>
              <td>
                <NuxtLink
                  :to="`/p/${projectPath}/-/pipelines/${one.pipeline_iid}`"
                  class="artifact-pipeline-link"
                  @click.stop
                >#{{ one.pipeline_iid }}</NuxtLink>
              </td>
              <td class="mono artifact-digest">
                {{ buildString(one, 'digest') ? buildString(one, 'digest').slice(0, 19) + '…' : '—' }}
              </td>
              <td>
                <span v-if="one.queue_priority < 100" class="artifact-queue-low">Low priority</span>
                <span v-else-if="one.status === 'pending'" class="muted">Normal priority</span>
                <span v-else class="muted">—</span>
              </td>
              <td class="muted small">{{ age(one.created_at) }}</td>
            </tr>
          </tbody>
        </table>

        <div class="artifact-pagination">
          <span class="muted">Page {{ page }} of {{ pages }}</span>
          <div>
            <button class="btn btn-small" type="button" :disabled="page <= 1" @click="previousPage">Previous</button>
            <button class="btn btn-small" type="button" :disabled="page >= pages" @click="nextPage">Next</button>
          </div>
        </div>
      </section>

      <aside class="card artifact-detail">
        <template v-if="selected">
          <div class="artifact-detail-head">
            <div>
              <h3 class="mono">{{ imageReference(selected) }}</h3>
              <p class="muted small">Pipeline #{{ selected.pipeline_iid }} · job #{{ selected.job_iid }} · {{ selected.stage }}</p>
            </div>
            <span v-if="logLoading" class="muted small">Updating log…</span>
          </div>
          <div class="artifact-detail-meta">
            <div>
              <span class="muted small">Status</span>
              <strong>{{ displayStatus(selected.status) }}</strong>
            </div>
            <div>
              <span class="muted small">Commit</span>
              <strong class="mono">{{ selected.sha }}</strong>
            </div>
            <div>
              <span class="muted small">Digest</span>
              <strong class="mono">{{ buildString(selected, 'digest') || 'Not pushed yet' }}</strong>
            </div>
            <div>
              <span class="muted small">Duration</span>
              <strong>{{ formatDuration(selected.duration_ms) }}</strong>
            </div>
          </div>
          <div class="artifact-log-head">
            <h4>Build log</h4>
            <button class="btn btn-small" type="button" @click="loadSelectedLog">Refresh</button>
          </div>
          <pre class="artifact-log">{{ logText || (selected.status === 'pending' ? 'Waiting for a runner…' : 'No log output yet.') }}</pre>
        </template>
        <div v-else class="empty artifact-detail-empty">
          <h3>Select an artifact</h3>
          <p class="muted">Choose a build from the list to inspect its metadata and live log.</p>
        </div>
      </aside>
    </div>
  </section>
</template>
