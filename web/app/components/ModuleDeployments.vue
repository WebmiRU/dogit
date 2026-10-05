<script setup lang="ts">
import { onRewake } from '~/lib/eventSocket'
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

/** One page of the history, and what is left of it. */
interface DeploymentsPage {
  reason?: string
  deployments?: Deployment[]
  total?: number
  page?: number
  pages?: number
  has_more?: boolean
}

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
  /** How many pods this operation dealt with. Zero when it never reached a cluster. */
  pods_wanted?: number
  pods_ready?: number
  pods_retired?: number
  /** The names this image had when this operation ran, as they were then. */
  tags?: string[]
  /** The short commit the image was built from. */
  commit?: string
  /** The name the repository gave this destination, when it named it. */
  place?: string
  /** What this operation said, in order, when the server has it to give. */
  log?: LogLine[]
}

interface LogLine {
  phase: string
  message: string
  step: number
  of: number
}

/**
 * What one operation did to the containers, in a few words.
 *
 * A deployment that is remembered by the fact that it succeeded has forgotten almost
 * everything: how many pods ran the new image, how many of the old ones went away.
 * Those numbers are the operation, and they are the only part of it that cannot be
 * read off the cluster afterwards — by then the old pods are gone.
 */
function podsOf(one: Deployment) {
  const wanted = Number(one.pods_wanted ?? 0)
  if (wanted <= 0) return ''
  const ready = Number(one.pods_ready ?? 0)
  const retired = Number(one.pods_retired ?? 0)

  const bits = [`${ready} of ${wanted} up`]
  if (retired > 0) bits.push(`${retired} old gone`)
  return bits.join(' · ')
}

/** How many rows a page holds, for both lists: twenty is what fits without scrolling
 *  past the end of it, and an operations list that stops at twenty is the whole
 *  history this instance has. */
const PAGE = 20
const IMAGE_PAGE = 20

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
const tab = ref<'now' | 'operations' | 'images'>('now')

/** The image to be shown, when somebody arrived here by clicking one. */
const wanted = ref('')
const imagePage = ref(1)

/** The operation under way, as the module describes it step by step. */
const active = ref<DeployProgress | null>(null)

/** The steps the core said this deployment goes through, in order. */
const plan = ref<{ key: string; label: string }[]>([])
const activeSeen = ref<DeployProgress[]>([])

/**
 * The place being asked about, if one was named.
 *
 * Not guessed from the module's kind. "deploy:kubernetes" is what the module is, and a
 * cluster is called something else entirely, so asking for a cluster named "kubernetes"
 * is asking about a place that does not exist and getting an empty history in reply.
 * Empty means every place, which is the right question for a page that lists operations
 * across all of them.
 */
const target = ref('')

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

/**
 * The image's address: where it is and which one it is.
 *
 * Its own column, because it is the only part of a row that identifies rather than
 * explains. The name and the commit sit beside it, and repeating either of them inside
 * this one is how a list stops being scannable.
 */
function namedImage(image: string): string {
  return shortImage(image)
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
  return [...seen.entries()].map(([place, deployment]) => ({
    place,
    deployment,
    // What was done to this place, which for the newest operation that finished well
    // is also what is in the cluster. The list above already marks the image that is
    // running now; saying it here as well would be the same fact in two places, one
    // of them true only some of the time.
    badge: deployment.state || 'unknown',
  }))
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
/**
 * What the card's edge says: working, fine, or broken.
 *
 * Read from the operation itself rather than worked out for the edge, so the edge
 * and the steps under it cannot disagree — a green edge beside a red step leaves
 * the reader deciding which of the two to believe.
 */
const cardState = computed<'working' | 'ok' | 'bad'>(() => {
  if (active.value) return 'working'
  if (activeSeen.value.some((one) => one.failed)) return 'bad'
  const last = deployments.value[0]
  if (last && (last.state === 'failed' || last.state === 'abandoned')) return 'bad'
  return 'ok'
})

/**
 * The log on show: what this operation has said so far, and when nothing is running,
 * what the last one said.
 *
 * The second case is the reason the module writes it down. A page opened between
 * deployments was never there for the lines, and a log that only exists while somebody
 * is watching is no use to whoever is trying to find out where it broke.
 */
const lastLog = ref<LogLine[]>([])

const shownLog = computed<LogLine[]>(() => {
  if (active.value) {
    return activeSeen.value.map((line) => ({
      phase: line.phase,
      message: line.message,
      step: line.step,
      of: line.of,
    }))
  }
  return lastLog.value
})

const idleProgress = computed<DeployProgress | null>(() => {
  const last = deployments.value[0]
  if (!last || active.value) return null

  const failed = last.state === 'failed' || last.state === 'abandoned'

  // No phase, on purpose, when the last operation ended well.
  //
  // There is no step in progress and there has not been for some time. Naming one
  // anyway — which is what this used to do, naming the rollout because that is where
  // deployments usually get to — draws an arrow on a step that is finished and leaves
  // everything after it blue for ever, which reads as a run that stalled one step from
  // the end. It said so twice today before I looked properly.
  //
  // A failure is the other matter: there the step it died on is worth pointing at, and
  // the module named it.
  return {
    phase: failed ? (last.phase || '') : '',
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
const images = ref<KnownImage[]>([])
const imagePages = ref(1)
const imageTotal = ref(0)
const imageHasMore = ref(false)


/**
 * Goes to that image on the images tab and points at it.
 *
 * The images tab rather than an action here, because putting an image back is a
 * decision made against the list of what could go in — the operations are a record of
 * what happened, and offering the same action in both places makes two doors onto one
 * room.
 */
function showImage(image: string) {
  const at = image.indexOf('@')
  const digest = at >= 0 ? image.slice(at + 1) : image

  wanted.value = digest
  tab.value = 'images'

  // Straight onto the page that holds it. Somebody who clicked an image in an operation
  // three pages back should not have to go looking for it.
  const index = images.value.findIndex((one) => one.digest.endsWith(digest))
  if (index >= 0) imagePage.value = Math.floor(index / IMAGE_PAGE) + 1
}

/** The images on this page. */
const shownImages = computed(() => images.value)

/**
 * The operations on show, newest first.
 *
 * Asked for by page and not cut up here. Loading the whole history and slicing it in
 * the browser is how a page stops opening after a year, and it makes the control next to
 * the rows a decoration: it says "page" while changing nothing about what was fetched.
 */
const total = ref(0)
const pages = ref(1)
const hasMore = ref(false)

const shown = computed(() => deployments.value)

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

/** Goes to a page of the history by asking for it. */
function goToPage(to: number) {
  if (to < 1 || (pages.value > 0 && to > pages.value)) return
  page.value = to
  void load()
}

/**
 * Whether a load is under way, and whether one more is owed.
 *
 * Coalesced, because there are several honest reasons to ask again at once — a
 * reconnection, an operation finishing, the buffer of recent events arriving — and
 * twenty identical requests in a millisecond is not a careful page reloading itself. It
 * is twenty pages' worth of work for one answer, and on a list this size it is the
 * difference between opening and not opening.
 */
let loadingNow = false
let loadAgain = false
let lastLoadedAt = 0

/** How close together two loads may be and still both be worth making. */
const LOAD_SETTLE_MS = 250

/**
 * The catalogue of images, asked for as its own question.
 *
 * It used to be worked out from the operations that had been loaded, which was free
 * while every operation was loaded and wrong the moment they were not: a page of twenty
 * rows cannot tell you about the three hundred deployments behind them, and the older
 * images are the ones a rollback is chosen from.
 */
async function loadImages() {
  try {
    const query = new URLSearchParams({
      page: String(imagePage.value),
      per_page: String(IMAGE_PAGE),
    })
    const answer = await api.get<{
      images?: {
        image: string
        first_seen: string
        times: number
        succeeded: number
        deployed?: {
          id: string
          cluster: string
          namespace: string
          workload: string
          state: string
          started_at: string
        }
      }[]
      pages?: number
      total?: number
      has_more?: boolean
    }>(`/projects/${props.projectId}/deploy-images?${query}`)

    imagePages.value = answer.pages ?? 1
    imageTotal.value = answer.total ?? 0
    imageHasMore.value = answer.has_more ?? false

    images.value = (answer.images ?? []).map((one) => {
      const at = one.image.indexOf('@')
      return {
        digest: at >= 0 ? one.image.slice(at + 1) : one.image,
        firstSeen: one.first_seen,
        times: one.times,
        succeeded: one.succeeded,
        live: one.deployed
          ? {
              id: one.deployed.id,
              cluster: one.deployed.cluster,
              namespace: one.deployed.namespace,
              workload: one.deployed.workload,
              state: one.deployed.state,
              started_at: one.deployed.started_at,
            }
          : null,
      }
    })
  } catch (caught) {
    // Said out loud rather than swallowed. An empty list and no claim that there are
    // no images is the right thing to draw, but it is indistinguishable from "there are
    // none" unless the failure says so, and a catalogue that silently reads as empty is
    // worse than one that admits it could not be read.
    console.warn('[dogit] the image catalogue could not be read', caught)
  }
}

/** Goes to a page of the catalogue by asking for it. */
function goToImagePage(to: number) {
  if (to < 1 || to > imagePages.value) return
  imagePage.value = to
  void loadImages()
}

async function load() {
  // A reconnection, a finished operation and the buffer of recent events arriving are
  // three honest reasons to ask again, and all three arrive within a hair of each other.
  // Twenty identical requests in a millisecond is not a careful page reloading itself.
  // Past that hair something did happen, and the load goes through.
  if (Date.now() - lastLoadedAt < LOAD_SETTLE_MS) {
    loadAgain = true
    return
  }
  if (loadingNow) {
    loadAgain = true
    return
  }
  loadingNow = true
  loading.value = true
  error.value = ''
  try {
    const query = new URLSearchParams({
      cluster: target.value,
      page: String(page.value),
      per_page: String(PAGE),
    })
    const answer = await api.get<DeploymentsPage>(
      `/projects/${props.projectId}/deployments?${query}`,
    )
    total.value = answer.total ?? 0
    pages.value = answer.pages ?? 1
    hasMore.value = answer.has_more ?? false
    // The newest row carries what it said, so the log is there before anything is
    // watched rather than only for a run somebody stayed to see.
    lastLog.value = answer.deployments?.[0]?.log ?? []

    if (answer.reason === 'no_deploy_module') {
      deployments.value = []
      reason.value = 'No deploy module is installed on this instance.'
      return
    }

    // The rows of the page that was asked for, and not those rows merged into the last
    // page. Merging turned every page change into "the list changed", which reset the
    // page, which fetched page one again: a control that could never leave the first
    // page and looked like it had.
    deployments.value = merge(deployments.value, answer.deployments ?? [])
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loadingNow = false
    loading.value = false
    lastLoadedAt = Date.now()
    if (loadAgain) {
      loadAgain = false
      setTimeout(() => void load(), LOAD_SETTLE_MS)
    }
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
let stopRewake: (() => void) | undefined
let stopPlan: (() => void) | undefined
let stopHistory: (() => void) | undefined
let stopOperation: (() => void) | undefined

onMounted(async () => {
  await load()
  void loadImages()

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

  // What a reconnection means for this page.
  //
  // Everything here was drawn from events, and while the connection was down the
  // deployment carried on without it: the steps that finished in that gap were never
  // seen, and the card would sit on the phase it happened to be showing when the
  // network blinked. So a reconnect is a reason to ask again rather than to carry on
  // believing — the same thing a person does when they notice they stopped hearing
  // something and look again.
  stopRewake = onRewake(() => {
    void load()
  })

  // The plan, once, before anything happens. Separate from the operation's own
  // events because it is a different kind of fact: the whole list, sent before the
  // first step is taken.
  stopPlan = watchEvents({
    kinds: ['deploy.plan'],
    project: () => props.projectPath,
    onEvent: (event) => {
      const steps = event.payload?.steps
      if (!Array.isArray(steps)) return
      plan.value = steps as { key: string; label: string }[]
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
  stopRewake?.()
  stopPlan?.()
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
    <!-- The tabs above the content, not below it: a row of names that sits under what
         it names is a legend for something the reader has already scrolled past. -->
    <template v-if="!reason && !loading">
      <nav class="tabs">
        <button
          class="tab"
          :class="{ on: tab === 'now' }"
          type="button"
          @click="tab = 'now'"
        >
          Now
        </button>
        <button
          class="tab"
          :class="{ on: tab === 'operations' }"
          type="button"
          @click="tab = 'operations'"
        >
          Operations
          <!-- How many there are, not how many have been fetched. The two used to be
               the same number, which is exactly why a reader who counted the rows and
               read the tab was told two different things once the list was paged. -->
          <span class="count">{{ total }}</span>
        </button>
        <button
          class="tab"
          :class="{ on: tab === 'images' }"
          type="button"
          @click="tab = 'images'"
        >
          Images
          <span class="count">{{ imageTotal }}</span>
        </button>
      </nav>
    </template>

    <!-- What is happening, or what last happened. Its own tab and the first one: this
         is the thing a page is opened for, and it answers one question, where the two
         lists below answer another. -->
    <div v-show="tab === 'now'">
    <section v-if="active" class="card active-card" :class="cardState">
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

        <DeploySteps :progress="active" :seen="activeSeen" :plan="plan" live />
        <DeployLog :lines="shownLog" :plan="plan" />

        <details class="log">
          <summary class="muted small">Where the whole of this is written down</summary>
          <p class="muted small">
            Every line is in the deploy job's log on the pipeline page, and it stays
            there after the operation is over.
          </p>
        </details>
      </div>
    </section>

    <!-- Nothing moving. The card is still here, and says what it is: this is what
         the last operation did, not an absence of operations. "Nothing is deploying"
         was true and useless — it described the moment rather than the thing on
         screen, and the thing on screen is a record of a run. -->
    <section v-else class="card active-card idle" :class="cardState">
      <div class="card-body">
        <div class="block-head">
          <h3 class="block-title">The last operation</h3>
          <span class="muted small">what it did</span>
          <span class="spacer" />
        </div>

        <div v-for="place in places" :key="place.place" class="running-place">
          <!-- What is actually there, rather than a word that was hard-coded when
               this card only ever appeared during a deployment. "running" on a
               deployment that finished twenty minutes ago is a claim about the
               cluster dressed up as a label on a row. -->
          <span class="badge" :class="badgeClass(place.deployment.state)">
            {{ place.badge }}
          </span>
          <span class="place-chip mono">{{ place.place }}</span>
          <span class="mono small">{{ shortImage(place.deployment.image) }}</span>
          <span class="muted small">{{ place.deployment.workload || '—' }}</span>
        </div>

        <!-- The steps are here even when nothing is moving, so the list is something
             to read rather than something to catch. -->
        <!-- Not live: this is the last operation's steps, and an arrow moving here
             would be claiming work that finished long ago. -->
        <!-- The steps of the last operation, when this page has seen which ones they
             were. It has not, on a page opened between runs: the plan is published
             with a run, and a page that arrives afterwards is told about the run that
             is already over and not about the list it went through. Better the summary
             above than a list of nothing. -->
        <DeploySteps
          v-if="idleProgress && plan.length > 0"
          :progress="idleProgress"
          :plan="plan"
        />
        <DeployLog :lines="shownLog" :plan="plan" />
      </div>
    </section>
    </div>

    <template v-if="!reason && !loading">
      <!-- Operations and images side by side: the first says what happened, the second
           says what could be put back, and the question is nearly always about both. -->


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
                <th>Place</th>
                <th>Tag</th>
                <th>Commit</th>
                <th>Image</th>
                <th>Status</th>
                <th>Pods</th>
                <th>What happened</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="one in shown" :key="one.id">
                <td class="nowrap">{{ when(one.started_at) }}</td>
                <!-- A name a person chose, and the commit it came from, in columns of
                     their own. Together in one cell they read as two releases when they
                     are one release and where it was built. -->
                <!-- The name the repository gave this destination. Without it a
                     repository deploying three places shows three rows that differ
                     only by where they went, and "which one failed" has no answer. -->
                <td class="small">{{ one.place || '—' }}</td>
                <td class="mono small">
                  <span v-if="one.tags && one.tags.length">{{ one.tags.join(', ') }}</span>
                  <span v-else class="muted">—</span>
                </td>
                <td class="mono small muted">{{ one.commit || '—' }}</td>
                <td class="mono small">
                  <button
                    v-if="one.image"
                    class="link"
                    type="button"
                    @click="showImage(one.image)"
                  >
                    {{ namedImage(one.image) }}
                  </button>
                  <span v-else>—</span>
                </td>
                <td class="state-cell">
                  <div class="badges">
                    <span class="badge" :class="badgeClass(one.state)">{{ one.state }}</span>
                    <span v-if="live.has(one.id)" class="badge badge-green">running now</span>
                  </div>
                </td>
                <!-- Its own column, not under the status: a status and a sentence are
                     two different facts and reading them as one — a badge with a
                     paragraph hanging off it — makes the paragraph look like part of
                     the status. -->
                <td class="pods-cell mono small">{{ podsOf(one) || '—' }}</td>
                <td class="what-cell">{{ one.reason || '—' }}</td>
              </tr>
            </tbody>
            </table>
          </div>

          <!-- Each move asks the server for that page. The count comes from the same
               answer, so the control cannot promise rows that were never counted. -->
          <div v-if="pages > 1" class="pager">
            <button
              class="btn btn-small"
              type="button"
              :disabled="page === 1"
              @click="goToPage(page - 1)"
            >
              Newer
            </button>
            <span class="muted small">Page {{ page }} of {{ pages }} · {{ total }}</span>
            <button
              class="btn btn-small"
              type="button"
              :disabled="!hasMore"
              @click="goToPage(page + 1)"
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
            <li
              v-for="image in shownImages"
              :key="image.digest"
              class="image"
              :class="{ wanted: wanted && image.digest.endsWith(wanted) }"
            >
              <span class="image-name mono">{{ shortImage(image.digest) }}</span>
              <span class="muted small">
                {{ image.times }} operation{{ image.times === 1 ? '' : 's' }},
                {{ image.succeeded }} successful
              </span>
              <span v-if="image.live && live.has(image.live.id)" class="badge badge-green">
                running now
              </span>
              <button
                v-if="props.canManage && image.live && !live.has(image.live.id)"
                class="btn btn-small"
                type="button"
                :disabled="busy"
                :title="
                  live.has(image.live.id)
                    ? 'This is the image running now, so there is nothing to put back'
                    : 'Put this image back on the workload'
                "
                @click="revertTo(image.live)"
              >
                Revert to this
              </button>
              <span v-else-if="!image.live || !live.has(image.live.id)" class="muted small">
                not in the cluster now
              </span>
            </li>
          </ul>

          <div v-if="imagePages > 1" class="pager">
            <button
              class="btn btn-small"
              type="button"
              :disabled="imagePage === 1"
              @click="goToImagePage(imagePage - 1)"
            >
              Newer
            </button>
            <span class="muted small">Page {{ imagePage }} of {{ imagePages }} · {{ imageTotal }}</span>
            <button
              class="btn btn-small"
              type="button"
              :disabled="!imageHasMore"
              @click="goToImagePage(imagePage + 1)"
            >
              Older
            </button>
          </div>
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

/* The edge is the card's verdict, in the same three colours as the marks beside the
   steps: yellow while something is moving, green when the last one ended well, red
   when it did not. Read at a glance from across a page, which is what an edge is
   for. */
.active-card.working {
  border-left-color: var(--yellow);
}

.active-card.ok {
  border-left-color: var(--green);
}

.active-card.bad {
  border-left-color: var(--red);
}

/* The rollout's own number, big enough to read while watching and not so big that
   it becomes the thing the card is about. */
.pods {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 10px 0 2px;
}

.pods-count {
  font-size: 20px;
  font-weight: 600;
  color: var(--text);
}

.pods-bar {
  flex: 1 1 auto;
  height: 4px;
  min-width: 60px;
  border-radius: 2px;
  background: var(--border);
  overflow: hidden;
  align-self: center;
}

.pods-fill {
  display: block;
  height: 100%;
  background: var(--green, #3fb950);
  transition: width 0.4s ease;
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

/* The image somebody arrived here to look at. Held rather than scrolled to, because
   the page it is on may change under them, and a row that flashes and goes is worse
   than one that stays. */
.image.wanted {
  background: var(--yellow-soft);
  border-radius: 4px;
  padding-left: 8px;
  margin-left: -8px;
}

/* An image in a row of operations is a link to that image, not an action on it. */
.link {
  background: none;
  border: none;
  padding: 0;
  color: var(--accent);
  font: inherit;
  font-family: var(--mono);
  font-size: 12px;
  cursor: pointer;
  text-align: left;
  text-decoration: underline;
}

.link:hover {
  color: var(--text);
}

/* The status is a badge and nothing else, so the column has room for one and the
   sentence beside it is a sentence in its own right. */
.state-cell {
  min-width: 130px;
}

/* The module's own words, wrapped rather than cut. They are the only part of the
   row that says what happened, and a sentence that stops at "...were left as they
   are" has stopped exactly where it started being interesting — and every row with
   a long reason is the same length on screen, so the column looks empty. */
.what-cell {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.4;
  white-space: normal;
  overflow-wrap: anywhere;
  min-width: 220px;
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