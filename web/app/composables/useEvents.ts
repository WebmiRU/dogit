/**
 * Follows the instance's events.
 *
 * The feed is a cursor-based read rather than a socket, and that is what the core
 * serves today: each poll asks for everything since the last id and the server says
 * when to come back. The shape is the one a WebSocket will speak, so swapping the
 * transport later is a change to this file and to nothing that uses it.
 *
 * Events are signals, not records: a page that needs the commits of a branch asks
 * for them again through the endpoint it already has. Pushing the data would only
 * ever be right for one page.
 *
 * The cadence is the server's, and a page with nothing to watch stops asking.
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

interface Feed {
  events: InstanceEvent[]
  cursor: number
  retry_ms?: number
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
   * re-created, and a value read once would keep asking about the project the
   * reader has already left. The core filters by it, so a page never hears about a
   * project it may not see.
   */
  project?: string | (() => string | undefined)
  /** How often to ask when the server has not said. */
  intervalMS?: number
  /** Called when one of the watched kinds arrives. */
  onChange?: () => void
}

/** Starts following and returns a stop function. */
export function watchEvents(options: WatchOptions): () => void {
  const { kinds, intervalMS = 4000 } = options

  let cursor = 0
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined

  // The kinds as a set, so each event costs one lookup rather than a scan.
  const watched = kinds?.length ? new Set(kinds) : null

  async function poll() {
    if (stopped) return

    const project = typeof options.project === 'function'
      ? options.project()
      : options.project
    const query = new URLSearchParams({ since: String(cursor) })
    if (project) query.set('project', project)

    try {
      const answer = await api.get<Feed>(`/events?${query.toString()}`)

      // The cursor moves whether or not anything was wanted: this page is not
      // responsible for other pages' events, and re-reading them forever would grow
      // without bound on a busy instance.
      if (answer.cursor > cursor) cursor = answer.cursor

      const interesting = (answer.events ?? []).some(
        (event) => !watched || watched.has(event.kind),
      )
      // The signal is not the data: whatever this page shows is fetched again from
      // the endpoint it trusts, so nothing is ever drawn from a payload.
      if (interesting) options.onChange?.()
    } catch {
      // A poll that failed is not a reason to stop watching: the page is over a
      // network that blips, and the next one tries again.
    }

    if (stopped) return
    // Nothing was said about the next attempt, so this one decides.
    timer = setTimeout(poll, Math.max(1000, intervalMS))
  }

  void poll()

  return () => {
    stopped = true
    if (timer) clearTimeout(timer)
  }
}