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
  /**
   * What this operation had last said about itself, when the core was keeping it. Absent for
   * one that has not begun, and for one that began before the core did — and a page told
   * nothing can say so, which is different from a page told zero.
   */
  progress?: Record<string, unknown>
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

/**
 * Cards this reader has put away, by job.
 *
 * Not persisted, and that is a decision rather than an omission. A card put away comes back on
 * the next visit to the page, which is the escape hatch: a card that could be hidden with no way
 * to bring it back is a card somebody stops trusting the page to show them. Persisting it would
 * need a decision about when it returns — a day? a week? until the deployment is pruned? — and
 * every one of those is a rule about somebody's memory that the interface has no business making.
 *
 * Kept by job id and not by position, so a card put away stays put away when the page reloads
 * its list underneath it, which is what happens every time a deployment ends.
 */
const putAway = ref<Record<number, true>>({})

/**
 * Operations this reader watched happen, rather than found already in the list.
 *
 * The distinction decides what a card is allowed to become. A card the reader watched from its
 * first line to its last keeps the shape it had while it was running — the steps, the log
 * streaming under them — and the reader can put it away. A card that was already in the list when
 * the page opened is an ordinary entry in a history: a badge, a line about when, and a log
 * folded away. It is not the reader's card, so it gets no cross, and it does not pretend to be
 * something they watched.
 *
 * Recorded by operation, and set the moment a line arrives about an operation this page had not
 * heard of. A page that was open when a deployment started has it; a page opened afterwards does
 * not, and draws the other thing — which is the honest answer, because it is the other thing.
 */
const caught = ref<Record<number, true>>({})

/** Whether this card is one the reader watched, and may put away. */
function isCaught(operation: DeployOperation): boolean {
  return operation.running || Boolean(caught.value[operation.job_id])
}
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
//
// Running and waiting are not lumped together: a card for something happening now and a card for
// something that has not started are read in the same glance, and putting the finished ones
// between them would break the only ordering a reader is actually using.
const cards = computed(() => [
  ...operations.value.filter((one) => one.running || one.queued),
  ...operations.value.filter((one) => !one.running && !one.queued),
])

/**
 * The reader's own cards: what is under way, and what they caught and have not put away.
 *
 * Above the list rather than instead of it. A deployment they watched is *also* down there,
 * with its own log — the card here is an extra thing to watch something while it happens, and it
 * was never meant to take the place of the record. It did, for as long as the two were the same
 * thing, and a page that stops listing what it has done the moment somebody starts watching it
 * is a page that forgets.
 *
 * A card here keeps the shape it had while it was running when it finishes: the steps, the log
 * under them, and the cross. A deployment that ends does not take its card down — the moment it
 * ended is usually the moment somebody starts reading why.
 */
const live = computed(() =>
  cards.value.filter((one) => (one.running || caught.value[one.job_id]) && !putAway.value[one.job_id]))

/** The record, as it always was: every operation, whether or not anybody watched it. */
const history = computed(() => cards.value)

/** Everything the core sends in one answer is both halves; the sort is by what was begun. */
function reorder(answer: OperationsAnswer) {
  // The operations endpoint can classify an operation as queued while its durable progress
  // snapshot already contains phase updates. Progress is stronger evidence: a queued operation
  // has not emitted deployment phases yet. Without reconciling these fields, a page refresh paints
  // an in-flight deployment blue ("Waiting") and lateProgress() discards its active arrows.
  const normalize = (operation: DeployOperation): DeployOperation => {
    if (!operation.queued || !operation.progress || typeof operation.progress !== 'object') {
      return operation
    }
    const progress = operation.progress
    const activePhases = Array.isArray(progress.active_phases)
      ? progress.active_phases.filter((phase) => typeof phase === 'string' && phase !== '')
      : []
    const history = Array.isArray(progress.phase_history)
      ? progress.phase_history.filter((line): line is Record<string, unknown> =>
        Boolean(line) && typeof line === 'object')
      : []
    // Older snapshots may have the phase history but no active_phases field. A phase's
    // latest recorded line is authoritative: finished phases stay green; an unfinished one
    // means the deployment is still doing work.
    const historyHasActive = history.some((line) =>
      typeof line.phase === 'string' && line.phase !== '' && line.finished !== true)
    const active = activePhases.length > 0 || historyHasActive ||
      (typeof progress.phase === 'string' && progress.phase !== '' && progress.finished !== true)
    if (!active) return operation
    return { ...operation, status: 'running', running: true, queued: false }
  }
  const next = [...(answer.active ?? []), ...(answer.finished ?? [])].map(normalize)
  operations.value = next

  // A reconnect is a fresh snapshot, not merely a request to redraw the list: whatever the page
  // missed while the socket was down is in here and nowhere else.
  //
  // Added to what this page already has, and not put in its place. A card the reader watched is
  // the richer record — every line of it, in the order they arrived — and a snapshot is one line
  // per phase. Replacing with it takes away what the reader was watching because their connection
  // blinked, and it is their own record being taken from them.
  //
  // The latest line is taken from the snapshot rather than kept, because that is the one thing it
  // is for: it is written as each line is relayed, so it is at least as fresh as anything this
  // page heard. The phases are the union of both, since a page says a phase began until it is
  // told otherwise and the core says the same, and the two agreeing is the normal case.
  const restored = { ...watching.value }
  for (const operation of next) {
    if (!operation.running || !operation.progress) continue
    const snapshot = lateProgress(operation)
    if (!snapshot) continue

    const mine = restored[operation.job_id]
    if (!mine) {
      restored[operation.job_id] = snapshot
      continue
    }

    const heard = new Set(mine.seen.map((line) => `${line.phase}\u0000${line.message}`))
    const missing = snapshot.seen.filter((line) => !heard.has(`${line.phase}\u0000${line.message}`))
    restored[operation.job_id] = {
      ...mine,
      kind: mine.kind ?? snapshot.kind,
      progress: snapshot.progress,
      seen: [...mine.seen, ...missing],
      phases: [...new Set([...mine.phases, ...snapshot.phases])],
    }
  }
  watching.value = restored
}

async function load() {
  loading.value = true
  try {
    const answer = await api.get<OperationsAnswer>(
      `/projects/${props.projectId}/deploy-operations?${query.value}`)
    reason.value = answer.reason ?? ''
    reorder(answer)
    error.value = ''
    // The log of anything under way that this page did not watch. One request per operation,
    // once, on arrival: a rollout says many lines a minute and the socket is carrying the ones
    // from here on, so this is not a stream being duplicated — it is the part of the account
    // that happened before anybody was looking.
    for (const one of operations.value) {
      if (one.running && !caught.value[one.job_id]) void askLog(one.job_id)
    }
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
  if (!caught.value[jobID]) {
    caught.value = { ...caught.value, [jobID]: true }
  }
  if (!operations.value.some((one) => one.job_id === jobID)) {
    operations.value = [
      ...operations.value,
      { job_id: jobID, status: 'running', name: '', place: props.place ?? '',
        error: '', started_at: Date.now(),
        finished_at: null, running: true, queued: false },
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
  // A line that names no step in a list of them is a line about the operation as a whole, and
  // zero of zero is what that is: the step list it is filed under has nothing to number yet.
  return (watching.value[jobID]?.seen ?? []).map((one) => ({
    phase: one.phase,
    message: one.message,
    step: one.step ?? 0,
    of: one.of ?? 0,
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
  // A snapshot can carry an active phase even when the API's queued/running flags are stale.
  // Use the same restored state as the badge and steps rather than suppressing its arrow.
  return knows(operation)?.phases ?? []
}

/** When a line was said, to the second — enough to place it, short enough not to shout. */
function clockOf(at: number): string {
  return new Date(at).toLocaleTimeString(undefined, { hour12: false })
}

/**
 * The one line that says what happened, for a card that did not go well.
 *
 * A refusal gets the module's own sentence, in its own words, and it is put where it cannot be
 * missed: a card that says "Declined" in grey and keeps the reason under a folded log has told
 * the reader nothing they would act on. A failure gets the same sentence, because the module
 * wrote it either way, and the two differ in what they mean rather than in who said it.
 */
function verdictOf(operation: DeployOperation): string {
  if (operation.status === 'refused' || operation.status === 'skipped') {
    return operation.error || 'nothing was deployed here'
  }
  if (operation.status === 'failed') {
    return operation.error || 'the deployment did not finish'
  }
  return ''
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
  const operation = operations.value.find((one) => one.job_id === jobID)
  const savedKind = operation?.progress?.deployment as { kind?: unknown } | undefined
  const kind = watching.value[jobID]?.kind === 'revert' || savedKind?.kind === 'revert'
    ? 'revert'
    : 'deploy'
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

/**
 * What a page that arrived late knows about an operation that is under way.
 *
 * The core keeps the fields of the last line each deployment said, and hands them over with the
 * list. That is what puts a mark on the step a rollout is on: without it a page opened halfway
 * through draws the plan — which it has, the steps are the same for every deployment of this
 * kind — with nothing under it, and every step in the colour of a step not yet reached. A reader
 * opening a deployment in progress was told it had not begun.
 *
 * Only the newest line, and only its fields. The account of how it got there is in the log, which
 * is fetched for this card like any other; this is here to answer where it is now, which is the
 * one thing a reader opening the page is asking.
 */
function lateProgress(operation: DeployOperation): Watching | null {
  // Only for a card this page did not watch. Asking "is it caught" here would be asking
  // "is it running", and that is true of every card under way — so the note the core kept
  // would be thrown away on exactly the cards it was kept for, and a page opened during a
  // rollout would be back to seven steps with nothing marked.
  if (operation.queued) return null
  const said = operation.progress
  if (!said || typeof said !== 'object') return null

  const toProgress = (line: Record<string, unknown>): DeployProgress => ({
    phase: String(line.phase ?? ''),
    message: String(line.message ?? ''),
    ready: Number(line.ready ?? 0),
    desired: Number(line.desired ?? 0),
    step: Number(line.step ?? 0),
    of: Number(line.of ?? 0),
    previous: typeof line.previous === 'string' && line.previous ? line.previous : undefined,
    retiring: typeof line.retiring === 'number' ? line.retiring : undefined,
    finished: line.finished === true,
    failed: line.failed === true,
  })
  const progress = toProgress(said)
  // The core keeps the latest line for each phase and the set of phases still open.
  // Use both: the last line alone cannot restore earlier green ticks or simultaneous
  // rollout/retire arrows after a refresh.
  const history = Array.isArray(said.phase_history)
    ? said.phase_history.filter((line): line is Record<string, unknown> =>
      Boolean(line) && typeof line === 'object')
    : []
  const seen = history.map(toProgress)
  if (progress.phase && !seen.some((line) => line.phase === progress.phase)) {
    seen.push(progress)
  }
  // Completion is state, not prose. Only the structured flag is authoritative here;
  // the API persists phase-closing records with finished=true.
  const finishedByHistory = new Set(history
    .filter((line) => line.finished === true)
    .map((line) => String(line.phase ?? ''))
    .filter(Boolean))
  const active = (Array.isArray(said.active_phases)
    ? said.active_phases.filter((phase): phase is string => typeof phase === 'string')
    : (progress.phase && !progress.finished ? [progress.phase] : []))
    .filter((phase) => !finishedByHistory.has(phase))
  const saidAbout = said.deployment as { kind?: unknown } | undefined
  return {
    ...(typeof saidAbout?.kind === 'string' && saidAbout.kind ? { kind: saidAbout.kind } : {}),
    progress,
    // No clock: this card was not watched, and inventing a start time would put a duration
    // on the page that nobody observed.
    seen,
    phases: active,
    since: 0,
  }
}

/** What this card knows about its own operation: what it watched, or what it was told. */
function knows(operation: DeployOperation): Watching | null {
  return watched(operation.job_id) ?? lateProgress(operation)
}

/**
 * Putting a card away, which is the one thing the reader decides about this page.
 *
 * It takes away the card the reader was watching and nothing else: the operation itself stays in
 * the list below with its own log, because the card up here was an extra way to watch something
 * happen and not the record that it happened.
 *
 * Kept by operation and not by position, and not written down anywhere. A card put away comes
 * back when the page is opened again, which is the escape hatch: a card that could be hidden with
 * no way to bring it back is a card somebody stops trusting the page to show them. Deciding when
 * it returns instead — a day, a week, until the deployment is pruned — is a rule about somebody's
 * memory, and the interface has no business making it.
 */
function hide(operation: DeployOperation): void {
  putAway.value = { ...putAway.value, [operation.job_id]: true }
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

    <!-- The reader's own: under way, and caught. The key carries the kind, because the same
         operation is deliberately in both this and the list below — a key of the job alone
         would make the second copy a duplicate of the first and Vue would drop one of them. -->
    <section
      v-for="operation in live"
      :key="`live-${operation.job_id}`"
      class="card operation"
      :class="edgeOf(operation)"
    >
      <div class="card-body">
        <div class="deploy-block-head">
          <span class="badge" :class="tone[operation.status] ?? 'badge-neutral'">
            {{ stateOf(operation) }}
          </span>
          <span v-if="operation.name" class="mono small">{{ operation.name }}</span>
          <span v-if="operation.place" class="place-chip mono">{{ operation.place }}</span>

          <span class="deploy-spacer" />

          <span v-if="when(operation)" class="muted small">{{ when(operation) }}</span>
          <span v-if="took(operation)" class="muted small mono">took {{ took(operation) }}</span>

          <button
            class="put-away"
            type="button"
            title="Put this card away. It comes back when you open the page again."
            :aria-label="`put away the deployment from ${when(operation) || 'now'}`"
            @click="hide(operation)"
          >
            ×
          </button>
        </div>

        <p v-if="verdictOf(operation)" class="why" :class="{ bad: operation.status === 'failed' }">
          {{ verdictOf(operation) }}
        </p>

        <DeploySteps
          v-if="isCaught(operation) || knows(operation)"
          :progress="knows(operation)?.progress ?? null"
          :seen="knows(operation)?.seen ?? []"
          :live="operation.running"
          :status="operation.status"
          :plan="planOf(operation.job_id)"
          :active-phases="openPhases(operation)"
        />

        <DeployLog
          v-if="watched(operation.job_id)"
          :lines="logLines(operation.job_id)"
          :plan="planOf(operation.job_id)"
        />
        <div v-else class="operation-log-wrap">
          <ul v-if="logOf(operation.job_id)?.length" class="operation-log">
            <li v-for="(entry, index) in logOf(operation.job_id)" :key="index" :class="entry.stream">
              <span v-if="entry.at" class="mono small muted">{{ clockOf(entry.at) }}</span>
              <span>{{ entry.text }}</span>
            </li>
          </ul>
          <p v-else class="muted small">{{ logLabel(operation.job_id) }}</p>
        </div>
      </div>
    </section>

    <!-- The record. One row per operation, as it has always been: badge, when, and the account
         folded away. No cross — the reader did not watch these happen, and a card offering to be
         put away invites the reader to treat it as theirs. -->
    <section
      v-for="operation in history"
      :key="`in-${operation.job_id}`"
      class="card operation"
      :class="edgeOf(operation)"
    >
      <div class="card-body">
        <div class="deploy-block-head">
          <span class="badge" :class="tone[operation.status] ?? 'badge-neutral'">
            {{ stateOf(operation) }}
          </span>
          <span v-if="operation.name" class="mono small">{{ operation.name }}</span>
          <span v-if="operation.place" class="place-chip mono">{{ operation.place }}</span>

          <span class="deploy-spacer" />

          <span v-if="when(operation)" class="muted small">{{ when(operation) }}</span>
          <span v-if="took(operation)" class="muted small mono">took {{ took(operation) }}</span>
        </div>

        <p v-if="verdictOf(operation)" class="why" :class="{ bad: operation.status === 'failed' }">
          {{ verdictOf(operation) }}
        </p>

        <details class="log" @toggle="askLog(operation.job_id)">
          <summary class="muted small">{{ logLabel(operation.job_id) }}</summary>
          <ul v-if="logOf(operation.job_id)?.length" class="operation-log">
            <li v-for="(entry, index) in logOf(operation.job_id)" :key="index" :class="entry.stream">
              <span v-if="entry.at" class="mono small muted">{{ clockOf(entry.at) }}</span>
              <span>{{ entry.text }}</span>
            </li>
          </ul>
        </details>
      </div>
    </section>
  </div>
</template>

