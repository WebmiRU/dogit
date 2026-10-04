/**
 * The one connection to the instance's events.
 *
 * A WebSocket, opened once for the whole application at the root, and never closed by
 * a page: every page listens to this rather than to a stream of its own. Two reasons,
 * and the second is the one that matters.
 *
 * The first is obvious — a tab with six pages open should hold one connection, not six.
 * The second is that a page's own connection is a promise about what it will hear, and
 * that promise is made by a URL: this project, these kinds. Change the page and the
 * connection has to change with it, which is a reconnect, and a reconnect is a moment
 * when nothing is arriving. One unfiltered socket and a filter in the listener means
 * navigation costs nothing and nothing is missed while the page decides what it cares
 * about.
 *
 * Why not an event stream: because a stream's silence is its own idea of failure. The
 * browser decides how long a connection has to say something and closes it when it
 * decides so, which is a decision no part of this programme can see or argue with — and
 * a page that was cut off mid-deployment goes stale while the socket still claims to be
 * open. A WebSocket has ping and pong in the protocol: the server asks, the client has
 * to answer, and both ends find out within a round trip rather than when somebody
 * notices. The event stream is still served, and still works; it is simply not what the
 * interface uses.
 *
 * Narrowing is not a loss of privacy and must not become one: the core filters by what
 * the person may see before anything is written, so this socket carries every event this
 * account can see and no event it cannot. The project a page cares about is a question
 * about what to draw, not about what is allowed.
 */

export interface InstanceEvent {
  id: number
  kind: string
  created_at: string
  project_id?: string
  project_path?: string
  actor?: string
  payload?: Record<string, unknown>
}

type Listener = (event: InstanceEvent) => void
type Rewake = () => void

/** How long to wait before opening another socket after one has gone. */
const retryDelayMS = 2000
/** The longest that wait grows to, so a server that is down is not hammered. */
const retryCeilingMS = 15000

let socket: WebSocket | null = null
let opening = false
let retrying = false
let attempts = 0
/** The highest event id ever delivered, so a reopened socket resumes rather than repeats. */
let lastSeenID = 0

/**
 * What arrived in the last moments, kept for a page that was not listening yet.
 *
 * The socket opens the instant the document is ready, and the core answers a new
 * connection with everything it has not sent: the plan for this deployment, the steps,
 * the pod counts. All of it can arrive while the page is still mounting, before a
 * panel has registered anything to hear it, and a page that has to be told twice by the
 * network is a page that will be told once and shown a summary.
 *
 * So the last few seconds are held here and handed to any listener that arrives after
 * them. Being a page that redraws, replaying a fact it has already drawn must be its
 * own doing; losing the fact entirely is not.
 */
const recent: Held[] = []
/** How long an event stays worth handing to a late listener. */
const recentWindowMS = 30_000

interface Held {
  event: InstanceEvent
  at: number
}

const listeners = new Set<Listener>()
const rewakers = new Set<Rewake>()

function remember(event: InstanceEvent) {
  const at = Date.now()
  while (recent.length && at - recent[0].at > recentWindowMS) recent.shift()
  recent.push({ event, at })
}

/** What a listener joining now has missed, oldest first. */
function sinceJoin(): InstanceEvent[] {
  const at = Date.now()
  while (recent.length && at - recent[0].at > recentWindowMS) recent.shift()
  return recent.map((held) => held.event)
}

function wakeAll() {
  for (const wake of [...rewakers]) {
    try {
      wake()
    } catch {
      // One page's mistake must not stop the others re-reading.
    }
  }
}

/**
 * Where the socket opens.
 *
 * The address is a configuration value rather than an assumption about this page's
 * origin. In the shape things are deployed the interface and the API are one origin and
 * nothing has to be said. In development the page is served by a separate dev server
 * whose proxy passes ordinary requests but refuses a protocol upgrade, so the socket is
 * pointed at the core directly, where cookies are shared because cookies belong to a
 * host and not to a port.
 */
function socketURL(): string {
  const configured = useRuntimeConfig().public.eventSocketURL as string | undefined
  if (configured) return configured
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${scheme}://${location.host}/api/v1/events/socket`
}

/** Opens the socket, or does nothing if it is already open or on its way. */
export function openEventSocket() {
  if (socket || opening) return
  if (import.meta.server) return

  // Not while the page is still loading: a connection opened during a load is one the
  // browser may abandon when the load finishes, and it says so in the console as
  // "aborted during page load". Nothing is missed by waiting — a socket that opens
  // later is sent the whole backlog anyway.
  if (document.readyState !== 'complete') {
    window.addEventListener('load', () => openEventSocket(), { once: true })
    return
  }

  opening = true
  const url = socketURL()
  const opened = new WebSocket(url)
  socket = opened

  opened.addEventListener('open', () => {
    opening = false
    // A socket has no query string to be reopened with, so where to resume from
    // travels in the first message. Sent immediately and unasked for: the server waits
    // for it before it sends anything, and a client that stays quiet makes every
    // connection wait for the server to time out.
    opened.send(JSON.stringify({ since: lastSeenID }))
    const first = attempts === 0
    attempts = 0
    console.info(
      `[dogit] event socket ${first ? 'connected' : 'reconnected'} at ${new Date().toLocaleTimeString()}`,
    )
  })

  opened.addEventListener('message', (message) => {
    let event: InstanceEvent
    try {
      event = JSON.parse(String(message.data)) as InstanceEvent
    } catch {
      // A line that is not an event is not something to act on, and not something to
      // stop listening over either.
      return
    }

    // The keep-alive is not news about anything, and a page that redrew itself because
    // the socket said hello would be worse than no keep-alive at all.
    if (event.kind === 'ping') return

    // The highest id ever seen, so a reopened socket can be asked for what came after it
    // rather than for everything again. Kept across connections, and deliberately not
    // reset on close: a close is not an erasure.
    if (typeof event.id === 'number' && event.id > lastSeenID) lastSeenID = event.id
    remember(event)

    for (const listener of [...listeners]) {
      try {
        listener(event)
      } catch {
        // One page's mistake must not stop the others hearing about it.
      }
    }
  })

  opened.addEventListener('error', () => {
    // The error event carries nothing useful by design; the close event says why.
    console.warn(`[dogit] event socket error at ${new Date().toLocaleTimeString()}`)
  })

  opened.addEventListener('close', (event) => {
    const wasOpen = attempts === 0 && event.wasClean === false
    opening = false
    if (socket === opened) socket = null

    console.warn(
      `[dogit] event socket closed (code ${event.code}${event.reason ? `, ${event.reason}` : ''}) ` +
        `at ${new Date().toLocaleTimeString()}; opening another in ${retryAfter()}ms`,
    )

    // Whoever is listening was blind for as long as that was, and a page showing a live
    // process has to know: the deployment carried on without it.
    wakeAll()

    if (wasOpen) return
    scheduleReopen()
  })

  function retryAfter() {
    return Math.min(retryDelayMS * 2 ** attempts, retryCeilingMS)
  }

  function scheduleReopen() {
    if (retrying) return
    retrying = true
    attempts += 1
    setTimeout(() => {
      retrying = false
      openEventSocket()
    }, retryAfter())
  }
}

/**
 * Told when the socket comes back, or after it has been gone.
 *
 * Separate from the events themselves: a reconnection is not something that happened on
 * this instance, it is something that happened to the connection.
 */
export function onRewake(wake: Rewake): () => void {
  openEventSocket()
  rewakers.add(wake)
  return () => {
    rewakers.delete(wake)
  }
}

/** Listens, and returns the way to stop. Never closes the socket itself. */
export function onEvent(listener: Listener): () => void {
  openEventSocket()
  // What has already gone past since this page loaded, given to it now rather than
  // lost. Everything, not only what is recent: a listener that mounted late is missing
  // the whole of the operation it is here to show.
  for (const event of sinceJoin()) {
    try {
      listener(event)
    } catch {
      // Same rule as for live events: one page's mistake is not the socket's problem.
    }
  }
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** How many pages are listening. For a check, not for the interface. */
export function listenerCount() {
  return listeners.size
}
