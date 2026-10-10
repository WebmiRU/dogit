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

// The row menu is teleported so the table's horizontal scroll container cannot clip it.
const openActions = ref<number | null>(null)
const downloadingLogs = ref<number | null>(null)
const actionPanel = ref({ top: 0, left: 0, up: false })

function jobsOf(run: ModulePipeline): PipelineStageJob[] {
  return (run.stages ?? []).flatMap((stage) => stage.jobs ?? [])
}

function failedJobs(run: ModulePipeline): PipelineStageJob[] {
  return jobsOf(run).filter((job) => job.status === 'failed')
}

function toggleActions(run: ModulePipeline, event: MouseEvent) {
  if (openActions.value === run.iid) {
    openActions.value = null
    return
  }

  const box = (event.currentTarget as HTMLElement).getBoundingClientRect()
  const up = window.innerHeight - box.bottom < 190
  actionPanel.value = {
    top: up ? box.top - 6 : box.bottom + 6,
    left: Math.max(8, Math.min(box.right - 210, window.innerWidth - 218)),
    up,
  }
  openActions.value = run.iid
}

function closeActions() {
  openActions.value = null
}

function onActionsPointerAway(event: PointerEvent) {
  if (openActions.value === null) return
  const target = event.target as HTMLElement | null
  if (target?.closest('.pipeline-actions-menu') || target?.closest('.pipeline-actions-trigger')) return
  closeActions()
}

function onActionsKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') closeActions()
}

async function downloadLogs(run: ModulePipeline) {
  closeActions()
  const jobs = jobsOf(run)
  if (!jobs.length) return

  downloadingLogs.value = run.iid
  try {
    const sections = await Promise.all(jobs.map(async (job) => {
      const response = await fetch(
        rawApiUrl(`/projects/${apiRef.value}/pipelines/${run.iid}/jobs/${job.iid}/log`),
        { credentials: 'include' },
      )
      const output = response.ok ? await response.text() : '(Log is not available yet.)'
      return `===== ${job.name} · ${job.status} =====\n\n${output.trimEnd()}\n`
    }))
    const blob = new Blob([sections.join('\n')], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `pipeline-${run.iid}-logs.txt`
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    notify(`Logs for pipeline #${run.iid} downloaded`, { type: 'success' })
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : 'could not download pipeline logs'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    downloadingLogs.value = null
  }
}

async function retryFailed(run: ModulePipeline) {
  closeActions()
  const jobs = failedJobs(run)
  if (!jobs.length) return

  error.value = ''
  try {
    for (const job of jobs) {
      await api.post(`/projects/${apiRef.value}/pipelines/${run.iid}/jobs/${job.iid}/retry`, {})
    }
    notify(`Retrying ${jobs.length} failed job${jobs.length === 1 ? '' : 's'} in pipeline #${run.iid}`, { type: 'success' })
    await load(true)
  } catch (caught) {
    const message = caught instanceof ApiError ? caught.message : 'the jobs could not be retried'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  }
}

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
  window.addEventListener('pointerdown', onActionsPointerAway, true)
  window.addEventListener('keydown', onActionsKeydown)
  stopWatching = watchEvents({
    kinds: ['pipeline.created', 'pipeline.updated'],
    project: () => props.projectPath,
    onChange: onChanged,
  })
})

onBeforeUnmount(() => {
  stopWatching?.()
  window.removeEventListener('pointerdown', onActionsPointerAway, true)
  window.removeEventListener('keydown', onActionsKeydown)
})

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
      <div class="pipeline-filters">
      <label class="search-wrap">
        <span class="search-icon" aria-hidden="true">
          <svg viewBox="0 0 20 20"><circle cx="8.5" cy="8.5" r="5.5"/><path d="m12.5 12.5 4 4"/></svg>
        </span>
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
    <table class="admin-table repository-flat-table pipeline-table">
      <thead>
        <tr>
          <th class="col-status">Status</th>
          <th class="col-pipeline">Run details</th>
          <th class="col-by">Triggered by</th>
          <th class="col-stages">Stages</th>
          <th class="col-actions">Actions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="run in pipelines" :key="run.iid">
          <td class="col-status">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines/${run.iid}`" class="result-link">
              <span class="result-primary">
                <span class="badge badge-label" :class="statusClass[run.status] ?? 'badge-neutral'">
                  {{ statusText[run.status] }}
                </span>
                <span class="mono duration">
                  {{ isLive(run) ? 'Running now' : formatDuration(run.duration_ms) }}
                </span>
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
              >
                <svg v-if="stage.status === 'success'" viewBox="0 0 20 20" aria-hidden="true">
                  <circle cx="10" cy="10" r="8.25" />
                  <path d="m6.2 10.1 2.5 2.5 5.1-5.2" />
                </svg>
                <svg v-else-if="stage.status === 'failed' || stage.status === 'canceled'" viewBox="0 0 20 20" aria-hidden="true">
                  <circle cx="10" cy="10" r="8.25" />
                  <path d="m7 7 6 6m0-6-6 6" />
                </svg>
                <svg v-else-if="stage.status === 'running'" viewBox="0 0 20 20" aria-hidden="true">
                  <circle cx="10" cy="10" r="8.25" />
                  <path d="M10 5.5v4.7l3.1 1.8" />
                </svg>
                <svg v-else-if="stage.status === 'pending'" viewBox="0 0 20 20" aria-hidden="true">
                  <circle cx="10" cy="10" r="8.25" stroke-dasharray="2.2 2.2" />
                  <path d="M10 6.2v4.1h3" />
                </svg>
                <svg v-else viewBox="0 0 20 20" aria-hidden="true">
                  <circle cx="10" cy="10" r="8.25" />
                  <path d="M6.5 10h7" />
                </svg>
              </button>

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

          <td class="col-actions">
            <button
              type="button"
              class="pipeline-actions-trigger"
              :aria-label="`Actions for pipeline #${run.iid}`"
              :aria-expanded="openActions === run.iid"
              aria-haspopup="menu"
              :disabled="!jobsOf(run).length"
              @click.stop="toggleActions(run, $event)"
            >
              <svg viewBox="0 0 20 20" aria-hidden="true">
                <path d="M10 2.5v9" />
                <path d="m6.5 8.5 3.5 3.5 3.5-3.5" />
                <path d="M3.5 13.5v3h13v-3" />
              </svg>
              <svg class="action-chevron" viewBox="0 0 16 16" aria-hidden="true">
                <path d="m4 6 4 4 4-4" />
              </svg>
            </button>
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
  
    <Teleport to="#teleports">
      <div
        v-if="openActions !== null"
        class="pipeline-actions-menu"
        :class="{ up: actionPanel.up }"
        :style="{ top: `${actionPanel.top}px`, left: `${actionPanel.left}px` }"
        role="menu"
      >
        <button
          type="button"
          role="menuitem"
          :disabled="downloadingLogs === openActions"
          @click="downloadLogs(pipelines.find(run => run.iid === openActions)!)"
        >
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M10 2.5v9"/><path d="m6.5 8.5 3.5 3.5 3.5-3.5"/><path d="M3.5 13.5v3h13v-3"/></svg>
          {{ downloadingLogs === openActions ? 'Preparing logs…' : 'Download logs' }}
        </button>
        <button
          v-if="pipelines.some(run => run.iid === openActions && failedJobs(run).length > 0)"
          type="button"
          role="menuitem"
          @click="retryFailed(pipelines.find(run => run.iid === openActions)!)"
        >
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3.5 6.5V3m0 3.5H7"/><path d="M4 6a7 7 0 1 1-1 6"/></svg>
          Retry failed jobs
        </button>
        <NuxtLink
          v-if="pipelines.some(run => run.iid === openActions)"
          role="menuitem"
          :to="`/p/${projectPath}/-/pipelines/${openActions}`"
          @click="closeActions"
        >
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M11 3.5h5.5V9"/><path d="m16 4-7 7"/><path d="M14 11v4.5H4.5V6H9"/></svg>
          Open pipeline
        </NuxtLink>
      </div>
    </Teleport>
</section>
</template>
