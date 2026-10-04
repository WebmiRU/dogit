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
import type { ModulePipeline, PipelineStage, PipelineStageJob } from '~/types/pipeline'
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

    pipelines.value = answer.pipelines ?? []
    total.value = answer.total ?? 0
    page.value = answer.page ?? 1
    pages.value = Math.max(1, answer.pages ?? 1)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
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
      void load()
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
  <div>
    <div class="repo-head">
      <div class="title">
        <h2 class="section-title" style="margin: 0">Pipelines</h2>
        <p class="page-subtitle">
          Runs of this project's <code class="mono">.dogit-ci.yml</code>, whether a
          push started one or you did.
        </p>
      </div>
      <button class="btn btn-primary" type="button" :disabled="starting" @click="start">
        {{ starting ? 'Starting…' : 'New pipeline' }}
      </button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div class="toolbar">
      <input
        v-model="typing"
        class="filter"
        type="search"
        placeholder="Search runs, branches and commits"
        aria-label="Search runs"
      />

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
    </div>

    <div v-if="loading && pipelines.length === 0" class="spinner">Loading pipelines…</div>

    <div v-else-if="pipelines.length === 0" class="card empty">
      Nothing has run yet. Add a <code class="mono">.dogit-ci.yml</code> to the project
      and press “New pipeline”.
    </div>

    <div v-else-if="pipelines.length === 0" class="card empty">
      Nothing runs here
      <template v-if="view.search">that matches “{{ view.search }}”</template>
      <template v-else-if="view.status || view.ref">with these filters</template>.
      <button class="btn btn-small" type="button" @click="clearFilters()">Clear the filters</button>
    </div>

    <table v-else class="pipeline-table">
      <thead>
        <tr>
          <th class="col-status">Status</th>
          <th class="col-pipeline">Pipeline</th>
          <th class="col-by">Created by</th>
          <th class="col-stages">Stages</th>
          <th class="col-actions">Actions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="run in pipelines" :key="run.iid">
          <td class="col-status">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines/${run.iid}`">
              <span class="badge" :class="statusClass[run.status] ?? 'badge-neutral'">
                {{ statusText[run.status] }}
              </span>
              <span class="cell-line mono duration">
                {{ isLive(run) ? 'in progress' : formatDuration(run.duration_ms) }}
              </span>
              <span class="cell-line muted small">{{ timeAgo(run.created_at) }}</span>
            </NuxtLink>
          </td>

          <td class="col-pipeline">
            <NuxtLink :to="`/p/${projectPath}/-/pipelines/${run.iid}`" class="cell-link">
              <span class="cell-line mono number">#{{ run.iid }}</span>
              <span class="cell-line title" :title="run.title">{{ run.title || '—' }}</span>
              <span class="cell-line meta">
                <span class="branch">
                  <span class="branch-icon">⑂</span>{{ run.ref }}
                </span>
                <span class="sha mono">
                  <span class="sha-icon">⟨⟩</span>{{ run.sha.slice(0, 8) }}
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
            <div class="stages">
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
                  >↻</button>
                </div>
              </div>
            </div>
          </td>

          <!-- Artifacts. Nothing has been declared yet, so this is here as the
               place they will appear rather than as a promise of a download: a
               button that cannot download anything is worse than no button. -->
          <td class="col-actions">
            <button
              type="button"
              class="download"
              disabled
              title="No artifacts have been declared yet"
              aria-label="Download artifacts"
            ><span class="download-arrow">⬇</span></button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="total > 0" class="pager">
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
      <span class="muted small">page {{ page }} of {{ pages }}</span>
      <button
        class="btn btn-small"
        type="button"
        :disabled="page >= pages"
        @click="remember({ page: page + 1 })"
      >
        Older
      </button>
    </div>
  </div>
</template>

<style scoped>
.section-title {
  font-size: 18px;
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

/* Artifacts, not yet implemented. Disabled rather than hidden so that the
   column's position is already where the button will be. */
/* Just the arrow. It is disabled until there is something to download, and a wide
   button saying so on every row of a list of runs is a lot of words about nothing. */
.download {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 5px 7px;
  font: inherit;
  font-size: 12px;
  color: var(--text-muted);
  background: var(--bg-subtle, rgba(255, 255, 255, 0.05));
  border: 1px solid var(--border);
  border-radius: 6px;
  cursor: not-allowed;
  opacity: 0.6;
}

.download-arrow {
  font-size: 14px;
  line-height: 1;
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

.col-status { width: 132px; }
.col-by { width: 190px; }
.col-stages { width: 150px; }
.col-actions { width: 140px; }

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
  height: 26px;
  padding: 0;
  border: none;
  border-radius: 6px;
  background: none;
  cursor: pointer;
  position: relative;
}

.stage-mark::after {
  content: '';
  position: absolute;
  inset: 6px;
  border-radius: 50%;
  background: currentColor;
}

.stage-mark:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.08));
}

.stage-mark.open {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.12));
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
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 4px;
  background: none;
  color: var(--text-muted);
  font-size: 14px;
  cursor: pointer;
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
</style>