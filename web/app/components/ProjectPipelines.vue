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
  projectPath: string
  defaultBranch?: string
}>()

const { add: notify } = useNotifyPool()

const pipelines = ref<ModulePipeline[]>([])
const loading = ref(true)
const error = ref('')
const starting = ref(false)
const filter = ref('')

/**
 * What the filter matches.
 *
 * Matched against everything a row says, because a person looking for a run
 * remembers whichever of those they happen to remember: the number, a word from
 * the commit message, the branch. Making them remember which of them is searchable
 * would be the filter's job, not theirs.
 */
const visible = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return pipelines.value

  return pipelines.value.filter((run) =>
    [
      String(run.iid),
      run.title ?? '',
      run.ref,
      run.sha,
      run.author_name ?? '',
      run.triggered_by?.name ?? '',
      run.triggered_by?.username ?? '',
      statusText[run.status],
    ]
      .join(' ')
      .toLowerCase()
      .includes(needle),
  )
})

/**
 * Reads the list.
 *
 * The list is fetched again rather than patched when something changes: an event
 * says a pipeline moved, not what it now looks like, and re-reading one small
 * endpoint is cheaper than being wrong.
 */
async function load(quiet = false) {
  // A reload that follows an event leaves the rows alone. Refreshing the whole list
  // every few seconds would move the row somebody is reading out from under them.
  if (!quiet) loading.value = true
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
 * The configuration is read from the commit, so a project with no .dogit-ci.yml
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
    await api.post(`/projects/${encodeURIComponent(props.projectPath)}/pipelines/${run.iid}/jobs/${job.iid}/retry`, {})
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

/** The stages as marks in order, coloured by how each one went. */
function stageMarks(stages: PipelineStage[] | undefined) {
  return (stages ?? []).map((stage) => ({
    ...stage,
    title: `${stage.name}: ${statusText[stage.status]}`,
  }))
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
        v-model="filter"
        class="filter"
        type="search"
        placeholder="Filter pipelines"
        aria-label="Filter pipelines"
      />
    </div>

    <div v-if="loading && pipelines.length === 0" class="spinner">Loading pipelines…</div>

    <div v-else-if="pipelines.length === 0" class="card empty">
      Nothing has run yet. Add a <code class="mono">.dogit-ci.yml</code> to the project
      and press “New pipeline”.
    </div>

    <div v-else-if="visible.length === 0" class="card empty">
      Nothing matches “{{ filter }}”.
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
        <tr v-for="run in visible" :key="run.iid">
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
                v-for="stage in stageMarks(run.stages)"
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
            >⬇ <span class="download-label">Download artifacts</span> <span class="chev">▾</span></button>
          </td>
        </tr>
      </tbody>
    </table>
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
.download {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  font: inherit;
  font-size: 12px;
  color: var(--text-muted);
  background: var(--bg-subtle, rgba(255, 255, 255, 0.05));
  border: 1px solid var(--border);
  border-radius: 6px;
  cursor: not-allowed;
  opacity: 0.6;
}

.download .chev {
  opacity: 0.6;
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