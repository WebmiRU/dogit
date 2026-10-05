/** Shapes of the recipients list, as the interface sees them. */

/**
 * One place notifications go.
 *
 * A row is a recipient rather than a module: one module can write to two chats, and
 * one chat can be inherited by twenty repositories. Everything the list needs to
 * answer about a row is here — what it is, where the row came from, and which level
 * is responsible for the value.
 */
export interface ModuleRow {
  id: string
  module_id: string
  module_kind: string
  module_name: string
  /** A name for the human, when somebody gave the row one. */
  label: string
  /** Every setting, inherited ones included. */
  values: Record<string, unknown>
  /** Only the settings this level set. */
  own_values: Record<string, unknown>
  set_here: Record<string, boolean>
  enabled: boolean
  /** Whether this level is the one that switched it, rather than agreeing with above. */
  enabled_here: boolean
  /**
   * The switches the module declared for a row, resolved: the most specific level that
   * said anything about a key decides it. A key that is absent was decided by nobody and
   * stands at the module's default — which is why absent is not the same as false.
   */
  flags?: Record<string, boolean>
  /** Only the switches this level itself decided. */
  flags_here?: Record<string, boolean>
  /** The level a change belongs to. */
  scope_type: string
  /** The level the row itself was created at. */
  inherited_from: string
  overridden: boolean
}

/**
 * A module that could be added to the list.
 *
 * What a recipient of it is made of comes from the module: it says which of its
 * settings name a destination, so the list can print an address instead of a row
 * number.
 */
/** The answer for one place: its recipients, the modules available, and what went. */
export interface ModuleRowsAnswer {
  targets: ModuleRow[]
  modules: ModuleRowsModule[]
  /**
   * How many settings were removed because they had stopped applying — a project moved
   * to another group, leaving them pointing at a channel that is not its own any more.
   * Said rather than done quietly: a setting that vanishes without a word is a setting
   * somebody will look for.
   */
  stale_removed?: number
}

export interface ModuleRowsModule {
  id: string
  kind: string
  name: string
  enabled: boolean
  target?: {
    /** The settings a recipient is made of. Absent means all of them. */
    settings?: string[]
    /** The settings that name a recipient, best first. */
    identify?: string[]
    title?: string
    description?: string
    /** The switches a row has besides the one every row has. */
    flags?: ModuleRowFlag[]
  }
  settings?: import('./module').SettingSpec[]
}
/** The row being added or changed in the recipients form. */
/** One switch a module says its rows have. */
export interface ModuleRowFlag {
  key: string
  label: string
  description?: string
  /** Where it stands where nobody has decided. True unless the module says otherwise. */
  default: boolean
}

export interface ModuleRowDraft {
  /** Empty while adding. */
  id: string
  moduleId: string
  label: string
  values: Record<string, string>
  /** The inherited row being changed rather than added. Empty when adding. */
  overrideId: string
}
