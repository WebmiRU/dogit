/** Shapes of the recipients list, as the interface sees them. */

/**
 * One place notifications go.
 *
 * A row is a recipient rather than a module: one module can write to two chats, and
 * one chat can be inherited by twenty repositories. Everything the list needs to
 * answer about a row is here — what it is, where the row came from, and which level
 * is responsible for the value.
 */
export interface Recipient {
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
export interface RecipientsModule {
  id: string
  kind: string
  name: string
  enabled: boolean
  target?: {
    identify?: string[]
    title?: string
    description?: string
  }
  settings?: import('./module').SettingSpec[]
}
/** The row being added or changed in the recipients form. */
export interface RecipientDraft {
  /** Empty while adding. */
  id: string
  moduleId: string
  label: string
  values: Record<string, string>
  /** The inherited row being changed rather than added. Empty when adding. */
  overrideId: string
}
