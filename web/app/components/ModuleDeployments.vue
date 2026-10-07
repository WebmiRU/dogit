<script setup lang="ts">
import { onRewake } from '~/lib/eventSocket'
import { bareDigest, shortImage as formatShortImage } from '~/utils/format'
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
  /**
   * The one place this block is about, as cluster and namespace.
   *
   * Everything below belongs to a place and not to the project: which pods came up,
   * what the module said, what has been deployed there and which images have ever been
   * there are three different questions with three different answers for a project that
   * deploys to two clusters. Drawn for the project as a whole they are all merged into
   * one, and a rollout on one of them is shown next to a version of the other that has
   * been running for a month — which reads as one deployment that has been going for a
   * month.
   *
   * Absent means every place, which is right for a page that has been asked about the
   * project and has not been asked about anywhere in particular.
   */
  place?: { cluster: string; namespace: string }
  /**
   * Whether the run under way has a deployment for this place that has not started.
   *
   * Not asked here: the row above already knows, and it knows it from the run rather
   * than from this card's own history — which is the whole point of it being a separate
   * fact. A card told nothing says what it last did; a card told this says that the
   * thing it last did was a different commit, and that this one has not got here yet.
   */
  queued?: boolean
  /**
   * What the module says is running in this place, right now.
   *
   * From the module's own answer and not from this card's history: the card knows what
   * it deployed, and the module knows what is there. Asked once for the whole page and
   * handed down, because the answer is per place and the page already has it.
   */
  nowRunning?: { image: string; tags?: string[]; pods: number; desired: number } | null
  /**
   * The image this place is being taken off, while a rollout takes one off.
   *
   * The other half of a rolling update: pods are on both images at once, and the one
   * they are leaving is still serving traffic. Marking only the one being put on says
   * half of what the cluster is doing, and the half that is missing is the one anybody
   * watching a slow drain needs.
   */
  replacing?: string
  /** How many pods are still on the image being taken off. */
  replacingPods?: number
  /**
   * Whether to show what the repository says about where this goes.
   *
   * Once per page rather than once per place: the file names the places and does not
   * change between them, so three copies of it under three rows is the same fact three
   * times, and a reader has to work out which copy belongs to the row above it.
   */
  showRepository?: boolean
}>()

/** Said when this place's settings were written, so a list of places can redraw. */
const emit = defineEmits<{
  settingsChanged: []
  /**
   * What this card thinks of itself, for the row above to draw as its dot.
   *
   * Said rather than asked for: the row and the card answer the same question — is this
   * place working — and read apart they come to disagree the moment one of them is a
   * moment older than the other. A row with a grey dot beside a green frame leaves the
   * reader deciding which of the two to believe, and both were drawn from facts about
   * the same deployment.
   */
  state: [said: { tone: string; word: string }]
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

/**
 * How many rows a page holds, for both lists.
 *
 * Ten. Twenty fitted on a screen and meant that the interesting row was often on the
 * next page, and a list whose second page nobody looks at is a list whose tenth row does
 * not exist — which is exactly the row a rollback is chosen from. Paging is already here;
 * ten rows a page makes the first page worth reading.
 */
const PAGE = 10
const IMAGE_PAGE = 10

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
const tab = ref<'now' | 'operations' | 'images' | 'settings'>('now')

/** The image to be shown, when somebody arrived here by clicking one. */
const wanted = ref('')
const imagePage = ref(1)

/** The operation under way, as the module describes it step by step. */
const active = ref<DeployProgress | null>(null)

/**
 * When the operation under way began, as a clock reading.
 *
 * Taken from the first thing said about it rather than from any timestamp the events
 * carry, because there is no other clock here to read it from: the module narrates, the
 * core relays, and neither stamps the moment. Zero means nothing is under way.
 */
const runningSince = ref(0)

/**
 * The phases happening right now, which is not one.
 *
 * A rollout brings the new pods up and sends the old ones away at the same time, and a
 * single arrow cannot say that: it has to be on one step or the other, so one of them is
 * drawn as finished work that is still going. Each phase is here from the moment it is
 * first mentioned until the module says it is finished with it.
 */
/**
 * The operation under way is over, however this page found out.
 *
 * One place, because it used to be three copies of the same four assignments and they
 * drifted: one of them missed the phases, one missed the record, and the page could be
 * left half-closed — no arrow, no clock, and a card still claiming to be deploying.
 * Anything that ends an operation calls this and nothing else.
 */
function finishOperation() {
  active.value = null
  activeSeen.value = []
  activePhases.value = []
  runningSince.value = 0
  running.value = null
  operationJobs.value = []
  startedHere.value = false
  refusalNoted = false
  // The rows are read once the operation is over and they have settled — see the note at
  // the read. While it was running they were left alone on purpose, and nothing else in
  // this page fetches them, so without this they would stay as they were until somebody
  // changed tabs.
  void loadImages()
}

const activePhases = ref<string[]>([])

/**
 * Whether an operation is under way on this place, and so whether this page may begin
 * another one.
 *
 * One answer read from every sign of it rather than three fields consulted in a template,
 * because the three disagree: `busy` covers the request, `running` the record the module
 * wrote, `active` the lines it is saying. A button that asked only the first was live
 * again a fraction of a second after the click, while the place was still busy — and the
 * click it accepted then had to be refused, having already been answered "yes, putting
 * it back".
 */
const underWay = computed(() => busy.value || startedHere.value ||
  running.value !== null || active.value !== null)

/**
 * The jobs this card has been spoken to by, which is how it tells one run from another.
 *
 * Two runs are under way at once more often than not: a push builds while a tag is being
 * rolled out, and every one of them says when it is over. A card that took every ending as
 * its own closed its log in the middle of a rollout because a build somewhere else had
 * finished — the log went, the clock restarted, and the steps before the one under way
 * went with them. So the ending is matched against the jobs that spoke, and an ending
 * about a job this card has never heard of is somebody else's news.
 */
const operationJobs = ref<number[]>([])

/**
 * Whether anything is under way right now, which is not the same as whether a
 * deployment finished.
 *
 * The place rows on the finished card are built from the last deployment that
 * completed, so a badge reading that deployment's outcome sits beside a card about the
 * one running — unless it is asked what is happening now.
 */
const deployBusy = computed(() => verdict.value.tone === 'working')

/** The steps the core said this deployment goes through, in order. */
const plan = ref<{ key: string; label: string }[]>([])
/** Whether that list is a rollback's, remembered because a run can change it under a page. */
const planIsRevert = ref(false)
/** What the operation on the page is, once it has said. Empty until one has started. */
const planKind = ref('')
/** Whether a refusal has already been said, so the same failure is not announced twice. */
let refusalNoted = false
/** That this page started an operation, which has not been heard to finish yet. */
const startedHere = ref(false)
const activeSeen = ref<DeployProgress[]>([])

/**
 * Read the step list for the kind of operation this page is showing.
 *
 * Its own function rather than a line in the fetch, because a run can start while the page
 * is already open — and then the list on it belongs to whatever this place did before this
 * run rather than to this one. Nothing about that looks wrong on the page: the steps are
 * real steps, drawn under a log that is going along perfectly well, and the only sign is
 * that the list is not the one the log is telling.
 */
async function loadPlan(wasRevert: boolean) {
  const stepList = await api.get<{ steps?: { key: string; label: string }[] }>(
    `/projects/${props.projectId}/deploy-plan${wasRevert ? '?kind=revert' : ''}`,
  )
  if (!Array.isArray(stepList.steps) || stepList.steps.length === 0) return
  plan.value = stepList.steps
  planIsRevert.value = wasRevert
}

/**
 * The place being asked about, if one was named.
 *
 * The part of the kind after the colon — "deploy:kubernetes" is the module, and
 * "kubernetes" is the name the core files it under. Not the cluster: a cluster is called
 * something else entirely, and asking for a cluster named "kubernetes" is asking about a
 * place that does not exist and getting an empty history in reply.
 *
 * It used to be empty, which asked the core for a module of kind "deploy:" — no such
 * thing is installed, so the whole card answered with a failure rather than with nothing.
 */
const target = computed(() => props.module.kind.split(':')[1] ?? '')

/** The cluster this block is about, if it was told one. */
const scopedCluster = computed(() => props.place?.cluster ?? '')

/** The namespace this block is about, if it was told one. */
const scopedNamespace = computed(() => props.place?.namespace ?? '')

/**
 * Whether a line said while watching belongs on this card.
 *
 * Every line a module says while deploying carries the record of the deployment it is
 * about, and a card about one place is about that place's record and nothing else. This
 * is the same question the rows are asked, in the same terms: a project with two places
 * in it has two rollouts under way at once, and one card showing both says the first
 * place is being deployed to while it is waiting for its turn — the same steps twice on
 * each card, in an order that belongs to neither.
 *
 * A line that names no place is no card's line but the card that was told no place at
 * all. Those are the lines of the run rather than of a deployment: the image being
 * built, the image being pushed, a runner saying what a job is up to. They belong to
 * the project — one build serves every place, and a place that has not been given
 * anything cannot have been the thing being built. Drawing them on a place's card says
 * that place is being deployed while it is still waiting its turn, and then the card
 * flips back to the last thing it did, which is a green run of an older commit, and the
 * reader is left watching a place that says it succeeded while the run is still going.
 */
function mineToSay(record: unknown): boolean {
  if (!scopedCluster.value) return true
  if (!record || typeof record !== 'object') return false
  const said = record as { cluster?: string; namespace?: string }
  if ((said.cluster ?? '') !== scopedCluster.value) return false
  // A card that was told no namespace makes no claim about one, which is what the
  // history asks the same rows about — otherwise a place that never named a namespace
  // would show nothing at all, having been told by the module which one it used.
  return !scopedNamespace.value || (said.namespace ?? '') === scopedNamespace.value
}

/**
 * Where an image is, in as few words as the catalogue says it.
 *
 * Not a `Deployment`, because it is not one: the catalogue's row is which image went
 * where and when, with no phase it died on and no reason, and a type that promises
 * those would either be lying or make the catalogue answer questions it was never
 * asked. Putting an image back needs the address and the digest, and this is the
 * address and the digest.
 */
interface PlacedImage {
  id: string
  image: string
  cluster: string
  namespace: string
  workload: string
  state: string
  started_at: string
}

/** An image, with what is known about it. */
interface KnownImage {
  /**
   * The image as it is addressed: repository and digest together.
   *
   * Whole, because the digest on its own names nothing anybody can pull — a registry
   * and a repository are what turn `sha256:836f…` into something a person can copy into
   * a `kubectl` command or compare with what a cluster is running.
   */
  name: string
  /** Just the digest, kept apart because that is what identifies one across renames. */
  digest: string
  firstSeen: string
  /** How many operations ran this image, and how many of them worked. */
  times: number
  succeeded: number
  /** The names this image was published under, as they were when it was deployed. */
  tags?: string[]
  /** The operation that put it into the cluster, if it did. */
  live: PlacedImage | null
}

/**
 * An image in one line, as `reg/app[bc9b83f2da0e]`.
 *
 * Shared, because the card, the place's row and the question in a confirmation all have
 * to print the same thing: three forms of one address on one page is a reader comparing
 * them instead of reading them.
 */
const shortImage = formatShortImage

/**
 * The repository half of an image address, without the digest.
 *
 * "registry.f220.ru/test/versions@sha256:90481ae2…" and "registry.f220.ru/test/versions"
 * are the same image, and only the second is something a person can paste into a pull. The
 * digest is not part of the name, so it does not belong inside it; it has a column.
 */
function imageRepository(image: string): string {
  const at = (image ?? '').indexOf('@')
  return at < 0 ? image : image.slice(0, at)
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

/**
 * How long ago something happened, in the few words people actually use.
 *
 * Not a formatted date: "3 minutes ago" is read without effort and "2026-10-05
 * 18:53:11" is worked out. Precise times are still one click away — the operations
 * list below has them in a column — so this is the summary, not the record.
 */
function ago(iso?: string, now?: number): string {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''

  const seconds = Math.max(0, Math.floor(((now ?? Date.now()) - then) / 1000))
  // Seconds, straight away, with no "just now" for the first of them.
  //
  // "Just now" is a claim about a moment that stays on screen after the moment has
  // passed: a card that says it for a minute reads as a run that is still going. A
  // number that keeps counting is never stale and never has to be taken on trust.
  if (seconds < 60) return `${seconds} second${seconds === 1 ? '' : 's'} ago`

  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`

  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`

  const days = Math.round(hours / 24)
  if (days < 30) return `${days} day${days === 1 ? '' : 's'} ago`

  const months = Math.round(days / 30)
  if (months < 12) return `${months} month${months === 1 ? '' : 's'} ago`

  const years = Math.round(months / 12)
  return `${years} year${years === 1 ? '' : 's'} ago`
}

/**
 * How long the last operation took, as a clock.
 *
 * A clock rather than a phrase — "2 min" beside a duration that is already counting
 * seconds invites reading the two as different things, and "took 2 min" beside a
 * figure ticking to 2:34 looks like one of them is stale. Same notation on both ends:
 * if one is a clock, the other should be too.
 */
function span(one: Deployment): string {
  if (!one.started_at || !one.finished_at) return ''
  const ms = new Date(one.finished_at).getTime() - new Date(one.started_at).getTime()
  if (Number.isNaN(ms) || ms < 0) return ''

  const total = Math.max(0, Math.round(ms / 1000))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const rest = total % 60

  // Under an hour it is m:ss, and a leading zero would only make it harder to read;
  // from an hour on it is h:mm:ss, padded so the clock does not change width as it
  // counts.
  if (hours === 0) return `${minutes}:${String(rest).padStart(2, '0')}`
  return `${hours}:${String(minutes).padStart(2, '0')}:${String(rest).padStart(2, '0')}`
}

/**
 * When the last operation ran, as one line.
 *
 * Both ends and the length between them, because any one alone leaves a question
 * open: the start without the end reads as something still going, and the end
 * without the start says nothing about how long the cluster was in this state.
 */
/**
 * A minute that passes, so "4 minutes ago" does not go on claiming to be four.
 *
 * Nothing is fetched: the list is already here and the time is arithmetic. The
 * alternative is a line that is correct when it is drawn and quietly wrong afterwards,
 * which is worse than one that is never drawn — a person reads a stale "just now" as
 * "this just happened", and that is exactly the mistake this line exists to prevent.
 *
 * A minute rather than a second, because a second is a redraw nobody asked for and the
 * coarsest reading it produces is "just now"; nothing changes for a reader in under a
 * minute anyway.
 */
const tick = ref(Date.now())
let stopTick: ReturnType<typeof setInterval> | undefined

/**
 * How long the operation under way has been going, as a clock.
 *
 * Hours, minutes and seconds rather than a rounded figure, because the whole question
 * this answers is "how long has it been stuck on this step" — and a rounded number is
 * exactly the thing that hides a step which has been sitting there for forty seconds.
 */
const runningFor = computed(() => {
  const began = runningSince.value
  if (!began) return ''

  const seconds = Math.max(0, Math.floor((tick.value - began) / 1000))
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60

  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(rest).padStart(2, '0')}`
  }
  return `${minutes}:${String(rest).padStart(2, '0')}`
})

const whenRun = computed(() => {
  // Read so that the timer below is a real dependency rather than a hint.
  void tick.value

  const last = deployments.value[0]
  if (!last?.started_at) return ''

  const started = ago(last.started_at, tick.value)
  if (!started) return ''

  // No end yet means it is still going, and saying so is the honest reading of a
  // missing time rather than leaving the reader to notice the gap.
  if (!last.finished_at) return `started ${started}, still going`

  const took = span(last)
  return `ran ${started}, took ${took}`
})

/**
 * The two ends of the last operation, in full.
 *
 * Both, on the card, because "how long ago" and "when exactly" are different questions
 * and the card is where they are both asked. Somebody checking a deployment against a
 * log or a release wants the clock, not "a while ago" — and the clock does not go stale
 * while it is being looked at, which "4 minutes ago" would.
 */
const whenRunExact = computed(() => {
  const last = deployments.value[0]
  if (!last?.started_at) return ''

  const from = new Date(last.started_at).toLocaleString()
  if (!last.finished_at) return `from ${from}, still going`

  const to = new Date(last.finished_at).toLocaleString()
  const took = span(last)
  // The end time alone rather than both full timestamps: they are seconds apart by
  // nature, and repeating the date for each says the same thing twice.
  const toTime = to.slice(to.indexOf(',') + 1).trim()
  return took ? `${from} → ${toTime} · took ${took}` : `${from} → ${toTime}`
})

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
const cardState = computed(() => verdict.value.tone)

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

/**
 * What is in each place, as one row.
 *
 * While a deployment runs, its own record — the one the module wrote down when it
 * started, which carries the tags it went out under and the workload it touches. At
 * rest, the newest deployment that finished well, per place.
 *
 * Same shape either way on purpose: the card at rest is the reference, and a card
 * that changes its rows as well as its badge is a different card. Tags and image are
 * not decoration on this line; they are the answer to "which version is out there",
 * and they used to go missing for the entire duration of the rollout.
 */
const running = ref<DeployProgress['deployment'] | null>(null)

/**
 * The image this card last saw going out to this place, kept after the operation ends.
 *
 * Kept because that is what the row's dot and its badges have to agree with once
 * nothing is moving: a page that watched a rollout knows which image it left behind,
 * and asking the module again — a request per deployment, for something it just said —
 * is both a round trip the answer has already made and a page whose resting state is a
 * second behind its own last event.
 */
const lastOut = ref<{ image: string; tags?: string[] } | null>(null)

/** The row the module has opened and not yet closed, if there is one. */
const newestRunning = computed(() =>
  deployments.value.find((one) => one.state === 'running') ?? null)

/** The names a record carries, each of them once. */
function oneEach(names?: string[]): string[] {
  return [...new Set((names ?? []).filter(Boolean))]
}

/**
 * A place, named the way the finished card names it: cluster and namespace.
 *
 * The record carries a name of its own as well — "dev", from the repository — but the
 * same place was on this card as "dev" during a deployment and as
 * "local-k3s/dogit-dev" a minute later, and a line that renames itself between two
 * states of one thing reads as two things happening.
 */
function placeOf(one: { place?: string; cluster?: string; namespace?: string }): string {
  return [one.cluster, one.namespace].filter(Boolean).join('/')
    || (one.place ?? '').trim()
    || '—'
}

const rows = computed(() => {
  // `.value`, and this is not a detail: a computed is an object, and an object is
  // always true. Written without it, the branch below was taken in every state,
  // which left the card at rest showing the place and the workload it borrows and
  // nothing else — no tag, no image, on a line whose whole job is to name what is
  // deployed. The template unwraps refs on its own, which is why the same name read
  // correctly there and wrongly here.
  if (deployBusy.value) {
    // Nothing to name yet is nothing to show.
    //
    // While an operation is under way these cells are about that operation, and until
    // it has said what it is pushing there is no honest answer for the name and the
    // image. Falling back to the last finished deployment put yesterday's tag next to
    // today's rollout, and the tag then changed on its own several seconds in — a name
    // swapping itself on a line that is supposed to be about one deployment, with no
    // event to explain it. A gap that fills in when the answer arrives beats a wrong
    // answer that corrects itself.
    //
    // The place and the workload are kept from the last deployment: they are where
    // this repository sends things, which does not change between runs, and blanking
    // them out for the first ten seconds of every deployment would say less about a
    // page that is working than leaving them there does.
    const record = running.value ?? newestRunning.value
    const last = places.value[0]
    return [{
      place: record ? placeOf(record) : (last?.place ?? ''),
      tags: oneEach(record?.tags),
      image: record?.image ?? '',
      workload: record?.workload ?? last?.deployment.workload ?? '',
    }]
  }

  return places.value.map((one) => ({
    place: one.place,
    tags: oneEach(one.deployment.tags),
    image: one.deployment.image,
    workload: one.deployment.workload ?? '',
  }))
})

/**
 * What this card says about itself: one word, one colour, one answer to "is it still
 * going". The badge, the edge and the timer are all read from here.
 *
 * Three facts used to be consulted separately — whether an event has arrived, whether
 * the run is finished, and what the history says — and they came to disagree: a card
 * with a green edge and a pill saying "deploying" on it, because the edge was drawn
 * from the last deployment in the history and the pill from the last thing said, and
 * the history had already caught up while the pill had not. There is no way to be half
 * wrong when there is only one answer to be wrong about.
 */
const verdict = computed<{ tone: 'working' | 'ok' | 'bad' | 'waiting'; word: string; cls: string }>(() => {
  // Two words, because two different things are happening and "deploying" says
  // neither of them right. An image being built and pushed is not a deployment: no pod
  // is being touched, and a card saying "deploying" over a forty-second image build
  // tells somebody the cluster is being changed while it is not. From the first
  // deployment phase on, it is one.
  if (active.value || runningSince.value) {
    // Which of the three words, decided by what is actually under way rather than by
    // the last thing said: a module closing a phase with a line of its own must not
    // turn the word back for a moment while the next one has not started yet.
    const deployPhase = activePhases.value.find((phase) => phase && !buildPhases.includes(phase))
    if (deployPhase) return { tone: 'working', word: 'deploying', cls: 'badge-warning' }

    const phase = active.value?.phase ?? ''
    const word = phase === 'push' || activePhases.value.includes('push')
      ? 'pushing'
      : 'building'
    return { tone: 'working', word, cls: 'badge-warning' }
  }
  if (activeSeen.value.some((one) => one.failed)) {
    return { tone: 'bad', word: 'failed', cls: 'badge-red' }
  }

  // Waiting its turn: the run under way has a deployment for this place and has not
  // started it. What the history holds is an older commit's work, and a green edge over
  // it — the edge the eye reads first, and the one the row above repeats in its dot —
  // says the run is finished with this place when it has not begun. No colour of its own
  // for that: the accent edge is the one this card already wears when it has nothing to
  // claim, and "nothing to claim yet" is exactly what it is saying.
  if (props.queued) return { tone: 'waiting', word: 'waiting its turn', cls: 'badge-neutral' }

  const last = deployments.value[0]
  if (!last) return { tone: 'ok', word: 'nothing yet', cls: 'badge-neutral' }
  // A row the module has started but not finished says the card is busy, not what to
  // call it: "running" is the module's word for its own bookkeeping and it appeared on
  // screen for a moment before the first event reached the page — a card that changes
  // its own name twice in two seconds, from a word meant for a database column to one
  // meant for a person.
  if (last.state === 'running') {
    return { tone: 'working', word: 'deploying', cls: 'badge-warning' }
  }
  if (last.state === 'abandoned') return { tone: 'bad', word: 'abandoned', cls: 'badge-red' }
  // The edge follows the run, not the badge: a red "failed" under a green edge is two
  // answers to one question, and the edge is what is read first — from across a page,
  // and from the row above, where the dot repeats this same colour.
  if (last.state === 'failed') return { tone: 'bad', word: 'failed', cls: 'badge-red' }
  return { tone: 'ok', word: last.state || 'unknown', cls: badgeClass(last.state) }
})

const stateBadge = computed(() => verdict.value.word)
const stateBadgeClass = computed(() => verdict.value.cls)

// To the row above, which draws it as a dot. Watched rather than sent at the moments
// this card learns something: a verdict that is only sent when it is read is a verdict
// the row cannot have.
watch(verdict, (said) => emit('state', { tone: said.tone, word: said.word }), { immediate: true })

/**
 * Whether this place is running this image right now.
 *
 * Told by the row above, which asked the module — the cluster is the thing that knows,
 * and the history is a record of what this module last did. They come apart the moment
 * a rollback is cut short or somebody changes an image by hand, and then the newest row
 * of the catalogue is not what is running: it is the one that was just taken away, and
 * marking that row "running now" answers "what is deployed" with the operation that was
 * most recently undone.
 *
 * Without the row's answer — a card drawn for a whole project rather than for a place —
 * the last operation that finished is the best anybody here can say, and it is marked as
 * what it is.
 */
const runningImage = computed(() => {
  // What this card watched going out, first: it is the freshest answer there is, and it
  // was correct at the moment it was given.
  if (lastOut.value?.image) return lastOut.value.image
  // Then what the place says now, for a page that was opened after the fact.
  if (props.nowRunning?.image) return props.nowRunning.image
  const settled = deployments.value.find((one) =>
    one.state === 'succeeded' || one.state === 'reverted' || one.state === 'rolled_back')
  return settled?.image ?? ''
})

/**
 * What the rollout under way says about the images, from the module's own lines.
 *
 * Read from the stream rather than asked for: during a rollout the answers are arriving
 * many times a minute anyway, and a page that asks the module separately is a request
 * behind the thing it is drawing — which is how "running 3/3" appeared only once the
 * rollout was over, on a card that had been showing a dash the whole time.
 */
const liveSplit = computed(() => {
  const image = running.value?.image
  if (!image) return null

  let ready = 0
  let desired = 0
  let previous = ''
  let retiring = 0
  for (const one of activeSeen.value) {
    if (one.ready || one.desired) {
      ready = one.ready || ready
      desired = one.desired || desired
    }
    if (one.previous) {
      previous = one.previous
      retiring = one.retiring ?? retiring
    }
  }
  if (!desired) return null
  return { image, ready, desired, previous, retiring }
})

/**
 * How many of this place's pods have stopped running the image it is being taken off.
 *
 * Counted forwards from what is left, against how many pods the place runs — the same
 * arithmetic the step in the log does, so the badge and the bar say the same thing at
 * the same moment. "Retiring now" on its own was true for the whole minute and could
 * not be watched.
 */
const retiredDone = computed(() => {
  const total = retiredTotal.value
  const left = liveSplit.value ? liveSplit.value.retiring : (props.replacingPods ?? 0)
  return total > 0 ? Math.max(0, total - left) : 0
})
const retiredTotal = computed(() => liveSplit.value?.desired || (props.nowRunning?.desired ?? 0))

/**
 * Whether this image is the one this place is on, right now.
 *
 * By image and not by record: two operations can have put the same image there, and the
 * one that did it last is not the one that is running now — after a rollback that is
 * exactly the difference, since the newest operation is the rollback and the image it
 * brought back is an older row.
 */
function isRunningImage(image: string | undefined): boolean {
  if (!image) return false
  if (liveSplit.value) return image === liveSplit.value.image
  return image === runningImage.value
}

/** How many pods are on this image right now, of how many this place runs. */
function runningPods(): { on: number; of: number } {
  if (liveSplit.value) return { on: liveSplit.value.ready, of: liveSplit.value.desired }
  if (lastOut.value) {
    // The rollout this card watched end: it said how many pods were up on it, and that
    // is a fact about a moment that has passed and does not need asking about again.
    const said = [...activeSeen.value].reverse().find((one) => one.ready || one.desired)
    if (said?.desired) return { on: said.ready ?? 0, of: said.desired }
  }
  return { on: props.nowRunning?.pods ?? 0, of: props.nowRunning?.desired ?? 0 }
}

/** The image this place is being taken off while a rollout takes one off. */
function retiringImage(): string {
  return liveSplit.value?.previous || (props.replacing ?? '')
}

/** The digest the run is applying, for the title attribute on the rows. */
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

/**
 * What the registry this place pulls from said about each image on this page.
 *
 * Keyed by the image name the catalogue has, because that is what a row carries and what
 * would have to be matched. Three states, and they are not two: `present`, `missing`, and
 * `unknown` — a registry that did not answer. Only `missing` is acted on, because a timeout
 * is not a deletion and refusing every rollback on an instance whose registry happens to be
 * slow would be a much worse failure than the one this is about.
 */
const availability = ref<Record<string, 'present' | 'missing' | 'unknown'>>({})

/** Whether the question about the page on screen is under way. */
const checking = ref(false)

/** The page the last question was about, so one page is never asked about twice. */
let askedAbout = ''

/**
 * Asks the registry about the page on screen, when the page on screen is this tab.
 *
 * Not at mount and not with the list: a page of places loads an image list for every place
 * it holds, and asking each of them about ten manifests before anybody has looked at the
 * tab is ten requests to somebody else's storage for an answer nobody has asked for. The
 * question waits for the tab.
 *
 * It still does not hold the list up. The page of images is on screen first, with its own
 * column of unknowns, and the answers land behind it — waiting for a registry before a list
 * can be read at all would make somebody else's outage this page's loading time.
 */
function ensureAvailability() {
  if (tab.value !== 'images' || !scopedCluster.value) return
  const names = images.value.map((one) => one.name)
  if (names.length === 0) return
  const about = names.join('\n')
  // Already asked about this page and the answers are here, or on their way: asking again
  // would be a second round of the same question.
  if (about === askedAbout && (checking.value || Object.keys(availability.value).length > 0)) {
    return
  }
  askedAbout = about
  void loadAvailability(names)
}

/** The question itself. The names are what is asked about — not a page number, because a
 *  page is a list of names and the names are the question. */
async function loadAvailability(names: string[]) {
  if (names.length === 0 || !scopedCluster.value) {
    availability.value = {}
    return
  }
  checking.value = true
  const askedAt = names.join('\n')
  try {
    const answer = await api.post<{
      images?: { image: string; state: 'present' | 'missing' | 'unknown' }[]
    }>(`/projects/${props.projectId}/deploy-images-availability?target=${encodeURIComponent(target.value)}`, {
      cluster: scopedCluster.value,
      namespace: scopedNamespace.value,
      images: names,
    })
    const states: Record<string, 'present' | 'missing' | 'unknown'> = {}
    for (const one of answer.images ?? []) states[one.image] = one.state
    // A page that changed while the question was in flight gets its answers dropped rather
    // than shown against rows that are not the ones that were asked about.
    if (askedAt === images.value.map((one) => one.name).join('\n')) availability.value = states
  } catch (caught) {
    console.warn('[dogit] the registry could not be asked about these images', caught)
    // Left empty: a page whose rows say nothing is a page that says nothing, which is what
    // a registry that could not be reached has honestly said.
    if (askedAt === images.value.map((one) => one.name).join('\n')) availability.value = {}
  } finally {
    checking.value = false
  }
}

// Asking happens when the tab is opened and when another page of it is read — not when the
// list arrives, because the list arrives for every place on the page and the tab is open for
// none of them.
watch(tab, (now) => {
  if (now === 'images') ensureAvailability()
})

/** What is known about one image, which is nothing until it has been answered about. */
function availabilityOf(image: string): 'present' | 'missing' | 'unknown' | '' {
  return availability.value[image] ?? ''
}

/**
 * Whether a version can be put back, and why not when it cannot.
 *
 * Only a registry that has said the image is not there. Every other state — including a
 * registry that said nothing — leaves the button as it was, because the module refuses the
 * rollback anyway if the image turns out to be gone, and a button greyed out on a network
 * hiccup teaches people that the button is broken.
 */
function revertBlocked(image: string): string {
  return availabilityOf(image) === 'missing'
    ? 'the registry this place pulls from has answered that it does not have this image'
    : ''
}

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
      target: target.value,
      page: String(imagePage.value),
      per_page: String(IMAGE_PAGE),
    })
    // Scoped like the history above: the catalogue of an image is the catalogue of
    // what has been in *this* place. Left unscoped it is every image the project has
    // ever put anywhere, and a rollback chosen from it would happily name an image
    // that has never been on this cluster.
    if (scopedCluster.value) query.set('cluster', scopedCluster.value)
    if (scopedNamespace.value) query.set('namespace', scopedNamespace.value)
    const answer = await api.get<{
      images?: {
        image: string
        first_seen: string
        times: number
        succeeded: number
        /** Every name this image was ever deployed under, whichever place. */
        tags?: string[]
        /**
         * The last deployment of this image that worked, and where it put it.
         *
         * It names its image because this is the row somebody clicks "put this back"
         * on, and the question that button asks is about an image: answered with a
         * workload and a namespace and no image, the page has nothing to put back and
         * fails on the way to finding out.
         */
        deployed?: {
          image: string
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
        name: one.image,
        digest: at >= 0 ? one.image.slice(at + 1) : one.image,
        tags: Array.isArray(one.tags) ? one.tags : [],
        firstSeen: one.first_seen,
        times: one.times,
        succeeded: one.succeeded,
        live: one.deployed
          ? {
              id: one.deployed.id,
              image: one.deployed.image,
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
    availability.value = {}
    return
  }
  ensureAvailability()
}

/** Goes to a page of the catalogue by asking for it. */
function goToImagePage(to: number) {
  if (to < 1 || to > imagePages.value) return
  imagePage.value = to
  // Another page, another question — and the marks of the page just left are about images
  // that are no longer on screen, so they go rather than waiting to be overwritten.
  availability.value = {}
  checking.value = false
  askedAbout = ''
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
    // Two different things, and they used to be sent as one: which module to ask, and
    // which place of that module to ask about. Sending the module's name as the cluster
    // asked about a place called "kubernetes", which exists nowhere, and left the core
    // with no module to ask at all.
    const query = new URLSearchParams({
      target: target.value,
      page: String(page.value),
      per_page: String(PAGE),
    })
    // The namespace as well as the cluster: one row is one place, and a place is both.
    // Scoped by the cluster alone, a project that deploys to two namespaces of one
    // cluster is shown the other one's rollouts as if they were this row's own.
    if (scopedCluster.value) query.set('cluster', scopedCluster.value)
    if (scopedNamespace.value) query.set('namespace', scopedNamespace.value)
    const answer = await api.get<DeploymentsPage>(
      `/projects/${props.projectId}/deployments?${query}`,
    )
    total.value = answer.total ?? 0
    pages.value = answer.pages ?? 1
    hasMore.value = answer.has_more ?? false
    // The newest row carries what it said, so the log is there before anything is
    // watched rather than only for a run somebody stayed to see.
    lastLog.value = answer.deployments?.[0]?.log ?? []

    // The step list, asked for rather than waited for.
    //
    // It arrives as an event when a run starts, which leaves a page opened between two
    // runs with no list at all — or, worse, with the last run's replayed at it. The
    // list belongs to the repository, so it is read from the repository.
    //
    // Whose list, though, depends on what this place last did. A page opened over a
    // place whose last operation was a rollback has to be given the rollback's three
    // steps: the deployment's seven, drawn beside a log about one image going back on
    // a workload, is a page claiming a build and a push while somebody watches pods.
    const wasRevert = answer.deployments?.[0]?.state === 'reverted'
    // From the operation on the page when it has named itself, and only then from the
    // newest on record. The list is the right answer about the past and the wrong answer
    // about the present: an operation being written is not in it yet, so reading the plan
    // from there during a rollback gives the rollback's steps away to the run before it.
    await loadPlan(planKind.value ? planKind.value === 'revert' : wasRevert)

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

    // The operation under way is over, said by the history rather than by an event.
    //
    // Events are how this page hears about things as they happen, and they are the
    // right way to hear about a thing. But a card that is only ever closed by one
    // particular event is a card that can get stuck: the event does not arrive, the
    // reload does, and the page is left claiming to be deploying an operation that
    // finished a minute ago — with a green edge beside it, because the edge was drawn
    // from the history that did arrive. So the history closes the card too.
    //
    // Matched on the image, and only when the row is finished, because those are the
    // two facts that mean this particular operation is over. A row still being written
    // is the operation still running, and must not close it.
    if (running.value?.image) {
      const done = (answer.deployments ?? []).some(
        (one) => one.finished_at && one.image === running.value?.image,
      )
      if (done) finishOperation()
    }
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

  const jobID = Number(payload.job_id ?? 0)
  if (jobID && !operationJobs.value.includes(jobID)) {
    operationJobs.value = [...operationJobs.value, jobID]
  }

  const said: DeployProgress = {
    phase: String(payload.phase ?? ''),
    message: payload.message,
    ready: Number(payload.ready ?? 0),
    desired: Number(payload.desired ?? 0),
    step: Number(payload.step ?? 0),
    of: Number(payload.of ?? 0),
    // What the rollout said about the image it is taking off, kept as it said it: the
    // figures on this page during a rollout come from the module's own lines, and a
    // figure read out of a sentence is a figure somebody has to keep correct.
    previous: typeof payload.previous === 'string' && payload.previous ? payload.previous : undefined,
    retiring: typeof payload.retiring === 'number' ? payload.retiring : undefined,
    finished: payload.finished === true,
    failed: payload.failed === true,
  }

  // The record, once the module has written it down. It is what the rows are built
  // from while the run is under way, so that the tags and the image are on screen
  // from the first line rather than after the last one.
  if (payload.deployment && typeof payload.deployment === 'object') {
    running.value = payload.deployment as NonNullable<DeployProgress['deployment']>
    const record = running.value
    if (record?.image) lastOut.value = { image: record.image, tags: record.tags }

    // The operation's own kind, named by the core, decides whose steps these are. A run
    // that has just begun makes the list on the page belong to whatever this place did
    // before it, and nothing about that looks wrong: the steps are real steps, drawn under
    // a log that is going along perfectly well. Read again here rather than at the next
    // reload, because the whole of this run is the part where it was wrong.
    //
    // A record that does not say is left alone. An older core sends no kind, and swapping
    // the list on a guess is the thing this was fixing.
    const kind = record?.kind
    if (kind === 'revert' && !planIsRevert.value) void loadPlan(true)
    else if (kind === 'deploy' && planIsRevert.value) void loadPlan(false)

    // What this operation is, kept. The list of operations behind it cannot answer while
    // it is being written: it is not in the list yet, so a page that re-reads the plan
    // from there in those seconds decides it from the operation before it, and the steps
    // swap wholesale in the middle of a rollback that has only just finished.
    if (kind) planKind.value = kind

    // A refusal said once. Warning and not error: nothing was broken and nothing is
    // red in the cluster — the place was busy, the operation simply did not happen, and
    // the next attempt may well work. Red would read as a fault to go and look at, which
    // is not what a busy place is. Yellow says it plainly: this is not done.
    if (said.failed && !refusalNoted) {
      refusalNoted = true
      notify(said.message, { type: 'warning', timer: 0 })
    }
  }

  // The phase joins the list of those under way, or leaves it. A phase that has said
  // its last word is not waiting for a second one that will never come, and an arrow
  // left on it would be claiming work that is over.
  if (said.phase) {
    // A phase the next one replaces is over, whether or not anybody said so.
    //
    // The module closes its phases itself — that is what its last line is — but the
    // runner announces a build and a push and never says "that was the end of it", so
    // a card that waited to be told kept both open and pointed at the build for the
    // whole rollout: an arrow on step one of seven while step six is what is happening.
    const others = activePhases.value.filter((one) => one !== said.phase &&
      !(buildPhases.includes(one) && !buildPhases.includes(said.phase)))
    activePhases.value = said.finished ? others : [...others, said.phase]
  }

  const last = activeSeen.value[activeSeen.value.length - 1]
  if (!last || last.phase !== said.phase || last.message !== said.message) {
    activeSeen.value = [...activeSeen.value, said]
  }
  active.value = said
  // The clock starts with the first thing said, not with the page: a page opened half
  // way through a rollout is watching the rest of it, and counting from its own load
  // would claim the rollout has been going longer than it has.
  if (!runningSince.value) runningSince.value = Date.now()
}

/**
 * Puts one version back.
 *
 * Named after what it does and not after "rollback": this puts back a particular image
 * — the row somebody clicked, by the digest that row recorded — rather than stepping
 * back through whatever the cluster still remembers. That history is bounded, prunable,
 * and gone entirely if the Deployment is recreated.
 */
async function revertTo(deployment: PlacedImage) {
  // The row came from the catalogue, which knows the image and names it in the
  // deployment it remembers — and if that ever stops being so, the button says which
  // image it cannot put back instead of failing on the first line of the question.
  const image = deployment.image ?? ''
  if (!image) {
    const message = 'This row does not say which image it is, so there is nothing to put back.'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
    return
  }

  // Asked here as well as on the button, because a button that is greyed out is not the only
  // way something gets here: an answer that arrived while this page sat open is the usual
  // way, and a refusal that arrives after the question was asked is a question somebody
  // watched fail for an answer they had.
  const blocked = revertBlocked(image)
  if (blocked) {
    const message = `This version cannot be put back: ${blocked}.`
    error.value = message
    notify(message, { type: 'error', timer: 0 })
    return
  }

  const where = `${deployment.cluster}/${deployment.namespace || 'its default namespace'}`
  const question =
    `Put ${shortImage(image)} back on ${where}?\n\n` +
    `What ${deployment.workload || 'the workload'} runs will be this image again.\n` +
    'A database migration, a ConfigMap and anything else applied alongside are left as ' +
    'they are — only the image goes back.'
  if (!globalThis.confirm(question)) return

  busy.value = true
  error.value = ''
  try {
    // Which module, in the query, as every other request to this module's endpoints does.
    // Left off, the core looks for a module of kind "deploy:" — the empty name — finds
    // nothing, and answers with an internal error about a server that is working: the
    // one thing wrong was that nobody said which module to ask.
    await api.post(
      `/projects/${props.projectId}/deployments/revert?target=${encodeURIComponent(target.value)}`,
      {
        cluster: deployment.cluster,
        namespace: deployment.namespace,
        workload: deployment.workload,
        deployment_id: deployment.id,
      },
    )
    // Said as a beginning and not as an outcome, because that is all this answer is: the
    // request was accepted, and nothing has been put back yet. Told as a success it is a
    // green toast on an operation that is about to be refused — the place is busy — and
    // the refusal arrives afterwards as an event that no longer has a toast to change.
    notify(`${shortImage(image)} is being put back`, { type: 'info' })
    // The place is taken from the moment this is accepted, and the clearing that used to
    // follow it is what let a second click through while the first was still running.
    // What ends it now is the operation's own ending, from the module's own last word.
    startedHere.value = true
    await load()
  } catch (caught) {
    startedHere.value = false
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
let stopRun: (() => void) | undefined

/**
 * The states a run cannot come back from.
 *
 * Spelled out rather than "not running", because a run is `pending`, `running` or one of
 * these, and treating anything else as unfinished would leave the card open for a state
 * nobody has heard of.
 */
const endedStatuses = ['success', 'failed', 'canceled', 'cancelled', 'abandoned', 'skipped']

/**
 * The phases that belong to building rather than to deploying.
 *
 * A run reports these whether or not it goes on to deploy anything, so they are the
 * ones that must not be taken as "a deployment is under way" — and they are how a run
 * that deploys nothing is told apart from one that has not started deploying yet.
 */
const buildPhases = ['build', 'push', 'pre', 'post']

onMounted(async () => {
  // Listening comes before fetching, and not for tidiness.
  //
  // A page that starts loading before it starts listening is deaf for as long as the
  // load takes: a deployment that begins meanwhile is missed, and a page showing a
  // finished operation from this morning because the live one never reached it is
  // worse than one that briefly shows nothing. Order is the whole of the difference.
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
      // Not this card's place, not this card's line.
      if (!mineToSay(payload.deployment)) return
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
  //
  // All of it in onEvent rather than in onChange, because the question "was that this
  // place?" can only be asked with the payload in hand — and onChange is told nothing
  // about what arrived.
  stopHistory = watchEvents({
    kinds: ['deploy.history'],
    project: () => props.projectPath,
    onEvent: (event) => {
      // About this place, and only this place.
      //
      // A project with two places has two deployments, and they do not end together: a
      // place that is switched off ends in a millisecond, long before the other has read
      // a manifest. Taking that ending as this card's own closed the card watching the
      // rollout that was still going, and took its log with it — the build's lines among
      // them — which is a log that comes apart under the reader halfway through.
      if (!mineToSay(event.payload?.deployment)) return

      // Finished: whatever was under way is not any more.
      finishOperation()
      void load()
      // The images with it, because a deployment that has just finished is what put
      // one here. Read at mount only, the list was whatever was in the place when the
      // page was opened, and the tab said so about a tag that had been in the cluster
      // for a minute — the newest image missing from the newest place, which is the one
      // anybody opens that tab to see.
      void loadImages()
    },
  })

  // The run ending, which is not the same thing as the deployment ending — and the
  // difference is a run that deploys nothing.
  //
  // A run on a branch no place matches still builds an image, and the build and push
  // report themselves as deployment progress. That is worth showing, because work
  // really is happening. But the run then finishes with no deployment in it, so no
  // deployment history is published, and the card sat saying "Deploying now" over a
  // phase that had ended minutes ago — the one claim on this page that can be wrong
  // with nothing left running to contradict it.
  stopRun = watchEvents({
    kinds: ['pipeline.updated'],
    project: () => props.projectPath,
    onEvent: (event) => {
      // A finished job is not a finished run.
      //
      // Both send this line, and the core is the only one that can tell them apart: a run
      // of three jobs says it is over when each of its jobs says so, so the build ending
      // is a line this handler used to read as the run ending — and a card watching a
      // place closed on the build's own ending and opened again a second later, over the
      // rollout that had not begun yet. So the question is asked rather than guessed at,
      // and a line about a job in a run with work left in it closes nothing.
      if (event.payload?.run_finished !== true) return

      // Not this card's run. Another run's ending is another run's news, and treating it
      // as this one closed the log over a rollout that was only beginning.
      if (!operationJobs.value.includes(Number(event.payload?.job_id ?? 0))) return

      const status = String(event.payload?.status ?? '')
      if (!endedStatuses.includes(status)) return

      // Only for a run that was never going to deploy anything.
      //
      // A run reports itself updated when each of its jobs finishes, not once at the
      // end — so the image job finishing says nothing about the deployment that comes
      // after it. Treating that as the end closed the card right after the push, and
      // the phases that were the whole reason for watching never appeared.
      //
      // Decided by what is under way rather than by the last thing said: a module that
      // closes a phase with a line carrying no phase of its own — "finished" — would
      // otherwise look exactly like a run that never deployed anything, and the card
      // would flicker back to the last operation and open again a moment later.
      if (activePhases.value.some((phase) => !buildPhases.includes(phase))) return

      finishOperation()
    },
  })

  await load()
  // Not while something is being done here. Every row in this list is a place to click,
  // and a list that reorders itself between two clicks of a person deciding quickly is a
  // list that puts back what they did not choose: the second click lands on whichever row
  // slid into the space, and the notice afterwards names that row's image, correctly and
  // uselessly. One read, when the operation is over and the rows have settled.
  if (!underWay.value) void loadImages()

  // The clock behind "4 minutes ago" and behind the running timer. Nothing is fetched —
  // the list is already here and both figures are arithmetic — but without it they are
  // correct when drawn and quietly wrong afterwards, which is the failure they exist to
  // prevent. A second, because a clock that counted in minutes would sit at "0:00" for
  // a minute looking like something that had stopped.
  stopTick = setInterval(() => {
    tick.value = Date.now()
  }, 1000)
})

onBeforeUnmount(() => {
  if (stopTick) clearInterval(stopTick)
  stopRewake?.()
  stopPlan?.()
  stopHistory?.()
  stopOperation?.()
  stopRun?.()
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
      <!-- What the repository says about where this goes.
           Folded away, because it is a fact about the repository rather than about this
           instance, it changes only when somebody edits a file, and it is read far less
           often than it takes up. Beside the card rather than above the whole page,
           because it is the same information the card's steps came from. -->
      <details v-if="props.showRepository !== false" class="places-fold">
        <summary class="muted small">
          Where this deploys — read from the default branch's .dogit-ci.yml
        </summary>
        <DeployPlaces :project-id="props.projectId" branch="the default branch" />
      </details>

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
        <!-- One place, so one place's settings. Shown only when this card was told which
             place it is about: a card drawn for a whole project has no one row to
             configure, and a tab that said "Settings" there would be a promise about
             something on the page that does not exist. -->
        <button
          v-if="scopedCluster"
          class="tab"
          :class="{ on: tab === 'settings' }"
          type="button"
          @click="tab = 'settings'"
        >
          Settings
        </button>
      </nav>
    </template>

    <!-- What is happening, or what last happened. Its own tab and the first one: this
         is the thing a page is opened for, and it answers one question, where the two
         lists below answer another. -->
    <div v-show="tab === 'now'">
    <!-- One card, and it is the same card in every state.
         The card at rest is the reference: the rows below say where, under which
         names, which image and which workload is there. A deployment under way is the
         same statement with different values in it, and it used to be a different set
         of rows that said less — no tags, no image, no workload — so the interesting
         half of a rollout was the half on screen with nothing on it. Three things
         change between states: the badge, the log, and which record the rows are
         built from. Nothing else. -->
    <section class="card active-card" :class="[cardState, { idle: !deployBusy }]">
      <div class="card-body">
        <!-- The head is the whole of it: what state, where, under which names, which
             image, which workload, and for how long. One line, one row, in every state
             — not a heading with a title and then the same facts again underneath. A
             heading on this card said nothing the badge does not, and it was the thing
             that had to be reworded whenever a state was added. -->
        <div class="block-head">
          <span class="badge" :class="stateBadgeClass">{{ stateBadge }}</span>
          <template v-for="row in rows" :key="row.place">
            <span class="place-chip mono">{{ row.place }}</span>
            <!-- The names it went out under, beside the digest it is addressed by.
                 The digest says which image; the tags say what it was called, and a
                 person comparing this against a release is looking for the name, not
                 for sixty-four characters of hexadecimal. -->
            <span v-if="row.tags.length" class="tags">
              <span v-for="tag in row.tags" :key="tag" class="tag mono">{{ tag }}</span>
            </span>
            <!-- Not the image we do not have. While a run is under way there is a
                 moment before the module has named its image, and there is no name
                 to put there yet — the digest appears the moment it is known. -->
            <span v-if="row.image" class="mono small">{{ shortImage(row.image) }}</span>
            <span v-else-if="deployBusy" class="muted small">choosing the image…</span>
            <span class="muted small">{{ row.workload || '—' }}</span>
          </template>
          <span class="spacer" />
          <!-- How long, and how long ago. The same two facts in every state: a clock
               that appears only while something is moving teaches people to look
               elsewhere the moment it stops. While it runs the seconds count. -->
          <span
            v-if="runningFor"
            class="muted small mono"
            title="how long this has been going"
          >{{ runningFor }}</span>
          <!-- After it, the ends are given here only where nothing above already says
               them. On a place's card the row that names the place carries "ran … , took
               …" and its dot, and saying it a second time below squeezes out the tags
               and the version the card is about. Drawn for a project on the module's own
               page there is no such row above, so the card says it itself. -->
          <span
            v-else-if="!scopedCluster && whenRun"
            class="muted small"
            :title="whenRunExact"
          >{{ whenRun }}</span>
        </div>

        <!-- Live while a run is under way, and still here afterwards: the list is
             something to read when nothing is happening, which is when there is time
             to read it. The arrow stands still when there is nothing to stand for. -->
        <DeploySteps
          v-if="active"
          :progress="active"
          :seen="activeSeen"
          :plan="plan"
          :active-phases="activePhases"
          live
        />
        <DeploySteps
          v-else-if="idleProgress && plan.length > 0"
          :progress="idleProgress"
          :plan="plan"
          :active-phases="[]"
        />
        <DeployLog :lines="shownLog" :plan="plan" />

        <details v-if="active" class="log">
          <summary class="muted small">Where the whole of this is written down</summary>
          <p class="muted small">
            Every line is in the deploy job's log on the pipeline page, and it stays
            there after the operation is over.
          </p>
        </details>
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
                <!-- "Tags", not "Tag": a commit can carry a dozen of them, and this is one of
                     the few places the count is the interesting part — a release
                     pushed under many names is a release that can be referred to in
                     any of them. -->
                <th>Tags</th>
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
                  <!-- Every tag, each on its own, rather than one comma-joined line: a dozen tags in
                       a sentence run is a wall of text, and the point of the column is
                       to be scanned. -->
                  <span v-if="one.tags && one.tags.length" class="tags">
                    <span
                      v-for="tag in one.tags"
                      :key="tag"
                      class="tag mono"
                    >{{ tag }}</span>
                  </span>
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
                    <!-- Whether this operation's image is the one on the place now, and
                         not whether it is the newest: after a rollback the newest is
                         the one that undid the last, and marking that as what is
                         running names the image that was just taken away. -->
                    <span v-if="isRunningImage(one.image)" class="badge badge-green">
                      running now
                    </span>
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

        <!-- This place's own settings: the same fields, in the same form, as the
             module's Settings page — because it is the same thing. One row only, the one
             this card is about. -->
        <section v-if="tab === 'settings' && scopedCluster" class="block">
          <ModuleSettingsForm
            :module="props.module"
            scope="project"
            :scope-id="props.projectId"
            :only-row="scopedCluster"
            :can-edit="props.canManage"
            @saved="emit('settingsChanged')"
            :note="`These are the settings ${scopedCluster} has for ${props.projectPath}. Anything left alone is inherited from the group or the instance.`"
          />
        </section>

        <section v-show="tab === 'images'" class="block">
          <div class="block-head">
            <h3 class="block-title">Images</h3>
            <span class="muted small">what can go into the cluster</span>
          </div>

          <p v-if="images.length === 0" class="muted small">No images yet.</p>

          <!-- A table, like the operations above, and for the same reason: each of
               these rows is four facts about one thing, and stacked as a paragraph they
               have to be read rather than scanned. In columns the same facts line up,
               and the one question this tab exists to answer — which image is live, and
               which can be put back — is a glance down a single column. -->
          <div v-else class="table-scroll">
            <table class="table images">
              <thead>
                <tr>
                  <th>Image</th>
                  <!-- Its own column, because a name and a digest are two different
                       facts and one of them is not a name. Squashed together they read as
                       one identifier written in an odd way, which is exactly how it
                       looked: "registry.f220.ru/test/versions[90481ae28906]" is not
                       something anybody can type. The repository is what a person pulls;
                       the digest is what makes it unambiguous. -->
                  <th>Digest</th>
                  <th>Tags</th>
                  <th>Operations</th>
                  <th>Where</th>
                  <!-- Its own column because it is a fact about the registry rather than
                       about this project: the same image is in the registry or it is not,
                       and it can change without anything here being deployed. -->
                  <th>In the registry</th>
                  <th class="actions-col">Put back</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="image in shownImages"
                  :key="image.digest"
                  :class="{ wanted: wanted && image.digest.endsWith(wanted) }"
                >
                  <!-- The repository on its own, which is the part a person can copy into a pull or a
                       kubectl. The digest beside it is in its own column, and together
                       they say the same thing "repo@digest" does, in the order a person
                       reads it. -->
                  <td class="mono small image-cell">{{ imageRepository(image.name) }}</td>
                  <!-- A chip like the tag beside it, because that is what it is: a label on
                       this image, read rather than copied. The brackets it used to carry
                       are shortDigest's, and those belong to a digest quoted inside a
                       sentence, where something has to tell the digest apart from the
                       words around it. In a cell of its own, under a column already headed
                       Digest, there are no words to be confused with. The full address is
                       in the title, since twelve characters are for reading and the rest
                       is for copying. -->
                  <td class="small">
                    <span v-if="bareDigest(image.name)" class="tag mono" :title="image.name">
                      {{ bareDigest(image.name) }}
                    </span>
                    <span v-else class="muted">—</span>
                  </td>
                  <!-- The names this image was published under. Shown as tags in their
                       own column rather than under the digest, because they are the
                       names a person knows it by and the digest is not one of them. -->
                  <td class="small tags-cell">
                    <span v-if="image.tags && image.tags.length" class="tags">
                      <span v-for="tag in image.tags" :key="tag" class="tag mono">{{ tag }}</span>
                    </span>
                    <span v-else class="muted">—</span>
                  </td>
                  <td class="small nowrap">
                    {{ image.times }}
                    <span class="muted">
                      ({{ image.succeeded }} ok)
                    </span>
                  </td>
                  <td class="small">
                    <span v-if="image.live" class="mono">
                      {{ image.live.cluster }}/{{ image.live.namespace }}
                    </span>
                    <span v-else class="muted">never deployed</span>
                  </td>
                  <!-- Asked of the registry this place pulls from, once per page, after
                       the page was drawn. "not asked" is drawn as a dash rather than as
                       "yes": a row that has not been answered about is not a row that has
                       been found. -->
                  <td class="small registry-cell">
                    <!-- Named for the state of the image rather than for the place it is
                         or is not in: "there" needs a "there" the reader has to supply,
                         and "gone" claims it once was — which is a claim the registry
                         never made. It said it does not have this manifest, and that is
                         all either word here is allowed to say. Blue is what a fact that
                         is all right wears. -->
                    <span
                      v-if="availabilityOf(image.name) === 'present'"
                      class="badge badge-blue"
                      title="the registry this place pulls from has this image"
                    >
                      Available
                    </span>
                    <span
                      v-else-if="availabilityOf(image.name) === 'missing'"
                      class="badge badge-warning"
                      title="the registry this place pulls from has answered that it does not have this image"
                    >
                      Unavailable
                    </span>
                    <span
                      v-else-if="availabilityOf(image.name) === 'unknown'"
                      class="badge badge-neutral"
                      title="the registry did not answer, so whether this image can be pulled from it is not known"
                    >
                      Unknown
                    </span>
                    <!-- Not an answer yet, so not a badge: the question is going out and
                         the row says what is happening to it. It turns into one of the
                         three above within a moment, and a badge that is about to change
                         is a badge that was never an answer. -->
                    <span
                      v-else-if="checking"
                      class="muted small"
                      title="asking the registry now"
                    >
                      Checking…
                    </span>
                    <span v-else class="badge badge-neutral">Unknown</span>
                  </td>
                  <td class="actions-col">
                    <!-- Not gated on the row having a deployment of its own: an image
                         that is being rolled out right now is the one this place is
                         being put on, and its catalogue row has nothing recorded yet —
                         no operations, no place, a dash in the last column. Gated on it,
                         the one row the whole rollout is about was the one row with no
                         numbers on it, and the figures appeared only once the
                         deployment was written down and the page had read the list
                         again. -->
                    <span
                      v-if="isRunningImage(image.name)"
                      class="badge badge-green"
                      :title="`${runningPods().on} of ${runningPods().of} pods are on this image right now`"
                    >
                      <!-- Without the fraction when there is none to give: a line that
                           carries no counts is the end of a rollout, and "0 of 0" is a
                           claim about a place with no pods rather than a place with
                           three. The words alone are then the whole of the answer. -->
                      <template v-if="runningPods().of > 0">
                        Running {{ runningPods().on }}/{{ runningPods().of }}
                      </template>
                      <template v-else>Running now</template>
                    </span>
                    <!-- Still up, and on its way out: neither "running here" nor
                         "gone", which is why it is a colour of its own rather than a
                         grey version of the first one. -->
                    <!-- The drain, counted forwards like the log counts it: "retire 1 of
                         3" is one old pod gone of three, which is the same figure the
                         step under it is filling towards — and a badge that only says
                         "retiring" cannot be watched, only noticed. -->
                    <span
                      v-else-if="image.live && retiringImage() && image.name === retiringImage()"
                      class="badge badge-warning"
                      :title="`${liveSplit?.retiring ?? props.replacingPods ?? 0} pod(s) are still on this image while the place is moved to another`"
                    >
                      Retire {{ retiredDone }}/{{ retiredTotal }}
                    </span>
                    <!-- Refused here, in advance, when the registry has said the image is
                         not there: the module refuses the same thing a moment later, and
                         refusing it before the question is asked saves a cluster from
                         being told to pull an image nobody can pull. Every other state
                         leaves the button alone — a registry that did not answer is not
                         a reason to stop anybody, it is a reason to know less. -->
                    <button
                      v-else-if="props.canManage && image.live"
                      class="btn btn-small"
                      type="button"
                      :disabled="underWay || !!revertBlocked(image.name)"
                      :title="revertBlocked(image.name)
                        || (underWay && !busy ? 'Another operation is under way on this place' : '')
                        || 'Put this image back on the workload'"
                      @click="revertTo(image.live)"
                    >
                      Revert to this
                    </button>
                    <span v-else-if="image.live" class="muted small">nothing to put back</span>
                    <span v-else class="muted">—</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

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
/* Yellow while it runs. Not green: green on this page means an operation ended well,
   and a rollout that is still going has not ended at all — a card wearing success for
   the two minutes everybody is waiting claims the answer before there is one. Red is
   the other end. */
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

/* Folded away by default: a fact about the repository, read rarely, and it sits in the
   middle of the page where everything that moves belongs to something else. */
.places-fold {
  margin-bottom: 10px;
}

.places-fold > summary {
  padding: 4px 0;
  cursor: pointer;
  user-select: none;
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

/* A digest is long and a row of tags is short, so the two columns are given what
 * they each need and no more: the digest wraps within its own cell rather than
 * pushing the tags off the right edge of the page. */
.images .image-cell {
  word-break: break-all;
  min-width: 220px;
}

.images .tags-cell {
  min-width: 120px;
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