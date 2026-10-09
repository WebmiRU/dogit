/**
 * Follows the instance's events.
 *
 * Everything comes through one connection that the application opened at its root
 * (see `app/lib/eventSocket.ts`). A page says here what it cares about — which
 * kinds, which project — and this file decides whether each event is one of them.
 *
 * The filtering is here rather than in the connection's address on purpose. A
 * connection that names a project has to be reopened when the reader moves to
 * another one, and a reopen is a moment when nothing arrives; a page that filters
 * costs nothing to navigate and misses nothing while it does. Narrowing is not a
 * question of permission either way: the core sends only what this account may
 * see, before any of this runs.
 *
 * Events are signals, not records: a page that needs the commits of a branch asks
 * for them again through the endpoint it already has. Pushing the data would only
 * ever be right for one page — the exception being a deployment's progress, which
 * is gone by the time anybody could ask what it was, and which `onEvent` is for.
 */
import { onEvent, onRewake, type InstanceEvent } from '~/lib/eventSocket'

export type { InstanceEvent }

/**
 * What to watch, and what to do about it.
 *
 * `kinds` narrows the feed to what a page cares about, so a page does not reload
 * because somebody's ssh key was added. `project` narrows it to one project, which
 * is what a page inside a project wants.
 */
export interface WatchOptions {
  /** Reload when one of these kinds arrives. Everything else is ignored. */
  kinds?: string[]
  /**
   * Narrow to one project.
   *
   * A function, because a page inside a project changes projects without being
   * re-created, and a value read once would keep listening to the project the
   * reader has already left.
   */
  project?: string | (() => string | undefined)
  /**
   * Called with each event that was wanted, payload and all.
   *
   * Off by default and normally wrong: the signal is not the data, and a page that
   * draws from a payload is drawing from something that was true when it was sent.
   * It exists for the one thing that cannot be re-read — a deployment's progress,
   * which is gone by the time anybody could ask what it was.
   */
  onEvent?: (event: InstanceEvent) => void
  /** Called when one of the watched kinds arrives. */
  onChange?: () => void
  /**
   * Called when the socket comes back.
   *
   * Its own option and not a kind, because a reconnection is not something that happened on this
   * instance — it is something that happened to the connection, and there is no event for it. A
   * page that patches itself from the events after a gap draws steps nobody saw finish: what was
   * missed is not the two lines that did not arrive, it is the whole of the operation.
   */
  onReconnect?: () => void
}

/** Starts following and returns a stop function. */
export function watchEvents(options: WatchOptions): () => void {
  const watched = options.kinds?.length ? new Set(options.kinds) : null

  // Registered before the listener, so a page is not told to re-read for an event it has not
  // been given yet: the socket's own history is handed to the listener below, and a reload
  // triggered before that would read the list and then read it again.
  const stopRewake = options.onReconnect ? onRewake(options.onReconnect) : null

  const stopEvents = onEvent((event) => {
    if (watched && !watched.has(event.kind)) return

    const project = typeof options.project === 'function' ? options.project() : options.project
    // An event about no project at all — a module registering, a runner going quiet
    // — belongs to the instance rather than to a project, and a project page has
    // no reason to redraw for one. The feed already dropped anything this account
    // may not see; this is about what this page is for.
    if (project && event.project_path !== project) return

    // The signal is not the data: whatever this page shows is fetched again from
    // the endpoint it trusts, so nothing is ever drawn from a payload.
    options.onChange?.()
    options.onEvent?.(event)
  })

  return () => {
    stopEvents()
    stopRewake?.()
  }
}
