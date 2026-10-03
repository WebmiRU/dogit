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
import type { ModulePipeline, PipelineStage } from '~/types/pipeline'
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

onMounted(load)
watch(() => props.projectPath, load)
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

          <td class="col-stages">
            <NuxtLink
              :to="`/p/${projectPath}/-/pipelines/${run.iid}`"
              class="cell-link stages"
            >
              <span
                v-for="stage in stageMarks(run.stages)"
                :key="stage.name"
                class="stage-dot"
                :class="statusClass[stage.status] ?? 'badge-neutral'"
                :title="stage.title"
              />
            </NuxtLink>
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

.pipeline-table tr:hover td {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.03));
}

.col-status { width: 132px; }
.col-by { width: 190px; }
.col-stages { width: 120px; }

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
   names are on the run's own page, where there is room for them. */
.stages {
  gap: 6px;
}

.stage-dot {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 1px solid currentColor;
  display: inline-block;
}

.stage-dot.badge-green { background: #3fb950; }
.stage-dot.badge-danger { background: #f85149; }
.stage-dot.badge-warning { background: #d29922; }
.stage-dot.badge-neutral { background: #6e7681; }
.stage-dot.badge-private { background: #a371f7; }

.small {
  font-size: 12px;
}
</style>