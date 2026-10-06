<script setup lang="ts">
/**
 * This project's places, one row each, and under each row the card about that place.
 *
 * A row is a place and the card under it is that place and nothing else: which pods came
 * up, what the module said, what has been deployed there and which images have ever been
 * there are four questions with four answers for a project that deploys to two
 * clusters. Drawn for the project as a whole they are merged into one, and a rollout on
 * one of them is shown next to a version of the other that has been running for a month
 * — which reads as one deployment that has been going for a month.
 *
 * What a row may change is what is this project's own answer about that place, and two
 * things: whether a push may deploy there by itself, and whether this project may deploy
 * there at all. Everything else about a cluster — where it is, what it is called, which
 * namespace, how long to wait — belongs to whoever installed the module and is on its
 * Options tab. A kubeconfig is a credential and this page is not the place for it: the
 * page is open to everybody who may read the project, and a cluster's address is not
 * theirs to read.
 *
 * Each row saves itself, because a row is what somebody decided: two clusters are two
 * credentials, and a Save that took both would make switching one switch a chance to
 * rewrite the other's answer.
 */
import type { ModuleRow } from '~/types/module'
import { formatDuration, shortDigest, timeAgo } from '~/utils/format'
import { onRewake } from '~/lib/eventSocket'

const props = defineProps<{
  projectId: string
  projectPath: string
  module: ModuleRow
  canManage: boolean
}>()

/** One row of the clusters list, as this project sees it. */
interface Place {
  /** The cluster, under the name the module has it by. The place's name is both of these. */
  name: string
  namespace: string
  /**
   * Whether this project has said anything of its own about this place.
   *
   * The core is what knows: a place it has decided nothing about is somebody else's, and
   * the only thing this project may say about it is whether it is in use here.
   */
  ours: boolean
  /**
   * What this project has written down about this place, as the core sent it: the name,
   * the row's identity, and every field this project has decided. Kept whole because a
   * save replaces the level's whole list: a row sent with one switch in it takes every
   * other decision of this project with it.
   */
  own: Record<string, unknown>
  /** The core's own identity of this row, carried on save so that a row keeps the row it is. */
  id?: string
  /** Whether this project may deploy here at all. Nobody having said means yes. */
  inUse: boolean
  /** Whether a push may do it by itself. Nobody having said means yes. */
  autodeploy: boolean
  /**
   * Where this place's images come from, and whether anybody has said.
   *
   * Text rather than a control: a project's page may say a place is in use here or not,
   * but choosing where a cluster pulls from is a decision about the instance, and the
   * only page that makes it is the module's own. An empty string means nobody has chosen,
   * which is not the same as the instance's own registry and is said as such — a
   * deployment is refused over it.
   */
  registry: string
  /** Whether the registry is the one the level above chose rather than this project. */
  registryFromAbove: boolean
  /**
   * Whether the run under way has a deployment for this place that has not started.
   *
   * From the run rather than from this page's own reading of the last deployment: the
   * last deployment is about an older commit, and a row showing it green beside a run
   * that is plainly still working through its other places is answering a question
   * nobody asked.
   */
  queued: boolean
}

const places = ref<Place[]>([])

/**
 * Which places the run under way has not reached yet, by name.
 *
 * A name rather than a row, because a name is what the repository's file says and the
 * file is where the deployment names its place. Two rows of one name are one entry in
 * it, and both of them are due: a deployment to a name goes to every place written
 * under it.
 */
const due = ref<Record<string, boolean>>({})

/**
 * What last ran in each place, as the module reports it.
 *
 * Asked for per place rather than once for the project: a page of places shows one
 * card, and a card for one place cannot answer for the others. One small request per
 * place is the price of every row on the page carrying its own answer, and the answer
 * comes from the same place the card would have asked — so the row and the card beside
 * it cannot disagree about the same deployment.
 */
const runs = ref<Record<string, { state: string; started_at?: string; finished_at?: string; duration_ms?: number }>>({})
/** What each place is running now, by the module's own answer. */
const current = ref<Record<string, CurrentAnswer>>({})
/** The same rows as they are stored at the levels above, keyed by name. */
const inherited = ref<Record<string, Record<string, unknown>>>({})
const { add: notify } = useNotifyPool()

const loading = ref(true)
const saving = ref('')
const error = ref('')

/** One deployment, as the module's history reports it. */
interface Run {
  state: string
  started_at?: string
  finished_at?: string
  duration_ms?: number
}

/** The name of the place being added, and whether the form for it is open. */
const adding = ref(false)
const newName = ref('')

/**
 * The address a row names for its registry, and where it came from.
 *
 * What this project wrote wins over what the level above wrote, and an empty string means
 * nobody wrote anything — which is drawn as "not chosen" rather than as a blank, because a
 * blank reads as a fact about a registry that does not exist.
 */
function registryOf(row: Record<string, unknown>, above?: string): string {
  const own = typeof row.registry === 'string' ? row.registry.trim() : ''
  if (own !== '') return own
  return above ?? ''
}

/** What each row's two switches said when it was loaded, so a row knows whether it has changed. */
const loaded = ref<Record<string, { inUse: boolean; autodeploy: boolean }>>({})

async function load() {
  // Only the first load blanks the page. A save is followed by a re-read, and a page that
  // goes back to "Loading…" in between takes the place somebody is standing in — the
  // fold closes, the tab returns to Now, and the fields they had just filled in are not
  // there any more. The list is the answer; it is re-read under the eyes of whoever is
  // reading it rather than in place of it.
  if (places.value.length === 0) loading.value = true
  error.value = ''
  try {
    // "own" and not "effective", with the section the levels above published added: a
    // registry this project has decided nothing about is still the registry its images
    // come from, and a card that could only show this project's own answers would say
    // "not chosen" about a place that pulls perfectly well. What the levels above
    // published is only what the module declared safe to publish, so a credential is not
    // in here to be shown in the first place.
    const answer = await api.get<{
      own?: Record<string, unknown>
      inherited?: Record<string, unknown>
    }>(
      `/modules/${props.module.id}/settings?scope=project&projectID=${encodeURIComponent(props.projectId)}`,
    )
    const rows = Array.isArray(answer.own?.clusters)
      ? (answer.own!.clusters as Record<string, unknown>[])
      : []
    const fromAbove = new Map<string, string>()
    for (const row of (answer.inherited?.clusters ?? []) as Record<string, unknown>[]) {
      const name = typeof row?.name === 'string' ? row.name.trim() : ''
      const registry = typeof row?.registry === 'string' ? row.registry.trim() : ''
      if (name !== '' && registry !== '') fromAbove.set(name, registry)
    }

    // A row with no name is a row somebody started filling in, and it is not a place.
    // The name a row is called comes from the core when this project has not named it
    // itself: a place exists here whether this project has an opinion about it or not,
    // and a project that has no name for a cluster still has to be able to say which
    // cluster it is not deploying to.
    const seen: Place[] = []
    for (const row of rows) {
      const name = typeof row?.name === 'string' && row.name.trim()
        ? row.name.trim()
        : (typeof row?.dogit_row_name === 'string' ? row.dogit_row_name.trim() : '')
      if (!name) continue
      seen.push({
        name,
        ours: row.dogit_row_own === true,
        own: { ...row },
        id: typeof row.dogit_row_id === 'string' ? row.dogit_row_id : undefined,
        namespace: typeof row.default_namespace === 'string' ? row.default_namespace : '',
        inUse: row.enabled !== false,
        autodeploy: row.auto_deploy !== false,
        queued: due.value[name] === true,
        registry: registryOf(row, fromAbove.get(name)),
        registryFromAbove: !('registry' in row) && fromAbove.has(name),
      })
    }
    places.value = seen

    const baseline: Record<string, { inUse: boolean; autodeploy: boolean }> = {}
    for (const one of seen) baseline[one.name] = { inUse: one.inUse, autodeploy: one.autodeploy }
    loaded.value = baseline
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

async function loadQueued() {
  try {
    const answer = await api.get<{ places?: { target?: string; queued?: boolean }[] }>(
      `/projects/${props.projectId}/deploy-places`,
    )
    const next: Record<string, boolean> = {}
    for (const one of answer.places ?? []) {
      if (one?.queued && typeof one.target === 'string') next[one.target] = true
    }
    due.value = next
  } catch {
    // A page that cannot tell what the run has not reached yet claims nothing about it:
    // every row falls back to its own last deployment, which is true of the past.
    due.value = {}
  }
  for (const place of places.value) place.queued = due.value[place.name] === true
}

/**
 * Reads what last ran in each place on this page.
 *
 * One request per place, and only for the places on the page. A place with no
 * deployment at all is left with no entry rather than an empty one, so that its row
 * shows nothing rather than a claim about a deployment that never happened.
 */
async function loadRuns() {
  const target = props.module.kind.split(':')[1] ?? ''
  const answers = await Promise.all(places.value.map(async (place) => {
    const query = new URLSearchParams({
      target,
      page: '1',
      per_page: '1',
      cluster: place.name,
      namespace: place.namespace,
    })
    const answer = await api.get<{ deployments?: Run[] }>(
      `/projects/${props.projectId}/deployments?${query}`,
    )
    return [place.name, answer.deployments?.[0]] as const
  }))
  const found: Record<string, Run> = {}
  for (const [name, run] of answers) if (run) found[name] = run
  runs.value = found
}

/**
 * What the card under a row says about that place, kept as it says it.
 *
 * The card is the thing that watches the deployment as it happens, so it is the thing
 * that knows; this row only draws its verdict as a dot. Recorded per place rather than
 * as one answer for the page, because two places in one project are two rollouts with
 * two verdicts, and a page of one dot colour for both is a page about neither.
 */
const cardStates = ref<Record<string, { tone: string; word: string }>>({})

function onCardState(name: string, said: { tone: string; word: string }) {
  cardStates.value = { ...cardStates.value, [name]: said }
}

/**
 * What each place is running right now, as its own module says.
 *
 * Asked of the module because only it knows how to ask: a cluster can be asked which
 * image its workload runs, and anything this module deploys to later will have its own
 * answer — or say plainly that it cannot tell you. Reading it out of the history instead
 * would answer a different question, the one about what was last done here, and the two
 * come apart the moment anybody changes a thing by hand.
 *
 * One request per place, and only for the places on the page. A place with no answer
 * left out rather than filled with an empty one, so the row says nothing where it knows
 * nothing.
 */
async function loadCurrent() {
  const target = props.module.kind.split(':')[1] ?? ''
  const answers = await Promise.all(places.value.map(async (place) => {
    const query = new URLSearchParams({
      target,
      cluster: place.name,
      namespace: place.namespace,
    })
    try {
      const answer = await api.get<CurrentAnswer>(
        `/projects/${props.projectId}/deploy-current?${query}`,
      )
      return [place.name, answer] as const
    } catch {
      // A place whose module cannot be reached has no answer, which is a different
      // thing from an answer that says it does not know — and the row shows nothing
      // rather than a claim it cannot support.
      return [place.name, null] as const
    }
  }))
  const found: Record<string, CurrentAnswer> = {}
  for (const [name, answer] of answers) if (answer) found[name] = answer
  current.value = found
}

/**
 * What a module says is running in a place.
 *
 * `asked` is kept because it is the difference between "this is what is there" and
 * "this is the last thing that was written down", and a page that shows both without
 * saying which is which has made two claims out of one line.
 */
interface CurrentAnswer {
  known?: boolean
  image?: string
  tags?: string[]
  workload?: string
  ready?: number
  desired?: number
  settled?: boolean
  /** What the pods are actually running, by digest — more than one while a rollout goes. */
  pods?: { image: string; pods: number; tags?: string[] }[]
  asked?: string
  reason?: string
}

/**
 * The image this place is on right now, and how many pods are on it.
 *
 * From the pods and not from what the workload is set to: the setting names the image
 * the rollout is heading for from the moment it is accepted, so a place mid-rollout
 * would be reported as running the new image while every pod is still on the old one.
 */
function onPods(place: Place): { image: string; pods: number; tags?: string[] }[] {
  const answer = current.value[place.name]
  if (!answer?.known) return []
  const pods = answer.pods ?? []
  return pods.length > 0 ? pods : answer.image ? [{ image: answer.image, pods: answer.desired ?? 0 }] : []
}

/** The image on the most pods here — what this place is actually being served by. */
function servedBy(place: Place): string {
  const pods = onPods(place)
  if (pods.length === 0) return ''
  const most = pods.reduce((a, b) => (b.pods > a.pods ? b : a))
  return most.image
}

/**
 * The image this place is being put on, as soon as one pod is on it.
 *
 * The workload's own image, not the one with the most pods: a rollout that has put one
 * of three pods on the new image is already a rollout onto that image, and calling the
 * place "running" the old one until the last pod turns over says the opposite of what is
 * happening for the whole minute it takes.
 */
function comingOn(place: Place): string {
  const answer = current.value[place.name]
  if (!answer?.known || !answer.image) return ''
  const started = (answer.pods ?? []).some((one) => one.image === answer.image)
  return started ? answer.image : ''
}

/**
 * The one line about what is here: the image being put on where a rollout has begun,
 * and the one it is being served from otherwise.
 */
function primaryImage(place: Place): string {
  return comingOn(place) || servedBy(place)
}

/**
 * The image being taken off this place, if a rollout is taking one off.
 *
 * A pod on an image the workload is no longer set to: on its way out and still serving
 * traffic, which is the one state that is neither "running here" nor "gone" and that a
 * page marking images in two colours has to be able to say.
 */
function replacing(place: Place): string {
  const answer = current.value[place.name]
  if (!answer?.known) return ''
  const here = primaryImage(place)
  const outgoing = onPods(place).filter((one) => one.image !== here)
  if (outgoing.length === 0) return ''
  const most = outgoing.reduce((a, b) => (b.pods > a.pods ? b : a))
  return most.image
}

/** How many pods are on the image this place is being taken off. */
function replacingPods(place: Place): number {
  const leaving = replacing(place)
  if (!leaving) return 0
  return onPods(place).find((one) => one.image === leaving)?.pods ?? 0
}

/**
 * What is running in this place, in one line: the names it was published under when
 * they are known, and the digest either way.
 *
 * The digest is the fact — it is what is in the cluster, and two tags can point at one
 * digest and one tag can be moved to another. The names are beside it because a person
 * comparing this against a release is looking for `v88.15`, not for sixty-four
 * characters of hexadecimal. Nothing here is drawn when the module does not know:
 * a page that guesses which image is there is the thing this whole answer exists to
 * stop.
 */
/**
 * What this place is running, as far as it is known: the names, if it has any, and the
 * digest either way.
 *
 * Both, and never one instead of the other: the names are what a person compares
 * against a release, and the digest is the only thing that says which image it is — a
 * tag can be moved after the fact, and two tags can point at one image.
 */
function runningTags(place: Place): string[] {
  const answer = current.value[place.name]
  if (!answer?.known) return []
  const image = primaryImage(place)
  const on = (answer.pods ?? []).find((one) => one.image === image)
  return on?.tags ?? answer.tags ?? []
}

function runningDigest(place: Place): string {
  const answer = current.value[place.name]
  if (!answer?.known) return ''
  return shortDigest(servedBy(place))
}

/** What the module says is running in this place, in the shape the card takes. */
function nowRunning(place: Place): { image: string; tags?: string[]; pods: number; desired: number } | null {
  const answer = current.value[place.name]
  if (!answer?.known) return null
  const image = primaryImage(place)
  if (!image) return null
  const on = (answer.pods ?? []).find((one) => one.image === image)
  return {
    image,
    tags: on?.tags ?? answer.tags,
    // How many pods are on it, of how many this place runs. In the badge rather than in
    // a tooltip: "running" is a claim about pods, and "1 of 3" is the whole of what it
    // means while a rollout is going.
    pods: on?.pods ?? 0,
    desired: answer.desired ?? 0,
  }
}

function runningNow(place: Place): boolean {
  const answer = current.value[place.name]
  return Boolean(answer?.known && servedBy(place))
}

/**
 * How the pods stand, while a rollout is going.
 *
 * "2 on it · 1 still on [75b0eb9ad0e2]" — the split, in the line everybody reads. A row
 * that says one image is running while a third of the place serves another is the same
 * lie as the one this whole line was added to stop, said more quietly.
 */
function podsWording(place: Place): string {
  const answer = current.value[place.name]
  if (!answer?.known) return ''
  const leaving = replacing(place)
  const pods = onPods(place)
  if (!leaving || pods.length < 2) return ''
  const on = pods.filter((one) => one.image !== leaving).reduce((sum, one) => sum + one.pods, 0)
  const off = pods.filter((one) => one.image === leaving).reduce((sum, one) => sum + one.pods, 0)
  return `${on} on it · ${off} still on ${shortDigest(leaving)}`
}

/** What this line is a claim about, in full — the digest, where it came from, and how many of its pods. */
function runningTitle(place: Place): string {
  const answer = current.value[place.name]
  if (!answer?.known) return ''
  const parts: string[] = []
  // What the workload is set to, and what the pods are on, said as two different facts
  // because during a rollout they are two different facts.
  if (answer.image) parts.push(`wanted: ${answer.image}`)
  for (const one of onPods(place)) {
    const names = one.tags?.length ? `${one.tags.join(' ')} ` : ''
    parts.push(`${one.pods} pod(s) on ${names}${one.image}`)
  }
  if (answer.workload) parts.push(`${answer.workload} in ${place.namespace}`)
  parts.push(answer.asked ? `asked: ${answer.asked}` : 'asked: this module')
  return parts.join(' · ')
}

/**
 * What one line of a deployment says about a place, taken into this page's answer.
 *
 * The module's own fields, read as the module wrote them: which image it is putting on,
 * how many pods are on it, which image it is taking off and how many are left there. A
 * line that says none of that — a build, a push, a phase of a deployment — is not about
 * what is on the place's pods and changes nothing here.
 */
function noteEvent(payload: Record<string, unknown>) {
  const record = payload.deployment as
    | { cluster?: string; image?: string; workload?: string; tags?: string[] }
    | undefined
  const place = record?.cluster
  if (!place || !record?.image) return

  const ready = Number(payload.ready ?? 0)
  const desired = Number(payload.desired ?? 0)
  const previous = typeof payload.previous === 'string' ? payload.previous : ''

  // A line that says nothing about the pods leaves the last numbers standing.
  //
  // The last line of every deployment is one of those — "finished", with no counts —
  // and writing its zeroes over what came before turned a place that had just been
  // deployed to three pods into one running "0 of 0", which is a claim about a place
  // with no pods.
  const before = current.value[place]
  const on = desired > 0 ? ready : (before?.ready ?? 0)
  const of = desired > 0 ? desired : (before?.desired ?? 0)

  // Never more being taken off than the place has pods.
  //
  // A pod of the new set is counted among the old for a few seconds, until the cluster
  // has named its revision — and "4 of 3 retiring" is a figure about a workload that
  // cannot exist.
  const leaving = typeof payload.retiring === 'number'
    ? (of > 0 ? Math.min(payload.retiring, of) : payload.retiring)
    : Math.max(0, of - on)

  const pods: { image: string; pods: number; tags?: string[] }[] = []
  if (of > 0) pods.push({ image: record.image, pods: on, tags: record.tags })
  if (previous) pods.push({ image: previous, pods: leaving })

  current.value = {
    ...current.value,
    [place]: {
      known: true,
      image: record.image,
      tags: record.tags,
      workload: record.workload,
      ready: on,
      desired: of,
      settled: of > 0 && leaving === 0 && on >= of,
      pods,
      asked: 'the module, as it reports it',
    },
  }
}

/** The clock, read every half minute so that "3 minutes ago" does not stand still. */
const tick = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | undefined
onMounted(() => { ticker = setInterval(() => { tick.value = Date.now() }, 30_000) })
onUnmounted(() => clearInterval(ticker))

/**
 * The one line a row carries about its last deployment.
 *
 * The same words the card under it uses, because it is the same fact: when the last run
 * was and how long it took. A row that said it more briefly than the card would be two
 * answers to one question, and the row is the one read first.
 */
function ranAt(place: Place): string {
  void tick.value
  const run = runs.value[place.name]
  if (!run?.started_at) return ''
  const when = timeAgo(run.started_at)
  if (!when) return ''
  if (!run.finished_at) return `started ${when}, still going`
  const took = formatDuration(run.duration_ms ?? spanOf(run))
  return took ? `ran ${when}, took ${took}` : `ran ${when}`
}

/** How long it took, from its own two ends when it did not say. */
function spanOf(run: Run): number | undefined {
  if (!run.started_at || !run.finished_at) return undefined
  const from = new Date(run.started_at).getTime()
  const to = new Date(run.finished_at).getTime()
  return Number.isNaN(from) || Number.isNaN(to) ? undefined : Math.max(0, to - from)
}

/**
 * The row's own colour, and what it says.
 *
 * The same three words and the same three colours as the edge of the card below it,
 * from the same run: green for one that ended well, yellow for one going on, red for
 * one that did not, and grey for a place nothing has been deployed to yet. It repeats
 * what the edge says on purpose — the edge is inside a fold, and a list of places has
 * to be readable without opening any of them.
 */
function stateTone(place: Place): { tone: string; word: string } {
  // What the card under it says, when the card has said anything.
  //
  // One answer drawn twice rather than two answers about the same thing. Read apart —
  // a dot from this page's own fetch of the history, an edge from the card's — they
  // disagree the moment either is a moment older than the other, and a row with a grey
  // dot beside a green frame leaves the reader deciding which of the two to believe.
  const said = cardStates.value[place.name]
  if (said) return said

  const run = runs.value[place.name]
  // This run has not got here yet, which is not the same answer as "the last one went
  // well". The last one was a different commit, and a green dot beside a run that is
  // still working through the places before this one says the run is finished with this
  // place when it has not started it.
  if (place.queued && run?.state !== 'running') {
    return { tone: 'unknown', word: 'this run has not reached this place yet' }
  }
  if (!run) return { tone: 'unknown', word: 'nothing deployed here yet' }
  const word = run.state || 'unknown'
  if (word === 'running') return { tone: 'working', word: 'deploying' }
  if (word === 'succeeded') return { tone: 'ok', word: 'the last deployment ended well' }
  if (word === 'failed' || word === 'abandoned') return { tone: 'bad', word: `the last deployment ${word}` }
  return { tone: 'unknown', word: `the last deployment ${word}` }
}

/**
 * Flips one switch and saves it there and then.
 *
 * No Save button and no pending state: a switch is a thing you do, not a thing you fill
 * in and confirm, and a switch that has to be saved is a switch somebody leaves in the
 * wrong position because they assumed it had taken. The row's text fields, which do have
 * to be typed and checked, are saved from the row's own Save on the Settings tab.
 *
 * The word beside the switch is the state, so the answer to "did that work" is on the
 * page and not only in a notification that has already gone.
 */
async function flip(place: Place, key: 'inUse' | 'autodeploy') {
  const was = loaded.value[place.name]
  if (!was) return
  const before = { inUse: place.inUse, autodeploy: place.autodeploy }
  place[key] = !place[key]
  saving.value = place.name
  error.value = ''
  try {
    await save(places.value)
    await load()
    const now = place[key]
    const what = key === 'inUse'
      ? `${place.name} is now ${now ? 'in use' : 'not in use'}`
      : `${place.name} is now deployed ${now ? 'by a push' : 'only by hand'}`
    // Seconds, not milliseconds: a number five thousand here is a notice that
    // stays for an hour and a half, and everybody's screen fills with them.
    notify(what, { type: 'success', timer: 5 })
  } catch (caught) {
    place.inUse = before.inUse
    place.autodeploy = before.autodeploy
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
    notify(error.value, { type: 'error', timer: 0 })
  } finally {
    saving.value = ''
  }
}

/**
 * Writes this project's list of places, as it now stands.
 *
 * The whole list, every time. A save replaces what this level holds for the module, so a
 * save of one row is a save of one row: every other place's answers would be gone, and
 * gone without a word, from a page where the only thing that was touched was one switch.
 * Two rows are two answers, and writing one of them must not decide the other.
 */
async function save(rows: Place[]) {
  return api.put(
    `/modules/${props.module.id}/settings/bulk?scope=project&projectID=${encodeURIComponent(props.projectId)}`,
    { values: { clusters: rows.map((one) => own(one)) } },
  )
}

/**
 * What to send about one place.
 *
 * The whole of what this project has written down about it, with its two switches as they
 * now stand — and not just the switch. A row sent with nothing but `enabled` says that
 * this project has decided nothing else about this place either: turn In use off and the
 * project's own Autodeploy off with it, which is two answers changed by one click, one
 * of them not even on screen.
 */
/** Closes the form for adding a place without adding one. */
function cancelAdd() {
  adding.value = false
  newName.value = ''
}

function own(place: Place): Record<string, unknown> {
  // A name is sent only when this project has one of its own: the name on the page is
  // usually the module's, and writing it here would say this project named this place.
  const row: Record<string, unknown> = {}
  if (typeof place.own.name === 'string' && place.own.name.trim()) row.name = place.own.name
  // The identity goes back with the row: without it the core gives a new one on every
  // save, and a row whose identity changes each time is a row nothing can be recognised as.
  if (place.id) row.dogit_row_id = place.id
  row.enabled = place.inUse
  row.auto_deploy = place.autodeploy
  return row
}

/**
 * Adds a place of this project's own.
 *
 * Below the list rather than inside one place's settings: a place is a row of the list,
 * and a button that adds a row belongs where the rows are, not inside one of them. What it
 * writes is a name and nothing else — where a cluster is, and what it is called at the top
 * of the chain, is the module's to say, and this project's row is only that it uses it
 * and under what name.
 */
async function addPlace() {
  const name = newName.value.trim()
  if (!name || saving.value) return
  saving.value = 'adding'
  error.value = ''
  try {
    await save([...places.value, {
      name, namespace: '', ours: true, own: { name }, inUse: true, autodeploy: true,
      // Nothing is deploying to a place that did not exist a moment ago, whatever the
      // run under way was doing.
      queued: false,
      // And nothing says where a place that is only a name pulls from: the row above is
      // where that is written down, and until it is, a deployment to this name is refused.
      // This is the state between adding a name here and the module having a row for it.
      registry: '', registryFromAbove: false,
    }])
    adding.value = false
    newName.value = ''
    await load()
    notify(`${name} is now one of this project's places`, { type: 'success', timer: 5 })
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
    notify(error.value, { type: 'error', timer: 0 })
  } finally {
    saving.value = ''
  }
}

/**
 * What each place last ran is part of what this list is: a row that cannot say how its
 * last deployment went is a row somebody has to open to find out. Read with the places,
 * and again whenever the socket comes back — a page that was blind while a deployment
 * ran must not go on showing what was true before it.
 */
async function loadEverything() {
  await loadQueued()
  await load()
  // What is running comes last: it is asked about each place, which is only known once
  // the places are, and it is the answer most likely to be slow — it goes to the module
  // and sometimes to the cluster itself. The row is drawn from the other two first, so
  // a place that takes a moment to answer still shows its name, its switches and its
  // dot.
  await Promise.all([
    loadRuns().catch(() => {
      // A row without its state is a row without a dot; it is not a page that has failed.
    }),
    loadCurrent().catch(() => {}),
  ])
}

onMounted(loadEverything)
watch(() => props.module.id, loadEverything)
onRewake(() => {
  void loadEverything()
})

// A dot that only changes when the page is opened is a dot about the past. Read again
// when something happens to a place, and not on every line of it: a rollout says where
// it has got several times a second, and the answer does not change that often.
let runsSoon: ReturnType<typeof setTimeout> | undefined
watchEvents({
  // `pipeline.updated` as well: a run beginning is the moment every row stops being
  // current, and a page that only re-read on a deployment's own lines says nothing for
  // the whole build — which on a slow build is most of a run.
  kinds: ['deploy.history', 'deploy.operation', 'pipeline.updated'],
  project: () => props.projectPath,
  // What is running is NOT asked again here, and that is the point of the rule this
  // project works by: a request is for a one-off read — the list of images, the list of
  // operations — and everything that happens while somebody watches arrives over the
  // socket. The module's own lines carry which pods are on which image as the rollout
  // goes, and a row that asked the module on every line would be a request behind the
  // thing it is drawing: it is how "running 3/3" showed up once the rollout had already
  // finished, on a card that had been saying nothing for forty seconds.
  onEvent: (event) => {
    // What the module just said, taken as it said it. The row is about one place, and
    // the line says which place and what is on its pods: applied here, the row's line
    // moves with the rollout and keeps the answer the module gave when it ends. Asked
    // for instead, the row is a request behind the thing it is drawing — it says the
    // image from before for as long as the rollout takes, which is the whole minute
    // somebody is watching it.
    noteEvent(event.payload ?? {})

    clearTimeout(runsSoon)
    runsSoon = setTimeout(() => {
      void loadQueued().catch(() => {})
      void loadRuns().catch(() => {})
    }, 800)
  },
})
</script>

<template>
  <section class="places">
    <h2 class="places-title">{{ module.name }} places</h2>
    <p class="muted small">
      One row per place {{ props.projectPath }} may deploy to, and under it everything
      about that place: what is happening there, what has been deployed there, which
      images have been there, and its own settings.
    </p>

    <p v-if="error" class="alert alert-error">{{ error }}</p>
    <p v-if="loading" class="spinner">Loading…</p>

    <p v-else-if="places.length === 0" class="muted small">
      This module has no place this project may name. Its Options tab is where they are
      written down.
    </p>

    <!-- One place, folded away. The name is the whole of what is shown until somebody
         asks for it, the same way the repository's own configuration folds: a page of
         places that is a page of open cards is a page where nothing stands out. -->
    <!-- Keyed by the core's identity of the row rather than by its name: a place that is
         renamed is the same place, and a page that tore it down and built it again over a
         name would close the fold and lose the tab somebody was on. -->
    <details v-for="(place, index) in places" :key="place.id ?? place.name" class="place">
      <summary class="place-head">
        <!-- Three things on one line, and three places for them: what this place is
             called and whose decision it is, the two answers this project has about it,
             and how its last deployment went. The switches sit in the middle because
             they are what the row is for, and the row's name and its last run are the
             two facts either side of them. -->
        <span class="place-leading">
          <span class="place-name mono">{{ place.name }}</span>

          <!-- Whose place this is, said once, where the place is named. A page that cannot
               answer "whose setting is this" is a page nobody will trust enough to switch
               anything off in. -->
          <span class="badge place-origin" :class="{ ours: place.ours }">
            {{ place.ours ? 'changed here' : 'inherited' }}
          </span>

          <!-- Where the images come from, as a fact rather than as a control: a project
               may read where a place pulls from and may not decide it here. Said even
               when nobody has chosen anything, because "nobody has chosen" is the thing
               that stops a deployment and a list of places is where somebody looks first. -->
          <span
            class="muted small place-registry mono"
            :class="{ unchosen: !place.registry }"
          >
            registry: {{ place.registry || 'not chosen' }}
            <span v-if="place.registryFromAbove" class="place-registry-from">(decided above)</span>
          </span>
        </span>

        <!-- The two answers this project has about this place, in the row that names the
             place, so that a list of places can be read without opening any of them.
             Clicking one saves it at once; clicking the row itself only opens or closes. -->
        <span class="place-switches">
          <label
            class="switch-pair"
            @click.stop
            title="Autodeploy: whether a push or a tag may deploy to this place by itself. Off means the run still happens and the image is still built — only the deployment here waits for somebody to start it by hand."
          >
            <button
              class="switch"
              :class="{ on: place.autodeploy }"
              type="button"
              role="switch"
              aria-label="Autodeploy here"
              :aria-checked="place.autodeploy"
              :disabled="saving === place.name || !props.canManage || !place.inUse"
              @click.stop="flip(place, 'autodeploy')"
            >
              <span class="knob" />
            </button>
            <span class="switch-name">Autodeploy</span>
          </label>

          <!-- This one's label is its state, ON or OFF, and it sits where the other
               labels sit: a switch with a name on its left and a switch with a name on
               its right is two rows that read as one, and every switch on the page
               should be found by looking in the same place for its name. What the switch
               answers — may this project deploy here at all — is in its tooltip. -->
          <label
            class="switch-pair"
            @click.stop
            title="In use: whether this project may deploy here at all. Off leaves the place configured on the module's own page and untouched here — switched off for this project, not deleted."
          >
            <button
              class="switch"
              :class="{ on: place.inUse }"
              type="button"
              role="switch"
              aria-label="In use here"
              :aria-checked="place.inUse"
              :disabled="saving === place.name || !props.canManage"
              @click.stop="flip(place, 'inUse')"
            >
              <span class="knob" />
            </button>
            <span class="switch-state">{{ place.inUse ? 'ON' : 'OFF' }}</span>
          </label>
        </span>

        <!-- What is running here right now, how the last deployment went, and one dot. The
             dot repeats the colour of the card's edge on purpose: the edge is inside a
             fold, and a list of places has to be read without opening any of them. -->
        <span class="place-trailing">
          <!-- What is there, before how it got there: the version is the answer to "what
               is deployed", and the one below it answers a different question. Written
               from the module's own answer rather than from the history, because the
               history says what was done and this says what is.

               Drawn as the Images tab draws them — the name as a tag chip, the digest as
               a badge — because a row that names a version two ways from the table
               underneath it is two facts to match up before either can be read. -->
          <span
            v-if="runningNow(place)"
            class="small place-running"
            :title="runningTitle(place)"
          >
            running
            <span
              v-for="tag in runningTags(place)"
              :key="tag"
              class="tag mono"
            >{{ tag }}</span>
            <span class="badge badge-neutral place-running-digest mono">{{ runningDigest(place) }}</span>
            <span v-if="podsWording(place)" class="muted place-running-split">
              {{ podsWording(place) }}
            </span>
          </span>
          <span v-else-if="current[place.name]?.reason" class="muted small place-running">
            {{ current[place.name]?.reason }}
          </span>
          <!-- Said before the last run rather than instead of it: both are true, and the
               order is what keeps them from being read as one. -->
          <span v-if="place.queued" class="muted small place-queued">waiting its turn</span>
          <span v-if="ranAt(place)" class="muted small place-ran">{{ ranAt(place) }}</span>
          <span
            class="place-state"
            :class="stateTone(place).tone"
            :title="stateTone(place).word"
            :aria-label="stateTone(place).word"
            role="img"
          />
        </span>
      </summary>

      <div class="place-body">
        <!-- The card about this place and no other. -->
        <ModuleDeployments
          :project-id="props.projectId"
          :project-path="props.projectPath"
          :module="module"
          :can-manage="props.canManage"
          :place="{ cluster: place.name, namespace: place.namespace }"
          :queued="place.queued"
          :now-running="nowRunning(place)"
          :replacing="replacing(place)"
          :replacing-pods="replacingPods(place)"
          :show-repository="index === 0"
          @state="onCardState(place.name, $event)"
          @settings-changed="load"
        />
      </div>
    </details>

    <!-- Adding a place: below the rows, because that is what it does to the list. -->
    <div v-if="props.canManage" class="places-add">
      <form v-if="adding" class="places-add-form" @submit.prevent="addPlace">
        <input
          v-model="newName"
          class="add-name"
          type="text"
          placeholder="a place, such as production-eu"
          aria-label="Name of the place to add"
          spellcheck="false"
        >
        <button
          class="btn btn-small btn-primary"
          type="submit"
          :disabled="saving === 'adding' || !newName.trim()"
        >
          {{ saving === 'adding' ? 'Adding…' : 'Add' }}
        </button>
        <button class="btn btn-small" type="button" @click="cancelAdd">
          Cancel
        </button>
      </form>
      <button v-else class="btn btn-small" type="button" @click="adding = true">
        Add a place
      </button>
    </div>
  </section>
</template>

<style scoped>
.places {
  margin-top: 28px;
}

/* Where a place is added: under the rows, with room to type a name in. */
.places-add {
  margin-top: 14px;
}

.places-add-form {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}


.add-name {
  flex: 1 1 320px;
  max-width: 420px;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
}

.places-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 4px;
}

.place {
  margin-top: 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

/* The row that is always visible: the name, and one word about what happens here by
   itself. Everything else is inside, and comes out when the name is clicked. */
/* Aligned along the bottom rather than through the middle: the row is a line of things
   of different heights — a name, a badge, a switch and two labels — and centring them
   puts four different lines through the middle of one row. Their feet on one line is what
   makes a row of mixed things read as one row. */
/* A grid of three, because that is what the row is: a name on the left, the two
   switches in the middle, the last run on the right, and nothing in the row that has
   to be measured to find its place. Every cell is centred on the same line — the row
   is one line of text with a switch and a dot in it, and a dot that sits a few pixels
   below its own sentence reads as belonging to the row underneath it. */
.place-head {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: center;
  gap: 8px 16px;
  padding: 12px 14px;
  cursor: pointer;
  list-style: none;
}

/* Name, namespace and the badge that says whose place this is: the left third, and it
   may be as long as it likes — the switches do not move when it is. */
.place-leading {
  display: flex;
  align-items: center;
  gap: 8px 10px;
  min-width: 0;
}

/* The two switches, in the middle of the row, because they are what the row is for. */
.place-switches {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px 18px;
}

/* The last run and its dot, hard against the right edge, and as wide as the right
   third so that the dot is the same distance from the edge on every row. */
.place-trailing {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  min-width: 0;
}

.place-ran {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* What is running: the word, then the names this image was published under, then the
   digest. Laid out as the Images tab lays out a row, because that is where the reader
   has already seen a tag and a digest side by side. */
.place-running {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}

.place-running-split {
  font-size: 11px;
}

.place-running-digest {
  font-size: 11px;
  /* Badges capitalise their text, which is right for a word and wrong for a digest:
     `Bc9b83f2da0e` is not the name of anything, and a reader who copies it off this
     row gets an address that does not exist. */
  text-transform: none;
}

/*
 * The state of the last deployment, as one dot: the same three colours as the card's
 * edge, because it is the same answer. Big enough to be seen from the end of a list,
 * quiet enough that a row of them does not shout.
 */
.place-state {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  align-self: center;
  border-radius: 50%;
  background: var(--border-strong, #30363d);
}

.place-state.ok { background: var(--green); }
.place-state.working { background: var(--yellow); }
.place-state.bad { background: var(--red); }

/* Narrow places: the switches keep their place in the middle and the row wraps under
   it, rather than the three columns squeezing each other into unreadability. */
@media (max-width: 900px) {
  .place-head {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .place-switches {
    grid-column: 1 / -1;
    grid-row: 2;
    justify-content: flex-start;
  }
}

.place-head::-webkit-details-marker {
  display: none;
}

.place-name {
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* The registry, said quietly and cut off rather than wrapped: it is a fact about the
   place, not the fact about it, and a row that grows to three lines stops being a list. */
.place-registry {
  font-size: 11px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.place-registry.unchosen {
  color: var(--yellow);
}

.place-registry-from {
  opacity: 0.75;
}

.place-note {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

/*
 * Whose place this is, said as a badge beside its name: a place written at the module or
 * at the group is somebody else's decision, and a place this project wrote is its own.
 * Two words, and the difference between them is the whole point of inheritance — a list
 * that cannot answer "whose setting is this" is a list nobody will trust enough to switch
 * anything off in.
 *
 * Nothing of its own about how it looks: the badge class says the chip, the padding and
 * the capital, and a badge that pads itself differently is a badge to be read twice — once
 * to see what it says and once to work out what kind of thing it is.
 *
 * What is its own is where it stands: it takes the room the name leaves, so that the two
 * words of differing length push the gap beside the name rather than the switches along
 * the row. A row whose controls move when a word changes is a row where a switch is
 * somewhere else every time somebody reads the badge.
 */
.place-origin {
  margin-right: auto;
}

.place-origin.ours {
  background: var(--accent-soft);
  color: var(--accent);
}

.place-body {
  padding: 0 14px 14px;
}

.place-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
  padding-bottom: 10px;
  margin-bottom: 10px;
  border-bottom: 1px solid var(--border);
}

/* The switch and the word beside it stand on the same line as everything else in the
   head, so their own row is aligned by its foot as well — a label that hangs below its
   switch is one pixel of nothing that two places in a row will not both have. */
.switch-pair {
  display: inline-flex;
  align-items: flex-end;
  gap: 6px;
  cursor: pointer;
  /* A label is given a bottom margin by the page's own form styles, and six pixels of it
     is six pixels between this row's switches and the row they belong to. */
  margin-bottom: 0;
}

.switch-name {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
  /* The width of the longest of these words, and the width of the ON/OFF word beside the
     other switch, so that switching one does not narrow the row and push everything
     after it sideways. */
  min-width: 68px;
}

/* The state, spelled out. Fixed width, so that ON and OFF take the same room. */
.switch-state {
  min-width: 26px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

.not-saved {
  margin-right: auto;
}
</style>