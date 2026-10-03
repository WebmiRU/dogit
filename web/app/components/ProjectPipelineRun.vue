<script setup lang="ts">
/**
 * One run, and its log while it runs.
 *
 * A separate page because watching a build is what people come here to do, and a
 * list that also does that does neither well. The log arrives in pieces from a
 * runner on another machine, so it is followed rather than streamed: there is no
 * connection to hold open, only a growing file to read the new part of.
 */
import type { ModulePipeline, PipelineJob } from '~/types/pipeline'

const props = defineProps<{
  projectPath: string
  runIid: number
  defaultBranch?: string
}>()

const { add: notify } = useNotifyPool()

const pipeline = ref<ModulePipeline | null>(null)
const jobs = ref<PipelineJob[]>([])
const lines = ref<Record<number, LogLine[]>>({})
const loading = ref(true)
const error = ref('')
const starting = ref(false)
const scroller = ref<HTMLElement | null>(null)

/** How often a running job is asked for its log. */
const pollInterval = 2000

/** A log line, and which stream it came from. */
interface LogLine {
  stream: 'out' | 'err'
  text: string
}

/**
 * How much of the log has been shown.
 *
 * The file is read whole and only the new part is added, so the page grows rather
 * than redraws. Redrawing a log from the top on every line makes the reader's
 * position jump, which is worse than not following it at all.
 */
const consumed = ref<Record<number, number>>({})

const running = computed(() => jobs.value.some((job) => job.status === 'running'))
const pending = computed(() => jobs.value.some((job) => job.status === 'pending'))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ pipeline: ModulePipeline; jobs: PipelineJob[] }>(
      `/projects/${encodeURIComponent(props.projectPath)}/pipelines/${props.runIid}`,
    )
    pipeline.value = answer.pipeline
    jobs.value = answer.jobs

    for (const job of answer.jobs) {
      await fetchLog(job)
    }
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/** Reads one job's log and appends what is new. */
async function fetchLog(job: PipelineJob) {
  try {
    const response = await fetch(
      rawApiUrl(
        `/projects/${encodeURIComponent(props.projectPath)}/pipelines/${props.runIid}/jobs/${job.iid}/log`,
      ),
      { credentials: 'include' },
    )
    if (!response.ok) return

    const text = await response.text()
    const seen = consumed.value[job.iid] ?? 0
    if (text.length <= seen) return

    const added = parseLog(text.slice(seen))
    consumed.value = { ...consumed.value, [job.iid]: text.length }

    const existing = lines.value[job.iid] ?? []
    // Only the tail is kept in the browser. A long build has thousands of lines and
    // nobody scrolls back through them; the file on the server has all of them.
    const merged = [...existing, ...added]
    lines.value = { ...lines.value, [job.iid]: merged.length > 800 ? merged.slice(-800) : merged }

    scrollToEnd()
  } catch {
    // A log that could not be read this time is not worth interrupting a build for;
    // the next poll tries again.
  }
}

/** Follows the log until nothing is running any more. */
async function follow() {
  while (true) {
    await new Promise((resolve) => setTimeout(resolve, pollInterval))

    const answer = await api
      .get<{ pipeline: ModulePipeline; jobs: PipelineJob[] }>(
        `/projects/${encodeURIComponent(props.projectPath)}/pipelines/${props.runIid}`,
      )
      .catch(() => null)
    if (!answer) continue

    pipeline.value = answer.pipeline
    jobs.value = answer.jobs

    for (const job of answer.jobs) {
      await fetchLog(job)
    }

    if (!jobs.value.some((job) => job.status === 'running')) {
      await load()
      if (pipeline.value?.status === 'success') {
        notify(`Pipeline #${props.runIid} passed`, { type: 'success' })
      } else if (pipeline.value?.status === 'failed') {
        notify(`Pipeline #${props.runIid} failed`, { type: 'error' })
      }
      return
    }
  }
}

/** Follows the bottom of the log — but only when the reader is already there. */
function scrollToEnd() {
  const element = scroller.value
  if (!element) return

  const atBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 40
  if (atBottom) element.scrollTop = element.scrollHeight
}

/**
 * Reads the stored log.
 *
 * Each line carries which stream it came from, so the file stays plain text that
 * reads sensibly in a terminal while the page can still colour a line by what it
 * is.
 */
function parseLog(text: string): LogLine[] {
  const result: LogLine[] = []

  for (const line of text.split('\n')) {
    if (!line) continue

    if (line.startsWith('err| ')) {
      result.push({ stream: 'err', text: line.slice(5) })
      continue
    }
    if (line.startsWith('out| ')) {
      result.push({ stream: 'out', text: line.slice(5) })
      continue
    }
    // A line with no marker came from somewhere that did not write one. Shown as
    // ordinary output rather than dropped: a missing line is worse than a wrong
    // colour.
    result.push({ stream: 'out', text: line })
  }
  return result
}

async function rerun() {
  starting.value = true
  error.value = ''
  try {
    const answer = await api.post<{ pipeline: ModulePipeline }>(
      `/projects/${encodeURIComponent(props.projectPath)}/pipelines`,
      { ref: props.defaultBranch || 'main' },
    )
    notify(`Pipeline #${answer.pipeline.iid} started`, { type: 'success' })
    await navigateTo(`/p/${props.projectPath}/-/pipelines/${answer.pipeline.iid}`)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    starting.value = false
  }
}

onMounted(async () => {
  await load()
  if (running.value) await follow()
})

watch(() => props.runIid, async () => {
  lines.value = {}
  consumed.value = {}
  await load()
  if (running.value) await follow()
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
    <div v-if="loading && !pipeline" class="spinner">Loading the run…</div>

    <div v-else-if="error && !pipeline" class="alert alert-error">{{ error }}</div>

    <template v-else-if="pipeline">
      <div class="repo-head">
        <div class="title">
          <p class="crumb">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines`">Pipelines</NuxtLink>
          </p>
          <h2 class="section-title" style="margin: 0">
            Pipeline #{{ pipeline.iid }}
            <span class="badge" :class="statusClass[pipeline.status] ?? 'badge-neutral'">
              {{ pipeline.status }}
            </span>
          </h2>
          <p class="page-subtitle">
            <span class="mono">{{ pipeline.ref }}</span>
            · <span class="mono">{{ pipeline.sha.slice(0, 8) }}</span>
            · {{ timeAgo(pipeline.created_at) }}
            <span v-if="running" class="badge badge-warning">running</span>
            <span v-else-if="pending" class="badge badge-private">waiting for a runner</span>
          </p>
        </div>
        <button class="btn" type="button" :disabled="starting" @click="rerun">
          {{ starting ? 'Starting…' : 'Run again' }}
        </button>
      </div>

      <div v-if="error" class="alert alert-error">{{ error }}</div>

      <div v-for="job in jobs" :key="job.iid" class="card job-card">
        <div class="card-header">
          <span class="badge" :class="statusClass[job.status] ?? 'badge-neutral'">
            {{ job.status }}
          </span>
          <strong>{{ job.name }}</strong>
          <span class="muted small">{{ job.stage }}</span>
          <div class="spacer" />
          <span class="muted small mono">{{ job.image }}</span>
          <span v-if="job.duration_ms" class="muted small">{{ formatDuration(job.duration_ms) }}</span>
        </div>

        <div class="card-body">
          <details v-if="job.script?.length" class="script">
            <summary class="muted small">Script ({{ job.script.length }} lines)</summary>
            <pre class="plain">{{ job.script.join('\n') }}</pre>
          </details>

          <div v-if="lines[job.iid]?.length" ref="scroller" class="log">
            <div
              v-for="(line, index) in lines[job.iid]"
              :key="index"
              class="log-line"
              :class="line.stream === 'err' ? 'stderr' : 'stdout'"
            >{{ line.text }}</div>
          </div>

          <p v-else-if="job.status === 'pending'" class="muted small">
            Waiting for a runner to take it.
          </p>
          <p v-else-if="job.status === 'running'" class="muted small">
            Running — nothing written yet.
          </p>
          <p v-else class="muted small">This job wrote nothing.</p>
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

.plain {
  margin: 0;
  padding: 12px;
  background: var(--bg-subtle, rgba(0, 0, 0, 0.03));
  border: 1px solid var(--border);
  border-radius: 6px;
  white-space: pre-wrap;
  font-size: 13px;
}

.log {
  margin: 0;
  padding: 12px;
  background: var(--bg-subtle, rgba(0, 0, 0, 0.03));
  border: 1px solid var(--border);
  border-radius: 6px;
  max-height: 460px;
  overflow: auto;
  font-size: 13px;
  line-height: 1.5;
}

.log-line {
  white-space: pre-wrap;
  word-break: break-word;
  min-height: 1em;
}

/* Ordinary output is most of what a build says, and it is said quietly. A line on
   stderr is the one a person is looking for, so it is the one that stands out. */
.log-line.stdout {
  color: var(--text-muted);
}

.log-line.stderr {
  color: #ff9d94;
}

.small {
  font-size: 12px;
}
</style>