<script setup lang="ts">
/**
 * What is deploying now, and what has been deployed.
 *
 * Three things, in the order somebody asks for them:
 *
 *   The operation under way — its steps filling in, with the log beside it. It changes
 *   several times a minute and is the only thing on the page that does.
 *   The operations that have finished — the last twenty, newest first. It changes once,
 *   when something ends.
 *   The images that can go into the cluster — the things the operations above refer to,
 *   kept beside them so that "what was here before this" is one glance rather than a
 *   scroll back through rows.
 *
 * The last two are separate lists because they answer different questions. Operations
 * say what happened and whether it worked; images say what could be put back, and which
 * of them is live right now.
 */
import type { ModuleRow } from '~/types/module'
import type { DeployProgress } from '~/types/pipeline'

const props = defineProps<{
  projectId: string
  /** Whose deployments these are. Absent means every project on the instance. */
  projectPath?: string
  module: ModuleRow
  /** Whether the viewer may undo. */
  canManage: boolean
}>()

interface Deployment {
  id: string
  cluster: string
  namespace: string
  image: string
  workload: string
  state: string
  phase: string
  reason: string
  started_at: string
  finished_at?: string
}

/** How many operations a page holds. */
const PAGE = 20

const { add: notify } = useNotifyPool()

const deployments = ref<Deployment[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const reason = ref('')
const page = ref(1)

/**
 * Which of the two lists is showing.
 *
 * Tabs rather than two columns side by side: the two lists are read one at a time and
 * neither is a summary of the other, so putting them next to each other only spends
 * the width making both of them narrower. Operations are what somebody opens the page
 * for; images are what they open once something went wrong.
 */
const tab = ref<'operations' | 'images'>('operations')

/** The operation under way, as the module describes it step by step. */
const active = ref<DeployProgress | null>(null)
const activeSeen = ref<DeployProgress[]>([])

const target = computed(() => props.module.kind.replace(/^deploy:/, ''))

/** An image, with what is known about it. */
interface KnownImage {
  digest: string
  firstSeen: string
  /** How many operations ran this image, and how many of them worked. */
  times: number
  succeeded: number
  /** The operation that put it into the cluster, if it did. */
  live: Deployment | null
}

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

function badgeClass(state: string): string {
  switch (state) {
    case 'succeeded':
    case 'reverted':
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
  return deployment.state === 'succeeded' || deployment.state === 'rolled_back' ||
    deployment.state === 'reverted'
}

/** What is running now, per place, from the newest successful deployment. */
const places = computed(() => {
  const seen = new Map<string, Deployment>()
  for (const one of deployments.value) {
    if (!isLive(one)) continue
    const key = `${one.cluster}/${one.namespace}`
    if (!seen.has(key)) seen.set(key, one)
  }
  return [...seen.entries()].map(([place, deployment]) => ({ place, deployment }))
})

const live = computed(() => new Set(places.value.map((one) => one.deployment.id)))

/**
 * What the operation under way is putting into the cluster.
 *
 * From what has been applied so far rather than from the steps, because the steps say
 * what is happening and this says what the result is: the place, and the image that is
 * going to be running there. Empty until the module has said anything about applying,
 * which is the honest answer at that moment — the operation has started and nothing is
 * in the cluster yet.
 */
/**
 * The steps to draw when nothing is deploying.
 *
 * The last operation's, worked out from what it ended as. Without this the steps are
 * only ever on screen while something is happening, which makes the list something you
 * have to catch rather than something you can read — and the times somebody wants to
 * read it are exactly the times nothing is happening.
 */
const idleProgress = computed<DeployProgress | null>(() => {
  const last = deployments.value[0]
  if (!last || active.value) return null

  const failed = last.state === 'failed' || last.state === 'abandoned'
  return {
    phase: last.phase || (failed ? 'apply' : 'rollout'),
    message: failed
      ? (last.reason || 'the operation did not finish')
      : `the last operation ${last.state} — ${last.workload || 'the workload'} ran ${shortImage(last.image)}`,
    desired: 0,
    ready: 0,
  }
})

const activeImage = computed(() => {
  const applied = [...activeSeen.value].reverse().find((one) => one.phase === 'apply' || one.phase === 'rollout')
  if (!applied) return ''
  const match = applied.message.match(/@[a-f0-9]{12,}/)
  return match ? match[0] : ''
})

/**
 * The images, newest first, with how they fared.
 *
 * Taken from the operations rather than asked for separately: every image that can go
 * into the cluster is one that was in it at some point, so the list is exactly as long
 * as the history is deep. It is also why an image that failed on its first attempt and
 * worked on the second appears once rather than twice.
 */
const images = computed<KnownImage[]>(() => {
  const byDigest = new Map<string, KnownImage>()
  const order: string[] = []

  for (const one of deployments.value) {
    if (!one.image) continue
    const at = one.image.indexOf('@')
    const digest = at >= 0 ? one.image.slice(at + 1) : one.image
    const full = one.image

    let known = byDigest.get(digest)
    if (!known) {
      known = { digest: full, firstSeen: one.started_at, times: 0, succeeded: 0, live: null }
      byDigest.set(digest, known)
      order.push(digest)
    }

    known.times++
    if (isLive(one)) known.succeeded++
    // The newest successful deployment of this image is what a rollback would name.
    if (!known.live && isLive(one)) known.live = one
  }

  return order.map((digest) => byDigest.get(digest)!)
})

/** The operations on this page, newest first. */
const pageCount = computed(() => Math.max(1, Math.ceil(deployments.value.length / PAGE)))
const shown = computed(() => deployments.value.slice((page.value - 1) * PAGE, page.value * PAGE))

/**
 * The rows as they now are, keeping the object of every row that has not changed.
 *
 * Vue patches by identity: a row whose object is the same one it had is not touched at
 * all, so its DOM — and any half-made selection in it — survives.
 */
function merge(previous: Deployment[], arrived: Deployment[]): Deployment[] {
  const byID = new Map(previous.map((one) => [one.id, one]))
  return arrived.map((one) => {
    const old = byID.get(one.id)
    return old && JSON.stringify(old) === JSON.stringify(one) ? old : one
  })
}

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

    const arrived = merge(deployments.value, answer.deployments ?? [])
    if (JSON.stringify(arrived) !== JSON.stringify(deployments.value)) {
      deployments.value = arrived
      page.value = 1
    }
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/**
 * The operation under way, as it narrates itself.
 *
 * Cleared when nothing is running any more. A card left on the page saying "3 of 4"
 * after the operation has finished is a stale claim that something is moving, which is
 * worse than no card at all.
 */
function noteOperation(payload: Record<string, unknown>) {
  if (typeof payload.message !== 'string' || !payload.message) return

  const said: DeployProgress = {
    phase: String(payload.phase ?? ''),
    message: payload.message,
    ready: Number(payload.ready ?? 0),
    desired: Number(payload.desired ?? 0),
    step: Number(payload.step ?? 0),
    of: Number(payload.of ?? 0),
  }

  const last = activeSeen.value[activeSeen.value.length - 1]
  if (!last || last.phase !== said.phase || last.message !== said.message) {
    activeSeen.value = [...activeSeen.value, said]
  }
  active.value = said
}

/**
 * Puts one version back.
 *
 * Named after what it does and not after "rollback": this puts back a particular image
 * — the row somebody clicked, by the digest that row recorded — rather than stepping
 * back through whatever the cluster still remembers. That history is bounded, prunable,
 * and gone entirely if the Deployment is recreated.
 */
async function revertTo(deployment: Deployment) {
  const where = `${deployment.cluster}/${deployment.namespace || 'its default namespace'}`
  const question =
    `Put ${shortImage(deployment.image)} back on ${where}?\n\n` +
    `What ${deployment.workload || 'the workload'} runs will be this image again.\n` +
    'A database migration, a ConfigMap and anything else applied alongside are left as ' +
    'they are — only the image goes back.'
  if (!globalThis.confirm(question)) return

  busy.value = true
  error.value = ''
  try {
    await api.post(`/projects/${props.projectId}/deployments/revert`, {
      cluster: deployment.cluster,
      namespace: deployment.namespace,
      workload: deployment.workload,
      deployment_id: deployment.id,
    })
    notify(`${shortImage(deployment.image)} is being put back`, { type: 'success' })
    active.value = null
    activeSeen.value = []
    await load()
  } catch (caught) {
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    // Said where it is being looked for: a refusal that only appears somewhere else on
    // the page is a refusal nobody reads, and the button looks broken rather than busy.
    notify(message, { type: 'error', timer: 0 })
  } finally {
    busy.value = false
  }
}

/** Two subscriptions, because two things change at different rates. */
let stopHistory: (() => void) | undefined
let stopOperation: (() => void) | undefined

onMounted(async () => {
  await load()

  // The operation under way, on its own channel. Many times a minute.
  stopOperation = watchEvents({
    kinds: ['deploy.operation'],
    project: () => props.projectPath,
    onEvent: (event) => {
      const payload = event.payload ?? {}
      // Only this project's deployments: the feed is filtered by project, but an
      // operation with no project at all would otherwise land on every project page.
      if (payload.project && payload.project !== props.projectPath) return
      if (!payload.job_id && !payload.phase) return
      noteOperation(payload)
    },
  })

  // The history, once, when something ends.
  stopHistory = watchEvents({
    kinds: ['deploy.history'],
    project: () => props.projectPath,
    onChange: () => {
      // Finished: whatever was under way is not any more.
      active.value = null
      activeSeen.value = []
      void load()
    },
  })
})

onBeforeUnmount(() => {
  stopHistory?.()
  stopOperation?.()
})

watch(() => props.module.id, load)
</script>

<template>
  <div class="deploy-admin">
    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-else-if="reason" class="muted">{{ reason }}</div>
    <div v-else-if="loading" class="spinner">Loading…</div>

    <!-- What is happening right now.
         A card and not a row: it is the only thing on this page that is moving, and a
         row among a table of finished work reads as another entry in the history
         rather than as the present. Its own background says so before a word does. -->
    <section v-if="active" class="card active-card">
      <div class="card-body">
        <div class="block-head">
          <h3 class="block-title">Deploying now</h3>
          <span v-for="place in places" :key="place.place" class="place-chip mono">
            {{ place.place }}
          </span>
          <span class="spacer" />
          <span class="muted small mono">
            going to {{ shortImage(activeImage || '') || '—' }}
          </span>
        </div>

        <DeploySteps :progress="active" :seen="activeSeen" />

        <details class="log">
          <summary class="muted small">Where the whole of this is written down</summary>
          <p class="muted small">
            Every line is in the deploy job's log on the pipeline page, and it stays
            there after the operation is over.
          </p>
        </details>
      </div>
    </section>

    <!-- Nothing moving. Said plainly rather than shown as an empty space, because an
         empty card reads as something that failed to load. -->
    <section v-else class="card active-card idle">
      <div class="card-body">
        <div class="block-head">
          <h3 class="block-title">Nothing is deploying</h3>
          <span class="muted small">what the last operation did</span>
        </div>

        <div v-for="place in places" :key="place.place" class="running-place">
          <span class="badge badge-green">running</span>
          <span class="place-chip mono">{{ place.place }}</span>
          <span class="mono small">{{ shortImage(place.deployment.image) }}</span>
          <span class="muted small">{{ place.deployment.workload || '—' }}</span>
        </div>

        <!-- The steps are here even when nothing is moving, so the list is something
             to read rather than something to catch. -->
        <DeploySteps v-if="idleProgress" :progress="idleProgress" />
      </div>
    </section>

    <template v-if="!reason && !loading">
      <!-- Operations and images side by side: the first says what happened, the second
           says what could be put back, and the question is nearly always about both. -->
      <nav class="tabs">
        <button
          class="tab"
          :class="{ on: tab === 'operations' }"
          type="button"
          @click="tab = 'operations'"
        >
          Operations
          <span class="count">{{ deployments.length }}</span>
        </button>
        <button
          class="tab"
          :class="{ on: tab === 'images' }"
          type="button"
          @click="tab = 'images'"
        >
          Images
          <span class="count">{{ images.length }}</span>
        </button>
      </nav>

      <div class="columns">
        <section v-show="tab === 'operations'" class="block">
          <div class="block-head">
            <h3 class="block-title">Operations</h3>
            <span class="muted small">the last {{ PAGE }}, newest first</span>
          </div>

          <p v-if="deployments.length === 0" class="muted small">
            Nothing has been deployed yet. A deployment happens when a pipeline with a
            <span class="mono">deploy:</span> block finishes its build.
          </p>

          <div v-else class="table-scroll">
            <table class="table">
            <thead>
              <tr>
                <th>When</th>
                <th>Image</th>
                <th>State</th>
                <th v-if="props.canManage" class="actions-col">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="one in shown" :key="one.id">
                <td class="nowrap">{{ when(one.started_at) }}</td>
                <td class="mono small">{{ shortImage(one.image) }}</td>
                <td class="state-cell">
                  <div class="badges">
                    <span class="badge" :class="badgeClass(one.state)">{{ one.state }}</span>
                    <span v-if="live.has(one.id)" class="badge badge-green">running now</span>
                  </div>
                  <div v-if="one.reason" class="muted small reason">{{ one.reason }}</div>
                </td>
                <td v-if="props.canManage" class="actions-col">
                  <button
                    class="btn btn-small"
                    type="button"
                    :disabled="busy || !isLive(one) || live.has(one.id)"
                    :title="
                      live.has(one.id)
                        ? 'This is the image running now, so there is nothing to put back'
                        : 'Put this image back on the workload'
                    "
                    @click="revertTo(one)"
                  >
                    Revert to this
                  </button>
                </td>
              </tr>
            </tbody>
            </table>
          </div>

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

        <section v-show="tab === 'images'" class="block">
          <div class="block-head">
            <h3 class="block-title">Images</h3>
            <span class="muted small">what can go into the cluster</span>
          </div>

          <p v-if="images.length === 0" class="muted small">No images yet.</p>

          <ul v-else class="image-list">
            <li v-for="image in images" :key="image.digest" class="image">
              <span class="image-name mono">{{ shortImage(image.digest) }}</span>
              <span class="muted small">
                {{ image.times }} operation{{ image.times === 1 ? '' : 's' }},
                {{ image.succeeded }} successful
              </span>
              <span v-if="image.live" class="badge badge-green">running now</span>
              <button
                v-if="props.canManage && image.live"
                class="btn btn-small"
                type="button"
                :disabled="busy"
                title="Put this image back on the workload"
                @click="revertTo(image.live)"
              >
                Revert to this
              </button>
              <span v-else class="muted small">not in the cluster now</span>
            </li>
          </ul>
        </section>
      </div>
    </template>
  </div>
</template>

<style scoped>
.block {
  margin-bottom: 22px;
}

.block-head {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 8px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border);
}

.block-title {
  margin: 0;
  font-size: 13px;
}

/* The operation under way gets a face of its own: it is the only thing here that is
   moving, and a card that looks like the rest of the page is read as part of the
   history rather than as something happening. */
.active {
  padding: 12px 14px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--bg-inset);
}

/* The card of what is happening. Tinted so that it is visibly not part of the history
   below it, and given a left edge so the eye finds it without reading anything. */
.active-card {
  margin-bottom: 22px;
  border-left: 3px solid var(--accent);
  background: var(--bg-inset);
}

.active-card.idle {
  border-left-color: var(--border-strong);
}

.place-chip {
  font-size: 12px;
  padding: 1px 7px;
  border: 1px solid var(--border);
  border-radius: 3px;
  background: var(--bg);
}

.running-place {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px 12px;
  padding: 4px 0;
}

.running-place + .running-place {
  border-top: 1px solid var(--border);
}

.spacer {
  flex: 1 1 auto;
}

/* The two lists are never on screen at once, so there is one column and no gap to
   manage — and the wider one is what the list of operations needs. */
.columns {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
}

.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--border);
}

.tab {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 7px 12px;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.tab.on {
  color: var(--text);
  border-bottom-color: var(--accent);
}

/* How many there are, so switching is a decision rather than a guess. */
.count {
  font-size: 11px;
  padding: 0 6px;
  border-radius: 8px;
  background: var(--bg-inset);
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

/* Scrolling sideways rather than squashing: a digest cannot be abbreviated to fit a
   column, and truncating it would make two images look like one. */
.table-scroll {
  overflow-x: auto;
}

.table {
  width: 100%;
  border-collapse: collapse;
}

th,
td {
  text-align: left;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}

.actions-col {
  width: 130px;
  white-space: nowrap;
}

/* The reason is the part of a row somebody reads, and it is the longest thing in it.
   Without a width of its own the table gives the column whatever is left over — which
   on a narrow page is a handful of characters — and a failure becomes a column of
   two-word lines that is taller than the whole rest of the table. */
.state-cell {
  min-width: 220px;
  max-width: 380px;
}

.badges {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}

.reason {
  margin-top: 4px;
  line-height: 1.4;
}

.image-list {
  list-style: none;
  margin: 0;
  padding: 0;
}

.image {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px 12px;
  padding: 6px 0;
  border-bottom: 1px solid var(--border);
}

.image-name {
  flex: 1 1 100%;
  font-size: 12px;
  word-break: break-all;
}

.pager {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
}

.log {
  margin-top: 10px;
}

.nowrap {
  white-space: nowrap;
}

.small {
  font-size: 12px;
}
</style>