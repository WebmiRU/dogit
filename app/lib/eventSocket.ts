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
/**
 * Whether the connection is still being given the backlog.
 *
 * True from opening until the server says the catch-up has ended. Everything read while
 * it is true is history, and history is not something a page may act on — see the sync
 * handling in the message listener.
 */
let catchingUp = true
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

/** Forgets what is older than the window, oldest first, so the held events stay in order. */
function forgetOlderThan(at: number): void {
  while (recent.length > 0) {
    const oldest = recent[0]
    // Nothing older than the oldest thing held, which is only possible if the list is empty.
    if (!oldest || at - oldest.at <= recentWindowMS) return
    recent.shift()
  }
}

function remember(event: InstanceEvent) {
  const at = Date.now()
  forgetOlderThan(at)
  recent.push({ event, at })
}

/** What a listener joining now has missed, oldest first. */
function sinceJoin(): InstanceEvent[] {
  forgetOlderThan(Date.now())
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
  let reconnected = false

  opened.addEventListener('open', () => {
    opening = false
    // A socket has no query string to be reopened with, so where to resume from
    // travels in the first message. Sent immediately and unasked for: the server waits
    // for it before it sends anything, and a client that stays quiet makes every
    // connection wait for the server to time out.
    opened.send(JSON.stringify({ since: lastSeenID }))
    // A reopened connection is given everything since the cursor, and all of it is
    // history by definition: it happened while this page was not watching.
    catchingUp = true
    reconnected = attempts > 0
    attempts = 0
    console.info(
      `[dogit] event socket ${reconnected ? 'reconnected' : 'connected'} at ${new Date().toLocaleTimeString()}`,
    )
    // A reopened socket catches up again, and everything it is about to send is history.
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

    // The end of the backlog, said by the server because only it knows where the
    // catch-up ended.
    //
    // Everything before it is history the page did not ask for and must not act on:
    // a deploy page that mounted in the middle of a run would be handed three
    // thousand old events and draw them as though they had just happened — the card
    // would jump back to "build the image" for a deployment that finished an hour ago,
    // and then jump forward again, and every step on the page would be a lie told in
    // the present tense.
    if (event.kind === 'sync') {
      catchingUp = false
      // The backlog is deliberately not replayed as live progress. Re-read durable state
      // only after catch-up finishes, otherwise a request made at close time can fail or
      // return an older snapshot and the page stays stale after the socket is healthy again.
      recent.length = 0
      if (reconnected) {
        reconnected = false
        wakeAll()
      }
      return
    }

    // Before the sync: taken for the cursor, given to nobody.
    if (catchingUp) {
      if (typeof event.id === 'number' && event.id > lastSeenID) lastSeenID = event.id
      return
    }

    // The highest id ever seen, so a reopened socket can be asked for what came after it
    // rather than for everything again. Kept across connections, and deliberately not
    // reset on close: a close is not an erasure.
    if (typeof event.id === 'number' && event.id > lastSeenID) lastSeenID = event.id
    remember(event)

    for (const listener of [...listeners]) {
      try {
        listener(event)
      } catch (thrown) {
        // One page's mistake must not stop the others hearing about it. Said out loud,
        // because a listener that throws on every event looks exactly like a socket
        // that delivers nothing, and there is no other trace of it.
        console.warn('[dogit] an event listener threw', thrown)
      }
    }
  })

  opened.addEventListener('error', () => {
    // The error event carries nothing useful by design; the close event says why.
    console.warn(`[dogit] event socket error at ${new Date().toLocaleTimeString()}`)
  })

  opened.addEventListener('close', (event) => {
    opening = false
    if (socket === opened) socket = null

    const delay = retryAfter()
    console.warn(
      `[dogit] event socket closed (code ${event.code}${event.reason ? `, ${event.reason}` : ''}) ` +
        `at ${new Date().toLocaleTimeString()}; opening another in ${delay}ms`,
    )

    // A close is a gap whether it was clean or not. In particular, code 1006 is the
    // browser reporting that the connection disappeared without a close frame. Reconnect
    // even then; listeners are refreshed after the new connection has caught up.
    scheduleReopen()
  })

  function retryAfter() {
    return Math.min(retryDelayMS * 2 ** attempts, retryCeilingMS)
  }

  function scheduleReopen() {
    if (retrying) return
    retrying = true
    const delay = retryAfter()
    attempts += 1
    setTimeout(() => {
      retrying = false
      openEventSocket()
    }, delay)
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
  // Added after the replay, so a listener is never handed an event by the loop that
  // is about to call it and then by the loop again.
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** How many pages are listening. For a check, not for the interface. */
export function listenerCount() {
  return listeners.size
}
