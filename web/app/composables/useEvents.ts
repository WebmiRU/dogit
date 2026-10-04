/**
 * Follows the instance's events.
 *
 * A stream, not a poll. The core holds the connection open and writes an event the
 * moment it happens, so a page hears about a deployment starting as it starts
 * rather than up to a few seconds later, and a page with nothing to watch costs
 * one idle connection instead of a request every few seconds for ever.
 *
 * Reconnection is the browser's: EventSource retries on its own, and the core is
 * told where to resume with the standard Last-Event-ID header, so a dropped
 * connection neither misses events nor repeats them. The fallback to polling is
 * gone on purpose — the query shape the cursor read speaks is still served, for a
 * script or a proxy that would rather ask than hold a connection open, but a page
 * has no reason to choose it.
 *
 * Events are signals, not records: a page that needs the commits of a branch asks
 * for them again through the endpoint it already has. Pushing the data would only
 * ever be right for one page.
 */
/** An event as the feed carries it: enough to know something happened. */
export interface InstanceEvent {
  id: number
  kind: string
  created_at: string
  project_id?: string
  project_path?: string
  actor?: string
  payload?: Record<string, unknown>
}

/**
 * What to watch, and what to do about it.
 *
 * `kinds` narrows the feed to what a page cares about, so a page does not reload
 * because somebody's ssh key was added. `project` narrows it to one project, which
 * is what a page inside a project wants: the feed is filtered by the core, so a
 * page never receives an event about a project it may not see.
 */
export interface WatchOptions {
  /** Reload when one of these kinds arrives. Everything else is ignored. */
  kinds?: string[]
  /**
   * Narrow the feed to one project.
   *
   * A function, because a page inside a project changes projects without being
   * re-created, and a value read once would keep listening to the project the
   * reader has already left. The core filters by it, so a page never hears about a
   * project it may not see.
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
}

/**
 * Connections, one per set of options.
 *
 * Not because the feed is a scarce resource, but because a page inside a project
 * can change project without being re-created: the connection names one project in
 * its address, so following a move to another project means opening another one.
 * Sharing them would mean every page on the instance listening to everything,
 * which is the opposite of what the project filter is for.
 */
const open = new Map<string, { stream: EventSource; refs: number }>()

/** Starts following and returns a stop function. */
export function watchEvents(options: WatchOptions): () => void {
  const watched = options.kinds?.length ? new Set(options.kinds) : null

  // What each connection is for, so several pages asking for the same stream share
  // one rather than each opening their own.
  const handle = (ev: MessageEvent) => {
    let event: InstanceEvent
    try {
      event = JSON.parse(ev.data) as InstanceEvent
    } catch {
      // A line that is not an event is not something to act on, and not something
      // to stop listening over either.
      return
    }
    if (watched && !watched.has(event.kind)) return

    // The signal is not the data: whatever this page shows is fetched again from
    // the endpoint it trusts, so nothing is ever drawn from a payload.
    options.onChange?.()
    options.onEvent?.(event)
  }

  // The connection is keyed by the project it is for, and by nothing else: two
  // pages in one project want the same events and the same filtering, and each
  // still gets its own callbacks.
  let key = '__instance__'
  let started = ''
  const connect = () => {
    const project = typeof options.project === 'function' ? options.project() : options.project
    const wanted = project ? `project=${encodeURIComponent(project)}` : ''

    if (wanted === started) return
    started = wanted

    // The project changed under a live page: the old connection is about a project
    // the reader has left.
    if (key !== '__instance__' || wanted) release(key)

    key = wanted || '__instance__'
    const existing = open.get(key)
    if (existing) {
      existing.refs++
      existing.stream.addEventListener('message', handle)
      return
    }

    const stream = new EventSource(`/api/v1/events/stream${wanted ? `?${wanted}` : ''}`, {
      withCredentials: true,
    })
    // "message", not "event": the feed sends no event name, and a stream line with
    // no name arrives under the default type. Listening for "event" waits for a
    // message that is never coming, and the connection looks perfectly healthy
    // while saying nothing for ever.
    stream.addEventListener('message', handle)
    open.set(key, { stream, refs: 1 })
  }

  const release = (which: string) => {
    const held = open.get(which)
    if (!held) return
    held.stream.removeEventListener('message', handle)
    held.refs--
    if (held.refs > 0) return
    held.stream.close()
    open.delete(which)
  }

  connect()
  // A page can be re-created around the same project without the old one being
  // disposed first, so the address is checked rather than trusted.
  const watch = setInterval(connect, 2000)

  return () => {
    clearInterval(watch)
    release(key)
  }
}