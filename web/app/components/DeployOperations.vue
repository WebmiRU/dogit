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

/** One operation, as the core described it when it was asked. */
interface DeployOperation {
  /** The operation's identity. Everything hangs off it, including which card. */
  job_id: number
  status: string
  name: string
  /** Which place, by name. Empty only on a page that was not told about a place. */
  place: string
  error: string
  /** Unix milliseconds. Null when the operation never began, which the core does not list. */
  started_at: number | null
  finished_at: number | null
  running: boolean
}

interface OperationsAnswer {
  active?: DeployOperation[]
  finished?: DeployOperation[]
  /** Drawn rather than treated as an error: "no deploy module" is an ordinary answer. */
  reason?: string
}

/** What the page knows about one operation while it watches. */
interface Watching {
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

onMounted(load)
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

  watching.value = {
    ...watching.value,
    [jobID]: {
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

/** When a line was said, to the second — enough to place it, short enough not to shout. */
function clockOf(at: number): string {
  return new Date(at).toLocaleTimeString(undefined, { hour12: false })
}

/** When to show, as a sentence a person would say. */
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

const tone: Record<string, string> = {
  success: 'badge-green',
  failed: 'badge-danger',
  error: 'badge-danger',
  cancelled: 'badge-warning',
  skipped: 'badge-neutral',
}

watchEvents({
  // The core's own line about a deployment, relayed whole. Many a minute while a rollout runs, so
  // this is the only path that has to be cheap.
  onEvent: (event) => {
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

/** The steps this page draws, when the core has said what they are. */
const plan = ref<{ key: string; label: string }[] | null>(null)
watchEvents({
  onEvent: (event) => {
    if (event.kind !== 'deploy.plan') return
    const steps = event.payload?.steps
    if (Array.isArray(steps) && steps.length) {
      plan.value = steps as { key: string; label: string }[]
    }
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
      :class="{ live: operation.running }"
    >
      <div class="card-body">
        <div class="block-head">
          <span class="badge" :class="tone[operation.status] ?? 'badge-neutral'">
            {{ operation.running ? 'Running' : operation.status }}
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

        <!-- The steps come from the module's own lines about *this* operation, which is the only
             thing on the page that can tell them apart. -->
        <DeploySteps
          v-if="watched(operation.job_id)"
          :progress="watched(operation.job_id)!.progress"
          :seen="watched(operation.job_id)!.seen"
          :live="operation.running"
          :plan="plan ?? undefined"
          :active-phases="watched(operation.job_id)!.phases"
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

/* A card for something happening now is worth noticing before anything else on the page, so it is
   marked by a line rather than by a colour: colour is already carrying the outcome, and one thing
   saying "running" and another saying it again in red is two answers to one question. */
.operation.live {
  border-left: 3px solid var(--accent);
}

.operation.live .badge {
  font-weight: 600;
}
</style>
