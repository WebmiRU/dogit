<script setup lang="ts">
/**
 * What has been deployed, and how to undo it.
 *
 * The admin side of a deploy module, reached from the project and from the module's
 * own page. It is a separate thing from configuring one: the settings decide where a
 * deployment goes, and this is a record of where it went and what to put back.
 *
 * The history comes from the module, which is where it lives — the core keeps no copy,
 * because a second copy of "what is deployed" is how two answers start to disagree.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{
  projectId: string
  /** Whose deployments these are, said in the sentence above the table. */
  projectPath?: string
  module: ModuleRow
  /** Whether the viewer may undo. Rolling back changes a running system. */
  canManage: boolean
}>()

interface Deployment {
  id: string
  cluster: string
  namespace: string
  image: string
  workload: string
  state: string
  reason: string
  started_at: string
  finished_at?: string
}

const deployments = ref<Deployment[]>([])
const reason = ref('')
const loading = ref(true)
const busy = ref(false)
const error = ref('')

/** The place whose history is shown. Empty means every place the module knows. */
const place = ref('')

const target = computed(() => props.module.kind.replace(/^deploy:/, ''))

/** A digest is the thing worth seeing first: it is what a rollback would return to. */
function shortImage(image: string): string {
  const at = image.indexOf('@')
  if (at < 0) return image || '—'
  return `${image.slice(0, image.lastIndexOf('/', at) + 1)}@${image.slice(at + 1, 19)}…`
}

function when(iso?: string): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const query = new URLSearchParams({ target: target.value })
    if (place.value) {
      query.set('cluster', place.value)
      query.set('namespace', '')
    }
    const answer = await api.get<{ reason?: string; deployments?: Deployment[] }>(
      `/projects/${props.projectId}/deployments?${query}`,
    )
    if (answer.reason === 'no_deploy_module') {
      deployments.value = []
      reason.value = 'No deploy module is installed on this instance.'
      return
    }
    deployments.value = answer.deployments ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/**
 * Undoes the last deployment of a place.
 *
 * Asked to confirm, because the answer cannot be taken back afterwards: the image it
 * returns to may itself be one somebody has since removed, and nothing here can say
 * whether it still pulls.
 */
async function rollback(deployment: Deployment) {
  const question =
    `Put ${deployment.cluster}/${deployment.namespace || 'its default namespace'} back to the deployment before this one?\n\n` +
    'This returns the previous image. It does not undo a database migration that ran with it.'
  if (!globalThis.confirm(question)) return

  busy.value = true
  error.value = ''
  try {
    await api.post(`/projects/${props.projectId}/deployments/rollback`, {
      cluster: deployment.cluster,
      namespace: deployment.namespace,
    })
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = false
  }
}

onMounted(load)
watch(() => props.module.id, load)
</script>

<template>
  <div>
    <h3 class="panel-title">Deployments</h3>

    <p class="muted small">
      Every deployment this module made for
      <span v-if="props.projectPath" class="mono">{{ props.projectPath }}</span>
      <template v-else>this instance's projects</template>, newest first. The image is the
      digest that was rolled out, not the tag it was pushed under — which is what a
      rollback returns to.
    </p>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-else-if="reason" class="muted">{{ reason }}</div>
    <div v-else-if="loading" class="spinner">Loading…</div>
    <div v-else-if="deployments.length === 0" class="muted">
      Nothing has been deployed yet. A deployment happens when a pipeline with a
      <span class="mono">deploy:</span> block finishes its build.
    </div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>When</th>
          <th>Place</th>
          <th>Image</th>
          <th>State</th>
          <th v-if="props.canManage" class="actions-col">Actions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="deployment in deployments" :key="deployment.id">
          <td class="nowrap">{{ when(deployment.started_at) }}</td>
          <td class="nowrap">
            {{ deployment.cluster }}
            <span v-if="deployment.namespace" class="muted">/ {{ deployment.namespace }}</span>
          </td>
          <td class="mono small">{{ shortImage(deployment.image) }}</td>
          <td>
            <span
              class="badge"
              :class="{
                'badge-ok': deployment.state === 'succeeded' || deployment.state === 'rolled_back',
                'badge-warn': deployment.state === 'running',
                'badge-error': deployment.state === 'failed',
              }"
            >
              {{ deployment.state }}
            </span>
            <div v-if="deployment.reason" class="muted small">{{ deployment.reason }}</div>
          </td>
          <td v-if="props.canManage" class="actions-col">
            <button
              class="btn btn-small"
              type="button"
              :disabled="busy || deployment.state !== 'succeeded'"
              title="Put the previous image back"
              @click="rollback(deployment)"
            >
              Roll back
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.panel-title {
  margin: 0 0 4px;
  font-size: 14px;
}

.table {
  width: 100%;
  margin-top: 10px;
  border-collapse: collapse;
}

th,
td {
  text-align: left;
  padding: 7px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}

.actions-col {
  width: 110px;
  white-space: nowrap;
}

.nowrap {
  white-space: nowrap;
}

.small {
  font-size: 12px;
}
</style>