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
 *
 * What is on this page is what somebody actually asks. Not a list of timestamps: what
 * is running right now, what failed and why, and which images have ever gone out —
 * because the question that follows "who put this here" is always "and what was here
 * before it", and that is answered by the images rather than by the rows.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{
  projectId: string
  /** Whose deployments these are. Absent means every project on the instance. */
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

/** How many rows a page holds. A page that grows without end is not a page. */
const PAGE = 20

const deployments = ref<Deployment[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const reason = ref('')

/** Which states are shown. Empty means all of them. */
const stateFilter = ref('')
const page = ref(1)

const target = computed(() => props.module.kind.replace(/^deploy:/, ''))

/**
 * The repository in full and the digest cut down.
 *
 * The repository is what somebody recognises; a digest is not read, it is compared,
 * and the first few characters are enough to tell two apart.
 */
function shortImage(image: string): string {
  const at = image.indexOf('@')
  if (at < 0) return image || '—'
  const digest = image.slice(at + 1).replace(/^sha256:/, '')
  return `${image.slice(0, at)}@${digest.slice(0, 12)}`
}

function when(iso?: string): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

/**
 * A state's colour, in the palette the rest of the interface uses.
 *
 * Written as one place rather than a class per row: a state means one colour, and a
 * row that decides for itself is a row that will eventually be a fourth shade.
 */
function badgeClass(state: string): string {
  switch (state) {
    case 'succeeded':
    case 'rolled_back':
      return 'badge-green'
    case 'failed':
    case 'abandoned':
      return 'badge-red'
    case 'running':
      return 'badge-warning'
    default:
      return 'badge-neutral'
  }
}

function isLive(deployment: Deployment): boolean {
  return deployment.state === 'succeeded' || deployment.state === 'rolled_back'
}

/**
 * The places this module deploys to, and what is running in each.
 *
 * Answered from the history rather than from a live question to the cluster: the
 * module is the only thing that knows, and the newest successful deployment of a
 * place is what a rollback would return to.
 */
const places = computed(() => {
  const seen = new Map<string, Deployment>()
  for (const one of deployments.value) {
    if (!isLive(one)) continue
    const key = `${one.cluster}/${one.namespace}`
    if (!seen.has(key)) seen.set(key, one)
  }
  return [...seen.entries()].map(([place, deployment]) => ({ place, deployment }))
})

/** How many of each state, for a line that says more than a list would. */
const counts = computed(() => {
  const tally: Record<string, number> = {}
  for (const one of deployments.value) tally[one.state] = (tally[one.state] ?? 0) + 1
  return tally
})

/** Every image this module has rolled out, newest first. */
const images = computed(() => {
  const seen = new Set<string>()
  const out: string[] = []
  for (const one of deployments.value) {
    if (!one.image || seen.has(one.image)) continue
    seen.add(one.image)
    out.push(one.image)
  }
  return out
})

const filtered = computed(() => {
  if (!stateFilter.value) return deployments.value
  return deployments.value.filter((one) => one.state === stateFilter.value)
})

const pageCount = computed(() => Math.max(1, Math.ceil(filtered.value.length / PAGE)))
const shown = computed(() =>
  filtered.value.slice((page.value - 1) * PAGE, page.value * PAGE),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const query = new URLSearchParams({ target: target.value })
    const answer = await api.get<{ reason?: string; deployments?: Deployment[] }>(
      `/projects/${props.projectId}/deployments?${query}`,
    )
    if (answer.reason === 'no_deploy_module') {
      deployments.value = []
      reason.value = 'No deploy module is installed on this instance.'
      return
    }
    deployments.value = answer.deployments ?? []
    page.value = 1
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/**
 * Undoes the last deployment of a place.
 *
 * Asked to confirm, and says what it will not do: the answer cannot be taken back
 * afterwards, the image it returns to may be one somebody has since removed, and
 * nothing here can say whether it still pulls.
 */
async function rollback(deployment: Deployment) {
  const question =
    `Put ${deployment.cluster}/${deployment.namespace || 'its default namespace'} back to ` +
    'the deployment before this one?\n\n' +
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

watch(stateFilter, () => {
  page.value = 1
})

/**
 * Re-read when something about this project's runs changes.
 *
 * Not optional. A deployment finishes long after the request that started it was
 * answered, so this panel is nearly always open across the moment it changes — and
 * without this it is the one place in the interface that shows a state the rest of
 * the page has already moved past. A notification arriving while the table says
 * "running" is exactly the moment somebody stops trusting it.
 */
let stopWatching: (() => void) | undefined

onMounted(async () => {
  await load()
  stopWatching = watchEvents({
    kinds: ['pipeline.updated', 'job.updated'],
    project: () => props.projectPath,
    onChange: () => void load(),
  })
})

onBeforeUnmount(() => stopWatching?.())

watch(() => props.module.id, load)
</script>

<template>
  <div>
    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-else-if="reason" class="muted">{{ reason }}</div>
    <div v-else-if="loading" class="spinner">Loading…</div>

    <div v-else-if="deployments.length === 0" class="muted">
      Nothing has been deployed yet. A deployment happens when a pipeline with a
      <span class="mono">deploy:</span> block finishes its build.
    </div>

    <template v-else>
      <!-- What is running now. The one question this page exists for. -->
      <section class="block">
        <h4 class="block-title">Running now</h4>
        <p v-if="places.length === 0" class="muted small">
          Nothing is running from a deployment this module recorded.
        </p>
        <ul v-else class="places">
          <li v-for="place in places" :key="place.place" class="place">
            <span class="place-name mono">{{ place.place }}</span>
            <span class="place-image mono">{{ shortImage(place.deployment.image) }}</span>
            <span class="muted small">{{ place.deployment.workload || '—' }}</span>
          </li>
        </ul>
      </section>

      <section class="block">
        <h4 class="block-title">
          History
          <span class="muted small">
            <template v-if="Object.keys(counts).length">
              {{ counts.succeeded ?? 0 }} succeeded ·
              {{ counts.failed ?? 0 }} failed<template v-if="counts.abandoned">
                · {{ counts.abandoned }} abandoned</template>
            </template>
          </span>
        </h4>

        <div class="filters">
          <label>
            <span class="small">State</span>
            <select v-model="stateFilter">
              <option value="">Any</option>
              <option v-for="(n, state) in counts" :key="state" :value="state">
                {{ state }} ({{ n }})
              </option>
            </select>
          </label>
        </div>

        <table class="table">
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
            <tr v-for="deployment in shown" :key="deployment.id">
              <td class="nowrap">{{ when(deployment.started_at) }}</td>
              <td class="nowrap">
                {{ deployment.cluster }}
                <span v-if="deployment.namespace" class="muted">/ {{ deployment.namespace }}</span>
              </td>
              <td class="mono small">{{ shortImage(deployment.image) }}</td>
              <td>
                <span
                  class="badge"
                  :class="badgeClass(deployment.state)"
                >
                  {{ deployment.state }}
                </span>
                <div v-if="deployment.reason" class="muted small reason">{{ deployment.reason }}</div>
              </td>
              <td v-if="props.canManage" class="actions-col">
                <button
                  class="btn btn-small"
                  type="button"
                  :disabled="busy || !isLive(deployment)"
                  title="Put the previous image back"
                  @click="rollback(deployment)"
                >
                  Roll back
                </button>
              </td>
            </tr>
          </tbody>
        </table>

        <div v-if="pageCount > 1" class="pager">
          <button class="btn btn-small" type="button" :disabled="page === 1" @click="page--">
            Newer
          </button>
          <span class="muted small">Page {{ page }} of {{ pageCount }}</span>
          <button
            class="btn btn-small"
            type="button"
            :disabled="page === pageCount"
            @click="page++"
          >
            Older
          </button>
        </div>
      </section>

      <section class="block">
        <h4 class="block-title">Images deployed</h4>
        <p class="muted small">
          Every image this module has rolled out for
          <span v-if="props.projectPath" class="mono">{{ props.projectPath }}</span>
          <template v-else>this instance</template>. A rollback returns to the one before,
          which is why they are worth keeping a list of.
        </p>
        <ul class="images">
          <li v-for="image in images" :key="image" class="mono small">{{ image }}</li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.block {
  margin-top: 18px;
}

.block-title {
  margin: 0 0 8px;
  font-size: 13px;
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.places,
.images {
  list-style: none;
  margin: 0;
  padding: 0;
}

.place {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 14px;
  padding: 6px 0;
  border-bottom: 1px solid var(--border);
}

.place-name {
  min-width: 220px;
  font-size: 13px;
}

.place-image {
  font-size: 12px;
}

.images li {
  padding: 3px 0;
  color: var(--text-muted);
  word-break: break-all;
}

.filters {
  margin-bottom: 8px;
}

.filters label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.filters select {
  padding: 5px 8px;
  font: inherit;
  font-size: 13px;
  color: inherit;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
}

.table {
  width: 100%;
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

/* A reason can be a whole sentence, and it is the part somebody reads. */
.reason {
  margin-top: 3px;
  max-width: 46ch;
}

.pager {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
}

.nowrap {
  white-space: nowrap;
}

.small {
  font-size: 12px;
}
</style>