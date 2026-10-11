<script setup lang="ts">
/** Artifact catalog for every image-producing job, including queued and failed builds. */
import { watchEvents } from '~/composables/useEvents'
import { formatDuration, statusClass, statusText } from '~/types/pipeline'
import type { JobStatus } from '~/types/pipeline'

interface BuildArtifact {
  job_id: number
  pipeline_id: number
  pipeline_iid: number
  job_iid: number
  job_name: string
  stage: string
  status: JobStatus
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

interface ArtifactDeployImage {
  name: string
  reference: string
}

interface ArtifactDeployOption {
  index: number
  name: string
  module: string
  target: string
  manifests: string[]
  pre_count: number
  post_count: number
  rollout: boolean
  timeout: string
  images: ArtifactDeployImage[]
  available: boolean
  reason?: string
}

interface ArtifactDeployOptions {
  artifact: {
    job_id: number
    job_name: string
    pipeline_iid: number
    ref: string
    sha: string
    reference: string
    digest: string
  }
  deployments: ArtifactDeployOption[]
}

const props = defineProps<{
  projectId?: string
  projectPath: string
  /** Only users allowed to trigger pipelines are offered the manual deploy action. */
  canTrigger?: boolean
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
const deployPanelOpen = ref(false)
const deployOptionsLoading = ref(false)
const deployOptions = ref<ArtifactDeployOptions | null>(null)
const deployOptionsError = ref('')
const selectedDeploymentIndex = ref<number | null>(null)
const deployingArtifact = ref(false)
const deployActionError = ref('')
let selectedLogRequest = 0
let deployOptionsRequest = 0

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

/** A manual deployment must use an already-pushed, digest-addressed image. */
function isDeployable(one: BuildArtifact | null): boolean {
  return !!one &&
    one.status === 'success' &&
    buildString(one, 'reference') !== '' &&
    buildString(one, 'digest').startsWith('sha256:')
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
    logLoading.value = false
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
    if (requestID === selectedLogRequest) logLoading.value = false
  }
}

watch(selectedJobId, async () => {
  logText.value = ''
  consumedLogLength.value = 0
  deployPanelOpen.value = false
  deployOptions.value = null
  deployOptionsError.value = ''
  deployActionError.value = ''
  selectedDeploymentIndex.value = null
  await loadSelectedLog()
})

async function openDeployPanel() {
  const artifact = selected.value
  if (!props.canTrigger || !isDeployable(artifact) || !artifact) return
  const request = ++deployOptionsRequest
  deployPanelOpen.value = true
  deployOptions.value = null
  deployOptionsError.value = ''
  deployActionError.value = ''
  selectedDeploymentIndex.value = null
  deployOptionsLoading.value = true
  try {
    const answer = await api.get<ArtifactDeployOptions>(
      '/projects/' + apiRef.value + '/artifacts/' + artifact.job_id + '/deploy-options',
    )
    if (request !== deployOptionsRequest || selectedJobId.value !== artifact.job_id) return
    deployOptions.value = answer
    selectedDeploymentIndex.value = answer.deployments.find(option => option.available)?.index ?? null
    if (answer.deployments.length === 0) {
      deployOptionsError.value = 'This commit has no deployment target permitted by .dogit-ci.yml.'
    }
  } catch (caught) {
    if (request !== deployOptionsRequest) return
    deployOptionsError.value = caught instanceof ApiError
      ? caught.message
      : 'Could not load deployment options.'
  } finally {
    if (request === deployOptionsRequest) deployOptionsLoading.value = false
  }
}

function closeDeployPanel() {
  deployPanelOpen.value = false
  deployOptionsError.value = ''
  deployActionError.value = ''
}

async function confirmArtifactDeploy() {
  const artifact = selected.value
  const options = deployOptions.value
  if (!artifact || !options || selectedDeploymentIndex.value == null || deployingArtifact.value) return
  const option = options.deployments.find(one => one.index === selectedDeploymentIndex.value)
  if (!option?.available) return

  deployingArtifact.value = true
  deployActionError.value = ''
  try {
    const answer = await api.post<{ pipeline: { iid: number } }>(
      '/projects/' + apiRef.value + '/artifacts/' + artifact.job_id + '/deploy',
      { deployment_index: option.index },
    )
    await navigateTo('/p/' + props.projectPath + '/-/pipelines/' + answer.pipeline.iid)
  } catch (caught) {
    deployActionError.value = caught instanceof ApiError
      ? caught.message
      : 'Could not start the deployment.'
  } finally {
    deployingArtifact.value = false
  }
}

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
                <span v-if="one.status === 'pending' && one.queue_priority < 100" class="artifact-queue-low">Low priority</span>
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
            <div class="artifact-detail-head-actions">
              <span v-if="logLoading" class="muted small">Updating log…</span>
              <button
                v-if="canTrigger && isDeployable(selected)"
                class="btn btn-small"
                type="button"
                @click="openDeployPanel"
              >Deploy</button>
            </div>
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
          <section v-if="deployPanelOpen" class="artifact-deploy-panel">
            <div class="artifact-deploy-panel-head">
              <div>
                <h4>Deploy artifact</h4>
                <p class="muted small">Source commit and image digests are fixed to this artifact. Nothing is rebuilt.</p>
              </div>
              <button class="btn btn-small" type="button" @click="closeDeployPanel">Cancel</button>
            </div>

            <p v-if="deployOptionsLoading" class="muted small">Reading .dogit-ci.yml from {{ selected.sha.slice(0, 8) }}…</p>
            <div v-else-if="deployOptionsError" class="alert alert-error artifact-deploy-error">{{ deployOptionsError }}</div>
            <template v-else-if="deployOptions">
              <div class="artifact-deploy-source">
                <span class="muted small">Source</span>
                <strong class="mono">{{ deployOptions.artifact.ref }} · {{ deployOptions.artifact.sha.slice(0, 8) }}</strong>
              </div>

              <div v-if="deployOptions.deployments.length" class="artifact-deploy-targets">
                <label class="muted small">Deployment target</label>
                <button
                  v-for="option in deployOptions.deployments"
                  :key="option.index"
                  type="button"
                  class="artifact-deploy-choice"
                  :class="{ selected: selectedDeploymentIndex === option.index }"
                  :disabled="!option.available || deployingArtifact"
                  @click="selectedDeploymentIndex = option.index"
                >
                  <span class="artifact-deploy-choice-head">
                    <strong>{{ option.name }}</strong>
                    <span class="mono small">{{ option.module }} → {{ option.target }}</span>
                  </span>
                  <span v-if="!option.available" class="artifact-deploy-reason">{{ option.reason }}</span>
                  <span v-else class="muted small">
                    {{ option.manifests.length }} manifest{{ option.manifests.length === 1 ? '' : 's' }}
                    · rollout {{ option.rollout ? 'on' : 'off' }}
                    <template v-if="option.timeout"> · timeout {{ option.timeout }}</template>
                  </span>
                  <span class="artifact-deploy-manifests mono">{{ option.manifests.join(' · ') }}</span>
                  <span v-for="image in option.images" :key="image.name" class="artifact-deploy-image">
                    <span class="muted">{{ image.name }}</span>
                    <strong class="mono">{{ image.reference }}</strong>
                  </span>
                </button>
              </div>

              <div v-if="deployActionError" class="alert alert-error artifact-deploy-error">{{ deployActionError }}</div>
              <div class="artifact-deploy-actions">
                <span class="muted small">Manifest and target settings come from this commit.</span>
                <button
                  class="btn btn-small"
                  type="button"
                  :disabled="deployingArtifact || selectedDeploymentIndex == null"
                  @click="confirmArtifactDeploy"
                >{{ deployingArtifact ? 'Starting…' : 'Confirm deploy' }}</button>
              </div>
            </template>
          </section>
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
