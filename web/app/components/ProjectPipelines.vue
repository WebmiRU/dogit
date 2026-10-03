<script setup lang="ts">
/**
 * One project's pipelines.
 *
 * The page exists mostly for the logs: what passed, what failed, and what it said.
 * A run is listed whether a push started it or a person did — they are the same
 * thing afterwards, and pretending otherwise means two lists nobody reads twice.
 */
import type { ModulePipeline, PipelineJob } from '~/types/pipeline'

const props = defineProps<{
  projectPath: string
  /** What "Run pipeline" runs against: the project's own default branch. */
  defaultBranch?: string
}>()

const route = useRoute()

const { add: notify } = useNotifyPool()

const pipelines = ref<ModulePipeline[]>([])
const loading = ref(true)
const error = ref('')
const starting = ref(false)

const jobs = ref<PipelineJob[]>([])
const selected = ref<ModulePipeline | null>(null)
const selectedJobs = ref<PipelineJob[]>([])
const logs = ref<Record<number, string>>({})
const loadingLog = ref<number | null>(null)
const streaming = ref<number | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ pipelines: ModulePipeline[] }>(
      `/projects/${encodeURIComponent(props.projectPath)}/pipelines`,
    )
    pipelines.value = answer.pipelines
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/**
 * Starts a run.
 *
 * The configuration is read from the commit, so a project with no .gitlab-ci.yml
 * says so here rather than producing an empty run nobody can explain.
 */
async function start() {
  starting.value = true
  error.value = ''
  try {
    const answer = await api.post<{ pipeline: ModulePipeline }>(
      `/projects/${encodeURIComponent(props.projectPath)}/pipelines`,
      { ref: props.defaultBranch || 'main' },
    )
    notify(`Pipeline #${answer.pipeline.iid} started`, { type: 'success' })
    await load()
    await open(answer.pipeline.iid)
  } catch (caught) {
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    starting.value = false
  }
}

async function open(iid: number) {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ pipeline: ModulePipeline; jobs: PipelineJob[] }>(
      `/projects/${encodeURIComponent(props.projectPath)}/pipelines/${iid}`,
    )
    selected.value = answer.pipeline
    selectedJobs.value = answer.jobs

    // Logs are fetched for finished jobs. A running one has none worth reading yet,
    // and asking anyway would produce an empty box that looks like a failure.
    for (const job of answer.jobs) {
      if (job.status === 'pending') continue
      await loadLog(job)
    }
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

async function loadLog(job: PipelineJob) {
  loadingLog.value = job.iid
  try {
    const response = await fetch(
      rawApiUrl(
        `/projects/${encodeURIComponent(props.projectPath)}/pipelines/${job.pipeline_id}/jobs/${job.iid}/log`,
      ),
      { credentials: 'include' },
    )

    const text = await response.text()
    logs.value = { ...logs.value, [job.iid]: text }

    // A job that is still running gets a live tail rather than a snapshot: the
    // whole point of watching a build is watching it move.
    if (job.status === 'running') {
      streaming.value = job.iid
      await tailLog(job)
    }
  } catch {
    logs.value = { ...logs.value, [job.iid]: 'the log could not be read' }
  } finally {
    loadingLog.value = null
  }
}

/**
 * Follows a running job's log.
 *
 * Polling rather than a stream, because the log is written by a runner on another
 * machine and arrives in pieces: there is nothing to hold open. Once the job is no
 * longer running the polling stops by itself.
 */
async function tailLog(job: PipelineJob) {
  const seen = logs.value[job.iid]?.length ?? 0

  for (let attempt = 0; attempt < 600; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 3000))

    try {
      const response = await fetch(
        rawApiUrl(
          `/projects/${encodeURIComponent(props.projectPath)}/pipelines/${job.pipeline_id}/jobs/${job.iid}/log`,
        ),
        { credentials: 'include' },
      )
      const text = await response.text()

      // Only what is new: re-rendering a growing log from the top makes the page
      // jump under the reader, which is worse than not following it at all.
      if (text.length > seen) {
        logs.value = { ...logs.value, [job.iid]: text.slice(seen) }
        seen = text.length
      }

      if (text.includes('image pushed:') || attempt % 10 === 9) {
        await open(job.pipeline_id)
        if (!selectedJobs.value.some((candidate) => candidate.iid === job.iid && candidate.status === 'running')) {
          streaming.value = null
          return
        }
        seen = logs.value[job.iid]?.length ?? seen
      }
    } catch {
      return
    }
  }
  streaming.value = null
}

onMounted(async () => {
  await load()
  // Which run is open lives in the URL, so a link to one particular build is a
  // link somebody can send and come back to.
  const asked = Number(route.query.run)
  if (Number.isFinite(asked) && asked > 0) await open(asked)
})
watch(() => props.projectPath, () => {
  selected.value = null
  selectedJobs.value = []
  logs.value = {}
  load()
})

const statusClass: Record<string, string> = {
  success: 'badge-green',
  failed: 'badge-danger',
  running: 'badge-warning',
  canceled: 'badge-neutral',
  pending: 'badge-private',
  interrupted: 'badge-warning',
  skipped: 'badge-neutral',
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h2 class="section-title" style="margin: 0">Pipelines</h2>
        <p class="page-subtitle">
          Runs of this project's <code class="mono">.dogit-ci.yml</code>, whether a push
          started one or you did.
        </p>
      </div>
      <button class="btn btn-primary" type="button" :disabled="starting" @click="start">
        {{ starting ? 'Starting…' : 'Run pipeline' }}
      </button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="loading && pipelines.length === 0" class="spinner">Loading pipelines…</div>

    <div v-else-if="pipelines.length === 0" class="card empty">
      Nothing has run yet. Add a <code class="mono">.gitlab-ci.yml</code> to the project
      and press “Run pipeline”.
    </div>

    <div v-else class="pipeline-list">
      <NuxtLink
        v-for="run in pipelines"
        :key="run.iid"
        class="pipeline-row"
        :class="{ active: selected?.iid === run.iid }"
        :to="`/p/${projectPath}/-/pipelines?run=${run.iid}`"
      >
        <span class="badge" :class="statusClass[run.status] ?? 'badge-neutral'">
          {{ run.status }}
        </span>
        <span class="mono">#{{ run.iid }}</span>
        <span class="muted">{{ run.ref }}</span>
        <span class="muted small mono">{{ run.sha.slice(0, 8) }}</span>
        <span class="muted small">{{ timeAgo(run.created_at) }}</span>
        <span v-if="run.jobs" class="muted small">{{ run.jobs }} job{{ run.jobs === 1 ? '' : 's' }}</span>
      </NuxtLink>
    </div>

    <template v-if="selected">
      <h3 class="section-title">
        Pipeline #{{ selected.iid }} · {{ selected.ref }}
        <span class="badge" :class="statusClass[selected.status] ?? 'badge-neutral'">
          {{ selected.status }}
        </span>
      </h3>

      <div v-for="job in selectedJobs" :key="job.iid" class="card job-card">
        <div class="card-header">
          <span class="badge" :class="statusClass[job.status] ?? 'badge-neutral'">
            {{ job.status }}
          </span>
          <strong>{{ job.name }}</strong>
          <span class="muted small">{{ job.stage }}</span>
          <div class="spacer" />
          <span class="muted small">{{ job.image }}</span>
          <span v-if="job.duration_ms" class="muted small">{{ formatDuration(job.duration_ms) }}</span>
        </div>

        <div class="card-body">
          <details v-if="job.script?.length" class="script">
            <summary class="muted small">Script ({{ job.script.length }} lines)</summary>
            <pre class="log">{{ job.script.join('\n') }}</pre>
          </details>

          <div v-if="loadingLog === job.iid" class="muted small">Reading the log…</div>
          <pre v-else-if="logs[job.iid]" class="log">{{ logs[job.iid] }}</pre>
          <p v-else-if="job.status === 'pending'" class="muted small">
            Waiting for a runner to take it.
          </p>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.section-title {
  font-size: 18px;
  display: flex;
  align-items: center;
  gap: 10px;
}

.pipeline-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-bottom: 20px;
}

/* One row per run, all the facts on one line: the state, the number, what it ran
   against and when. A table here would say the same things with more machinery. */
.pipeline-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border: 1px solid transparent;
  border-radius: 6px;
  color: inherit;
  text-decoration: none;
  font-size: 13px;
}

.pipeline-row:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.03));
  border-color: var(--border);
}

.pipeline-row.active {
  border-color: var(--accent);
}

.job-card {
  margin-bottom: 12px;
}

.card-header .spacer {
  flex: 1;
}

.script summary {
  cursor: pointer;
  margin-bottom: 8px;
}

.log {
  margin: 0;
  padding: 12px;
  background: var(--bg-subtle, rgba(0, 0, 0, 0.03));
  border: 1px solid var(--border);
  border-radius: 6px;
  max-height: 420px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 13px;
  line-height: 1.5;
}

.small {
  font-size: 12px;
}
</style>