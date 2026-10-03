<script setup lang="ts">
/**
 * One run, and its log while it runs.
 *
 * A separate page because watching a build is what people come here to do, and a
 * list that also does that does neither well. The log arrives in pieces from a
 * runner on another machine, so it is followed rather than streamed: there is no
 * connection to hold open, only a growing file to read the new part of.
 *
 * Jobs are grouped by stage, because that is how the pipeline is written and
 * therefore how it is thought about. A job's log opens where the job is rather
 * than on a page of its own, so the whole shape of the run stays visible while
 * one part of it is being read.
 */
import type { ModulePipeline, PipelineJob, PipelineStage } from '~/types/pipeline'
import { formatDuration, statusClass, statusText } from '~/types/pipeline'

const props = defineProps<{
  /**
   * What the API is asked about.
   *
   * The id rather than the path, because a grouped project's path contains a
   * slash and no single path segment can carry one. The path is still there for
   * everything shown to a person.
   */
  projectId?: string
  projectPath: string
  runIid: number
  /**
   * Which job the address names, or 0 for none.
   *
   * The job is in the address because somebody arrived here by clicking its name:
   * a link that opened some other job's log would be a link that lied, and the
   * back button would go somewhere the reader was not.
   */
  jobIid?: number
  defaultBranch?: string
}>()

const { add: notify } = useNotifyPool()

/** The project as the API is asked about it: its id when there is one. */
const apiRef = computed(() => props.projectId || encodeURIComponent(props.projectPath))


const pipeline = ref<ModulePipeline | null>(null)
const jobs = ref<PipelineJob[]>([])
const lines = ref<Record<number, LogLine[]>>({})
const loading = ref(true)
const error = ref('')
const starting = ref(false)
/** Which job's log is on the right. Null until something is known to exist. */
const selectedIID = ref<number | null>(null)

/**
 * Stages that are folded away.
 *
 * Folding is here for the long pipelines, where the outline of thirty jobs is
 * longer than the log beside it. It starts unfolded: somebody opening a run is
 * looking for a job, and a folded outline hides the one they came for.
 */
const foldedStages = ref<Record<string, boolean>>({})

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

/** True while the following loop is in flight, so a second one is not started. */
const followLoopRunning = ref(false)

const running = computed(() => jobs.value.some((job) => job.status === 'running'))
const pending = computed(() => jobs.value.some((job) => job.status === 'pending'))

/** The jobs of each stage, in the order the stages appear in the pipeline. */
const stages = computed<PipelineStage[]>(() => {
  if (pipeline.value?.stages?.length) return pipeline.value.stages

  // A run read from somewhere without stage grouping still has to be laid out
  // somehow, and the job's own stage name is the truth in that case.
  const order: string[] = []
  const grouped = new Map<string, PipelineJob[]>()
  for (const job of jobs.value) {
    const stage = job.stage || 'test'
    if (!grouped.has(stage)) {
      grouped.set(stage, [])
      order.push(stage)
    }
    grouped.get(stage)!.push(job)
  }
  return order.map((name) => {
    const inside = grouped.get(name) ?? []
    return {
      name,
      status: inside[0]?.status ?? 'pending',
      job_count: inside.length,
      jobs: inside.map((job) => ({ iid: job.iid, name: job.name, status: job.status })),
    }
  })
})

const jobByIID = computed(() => {
  const map = new Map<number, PipelineJob>()
  for (const job of jobs.value) map.set(job.iid, job)
  return map
})

const selectedJob = computed(() =>
  selectedIID.value === null ? null : (jobByIID.value.get(selectedIID.value) ?? null),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ pipeline: ModulePipeline; jobs: PipelineJob[] }>(
      `/projects/${apiRef.value}/pipelines/${props.runIid}`,
    )
    pipeline.value = answer.pipeline
    jobs.value = answer.jobs

    // The job to show is picked by what the run is doing: the one that failed, or
    // the one running, or the last one. Somebody opening a finished run is almost
    // always after a specific job, and the one they want is the one that went
    // wrong.
    // The address wins: it is where the reader asked to be. Without one, the run
    // itself chooses — the job that failed, else the one running, else the last,
    // which is the job somebody opening a run is almost always after.
    const asked = props.jobIid ?? 0
    if (asked && answer.jobs.some((job) => job.iid === asked)) {
      selectedIID.value = asked
    } else if (selectedIID.value === null || !answer.jobs.some((job) => job.iid === selectedIID.value)) {
      const failed = answer.jobs.find((job) => job.status === 'failed')
      const live = answer.jobs.find((job) => job.status === 'running')
      const last = answer.jobs[answer.jobs.length - 1]
      selectedIID.value = (failed ?? live ?? last)?.iid ?? null
    }

    if (selectedIID.value !== null) await fetchLog(jobByIID.value.get(selectedIID.value)!)
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
        `/projects/${apiRef.value}/pipelines/${props.runIid}/jobs/${job.iid}/log`,
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
    // the file on the server holds all of them; the reader is watching the end.
    const merged = [...existing, ...added]
    lines.value = { ...lines.value, [job.iid]: merged.length > 2000 ? merged.slice(-2000) : merged }
  } catch {
    // A log that could not be read this time is not worth interrupting a build for;
    // the next poll tries again.
  }
}

/**
 * Follows a run until nothing is running any more.
 *
 * A run that lasts an hour is watched for the last minute of it, and the only
 * thing that has to stay true meanwhile is that the log grows.
 */
async function follow() {
  if (followLoopRunning.value) return
  followLoopRunning.value = true
  try {
    await followLoop()
  } finally {
    followLoopRunning.value = false
  }
}

async function followLoop() {
  while (true) {
    await new Promise((resolve) => setTimeout(resolve, pollInterval))

    const answer = await api
      .get<{ pipeline: ModulePipeline; jobs: PipelineJob[] }>(
        `/projects/${apiRef.value}/pipelines/${props.runIid}`,
      )
      .catch(() => null)
    // A request that failed is not a reason to stop watching: the page is over a
    // network that blips, and the run is not over.
    if (!answer) continue

    pipeline.value = answer.pipeline
    jobs.value = answer.jobs

    // A page opened for a particular job stays on it: somebody who followed a link
    // to one job's output is reading that job, not browsing the run. Only a page
    // with no job named follows the run along.
    if (!props.jobIid && selectedJob.value && selectedJob.value.status === 'pending') {
      const live = answer.jobs.find((job) => job.status === 'running')
      if (live) selectedIID.value = live.iid
    }

    if (selectedJob.value) await fetchLog(selectedJob.value)

    if (jobs.value.some((job) => job.status === 'running')) continue

    // Nothing more will be written; one last read so the final lines are shown.
    await load()

    if (pipeline.value?.status === 'success') {
      notify(`Pipeline #${props.runIid} passed`, { type: 'success' })
    } else if (pipeline.value?.status === 'failed') {
      notify(`Pipeline #${props.runIid} failed`, { type: 'error' })
    }
    return
  }
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

/** Folds or unfolds one stage's jobs. */
function toggleStage(name: string) {
  foldedStages.value = { ...foldedStages.value, [name]: !foldedStages.value[name] }
}

/**
 * Shows one job's log.
 *
 * The address changes with it, so this page can be linked to and the browser's
 * back button walks the jobs that were read.
 */
async function select(job: PipelineJob) {
  selectedIID.value = job.iid
  await fetchLog(job)

  if (props.jobIid !== job.iid) {
    await navigateTo(`/p/${props.projectPath}/-/pipelines/${props.runIid}/jobs/${job.iid}`, {
      replace: true,
    })
  }
}

async function rerun() {
  starting.value = true
  error.value = ''
  try {
    const answer = await api.post<{ pipeline: ModulePipeline }>(
      `/projects/${apiRef.value}/pipelines`,
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

let stopWatching: (() => void) | undefined

onMounted(async () => {
  await load()
  if (running.value) await follow()

  // The poll above only runs while something is running here. Once it is over this
  // keeps listening, so a job retried from another page — or a run started from the
  // list — is picked up without a reload.
  stopWatching = watchEvents({
    kinds: ['pipeline.created', 'pipeline.updated'],
    project: () => props.projectPath,
    onChange: () => {
      if (!followLoopRunning.value && (running.value || pending.value)) void follow()
    },
  })
})

onBeforeUnmount(() => stopWatching?.())

watch(() => props.jobIid, async (asked) => {
  const iid = asked ?? 0
  if (!iid || selectedIID.value === iid) return
  const job = jobByIID.value.get(iid)
  if (!job) return
  selectedIID.value = iid
  await fetchLog(job)
})

watch(() => props.runIid, async () => {
  lines.value = {}
  consumed.value = {}
  selectedIID.value = null
  await load()
  if (running.value) await follow()
})
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
          <h2 class="section-title">
            <span class="mono">#{{ pipeline.iid }}</span>
            <span class="badge" :class="statusClass[pipeline.status] ?? 'badge-neutral'">
              {{ statusText[pipeline.status] }}
            </span>
          </h2>
          <p class="page-subtitle">
            <span v-if="pipeline.title" class="commit-title">{{ pipeline.title }}</span>
            <span class="branch">{{ pipeline.ref }}</span>
            <span class="mono muted small">{{ pipeline.sha.slice(0, 8) }}</span>
            <span class="muted small">{{ timeAgo(pipeline.created_at) }}</span>
            <span class="muted small mono duration">{{
              running ? 'in progress' : formatDuration(pipeline.duration_ms)
            }}</span>
            <span v-if="pipeline.triggered_by" class="muted small">
              triggered by {{ pipeline.triggered_by.name || pipeline.triggered_by.username }}
            </span>
          </p>
        </div>
        <button class="btn" type="button" :disabled="starting" @click="rerun">
          {{ starting ? 'Starting…' : 'Run again' }}
        </button>
      </div>

      <div v-if="error" class="alert alert-error">{{ error }}</div>

      <div v-if="pending && !running" class="alert alert-info">
        Waiting for a runner to take this pipeline.
      </div>

      <!-- Two columns, the way a pipeline is actually read: the shape of the run on
           the left, the output of one job on the right. They are side by side so
           that moving from "which job" to "what did it say" is a glance rather
           than a page load, and so the place it got to is never off-screen while
           its output is on it. -->
      <div class="run-layout">
        <nav class="outline" aria-label="Pipeline stages and jobs">
          <section v-for="stage in stages" :key="stage.name" class="outline-stage">
            <button
              type="button"
              class="outline-stage-head"
              :aria-expanded="!foldedStages[stage.name]"
              @click="toggleStage(stage.name)"
            >
              <span class="chevron" :class="{ open: !foldedStages[stage.name] }">▸</span>
              <span class="dot" :class="statusClass[stage.status] ?? 'badge-neutral'" />
              <span class="outline-stage-name">{{ stage.name }}</span>
              <span class="muted small">{{ stage.job_count }}</span>
            </button>

            <template v-if="!foldedStages[stage.name]">
            <button
              v-for="entry in stage.jobs"
              :key="entry.iid"
              type="button"
              class="outline-job"
              :class="{ active: selectedIID === entry.iid }"
              @click="select(jobByIID.get(entry.iid)!)"
            >
              <span class="dot" :class="statusClass[entry.status] ?? 'badge-neutral'" />
              <span class="outline-job-name">{{ entry.name }}</span>
              <span v-if="entry.duration_ms" class="muted small mono">
                {{ formatDuration(entry.duration_ms) }}
              </span>
            </button>
            </template>
          </section>
        </nav>

        <div class="detail">
          <div v-if="!selectedJob" class="card empty">
            This pipeline has no jobs to show.
          </div>

          <template v-else>
            <div class="card job-card">
              <div class="card-header">
                <span class="badge" :class="statusClass[selectedJob.status] ?? 'badge-neutral'">
                  {{ statusText[selectedJob.status] }}
                </span>
                <strong>{{ selectedJob.name }}</strong>
                <span class="muted small">{{ selectedJob.stage }}</span>
                <div class="spacer" />
                <span class="muted small mono">{{ selectedJob.image }}</span>
                <span v-if="selectedJob.duration_ms" class="muted small mono">
                  {{ formatDuration(selectedJob.duration_ms) }}
                </span>
              </div>

              <div class="card-body">
                <details v-if="selectedJob.script?.length" class="script">
                  <summary class="muted small">
                    Script ({{ selectedJob.script.length }} lines)
                  </summary>
                  <pre class="plain">{{ selectedJob.script.join('\n') }}</pre>
                </details>

                <div v-if="lines[selectedJob.iid]?.length" class="log">
                  <div
                    v-for="(line, index) in lines[selectedJob.iid]"
                    :key="index"
                    class="log-line"
                    :class="line.stream === 'err' ? 'stderr' : 'stdout'"
                  >{{ line.text }}</div>
                </div>

                <p v-else-if="selectedJob.status === 'pending'" class="muted small">
                  Waiting for a runner to take it.
                </p>
                <p v-else-if="selectedJob.status === 'running'" class="muted small">
                  Running — nothing written yet.
                </p>
                <p v-else class="muted small">This job wrote nothing.</p>
              </div>
            </div>
          </template>
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

.commit-title {
  color: var(--text);
}

/* The commit's message is the answer to "what was this for", so it is the only
   part of the line that is not muted: the rest is context for it. */
.page-subtitle {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.duration {
  font-variant-numeric: tabular-nums;
}

.branch {
  font-size: 11px;
  padding: 1px 7px;
  border: 1px solid var(--border);
  border-radius: 10px;
  color: var(--text-muted);
}

.stage-block {
  margin-bottom: 18px;
}

/* The run's shape and its output, side by side. The outline is fixed and
   scrollable on its own so that the run's progress stays put while a long log is
   read beside it — the page itself never scrolls for this, only the log column. */
.run-layout {
  display: grid;
  grid-template-columns: 260px 1fr;
  gap: 16px;
  align-items: start;
}

.outline {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px;
  background: var(--bg-subtle, rgba(255, 255, 255, 0.02));
}

.outline-stage + .outline-stage {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}

.outline-stage-head,
.outline-job {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 5px 6px;
  border-radius: 4px;
}

/* The stage is a group of jobs, so its header is a button that folds them away:
   the shape of the pipeline is what people scan, and folding is how they get to
   the part they care about without losing the rest of the outline. */
.outline-stage-head {
  background: none;
  border: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  width: 100%;
}

.outline-stage-head:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.04));
}

.chevron {
  color: var(--text-muted);
  transition: transform 0.12s ease;
  flex: 0 0 auto;
}

.chevron.open {
  transform: rotate(90deg);
}

.outline-stage-name {
  flex: 1;
  font-weight: 600;
  text-transform: capitalize;
  font-size: 13px;
}

.outline-job {
  background: none;
  border: none;
  color: inherit;
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}

.outline-job:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.04));
}

.outline-job.active {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.07));
  box-shadow: inset 2px 0 0 currentColor;
}

.outline-job-name {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* A status mark rather than a badge: the outline is a list of positions, and a
   coloured dot says how it went without making the list shout. */
.dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  flex: 0 0 auto;
  background: currentColor;
}

.dot.badge-green { background: #3fb950; }
.dot.badge-danger { background: #f85149; }
.dot.badge-warning { background: #d29922; }
.dot.badge-neutral { background: #6e7681; }
.dot.badge-private { background: #a371f7; }

.detail {
  min-width: 0;
}

.job-card {
  margin-bottom: 8px;
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

/* As tall as the log is.
   A capped window with its own scrollbar is worse in both directions: the page
   has to be scrolled twice to get anywhere, and every line that arrives pushes
   the one being read out of sight. Growing the page instead puts one scrollbar
   where there already was one, and leaves it to the reader. */
.log {
  margin: 0;
  padding: 12px;
  background: var(--bg-subtle, rgba(0, 0, 0, 0.03));
  border: 1px solid var(--border);
  border-radius: 6px;
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