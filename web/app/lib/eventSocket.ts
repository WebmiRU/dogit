/**
 * The one connection to the instance's events.
 *
 * It is opened once, for the whole application, and never closed: every page
 * listens to this and not to a stream of its own. Two reasons, and the second is
 * the one that matters.
 *
 * The first is obvious — a tab with six pages open should hold one connection, not
 * six. The second is that a page's own connection is a promise about what it will
 * hear, and that promise is made by a URL: this project, these kinds. Change the
 * page and the connection has to change with it, which is a reconnect, and a
 * reconnect is a moment when nothing is arriving. One unfiltered socket and a filter
 * in the listener means navigation costs nothing and nothing is missed while the page
 * decides what it cares about.
 *
 * Narrowing is not a loss of privacy and must not become one: the core filters by
 * what the person may see before anything is written, so this socket carries every
 * event this account can see and no event it cannot. The project a page cares about
 * is a question about what to draw, not about what is allowed.
 *
 * A connection that stops delivering does not say so. The socket stays in its open
 * state, the browser does not reconnect, and the page goes quietly stale — which from
 * here is indistinguishable from a deployment that has stopped saying anything. So
 * something watches the clock: the core pings every fifteen seconds, and silence
 * across several of those is treated as death and answered by opening another.
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

/**
 * How long the connection may be silent before it is treated as gone.
 *
 * Three of the server's fifteen-second pings: long enough that a slow network or a
 * busy laptop is not mistaken for a dead connection, and short enough that a page
 * which has lost the stream starts asking again while somebody is still watching.
 */
const silenceIsDeathMS = 45_000

/** How often the watchdog looks. */
const watchdogEveryMS = 10_000

let stream: EventSource | null = null
let opening = false
/** A retry is already scheduled, so a refused socket does not queue up a dozen. */
let retrying = false
/** When the last thing arrived, whether an event or a ping. */
let lastHeard = Date.now()

const listeners = new Set<Listener>()
const rewakers = new Set<Rewake>()

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
 * Opens the socket, or does nothing if it is already open.
 *
 * Safe to call from anywhere and any number of times: a page asking for the events
 * must not be able to open a second connection by asking twice.
 */
export function openEventSocket() {
  if (stream || opening) return
  if (import.meta.server) return

  // Not while the page is still loading.
  //
  // A connection opened during load is one the browser may abandon when the load
  // finishes — and it says so, in the console, as "the event stream was aborted
  // during page load". Waiting costs nothing: the events that matter happen after
  // somebody starts reading, and a connection that opens later is sent the same
  // backlog by the same code.
  if (document.readyState !== 'complete') {
    window.addEventListener('load', () => openEventSocket(), { once: true })
    return
  }

  opening = true
  const opened = new EventSource('/api/v1/events/stream', { withCredentials: true })

  // Kept only once it has actually opened.
  //
  // A connection that has been created but not opened is one the page may be about
  // to unload, or one something in the middle has refused. Holding on to it means the
  // next caller believes there is a live stream, joins a socket that is never going to
  // carry anything, and never asks again.
  let openedYet = false

  // "message", not "event": the feed sends no name for its lines, and a stream line
  // with no name arrives under the default type. Listening for "event" waits for a
  // message that is never coming, and the connection looks perfectly healthy while
  // saying nothing for ever.
  opened.addEventListener('message', (message) => {
    let event: InstanceEvent
    try {
      event = JSON.parse((message as MessageEvent).data as string) as InstanceEvent
    } catch {
      // A line that is not an event is not something to act on, and not something
      // to stop listening over either.
      return
    }
    lastHeard = Date.now()

    // The keep-alive is not news about anything. It exists so the connection is
    // visibly alive, and a page that redrew itself every fifteen seconds because the
    // socket said hello would be worse than no keep-alive at all.
    if (event.kind === 'ping') return

    for (const listener of [...listeners]) {
      try {
        listener(event)
      } catch {
        // One page's mistake must not stop the others hearing about it.
      }
    }
  })

  // The server's own ping, on the same connection. Proof it carries, and what the
  // watchdog below measures silence from.
  opened.addEventListener('ping', () => {
    lastHeard = Date.now()
  })

  opened.addEventListener('open', () => {
    const wasOpen = openedYet
    openedYet = true
    stream = opened
    opening = false
    lastHeard = Date.now()

    // A connection that was open and is open again has been through a gap, and
    // anything that happened in it was not delivered to anybody. A page showing the
    // present cannot carry on from what it last heard: it has to ask again.
    if (wasOpen) {
      console.info('[dogit] the event stream was interrupted and is back')
      wakeAll()
    }
  })

  opened.addEventListener('error', () => {
    if (openedYet) {
      // The browser reconnects on its own and resumes from where it got to, so there
      // is nothing to do but say so. Nothing here rebuilds the socket: a reconnect
      // that started again from the beginning would replay the instance's whole
      // history at a page that has already seen it.
      console.warn('[dogit] the event stream is reconnecting')
      return
    }

    // Never opened: let go of it and try again shortly, so a stream refused while a
    // page was loading is not the end of events for that session.
    console.warn('[dogit] the event stream could not be opened')
    opened.close()
    opening = false
    if (stream === null && !retrying) {
      retrying = true
      setTimeout(() => {
        retrying = false
        openEventSocket()
      }, 1000)
    }
  })

  stream = opened
  opening = false
  lastHeard = Date.now()

  setInterval(() => {
    if (stream !== opened) return
    const silent = Date.now() - lastHeard
    if (silent < silenceIsDeathMS) return

    console.warn(`[dogit] the event stream has been silent for ${Math.round(silent / 1000)}s; reopening`)
    // Whoever is watching must know it was blind, or they keep believing a card that
    // stopped hearing anything.
    wakeAll()
    opened.close()
    stream = null
    opening = false
    openEventSocket()
  }, watchdogEveryMS)
}

/**
 * Told when the connection comes back after having been open, or after going silent.
 *
 * Separate from the events themselves: a reconnection is not something that happened
 * on this instance, it is something that happened to the connection. A page that
 * draws a live process has to know, because while the connection was down the process
 * carried on without it.
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
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** How many pages are listening. For a check, not for the interface. */
export function listenerCount() {
  return listeners.size
}
