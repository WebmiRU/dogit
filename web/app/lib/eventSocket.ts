/**
 * The one connection to the instance's events.
 *
 * It is opened once, for the whole application, and never closed: every page
 * listens to this and not to a stream of its own. Two reasons, and the second is
 * the one that matters:
 *
 * The first is obvious — a tab with six pages open should hold one connection, not
 * six. The second is that a page's own connection is a promise about what it will
 * hear, and that promise is made by a URL: this project, these kinds. Change the
 * page and the connection has to change with it, which is a reconnect, and a
 * reconnect is a moment when nothing is arriving. One unfiltered socket and a
 * filter in the listener means navigation costs nothing and nothing is missed
 * while the page decides what it cares about.
 *
 * Narrowing is not a loss of privacy and must not become one: the core filters by
 * what the person may see before anything is written, so this socket carries every
 * event this account can see and no event it cannot. The project a page cares
 * about is a question about what to draw, not about what is allowed.
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

let stream: EventSource | null = null
let opening = false
/** A retry is already scheduled, so a refused socket does not queue up a dozen. */
let retrying = false
const listeners = new Set<Listener>()

/** Reported once, to whoever is looking, rather than to nobody. */
let onProblem: ((what: string) => void) | null = null

export function reportSocketProblem(what: string) {
  onProblem?.(what)
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

  opening = true
  const opened = new EventSource('/api/v1/events/stream', { withCredentials: true })

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
    for (const listener of [...listeners]) {
      try {
        listener(event)
      } catch {
        // One page's mistake must not stop the others hearing about it.
      }
    }
  })

  opened.addEventListener('error', () => {
    // The browser reconnects on its own and resumes from where it got to, so there
    // is nothing to do but say so. Nothing here rebuilds the socket: a reconnect
    // that starts again from the beginning would replay the instance's whole
    // history at a page that has already seen it.
    reportSocketProblem('the event stream is reconnecting')
  })

  stream = opened
  opening = false
}

/** Listens, and returns the way to stop. Never closes the socket itself. */
export function onEvent(listener: Listener): () => void {
  openEventSocket()
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/**
 * Feeds an event to the listeners as though the socket had sent it.
 *
 * TEMPORARY, and for the interface only. It exists so the deploy card's progress can
 * be watched working without waiting for a real cluster to be slow on purpose: the
 * steps, the spinner and the pod counter are driven from the same place a real
 * event would drive them from, so what is being checked is the page.
 *
 * It proves nothing about delivery. Anything that depends on events actually
 * arriving has to be checked against a real run.
 */
export function injectLocalEvent(event: InstanceEvent) {
  for (const listener of [...listeners]) {
    try {
      listener(event)
    } catch {
      // A page's mistake must not stop the others hearing about it.
    }
  }
}

/** How many pages are listening. For a check, not for the interface. */
export function listenerCount() {
  return listeners.size
}
