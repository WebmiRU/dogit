/**
 * Opens the instance's one event connection, at the root, for the whole
 * application.
 *
 * A plugin rather than a component because the connection belongs to the interface
 * and not to any page: it is opened while the shell is being built and stays open
 * while the reader moves about, so navigating from a pipeline to a project to a
 * deploy page is not three connections and two moments of silence.
 */
import { openEventSocket } from '~/lib/eventSocket'

export default defineNuxtPlugin(() => {
  if (import.meta.server) return

  openEventSocket()
})
