<script setup lang="ts">
/**
 * A project's pipelines.
 *
 * A table, because a pipeline has a fixed set of facts about it and they line up
 * in columns: how it went, what it was for, who asked, how far it got. Rows of
 * free text make the eye work to find the same answers, and the answers are the
 * whole reason this page exists.
 *
 * Every run has its own page, where the log is — watching a build is what people
 * come here for, and a table that also did that would do neither well.
 */
import type { ModulePipeline, PipelineStageJob } from '~/types/pipeline'
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
  defaultBranch?: string
}>()

const { add: notify } = useNotifyPool()

/** The project as the API is asked about it: its id when there is one. */
const apiRef = computed(() => props.projectId || encodeURIComponent(props.projectPath))


const pipelines = ref<ModulePipeline[]>([])
const loading = ref(true)
const error = ref('')
// Ignore responses from older searches/filters if a newer request finishes first.
let requestVersion = 0
const starting = ref(false)

/** The run list, and how much of it there is. */
const total = ref(0)
const page = ref(1)
const pages = ref(1)

/** The word being typed, apart from what has been searched for yet. */
const typing = ref('')

/** Where the run page lives. The repository page owns the address bar, so this keeps
 *  its own key for these settings rather than fighting over the project's tabs. */
const PIPE_KEY = 'dogit:pipelines'

interface PipelineView {
  ref?: string
  status?: string
  source?: string
  search?: string
  page?: number
}

// One state object for the whole component, created here rather than inside the
// handler that changes it: a state created outside setup is not the same state.
const stored = useState<PipelineView>(PIPE_KEY, () => ({}))

const view = computed<Required<PipelineView>>(() => {
  const current = stored.value
  return {
    ref: current.ref ?? '',
    status: current.status ?? '',
    source: current.source ?? '',
    search: current.search ?? '',
    page: Math.max(1, current.page ?? 1),
  }
})

function remember(change: Partial<PipelineView>) {
  const next = { ...view.value, ...change }
  // Any narrowing starts again at the first page: page seven of a search that now
  // matches two runs is a page about nothing.
  if (change.search !== undefined || change.ref !== undefined ||
      change.status !== undefined || change.source !== undefined) {
    next.page = 1
  }
  stored.value = next
}

/**
 * Reads the list.
 *
 * The list is fetched again rather than patched when something changes: an event says a
 * pipeline moved, not what it now looks like, and re-reading one small endpoint is
 * cheaper than being wrong.
 *
 * A reload that follows an event leaves the rows alone. Refreshing the whole list every
 * few seconds would move the row somebody is reading out from under them — and with a
 * page of it, would drop them on a different page entirely.
 */
async function load(quiet = false) {
  const version = ++requestVersion
  if (!quiet) loading.value = true
  error.value = ''
  try {
    const parts = new URLSearchParams({ per_page: '20', page: String(view.value.page) })
    for (const key of ['search', 'ref', 'status', 'source'] as const) {
      if (view.value[key]) parts.set(key, view.value[key])
    }

    const answer = await api.get<{
      pipelines: ModulePipeline[]
      total: number
      page: number
      pages: number
    }>(`/projects/${apiRef.value}/pipelines?${parts.toString()}`)

    if (version !== requestVersion) return
    pipelines.value = answer.pipelines ?? []
    total.value = answer.total ?? 0
    page.value = answer.page ?? 1
    pages.value = Math.max(1, answer.pages ?? 1)
  } catch (caught) {
    if (version !== requestVersion) return
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

// Searching waits for the typing to stop: every keystroke is a request otherwise, and
// on a slow connection they arrive out of order, so the list shows results for a word
// typed two searches ago.
let wait: ReturnType<typeof setTimeout> | undefined
watch(typing, (word) => {
  clearTimeout(wait)
  wait = setTimeout(() => {
    if (word !== view.value.search) {
      remember({ search: word })
    }
  }, 300)
})

watch(view, () => void load())
onBeforeUnmount(() => clearTimeout(wait))

/** The branches that have runs, so the filter offers the ones there are. */
const branches = computed(() => {
  const seen = new Set<string>()
  for (const run of pipelines.value) seen.add(run.ref)
  return [...seen].sort()
})

const states = [
  { value: '', label: 'Any state' },
  { value: 'success', label: 'Success' },
  { value: 'failed', label: 'Failed' },
  { value: 'canceled', label: 'Canceled' },
  { value: 'running', label: 'Running' },
]

/** A search or a filter, dropped. */
function clearFilters() {
  typing.value = ''
  remember({ search: '', ref: '', status: '', source: '' })
}

/** "21–40 of 340" — what is shown out of what there is. */
const range = computed(() => {
  if (total.value === 0) return 'nothing'
  const first = (page.value - 1) * 20 + 1
  const last = Math.min(first + pipelines.value.length - 1, total.value)
  return `${first}\u2013${last} of ${total.value}`
})

/**
 * Starts a run.
 *
 * The configuration is read from the commit, so a project with no .dogit-ci.yml
 * says so here rather than producing an empty run nobody can explain.
 */
async function start() {
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
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    starting.value = false
  }
}

/**
 * Which stage is opened, as "run number:stage name".
 *
 * The stage's jobs are not in the list itself: a pipeline of thirty jobs would
 * make the table unreadable, and the mark in the Stages column says which stages
 * exist. What a mark opens is a small list of names — enough to know which job to
 * go to, and to retry the one that failed.
 */
const openStage = ref<string | null>(null)

function stageKey(run: ModulePipeline, name: string) {
  return `${run.iid}:${name}`
}

/** Which stage this row has open, or null. The row asks for itself rather than
 *  for the opened stage, so two rows can never show each other's jobs. */
const openStageRef = ref<ModulePipeline | null>(null)
const openStageName = ref('')

/** The stage's jobs for one row, or null when nothing of that row is open. */
function openedStage(run: ModulePipeline) {
  if (!openStage.value || openStageRef.value !== run) return null
  return run.stages?.find((one) => one.name === openStageName.value) ?? null
}

function toggleStage(run: ModulePipeline, name: string) {
  const key = stageKey(run, name)
  if (openStage.value === key) {
    openStage.value = null
    openStageRef.value = null
    openStageName.value = ''
    return
  }
  openStage.value = key
  openStageRef.value = run
  openStageName.value = name
}

function closeStage() {
  openStage.value = null
  openStageRef.value = null
  openStageName.value = ''
}

async function retry(run: ModulePipeline, job: PipelineStageJob) {
  error.value = ''
  try {
    await api.post(`/projects/${apiRef.value}/pipelines/${run.iid}/jobs/${job.iid}/retry`, {})
    notify(`Retrying ${job.name} in pipeline #${run.iid}`, { type: 'success' })
    await load()
  } catch (caught) {
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  }
}

/**
 * A picture, or the initials that stand in for one.
 *
 * An avatar is a nicety, not a fact: a failed fetch leaves the initials, and the
 * row is just as readable without the picture.
 */
const brokenAvatars = ref<Record<string, boolean>>({})

function avatar(src?: string, person?: string) {
  if (!src || !person || brokenAvatars.value[person]) return null
  return src
}

/**
 * The stages as marks in order, coloured by how each one went.
 *
 * A stage that never started because an earlier one failed is drawn grey, not blue.
 * It arrives from the API as "pending", because its jobs are — and pending next to
 * a failed stage reads as a run that is still going to carry on. Which is the
 * opposite of what happened, and is what somebody sees on every failed run here.
 */
function stageMarks(run: ModulePipeline) {
  const over = run.status === 'failed' || run.status === 'canceled'

  return (run.stages ?? []).map((stage) => {
    const status = over && stage.status === 'pending' ? 'skipped' : stage.status
    return {
      ...stage,
      status,
      title: `${stage.name}: ${statusText[status]}`,
    }
  })
}

/** Whether the run is still going, which is the only thing that makes the clock matter. */
function isLive(pipeline: ModulePipeline) {
  return pipeline.status === 'running' || pipeline.status === 'pending'
}

let stopWatching: (() => void) | undefined

/**
 * Reloads when this project's pipelines move.
 *
 * A page that shows the state of builds and needs a button to be told they have
 * finished is a page nobody leaves open. The feed carries only the fact that
 * something happened, so this re-reads the list it already knows how to read.
 */
function onChanged() {
  void load(true)
}

onMounted(() => {
  // The search box comes back holding whatever was searched for, so a page reloaded
  // — or a different tab opened — does not silently show a different list.
  typing.value = view.value.search
  void load()
  stopWatching = watchEvents({
    kinds: ['pipeline.created', 'pipeline.updated'],
    project: () => props.projectPath,
    onChange: onChanged,
  })
})

onBeforeUnmount(() => stopWatching?.())

watch(() => props.projectPath, () => load())
</script>

<template>
  <section class="pipelines-page">
    <header class="pipelines-heading">
      <div class="pipelines-heading-copy">
        <nav class="pipelines-breadcrumb" aria-label="Breadcrumb">
          <NuxtLink :to="`/p/${projectPath}`">{{ projectPath }}</NuxtLink>
          <span aria-hidden="true">/</span>
          <span aria-current="page">Pipelines</span>
        </nav>
        <h1 class="pipelines-title">Pipelines</h1>
        <p class="pipelines-description">
          Builds, tests and deployments for this project.
          Configuration: <code class="mono">.dogit-ci.yml</code>
        </p>
      </div>
      <button class="btn btn-primary pipelines-new" type="button" :disabled="starting" @click="start">
        <span class="new-plus" aria-hidden="true">＋</span>
        {{ starting ? 'Starting…' : 'New pipeline' }}
      </button>
    </header>

    <div class="pipelines-summary" aria-label="Pipeline summary for this page">
      <div class="summary-item">
        <span class="summary-value">{{ total }}</span>
        <span class="summary-label">Matching runs</span>
      </div>
      <div class="summary-divider" />
      <div class="summary-item">
        <span class="summary-value summary-running">{{ pipelines.filter(run => isLive(run)).length }}</span>
        <span class="summary-label">Active on this page</span>
      </div>
      <div class="summary-divider" />
      <div class="summary-item">
        <span class="summary-value summary-failed">{{ pipelines.filter(run => run.status === 'failed').length }}</span>
        <span class="summary-label">Failed on this page</span>
      </div>
      <span class="summary-context">Current results</span>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div class="pipeline-toolbar">
      <div class="toolbar-heading">
        <span class="toolbar-title">Run history</span>
        <span class="toolbar-count">{{ range }}</span>
        <span v-if="loading" class="refreshing"><span class="refreshing-dot" /> Updating</span>
      </div>
      <div class="toolbar">
      <label class="search-wrap">
        <span class="search-icon" aria-hidden="true">⌕</span>
        <input
        v-model="typing"
        class="pipelines-filter"
        type="search"
        placeholder="Search runs, branches and commits"
        aria-label="Search runs"
      />
      </label>

      <select
        :value="view.ref"
        aria-label="Branch"
        @change="remember({ ref: ($event.target as HTMLSelectElement).value })"
      >
        <option value="">Any branch</option>
        <option v-for="branch in branches" :key="branch" :value="branch">{{ branch }}</option>
        <option v-if="view.ref && !branches.includes(view.ref)" :value="view.ref">
          {{ view.ref }} (not on this page)
        </option>
      </select>

      <select
        :value="view.status"
        aria-label="State"
        @change="remember({ status: ($event.target as HTMLSelectElement).value })"
      >
        <option v-for="one in states" :key="one.value" :value="one.value">{{ one.label }}</option>
      </select>

      <select
        :value="view.source"
        aria-label="What started it"
        @change="remember({ source: ($event.target as HTMLSelectElement).value })"
      >
        <option value="">Pushes and people</option>
        <option value="push">Pushes</option>
        <option value="manual">Started by hand</option>
      </select>
      <button
        v-if="view.search || view.status || view.ref || view.source"
        type="button"
        class="clear-filters"
        @click="clearFilters"
      >Clear filters</button>
      </div>
    </div>

    <div v-if="loading && pipelines.length === 0" class="spinner pipelines-loading">Loading pipelines…</div>

    <div v-else-if="pipelines.length === 0 && !view.search && !view.status && !view.ref && !view.source" class="card empty pipelines-empty">
      <div class="empty-symbol" aria-hidden="true">⌁</div>
      <h3>No pipelines yet</h3>
      <p>Add a <code class="mono">.dogit-ci.yml</code> to the project, then start a run to see its progress here.</p>
      <button class="btn btn-primary" type="button" :disabled="starting" @click="start">
        {{ starting ? 'Starting…' : 'Run a pipeline' }}
      </button>
    </div>

    <div v-else-if="pipelines.length === 0" class="card empty pipelines-empty">
      <div class="empty-symbol" aria-hidden="true">⌕</div>
      <h3>No matching runs</h3>
      <p>
        <template v-if="view.search">No runs match “{{ view.search }}”.</template>
        <template v-else>No runs match the selected filters.</template>
        Try broadening your search or clearing the filters.
      </p>
      <button class="btn btn-small" type="button" @click="clearFilters()">Clear filters</button>
    </div>

    <div v-else class="pipeline-table-wrap" :aria-busy="loading">
    <table class="admin-table pipeline-table">
      <thead>
        <tr>
          <th class="col-status">Status</th>
          <th class="col-pipeline">Run details</th>
          <th class="col-by">Triggered by</th>
          <th class="col-stages">Stages <span class="th-hint">· select a stage for jobs</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="run in pipelines" :key="run.iid">
          <td class="col-status">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines/${run.iid}`" class="result-link">
              <span class="badge badge-label" :class="statusClass[run.status] ?? 'badge-neutral'">
                {{ statusText[run.status] }}
              </span>
              <span class="cell-line mono duration">
                {{ isLive(run) ? 'Running now' : formatDuration(run.duration_ms) }}
              </span>
              <span class="cell-line muted small">{{ timeAgo(run.created_at) }}</span>
            </NuxtLink>
          </td>

          <td class="col-pipeline">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines/${run.iid}`" class="cell-link">
              <span class="cell-line mono number">Pipeline #{{ run.iid }}</span>
              <span class="cell-line title commit-title" :title="run.title">{{ run.title || 'No commit message' }}</span>
              <span class="cell-line meta">
                <span class="branch">
                  <span class="branch-icon" aria-hidden="true">⑂</span>{{ run.ref }}
                </span>
                <span class="sha mono">
                  <span class="sha-icon" aria-hidden="true">⌘</span>{{ run.sha.slice(0, 8) }}
                </span>
                <img
                  v-if="avatar(run.avatar_url, run.author_name)"
                  class="avatar-img"
                  :src="run.avatar_url"
                  :alt="run.author_name"
                  width="14"
                  height="14"
                  @error="brokenAvatars[run.author_name ?? ''] = true"
                />
                <UserAvatar v-else-if="run.author_name" :name="run.author_name" :size="14" />
                <span v-if="run.jobs" class="muted small">
                  {{ run.jobs }} job{{ run.jobs === 1 ? '' : 's' }}
                </span>
              </span>
            </NuxtLink>
          </td>

          <td class="col-by">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines/${run.iid}`" class="cell-link">
              <img
                v-if="run.triggered_by && avatar(run.triggered_by.avatar_url, run.triggered_by.username)"
                class="avatar-img big"
                :src="run.triggered_by.avatar_url"
                :alt="run.triggered_by.name"
                width="32"
                height="32"
                @error="brokenAvatars[run.triggered_by.username] = true"
              />
              <UserAvatar
                v-else
                :name="run.triggered_by?.name || 'push'"
                :size="32"
              />
              <span class="by-name">
                {{ run.triggered_by?.name || run.triggered_by?.username || 'Push' }}
              </span>
            </NuxtLink>
          </td>

          <td class="col-stages" @mouseleave="closeStage">
            <div v-if="stageMarks(run).length" class="stages" :aria-label="`${stageMarks(run).length} stages`">
              <button
                v-for="stage in stageMarks(run)"
                :key="stage.name"
                type="button"
                class="stage-mark"
                :class="[statusClass[stage.status] ?? 'badge-neutral', { open: openStage === stageKey(run, stage.name) }]"
                :aria-label="stage.title"
                :aria-expanded="openStage === stageKey(run, stage.name)"
                @click="toggleStage(run, stage.name)"
              />

              <!-- The stage's jobs. Kept out of the row because a pipeline of thirty
                   jobs would make the table unreadable; what a mark opens is the
                   names, and a way to run the failed one again. -->
              <div v-if="openedStage(run)" class="stage-pop">
                <div class="stage-pop-head">Stage: {{ openedStage(run)!.name }}</div>
                <div v-for="entry in openedStage(run)!.jobs" :key="entry.iid" class="stage-pop-job">
                  <span class="pop-dot" :class="statusClass[entry.status] ?? 'badge-neutral'" />
                  <NuxtLink
                    :to="`/p/${projectPath}/-/pipelines/${run.iid}/jobs/${entry.iid}`"
                    class="pop-name"
                  >
                    {{ entry.name }}
                  </NuxtLink>
                  <button
                    type="button"
                    class="pop-retry"
                    :title="`Retry ${entry.name}`"
                    :aria-label="`Retry ${entry.name}`"
                    :disabled="entry.status === 'running' || entry.status === 'pending'"
                    @click="retry(run, entry)"
                  ><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></button>
                </div>
              </div>
            </div>
            <span v-else class="no-stages">No stage details</span>
          </td>
        </tr>
      </tbody>
    </table>
    <div class="stage-legend" aria-label="Stage status legend">
      <span><i class="legend-dot legend-success" /> Passed</span>
      <span><i class="legend-dot legend-running" /> Running</span>
      <span><i class="legend-dot legend-failed" /> Failed</span>
      <span><i class="legend-dot legend-pending" /> Waiting</span>
      <span><i class="legend-dot legend-skipped" /> Skipped</span>
    </div>
    </div>

    <div v-if="total > 0" class="pipelines-pager">
      <span class="muted small">{{ range }}</span>
      <div class="spacer" />
      <button
        class="btn btn-small"
        type="button"
        :disabled="page <= 1"
        @click="remember({ page: page - 1 })"
      >
        Newer
      </button>
      <span class="page-indicator">Page <strong>{{ page }}</strong> of {{ pages }}</span>
      <button
        class="btn btn-small"
        type="button"
        :disabled="page >= pages"
        @click="remember({ page: page + 1 })"
      >
        Older
      </button>
    </div>
  </section>
</template>

<style scoped>
.pipelines-page {
  --pipeline-line: color-mix(in srgb, var(--border) 82%, transparent);
  max-width: 1440px;
  margin: 0 auto;
  color: var(--text);
}

 .pipelines-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 24px;
  padding: 10px 0 22px;
}

.pipelines-heading-copy { min-width: 0; }

.pipelines-breadcrumb {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 12px;
  color: var(--text-muted);
  font-size: 12px;
}
.pipelines-breadcrumb a {
  max-width: min(55vw, 420px);
  overflow: hidden;
  color: var(--text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pipelines-breadcrumb a:hover { color: var(--text); }
.pipelines-breadcrumb span[aria-current="page"] { color: var(--text); font-weight: 600; }

.pipelines-title {
  margin: 0;
  font-size: clamp(27px, 3vw, 34px);
  font-weight: 680;
  letter-spacing: -.045em;
  line-height: 1.15;
}

.pipelines-description {
  max-width: 660px;
  margin: 9px 0 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.65;
}

.pipelines-description code {
  padding: 2px 5px;
  border: 1px solid var(--pipeline-line);
  border-radius: 5px;
  color: var(--text);
  font-size: 12px;
}

.pipelines-new {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-height: 38px;
  padding: 9px 14px;
  border-radius: 8px;
  white-space: nowrap;
  box-shadow: 0 2px 7px rgba(0, 0, 0, .12);
}

.new-plus { font-size: 17px; line-height: 12px; }

.pipelines-summary {
  display: flex;
  align-items: center;
  gap: 22px;
  min-height: 78px;
  padding: 15px 20px;
  margin-bottom: 22px;
  border: 1px solid var(--pipeline-line);
  border-radius: 11px;
  background: var(--bg-elevated);
}

.summary-item { display: flex; flex-direction: column; gap: 4px; }
.summary-value {
  font-size: 21px;
  line-height: 1;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
  letter-spacing: -.035em;
}
.summary-label { color: var(--text-muted); font-size: 11px; }
.summary-running { color: var(--info, #58a6ff); }
.summary-failed { color: var(--red, #f85149); }
.summary-divider { width: 1px; height: 34px; background: var(--pipeline-line); }
 .summary-context {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  margin-left: auto;
  color: var(--text-muted);
  font-size: 11px;
}

.pipeline-toolbar {
  overflow: visible;
  margin-bottom: 0;
  border: 1px solid var(--pipeline-line);
  border-bottom: 0;
  border-radius: 11px 11px 0 0;
  background: var(--bg-elevated);
}

.toolbar-heading {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 15px 16px 2px;
}
.toolbar-title { font-size: 13px; font-weight: 650; }
.toolbar-count { color: var(--text-muted); font-size: 11px; }

 .pipeline-toolbar .toolbar {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 12px 16px 15px;
  margin: 0;
  border: 0;
}

 .search-wrap {
  position: relative;
  display: block;
  flex: 1 1 280px;
  min-width: 220px;
  max-width: 440px;
  margin: 0;
}
.search-icon {
  position: absolute;
  z-index: 1;
  left: 12px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  font-size: 19px;
  pointer-events: none;
}
.pipeline-toolbar .filter {
  width: 100%;
  min-width: 0;
  min-height: 36px;
  padding: 8px 12px 8px 35px;
  background: var(--bg-inset);
  border-color: var(--pipeline-line);
  border-radius: 7px;
}

 .pipeline-toolbar select {
  min-height: 36px;
  max-width: 200px;
  padding: 7px 30px 7px 10px;
  border-radius: 7px;
  background-color: var(--bg-inset);
  border-color: var(--pipeline-line);
}

 .pipeline-table-wrap {
  overflow-x: auto;
  border: 1px solid var(--pipeline-line);
  border-radius: 0 0 11px 11px;
  background: var(--bg-elevated);
}

 .pipelines-loading {
  border: 1px solid var(--pipeline-line);
  border-radius: 0 0 11px 11px;
}

.pipelines-empty {
  margin-top: 0;
  padding: 52px 24px;
  border: 1px solid var(--pipeline-line);
  border-radius: 0 0 11px 11px;
  background: var(--bg-elevated);
}
.pipelines-empty h3 { margin: 12px 0 6px; color: var(--text); font-size: 16px; }
.pipelines-empty p {
  max-width: 440px;
  margin: 0 auto 18px;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.6;
}
.empty-symbol {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  margin: 0 auto;
  border: 1px solid var(--pipeline-line);
  border-radius: 12px;
  background: var(--bg-inset);
  color: var(--accent);
  font-size: 27px;
}

@media (max-width: 760px) {
  .pipelines-heading { gap: 14px; flex-direction: column; }
  .pipelines-new { align-self: flex-start; }
  .pipelines-summary { gap: 12px; padding: 13px; }
  .summary-context { display: none; }
  .summary-value { font-size: 18px; }
  .summary-label { max-width: 88px; font-size: 10px; }
  .pipeline-toolbar .toolbar { align-items: stretch; }
  .pipeline-toolbar .search-wrap { flex: 1 1 100%; min-width: 0; max-width: none; }
  .pipeline-toolbar select { flex: 1 1 calc(50% - 8px); min-width: 0; max-width: none; }
  .summary-context { display: none; }
  .pipeline-toolbar .toolbar { gap: 8px; }
  .th-hint { display: none; }
  .stage-legend { gap: 10px; }
  .pipeline-table td.col-pipeline { min-width: 280px; }
  .pipeline-table { min-width: 760px; }
}

.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}

/* The search takes what is left; the filters are as wide as their words. Inputs are
   full width everywhere else here, which is right in a form and wrong in a toolbar:
   three stacked filters read as three separate controls. */
.toolbar select {
  flex: 0 0 auto;
  width: auto;
}

.pager {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
}

.pager .spacer {
  flex: 1;
}

.filter {
  flex: 1;
  max-width: 480px;
  padding: 7px 10px;
  font: inherit;
  font-size: 13px;
  color: inherit;
  background: var(--bg, transparent);
  border: 1px solid var(--border);
  border-radius: 6px;
}

.filter:focus {
  outline: 2px solid var(--accent);
  outline-offset: -1px;
}

/* A table because a pipeline is a fixed set of facts that line up in columns. */
.pipeline-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

 .pipeline-table thead {
  background: color-mix(in srgb, var(--bg-inset) 65%, var(--bg-elevated));
}
.pipeline-table th {
  text-align: left;
  font-weight: 600;
  font-size: 12px;
  color: var(--text-muted);
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}

.pipeline-table td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

/* The rows are positioned so that a popover opened from one is not painted over
   by the rows below it. */
.pipeline-table tbody tr {
  position: relative;
}
.pipeline-table td.col-stages {
  position: relative;
}

.pipeline-table tr:hover td {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.03));
}

 .col-status { width: 148px; }
.col-by { width: 190px; }
.col-stages { width: 180px; }

.result-link { display: block; }
 .result-state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 650;
}
.result-symbol {
  display: inline-grid;
  place-items: center;
  width: 19px;
  height: 19px;
  border-radius: 50%;
  background: color-mix(in srgb, currentColor 15%, transparent);
  font-size: 12px;
  line-height: 1;
}
 .commit-title { display: block; max-width: 460px; font-weight: 550; line-height: 1.45; }
.pipeline-table td.col-pipeline { min-width: 330px; }
.pipeline-table td.col-status { white-space: nowrap; }
.pipeline-table td.col-by { min-width: 160px; }
.pipeline-table tbody tr { transition: background .15s ease; }
.pipeline-table tbody tr:hover td { background: color-mix(in srgb, var(--accent) 5%, var(--bg-elevated)); }
.pipeline-table tbody tr:hover .commit-title { color: var(--accent); }


/* One column holds one fact, and the fact is the whole cell — no underlines
   under text that is a link, no hover affordance pretending to be a button. */
td a,
td a:visited,
td a:hover {
  color: inherit;
  text-decoration: none;
}

.cell-line {
  display: block;
}

.duration {
  font-variant-numeric: tabular-nums;
  margin-top: 4px;
}

/* The commit's message is the answer to "what was this for", so it is the only
   part of the column that is not muted: the rest is context for it. */
.title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 460px;
}

.number {
  color: var(--text-muted);
}

.meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  flex-wrap: wrap;
}

.branch,
.sha {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  padding: 1px 7px;
  border-radius: 10px;
  border: 1px solid var(--border);
  color: var(--text-muted);
}

.branch-icon,
.sha-icon {
  opacity: 0.7;
}

.avatar-img {
  border-radius: 50%;
  flex: 0 0 auto;
}

.avatar-img.big {
  width: 32px;
  height: 32px;
}

.cell-link {
  display: flex;
  align-items: center;
  gap: 10px;
}

.by-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Stages as marks in a row, no names: how far it got, read at a glance. The
   names are one click away, where there is room for them. */
/* The marks, and the anchor for what they open. A row is three lines tall, so
   positioning the popover against the cell would drop it below the whole row —
   a long way from the dot that was clicked, and further down with every row that
   had more stages. */
 .stages {
  display: flex;
  align-items: center;
  gap: 2px;
  position: relative;
}

/* The mark itself is small — it is a dot in a column of dots — but what takes the
   click is the padding around it. A fourteen-pixel target has to be hit exactly,
   and asking somebody to do that to see a job's name is asking too much. */
.stage-mark {
  width: 26px;
  height: 28px;
  flex: 0 0 26px;
  padding: 0;
  border: 1px solid var(--pipeline-line);
  border-radius: 7px;
  background: var(--bg-inset);
  cursor: pointer;
  position: relative;
  transition: border-color .15s ease, background .15s ease, transform .15s ease;
}

.stage-mark::after {
  content: '';
  position: absolute;
  inset: 8px;
  border-radius: 50%;
  background: currentColor;
}

.stage-mark:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.stage-mark:hover {
  transform: translateY(-1px);
  border-color: currentColor;
}

.stage-mark.open {
  background: color-mix(in srgb, currentColor 12%, var(--bg-elevated));
  border-color: currentColor;
  box-shadow: inset 0 0 0 1px currentColor;
}

.stage-mark.badge-green { color: #3fb950; }
.stage-mark.badge-danger { color: #f85149; }
.stage-mark.badge-warning { color: #d29922; }
.stage-mark.badge-neutral { color: #6e7681; }
/* Waiting, in the blue an avatar is drawn in: hsl(220 55% 32%).
   Without a rule of its own a mark in this state takes currentColor, which is the
   page's text colour — a pale blue-grey that reads as "dimmed" rather than as
   waiting, and that no other mark on the page is allowed to be. */
.stage-mark.badge-blue { color: hsl(220 55% 32%); }
.stage-mark.badge-private { color: #a371f7; }

/* The stage's jobs, under the mark that opened them. Positioned rather than in
   the row so that opening one does not push the table about under the pointer. */
.stage-pop {
  position: absolute;
  z-index: 20;
  /* Under the marks themselves, and to their left so that the last stage of a
     pipeline — the rightmost mark, and the one people look for — opens its list
     inwards rather than off the edge of the table. */
  right: 0;
  top: calc(100% + 2px);
  width: 260px;
  max-height: 320px;
  overflow: auto;
  padding: 6px;
  background: var(--bg-elevated, #1f2126);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
}

.stage-pop-head {
  padding: 4px 8px 6px;
  font-size: 12px;
  color: var(--text-muted);
}

.stage-pop-job {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border-radius: 4px;
}

.stage-pop-job:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.05));
}

.pop-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: currentColor;
  flex: 0 0 auto;
}

.pop-dot.badge-green { background: #3fb950; }
.pop-dot.badge-danger { background: #f85149; }
.pop-dot.badge-warning { background: #d29922; }
.pop-dot.badge-neutral { background: #6e7681; }
.pop-dot.badge-private { background: #a371f7; }

.pop-name {
  flex: 1;
  color: inherit;
  text-decoration: none;
}

/* Run it again. Small, because it is offered on every job and used once. */
.pop-retry {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 24px;
  width: 24px;
  height: 24px;
  padding: 0;
  border: none;
  border-radius: 4px;
  background: none;
  color: var(--text-muted);
  cursor: pointer;
}

.pop-retry svg {
  display: block;
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.pop-retry:hover:not(:disabled) {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.1));
  color: var(--text);
}

.pop-retry:disabled {
  opacity: 0.35;
  cursor: default;
}

.small {
  font-size: 12px;
}

.refreshing { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 11px; }
.refreshing-dot { background: var(--accent); animation: pipeline-pulse 1.2s ease-in-out infinite; }
.clear-filters {
  flex: 0 0 auto;
  border: 0;
  padding: 7px 5px;
  background: transparent;
  color: var(--accent);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.clear-filters:hover { text-decoration: underline; }
.stage-legend {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 15px;
  padding: 11px 14px;
  border-top: 1px solid var(--pipeline-line);
  color: var(--text-muted);
  font-size: 11px;
}
.stage-legend span { display: inline-flex; align-items: center; gap: 6px; }
.legend-dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; }
.legend-success { background: var(--green); }
.legend-running { background: var(--yellow); }
.legend-failed { background: var(--red); }
.legend-pending { background: var(--accent); }
.legend-skipped { background: #6e7681; }
.no-stages { color: var(--text-muted); font-size: 11px; }
.page-indicator { color: var(--text-muted); font-size: 12px; }
.page-indicator strong { color: var(--text); font-weight: 650; }
@keyframes pipeline-pulse { 50% { opacity: .4; } }

@media (prefers-reduced-motion: reduce) {
  .stage-mark, .pipeline-table tbody tr { transition: none; }
  .refreshing-dot { animation: none; }
}
</style>