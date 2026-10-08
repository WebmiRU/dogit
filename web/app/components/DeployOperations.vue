// Every deployment under way, and the ones just finished, one card each.
//
// The question this page is opened for is "what is happening to this place right now", and the
// answer is not one thing: two deployments to the same place, or one to each of two places, happen
// at the same time and have to be drawn apart. A page with one card cannot draw them apart — it has
// one set of steps, and whichever operation spoke last owns them.
//
// So every card is about one operation, and a card's identity is the job id, because that is what
// every event about a deployment carries. Not the place: a place is what the card is *about*, and two
// operations can share one. Not the time either, for the same reason.
//
// Where the list comes from matters as much as how it is drawn. It is fetched first and the socket
// only ever adds to it, because a socket knows about what happened while somebody was watching: open
// the page an hour into a rollout and the feed has nothing to say, so a page built from the feed alone
// draws an empty page for a project that is mid-deploy.
<script setup lang="ts">
import type { DeployProgress } from '~/types/pipeline'

import DeployLog from './DeployLog.vue'
import DeploySteps from './DeploySteps.vue'

/** One operation, as the core described it when it was asked. */
interface DeployOperation {
  /** The operation's identity. Everything hangs off it, including which card. */
  job_id: number
  status: string
  name: string
  /** Which place, by name. Empty only on a page that was not told about a place. */
  place: string
  error: string
  /** Unix milliseconds. Null when the operation has not begun yet. */
  started_at: number | null
  finished_at: number | null
  running: boolean
  /** Waiting its turn: asked for, not started, and not over. */
  queued: boolean
}

interface OperationsAnswer {
  active?: DeployOperation[]
  finished?: DeployOperation[]
  /** Drawn rather than treated as an error: "no deploy module" is an ordinary answer. */
  reason?: string
}

/** What the page knows about one operation while it watches. */
interface Watching {
  /** What the core called this operation: a deployment or a rollback. Its steps differ. */
  kind?: string
  /** The most recent thing said about it. */
  progress: DeployProgress | null
  /** Everything said so far, so a step that finished earlier keeps its tick. */
  seen: DeployProgress[]
  /** The phases happening at once, which is not one: a rollout sends old pods away as it brings new ones up. */
  phases: string[]
  /** When the first line arrived, which is the only clock here — nothing upstream stamps the moment. */
  since: number
}

const props = defineProps<{
  projectId: string
  /**
   * The name of the place this card is about, or empty for a page that was not told about one.
   *
   * A name and not a cluster, because that is what a deploy step says: `target: jabjab.ru`. The
   * name is a row of the deploy module's own settings, and what that row holds inside it is the
   * module's business. Asking for a cluster instead would be asking a question the step never
   * answered, and a project with two places in one cluster would get a page for each rather than
   * one for the place.
   */
  place?: string
}>()

/** How many finished operations to keep. Ten fills a page and still scrolls. */
const FINISHED = 10

/** One line of an operation's log, as the core stored it. */
interface LogEntry {
  /** Unix milliseconds. Absent for a line written before times were recorded. */
  at?: number
  stream: 'out' | 'err'
  text: string
}

const operations = ref<DeployOperation[]>([])
const reason = ref('')
const loading = ref(false)
const error = ref('')
const watching = ref<Record<number, Watching>>({})

/** The log of a finished operation, fetched on demand and kept per operation. */
const logs = ref<Record<number, LogEntry[] | 'asked'>>({})

/** When this page subscribed, so that a line older than this says a hole was missed. */
let subscribedAt = Date.now()

/** Whether a hole has already sent this page back for its list. One is enough. */
let gapNoticed = false

const query = computed(() => {
  const parts = [`finished=${FINISHED}`]
  if (props.place) parts.push(`place=${encodeURIComponent(props.place)}`)
  return parts.join('&')
})

// Running first, and each half newest-first, which is the order the core sends them in. The join
// rather than a single sort because "running deployments, oldest last" and "finished deployments,
// newest last" are two orderings, and one sort over the union of both cannot express either.
const cards = computed(() => [
  ...operations.value.filter((one) => one.running),
  ...operations.value.filter((one) => !one.running),
])

/** Everything the core sends in one answer is both halves; the sort is by what was begun. */
function reorder(answer: OperationsAnswer) {
  operations.value = [...(answer.active ?? []), ...(answer.finished ?? [])]
}

async function load() {
  loading.value = true
  try {
    const answer = await api.get<OperationsAnswer>(
      `/projects/${props.projectId}/deploy-operations?${query.value}`)
    reason.value = answer.reason ?? ''
    reorder(answer)
    error.value = ''
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  void loadPlan()
})
watch(() => [props.projectId, query.value], load)

/**
 * Whether this card belongs on this page.
 *
 * A card told about a place only claims lines about that place. A card told about none claims
 * everything — it is the page about a whole project, and every deployment in it is one of its lines.
 * The same question the rows ask, in the same terms: a project with two places has two rollouts at
 * once, and one card showing both says the first place is deploying while it waits its turn.
 */
function isMine(payload: Record<string, unknown>): boolean {
  if (!props.place) return true
  const said = payload.deployment as { cluster?: string; namespace?: string } | undefined
  if (!said || typeof said !== 'object') return false
  // Either spelling of the place, for the same reason the query asks by name and matches both:
  // a module of the current vintage reports the name it was given, and an older one reports the
  // cluster and namespace it was configured with. Comparing only the new one drops every line of
  // an operation already under way, and the card then shows its state and none of its steps.
  const cluster = said.cluster ?? ''
  const namespace = said.namespace ?? ''
  return cluster === props.place || namespace === props.place
}

/**
 * What one line did to what is known about its operation.
 *
 * Every line is kept, keyed by the job it is about, so two operations interleaving on the socket
 * each keep their own steps. One shared set of steps is what the page used to have, and it is why
 * a rollout and a rollback at the same moment drew the same arrows.
 */
function note(payload: Record<string, unknown>) {
  if (typeof payload.message !== 'string' || !payload.message) return
  const jobID = Number(payload.job_id ?? 0)
  if (!jobID) return

  // A line older than the moment this page subscribed is a line the fetch that drew this list did
  // not have. The log would then begin in the middle and look whole, which is the one thing a
  // deployment log must not be. One request per page, not one per line: the first such line is
  // enough to know, and every later one says the same.
  const at = typeof payload.at === 'number' ? payload.at : null
  if (at !== null && at < subscribedAt && !gapNoticed) {
    gapNoticed = true
    void load()
  }

  const said: DeployProgress = {
    phase: String(payload.phase ?? ''),
    message: payload.message,
    ready: Number(payload.ready ?? 0),
    desired: Number(payload.desired ?? 0),
    step: Number(payload.step ?? 0),
    of: Number(payload.of ?? 0),
    previous: typeof payload.previous === 'string' && payload.previous ? payload.previous : undefined,
    retiring: typeof payload.retiring === 'number' ? payload.retiring : undefined,
    finished: payload.finished === true,
    failed: payload.failed === true,
  }

  const already = watching.value[jobID]
  const phases = new Set(already?.phases ?? [])
  if (said.phase) {
    if (said.finished) phases.delete(said.phase)
    else phases.add(said.phase)
  }

  const saidAbout = payload.deployment as { kind?: string } | undefined
  watching.value = {
    ...watching.value,
    [jobID]: {
      // What this operation is, the first time the core says. Kept because the steps drawn on
      // this card are the ones for that kind of operation, and a rollback drawn with a
      // deployment's steps is a card describing work that is not happening.
      ...(typeof saidAbout?.kind === 'string' && saidAbout.kind
        ? { kind: saidAbout.kind }
        : already?.kind
          ? { kind: already.kind }
          : {}),
      progress: said,
      seen: [...(already?.seen ?? []), said],
      phases: [...phases],
      // From the first line rather than from the event's own clock: the module narrates, the core
      // relays, and neither stamps the moment. Zero means "just now", which is true here.
      since: already?.since ?? Date.now(),
    },
  }

  // A deployment the core has not heard of yet is added, so that a page open when one starts shows
  // it without waiting for a reload. The start moment is the line's own: the core's answer will
  // replace it with the real one a moment later, and a card that moved once on load is better than
  // a deployment that did not appear until a reload.
  if (!operations.value.some((one) => one.job_id === jobID)) {
    operations.value = [
      ...operations.value,
      { job_id: jobID, status: 'running', name: '', place: props.place ?? '',
        error: '', started_at: Date.now(),
        finished_at: null, running: true },
    ]
  }
}

/**
 * The log of an operation that is over, asked for once.
 *
 * Asked rather than carried on the list because most cards are never opened, and a log for every
 * one of ten finished deployments is ten round trips for a page nobody reads that far down. Live
 * operations do not come here at all — their lines are arriving over the socket, and a card that
 * showed both would show every line twice.
 */
async function askLog(jobID: number) {
  if (logs.value[jobID]) return
  logs.value = { ...logs.value, [jobID]: 'asked' }

  try {
    const answer = await api.get<{ entries?: LogEntry[] }>(
      `/projects/${props.projectId}/deploy-operations/${jobID}/log`)
    logs.value = { ...logs.value, [jobID]: answer.entries ?? [] }
  } catch {
    // A log that could not be read is drawn as "not recorded" rather than as a failure card: the
    // deployment's own outcome is on the card above it and is unaffected by whether its output is
    // readable.
    logs.value = { ...logs.value, [jobID]: [] }
  }
}

/**
 * What a card is watching, or nothing.
 *
 * Nothing is a real answer and not an empty object: a card for a finished operation has no lines
 * arriving and must not be drawn as though some had.
 */
function watched(jobID: number): Watching | null {
  return watching.value[jobID] ?? null
}

/**
 * What an operation's log says, or nothing while it is being asked for.
 *
 * The `'asked'` marker is what keeps a disclosure from firing a request on every click: an empty
 * array is an answer, and so is a promise of one.
 */
function logOf(jobID: number): LogEntry[] | undefined {
  const found = logs.value[jobID]
  return Array.isArray(found) ? found : undefined
}

/**
 * What a disclosure says about a log that may not have been asked for yet.
 *
 * Three states and not two: not asked, asked and empty, asked and not. The middle one is what a
 * two-state version gets wrong — it says the deployment recorded nothing when in fact nobody has
 * looked, which on a page full of old deployments is a claim about all of them at once.
 */
function logLabel(jobID: number): string {
  const found = logs.value[jobID]
  if (found === 'asked') return 'Looking…'
  if (!found) return 'What it said'
  return found.length ? `What it said (${found.length})` : 'Nothing was recorded'
}

/**
 * What an operation under way has said, in the shape the log draws.
 *
 * Taken from this operation's own lines and no other. A shared list of lines is what made a
 * rollout and a rollback arriving together draw one log between them, and it is the same mistake
 * the single card made with its steps.
 */
function logLines(jobID: number): { phase: string; message: string; step: number; of: number }[] {
  return (watching.value[jobID]?.seen ?? []).map((one) => ({
    phase: one.phase,
    message: one.message,
    step: one.step,
    of: one.of,
  }))
}

/**
 * The phases of this operation that are still open — and none at all, once it is over.
 *
 * The module closes a phase when the next one begins, so the last phase of a deployment is
 * never closed by a line: there is no next one. That is deliberate, and the contract is that the
 * page decides the operation is over from the operation itself. Taken straight off the socket
 * the arrow therefore stayed on "Bring the new pods up" for ever, on a rollout whose own log
 * said "every pod is running the new image (3 of 3)" and then "finished" — the lines that
 * should have closed it were on the card, and the step they belonged to stayed open anyway.
 *
 * The list is what says an operation is over, so the list is what closes it. A page that had
 * been open the whole time and a page opened afterwards are then in the same state, which is
 * the only reason to have a list at all.
 */
function openPhases(operation: DeployOperation): string[] {
  if (!operation.running) return []
  return watched(operation.job_id)?.phases ?? []
}

/** When a line was said, to the second — enough to place it, short enough not to shout. */
function clockOf(at: number): string {
  return new Date(at).toLocaleTimeString(undefined, { hour12: false })
}

/**
 * What the card calls this operation, in a word.
 *
 * The job's own status is a word from a database column, and four of them are not words a person
 * says. "refused" and "skipped" in particular: both mean nothing happened, and a card that says
 * either of them is quoting the column rather than answering the reader's question.
 */
function stateOf(operation: DeployOperation): string {
  if (operation.queued) return 'Waiting'
  if (operation.running) return 'Running'
  if (operation.status === 'refused') return 'Declined'
  if (operation.status === 'skipped') return 'Skipped'
  if (operation.status === 'canceled') return 'Cancelled'
  if (operation.status === 'interrupted') return 'Interrupted'
  if (operation.status === 'success') return 'Success'
  return operation.status || 'Failed'
}

/**
 * The card's edge, from the same rule as its badge.
 *
 * Three, and the same three the steps use: yellow while it happens, blue while it waits, and no
 * colour of its own when nothing happened. One fact drawn in two colours is a reader being asked
 * to decide which of them to believe.
 */
function edgeOf(operation: DeployOperation): string {
  if (operation.queued) return 'queued'
  if (operation.running) return 'live'
  if (operation.status === 'refused' || operation.status === 'skipped') return 'dismissed'
  return ''
}


function when(operation: DeployOperation): string {
  if (!operation.started_at) return ''
  return timeAgo(new Date(operation.started_at))
}

/** How long it took, once it is over. Nothing while it is going: a clock would have to tick. */
function took(operation: DeployOperation): string {
  if (!operation.started_at || !operation.finished_at) return ''
  const ms = operation.finished_at - operation.started_at
  // Nothing said for a deployment that started and ended in the same instant. The number is
  // real, but "took 0s" beside "3 days ago" reads as a machine that has just run, and a reader
  // checks the clock to see which of the two they believe.
  if (ms < 1000) return ''
  return formatDuration(ms)
}

/**
 * One colour per meaning, and the same meaning everywhere on the page.
 *
 * Five states, and the colours are the ones the steps already use, because a card and the list
 * of steps under it are two renderings of one deployment and a reader should not have to learn
 * the vocabulary twice:
 *
 *   green   it ended well
 *   yellow  it is happening now
 *   blue    it has not started — waiting its turn, and known to be coming
 *   grey    it did not happen: refused by the module, or skipped by a rule
 *   red     it broke
 *
 * Red is reserved for the last of those on purpose. A refusal is not a fault: nothing is broken
 * and nothing is red in the cluster, the place was simply busy, and the next attempt may well
 * work. Painted red it reads as somebody's afternoon going wrong, over a sentence that says
 * something was declined and nothing was touched.
 */
const tone: Record<string, string> = {
  success: 'badge-green',
  running: 'badge-warning',
  pending: 'badge-blue',
  refused: 'badge-neutral',
  skipped: 'badge-neutral',
  interrupted: 'badge-neutral',
  failed: 'badge-danger',
  error: 'badge-danger',
  canceled: 'badge-neutral',
  abandoned: 'badge-danger',
}

watchEvents({
  // The core's own line about a deployment, relayed whole. Many a minute while a rollout runs, so
  // this is the only path that has to be cheap.
  onEvent: (event) => {
    if (event.kind === 'deploy.history') {
      // A deployment ended, and this is the only line that says so. Without it the list is
      // read once and never again, so a card stays "Running" with its last phase open for as
      // long as the page is left open — a rollout that finished in a third of a second went on
      // saying "Bring the new pods up" for five minutes, with the line that closed it sitting
      // in the log underneath.
      void load()
      return
    }
    if (event.kind !== 'deploy.operation') return
    const payload = event.payload ?? {}
    if (!isMine(payload)) return
    note(payload)
  },
  // A reconnection means events were missed, and an event is the only place a live operation's
  // progress lives. The list is asked again rather than patched: what was missed is more than a
  // line or two, and a card patched from a gap draws steps nobody saw finish.
  onReconnect: load,
})

/**
 * The steps a deployment goes through, asked of the core rather than collected from what arrives.
 *
 * This is the difference between a card that says what is going to happen and a card that reveals
 * it a line at a time. The steps are not news: they come out of the deploy step's own
 * configuration, they are the same for every deployment of this kind, and they are known before
 * the first pod is touched. Waiting for lines to announce them draws a card that starts empty and
 * fills in, which reads as work starting late rather than as a page that had not been told — and
 * a reader watching it cannot see how far along it is, because there is nothing yet to be along of.
 *
 * Both kinds, because a rollback has its own steps, and drawing a deployment's under a rollback is
 * a card describing something that is not happening.
 */
const plans = ref<Record<string, { key: string; label: string }[]>>({})

/** The steps for one operation, by what that operation is. A deployment unless it says otherwise. */
function planOf(jobID: number): { key: string; label: string }[] {
  const kind = watching.value[jobID]?.kind === 'revert' ? 'revert' : 'deploy'
  return plans.value[kind] ?? []
}

async function loadPlan() {
  for (const kind of ['deploy', 'revert']) {
    try {
      const answer = await api.get<{ steps?: { key: string; label: string }[] }>(
        `/projects/${props.projectId}/deploy-plan${kind === 'revert' ? '?kind=revert' : ''}`)
      if (Array.isArray(answer.steps) && answer.steps.length) {
        plans.value = { ...plans.value, [kind]: answer.steps }
      }
    } catch {
      // A plan that cannot be had is a card with no steps on it, which is what this page drew
      // before it asked. Not an error on the card: the deployment is going ahead either way, and
      // a red line over somebody's rollout says the rollout broke.
    }
  }
}

/** The core announcing a plan on the socket, which it does as well as over HTTP. */
watchEvents({
  onEvent: (event) => {
    if (event.kind !== 'deploy.plan') return
    const steps = event.payload?.steps
    if (!Array.isArray(steps) || !steps.length) return
    const kind = event.payload?.kind === 'revert' ? 'revert' : 'deploy'
    plans.value = { ...plans.value, [kind]: steps as { key: string; label: string }[] }
  },
})
</script>

<template>
  <div class="operations">
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="reason" class="empty">{{ reason }}</div>
    <div v-else-if="loading && cards.length === 0" class="empty">Looking…</div>
    <div v-else-if="cards.length === 0" class="empty">
      Nothing is being deployed to this place, and nothing has been for a while.
    </div>

    <!-- One card per operation. The key is the job: two cards for one operation is the bug this
         replaces, and a card that changes identity as it runs is the same bug wearing a hat. -->
    <section
      v-for="operation in cards"
      :key="operation.job_id"
      class="card operation"
      :class="edgeOf(operation)"
    >
      <div class="card-body">
        <div class="block-head">
          <span class="badge" :class="tone[operation.status] ?? 'badge-neutral'">
            {{ stateOf(operation) }}
          </span>
          <span v-if="operation.name" class="mono small">{{ operation.name }}</span>
          <span v-if="operation.place" class="place-chip mono">{{ operation.place }}</span>

          <span class="spacer" />

          <span v-if="when(operation)" class="muted small">{{ when(operation) }}</span>
          <!-- Only when it took something. A duration of zero is a card saying "0s" beside
               "3 days ago", which reads as a machine that has just run rather than one that
               finished three days ago in no time at all. -->
          <span v-if="took(operation)" class="muted small mono">took {{ took(operation) }}</span>
        </div>

        <!-- The steps, from the core's plan rather than from what has arrived, and marked off by
             the lines about *this* operation — which is the only thing on the page that can tell
             two of them apart. Drawn for a card that is running even before the first line: an
             operation that has begun and said nothing yet is still an operation with steps, and a
             card that shows none of them until the first pod moves is a card that looks idle. -->
        <DeploySteps
          v-if="operation.running"
          :progress="watched(operation.job_id)?.progress ?? null"
          :seen="watched(operation.job_id)?.seen ?? []"
          :live="true"
          :plan="planOf(operation.job_id)"
          :active-phases="openPhases(operation)"
        />

        <!-- The log of an operation under way, streaming, under its steps. Not asked for: these
             lines are arriving, and a request for what has already been said would be a request
             for a slower copy of the same thing. -->
        <DeployLog
          v-if="operation.running"
          :lines="logLines(operation.job_id)"
          :plan="planOf(operation.job_id)"
        />

        <!-- The log of an operation that is over, and only of an operation that is over. -->
        <details v-if="!operation.running" class="log" @toggle="askLog(operation.job_id)">
          <!-- Says what it knows before it has looked. "Nothing was recorded" on a log that
               has not been asked for yet is a claim made without having checked, and it is
               the wrong claim on the deployments that are the most interesting to look at. -->
          <summary class="muted small">{{ logLabel(operation.job_id) }}</summary>
          <ul v-if="logOf(operation.job_id)?.length" class="operation-log">
            <li
              v-for="(entry, index) in logOf(operation.job_id)"
              :key="index"
              :class="entry.stream"
            >
              <span v-if="entry.at" class="mono small muted">{{ clockOf(entry.at) }}</span>
              <span>{{ entry.text }}</span>
            </li>
          </ul>
        </details>

        <p v-if="operation.error" class="alert alert-error small">{{ operation.error }}</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.operations {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* Its own copy, because a scoped rule reaches nothing outside the component that writes it.
 * Borrowing the name from the card this one replaces got the line of badges and names to run
 * together with no gap at all — "deploylocal-k3s/dogit-dev" — which is what a heading with no
 * definition looks like. */
.block-head {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 6px 10px;
  margin-bottom: 8px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border, #e2e2e6);
}

/* The log of an operation that is over, in the same shape the deploy log is drawn in elsewhere:
   one line each, errors in the error colour. A time beside each, because that is what makes a gap
   visible to whoever is reading rather than only to whoever wrote the page. */
.operation-log {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 12px;
  line-height: 1.6;
}

.operation-log li {
  display: flex;
  gap: 10px;
  padding: 1px 0;
  word-break: break-word;
}

.operation-log li.err {
  color: var(--danger, #d9534f);
}

.operation-log .mono {
  flex: 0 0 auto;
  opacity: 0.65;
}

/* The place, as a chip rather than as text among text. It repeats on every card of a project with
   one place, so it is not news — but on a page whose cards are the unit, the place is the question
   a reader answers first, and a line that mixes it in with the step name makes it the second. */
.place-chip {
  font-size: 12px;
  padding: 1px 7px;
  border: 1px solid var(--border);
  border-radius: 3px;
  background: var(--bg);
}

/* The card of a deployment under way, marked by a line down its edge.
   Yellow, and one rule for everything that says so — the edge and the badge and the word beside
   them. They are three renderings of one fact, and a page where the edge is one colour and the
   badge another gives a reader two answers to "is it going well" and leaves them to pick.

   Yellow rather than green, and this is not a preference. On this page green means a deployment
   ended well, and a rollout that is still going has not ended at all: an edge wearing success
   for the two minutes everybody is waiting claims the answer before there is one. Not red
   either — nothing has failed, and a red edge is a thing to go and look at.

   The accent colour it used to wear said nothing in particular. It read as "this is the selected
   one" or "this is the current page", neither of which is a thing anybody asked about. */
.operation.live {
  border-left: 3px solid var(--yellow);
}

/* Waiting. The accent blue, which is the colour the steps already use for a step not yet
   reached, and deliberately not grey: "has not come to it yet" is a known state and not an
   absence, and grey reads as this page having not been told. */
.operation.queued {
  border-left: 3px solid var(--accent);
}

/* Declined or skipped. No edge of its own — nothing happened, and there is nothing here to
   draw attention to. What is on the card is the sentence the module wrote. */
.operation.dismissed {
  border-left: 3px solid var(--border-strong, var(--border));
}

.operation.live .badge {
  font-weight: 600;
}
</style>
