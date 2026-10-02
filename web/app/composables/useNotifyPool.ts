/**
 * The notification pool.
 *
 * One pool for the whole application, held in shared state: a notice about
 * something that happened on another page has to survive the navigation, and
 * that is the normal case here — moving a project sends the reader to its new
 * address.
 *
 * The shape follows the one used in the shop's interface: newest first, a timer
 * that empties the notice on its own, links for the things worth going to, and
 * a progress bar so the time left is visible rather than guessed.
 */
export interface NotifyLink {
  label: string
  to: string
}

export interface NotifyItem {
  id: number
  message: string
  type: 'success' | 'error' | 'warning' | 'info'
  /** Seconds on screen; zero keeps the notice until it is dismissed. */
  timer: number
  links: NotifyLink[]
  /** A note that the list shown is not the whole of it, such as "…". */
  more: string
}

const DEFAULT_TIMER = 5

export function useNotifyPool() {
  // The key is its own: notices from an earlier shape of this pool are still in
  // the browser's persisted state, and a component that expects a message would
  // crash on them rather than ignore them.
  const items = useState<NotifyItem[]>('dogit:notify-pool', () => [])

  let nextId = 1

  /** Removes a notice; the component asks for it once its animation is over. */
  function remove(id: number) {
    const index = items.value.findIndex((item) => item.id === id)
    if (index !== -1) items.value.splice(index, 1)
  }

  interface NotifyOptions {
    type?: NotifyItem['type']
    /** Seconds; pass 0 for a notice that waits to be dismissed. */
    timer?: number
    links?: NotifyLink[]
    more?: string
  }

  /**
   * Puts a notice up.
   *
   * The message may carry newlines: each line becomes a row, which is how a
   * result with an old and a new address is readable without a table.
   */
  function add(message: string, options: NotifyOptions = {}): number {
    const id = nextId++
    items.value.unshift({
      id,
      message,
      type: options.type ?? 'success',
      timer: options.timer ?? DEFAULT_TIMER,
      links: options.links ?? [],
      more: options.more ?? '',
    })
    return id
  }

  return { items, add, remove }
}