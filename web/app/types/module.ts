/** Shapes of the module API, as the interface sees them. */

/**
 * What one entry of a "list" setting is made of.
 *
 * The fields are described rather than guessed, for two reasons: the form renders from
 * this instead of inventing inputs, and a secret field inside an entry can be marked so
 * it is masked on the way out. A password inside a list stored as one piece of JSON
 * cannot be masked at all — the blob comes back whole or not at all.
 */
export interface SettingItems {
  fields: SettingSpec[]
  /** What the button to add another entry says, in the module's words. */
  add_label?: string
  /**
   * The fields that say which entry this is — a cluster's name, a recipient's chat id.
   *
   * Naming them is what makes a list inherit entry by entry: a project that changes
   * one cluster's namespace has not stopped using the others.
   */
  identify?: string[]
}

export interface SettingSpec {
  key: string
  label: string
  /** string | bool | int | enum | url | list */
  type: string
  default?: unknown
  options?: string[]
  description?: string
  secret?: boolean
  /** Present when the type is "list": what one row of it is made of. */
  items?: SettingItems
}

/** One row of a list setting: its fields, keyed by their own keys. */
export type SettingEntry = Record<string, unknown>

/**
 * One switch in a module's removal dialog.
 *
 * The wording is the module's own: the core renders these and knows nothing about
 * what any of them do, so "Delete all images" comes from the registry and would
 * read just as well from a build cache or a package feed.
 */
export interface UninstallOption {
  key: string
  label: string
  description?: string
  default?: boolean
  dangerous?: boolean
  required?: boolean
}

export interface ModuleRouting {
  domains?: string[]
  path?: string
  websocket?: boolean
}

export interface ModuleManifest {
  version: string
  description?: string
  scopes: string[]
  settings: SettingSpec[]
  depends_on?: string[]
  uninstall?: { options?: UninstallOption[] }
  routing?: ModuleRouting
  /**
   * What one of this module's rows is made of, when the module has rows.
   *
   * A notification module's rows are the chats it writes to and a deployment module's
   * are the clusters it reaches, and the two are the same thing to the core: something
   * named, with values, inherited downwards, switched on and off per level. Which is
   * why this sits on the manifest rather than being asked of the kind.
   */
  target?: ModuleTargetSpec
}

/** What a module says one of its rows is. */
export interface ModuleTargetSpec {
  /** The settings a row is made of. Absent means all of them. */
  settings?: string[]
  /** The settings that name a row, best first. */
  identify?: string[]
  /** What the page calls the list, in the module's own words. */
  title?: string
  description?: string
  /** The switches a row has besides the one every row has. */
  flags?: ModuleRowFlagSpec[]
}

/** One switch a module says its rows have. */
export interface ModuleRowFlagSpec {
  key: string
  label: string
  description?: string
  /** Where it stands where nobody has decided. True unless the module says otherwise. */
  default: boolean
}

/** What a module reported with its heartbeat. Absent means it reported nothing. */
export interface ModuleStats {
  at: string
  storage_total_bytes?: number
  storage_used_bytes?: number
  process_cpu_percent?: number
  process_memory_bytes?: number
  host_cpu_percent?: number
  host_memory_total_bytes?: number
  host_memory_used_bytes?: number
  host_load1?: number
  uptime_seconds?: number
  extra?: Record<string, string>
}

export interface ModuleRow {
  id: string
  kind: string
  name: string
  endpoint: string
  module_version: string
  manifest: ModuleManifest
  status: string
  enabled: boolean
  last_seen_at?: string
  registered_at: string
  settings: Record<string, unknown>
  scopes: string[]
  /** Where the module is reachable from outside, when it asked to be. */
  public_url?: string | null
  dedicated_host?: boolean
  stats?: ModuleStats | null
  /**
   * Connections the module has open to the core right now.
   *
   * Zero is not an error on its own — a module that has nothing to say may hold no channel —
   * but it is the difference between a module that is absent and one that is here and idle.
   */
  channel_connections?: number
  /**
   * Commands the core is holding for this module, to give it when it comes back.
   *
   * Non-zero while the module is connected means the core made a command that has not been
   * delivered yet, which is worth knowing before somebody concludes the module ignored it.
   */
  channel_pending_commands?: number
}

export type UninstallStatus =
  | 'queued'
  | 'running'
  | 'done'
  | 'failed'
  | 'stalled'
  | 'interrupted'

export interface UninstallJob {
  id: string
  integration_id: string
  options: string[]
  status: UninstallStatus
  progress_done?: number
  progress_total?: number
  summary?: Record<string, unknown>
  error?: string
  started_at?: string
  finished_at?: string
  created_at: string
  updated_at: string
  last_line_at?: string
}

export interface UninstallLogLine {
  id: number
  job_id: string
  created_at: string
  level: string
  message: string
  progress?: { done?: number; total?: number }
}

/** Wording for a job's state, in the terms an administrator thinks in. */
export const uninstallStatusText: Record<UninstallStatus, string> = {
  queued: 'Starting',
  running: 'Running',
  done: 'Finished',
  failed: 'Failed',
  stalled: 'Stopped talking',
  interrupted: 'Outcome unknown',
}