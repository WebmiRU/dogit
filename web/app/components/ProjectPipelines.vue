<script setup lang="ts">
/**
 * A project's pipelines.
 *
 * A list of runs, newest first, and a button to start one. Each run has its own
 * page, where the log is — watching a build is what people come here for, and a
 * list that also did that would do neither well.
 */
import type { ModulePipeline } from '~/types/pipeline'

const props = defineProps<{
  projectPath: string
  defaultBranch?: string
}>()

const { add: notify } = useNotifyPool()

const pipelines = ref<ModulePipeline[]>([])
const loading = ref(true)
const error = ref('')
const starting = ref(false)

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

onMounted(load)
watch(() => props.projectPath, load)

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
          Runs of this project's <code class="mono">.dogit-ci.yml</code>, whether a
          push started one or you did.
        </p>
      </div>
      <button class="btn btn-primary" type="button" :disabled="starting" @click="start">
        {{ starting ? 'Starting…' : 'Run pipeline' }}
      </button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="loading && pipelines.length === 0" class="spinner">Loading pipelines…</div>

    <div v-else-if="pipelines.length === 0" class="card empty">
      Nothing has run yet. Add a <code class="mono">.dogit-ci.yml</code> to the project
      and press “Run pipeline”.
    </div>

    <div v-else class="pipeline-list">
      <NuxtLink
        v-for="run in pipelines"
        :key="run.iid"
        class="pipeline-row"
        :to="`/p/${projectPath}/-/pipelines/${run.iid}`"
      >
        <span class="badge" :class="statusClass[run.status] ?? 'badge-neutral'">
          {{ run.status }}
        </span>
        <span class="mono">#{{ run.iid }}</span>
        <span class="muted">{{ run.ref }}</span>
        <span class="muted small mono">{{ run.sha.slice(0, 8) }}</span>
        <span class="muted small">{{ timeAgo(run.created_at) }}</span>
        <span v-if="run.jobs" class="muted small">
          {{ run.jobs }} job{{ run.jobs === 1 ? '' : 's' }}
        </span>
      </NuxtLink>
    </div>
  </div>
</template>

<style scoped>
.section-title {
  font-size: 18px;
}

/* One row per run, everything on one line: the state, the number, what it ran
   against and when. A table here would say the same things with more machinery. */
.pipeline-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

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

.small {
  font-size: 12px;
}
</style>