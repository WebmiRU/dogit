/**
 * One registry as the core gives it to a browser.
 *
 * Note what is not here: the password. It is stored and it is editable, and the core
 * never sends it back — so a list of ten registries does not put ten passwords into
 * the page, into the browser's memory and into whatever took a photograph of the
 * screen. `has_password` is what the form needs to say "set" instead of showing a
 * value nobody should be shown.
 */
export interface DockerRegistry {
  id?: string
  /**
   * Where the row came from.
   *
   * `written` is an address an administrator wrote down, and it has an id, a form and
   * an Edit button. `module` is a registry this instance runs as a module: it has no
   * form here at all, because everything about it is configured on the module's own
   * page, and it can disappear when the module is uninstalled. A list that showed both
   * without saying which was which would have a reader editing a row that cannot be
   * edited, or worse, deleting one.
   */
  source?: 'module' | 'written'
  /** The module's own record, for a `module` row. */
  integration_id?: string
  module_kind?: string
  /** False while the module publishes no address anybody could push to. */
  published?: boolean
  /** What the module last said about itself: online, offline, and so on. */
  status?: string
  /** A name for the human only. Empty means the list shows the address. */
  name: string
  url: string
  login?: string
  has_password?: boolean
  insecure_tls?: boolean
  read_only?: boolean
  note?: string
  enabled?: boolean
  created_at?: string
  updated_at?: string
}

/**
 * A registry an administrator wrote down, as the edit form receives it.
 *
 * The fields a `module` row does not have are back to being required here, because this is
 * the shape the form edits and a form whose fields are all optional is a form that has to
 * ask "was that sent?" about every one of them. The list reads `DockerRegistry`, which can
 * be either kind of row; the form reads this, which can only be one.
 */
export interface WrittenDockerRegistry extends DockerRegistry {
  id: string
  url: string
  login: string
  has_password: boolean
  insecure_tls: boolean
  read_only: boolean
  note: string
  enabled: boolean
  created_at: string
  updated_at: string
}

/** One page of the list, and how much of the list there is. */
export interface DockerRegistryPage {
  registries: DockerRegistry[]
  total: number
  page: number
  pages: number
  per_page: number
  /** True when the address asked for a page past the end of a list that has shrunk. */
  past_the_end?: boolean
}

/** What a form sends when saving a registry. */
export interface DockerRegistryInput {
  name?: string
  url?: string
  login?: string
  /**
   * Undefined leaves the stored password alone, which is what a form that was never
   * given one has to say. An empty string clears it, deliberately.
   */
  password?: string
  insecure_tls?: boolean
  read_only?: boolean
  note?: string
  enabled?: boolean
}

/**
 * What one scope has written down about a registry, and what a build for that scope
 * would use.
 *
 * Two answers because a form needs both, and they are not the same. `credential_source`,
 * `login` and `has_password` are what *this* scope has set: an empty one here means
 * inherited, not cleared. `resolved` is the whole chain applied — what a build would
 * actually push with.
 *
 * A form shown only the second would offer to save an inherited login as though the
 * group had chosen it, and saving would turn an inheritance into a copy. That looks
 * right, works, and silently stops following the thing it was inheriting from: somebody
 * changes the instance's login, and this group quietly stays on the old account.
 */
export interface RegistryCredentials {
  scope_type: 'instance' | 'group' | 'project'
  scope_id?: string
  /** False when this scope has written nothing at all. */
  written: boolean
  /** Empty when this scope set none, whatever the chain resolves to. */
  credential_source: string
  login: string
  has_password: boolean
  resolved: {
    credential_source: string
    login: string
    has_password: boolean
  }
  /**
   * Set when this registry cannot be pushed to at all. Said here rather than as an
   * error, because the question asked was what this scope has written and that is
   * answerable whatever the registry will do with a build.
   */
  resolved_error?: string
}

/** What a form sends when saving one scope's credential. */
export interface RegistryCredentialsInput {
  credential_source?: string
  login?: string
  /** Undefined leaves it alone; an empty string removes this scope's own password. */
  password?: string
}
