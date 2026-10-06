<script setup lang="ts">
/**
 * One module's settings, at one level.
 *
 * Extracted from the module's own page because the same form is wanted in three
 * places — the instance, a group and a project — and three copies of a form drift
 * apart within a month. What differs between them is only whose values these are,
 * which is what `scope` says.
 *
 * The values shown are the effective ones, so a project that inherits its channel
 * from the group sees what it is actually using rather than a blank box that might
 * mean "empty" or "inherited". What is saved here is saved for this scope alone: a
 * project's own value does not change what its group or the instance uses.
 */
import type { ModuleRow, SettingEntry, SettingSpec } from '~/types/module'
import type { DockerRegistry, DockerRegistryPage } from '~/types/registry'

const props = withDefaults(
  defineProps<{
    module: ModuleRow
  /**
   * Fields this page must not show, by key.
   *
   * A field is hidden rather than emptied, and the form says why: a cluster's
   * kubeconfig is a credential, and the project page is open to everybody who may read
   * the project. What is hidden here is written on the module's own Options page, by
   * somebody allowed to.
   */
  hideFields?: string[]
  /**
   * One row of a list setting, by name, rather than all of them.
   *
   * Used by a place's own page: a place is one cluster, and a card about that cluster
   * configures that cluster — with the same fields, in the same form, as the module's own
   * Settings page. Two forms for one thing would be two places to be wrong in the same
   * way.
   */
  onlyRow?: string
  /**
   * Whether this viewer may change anything here.
   *
   * Off unless the page says otherwise, because Vue gives an absent Boolean prop the
   * value false and a form that silently became read-only is worse than one that never
   * could be edited. A page that is open to everybody who may read a project shows its
   * settings with the controls still there, and does not: a control that cannot work
   * makes the page look broken, and the point there is that a developer can see what
   * they deploy to.
   */
  canEdit?: boolean
  scope?: 'instance' | 'group' | 'project'
    /**
     * Which group or project, when the scope is not the instance.
     *
     * Spelled with a lower-case d on purpose: Vue turns the attribute
     * "scope-id" into "scopeId", and a prop named "scopeID" is never given a
     * value — it is simply absent, silently.
     */
    scopeId?: string
    /** A sentence about where this sits in the inheritance. */
    note?: string
  }>(),
  { scope: 'instance' },
)

const { add: notify } = useNotifyPool()

// Values are not all strings. A list setting is a set of rows, each of which has
// fields of its own, so the form keeps what the module said and coerces only on the
// way out. Storing "the string" of a list would mean JSON.parse-ing on every render
// and having nothing to render before the first keystroke.
const values = ref<Record<string, unknown>>({})
const saved = ref<Record<string, unknown>>({})
const busy = ref(false)
const error = ref('')

/** The query that says whose settings are being read and written. */
const mayEdit = computed(() => props.canEdit !== false)

/**
 * A copy, all the way down, that works on what Vue is holding.
 *
 * structuredClone refuses a reactive proxy — and everything in `values` and `saved` is
 * one — which is why Discard threw a DataCloneError and did nothing at all: the one
 * button whose whole job is to put a row back could not put it back. Going through JSON
 * is what these values are made of: a setting is JSON, so there is nothing in one that
 * JSON cannot carry.
 */
function clone<T>(value: T): T {
  return value === undefined ? value : (JSON.parse(JSON.stringify(value)) as T)
}

const scopeQuery = computed(() => {
  if (props.scope === 'instance') return 'scope=instance'
  const name = props.scope === 'group' ? 'groupID' : 'projectID'
  return `scope=${props.scope}&${name}=${encodeURIComponent(props.scopeId ?? '')}`
})

function asString(value: unknown): string {
  if (value === undefined || value === null) return ''
  return String(value)
}

/**
 * Fills the form from what this scope has decided.
 *
 * Read from "own": the values a level above holds are not sent to a page at all, so what
 * arrives is a row's name and the fields this scope overrode. A field that is missing is
 * therefore not an empty value — it is one somebody else decided, and the field says so
 * in its placeholder rather than pretending to be blank.
 */
function fill(answer: { own?: Record<string, unknown>; inherited?: Record<string, unknown> }) {
  inheritedValues.value = answer.inherited ?? {}
  const next: Record<string, unknown> = {}
  for (const spec of props.module.manifest?.settings ?? []) {
    const stored = answer.own?.[spec.key]
    next[spec.key] = isList(spec)
      ? entriesOf(stored)
      : Object.prototype.hasOwnProperty.call(answer.own ?? {}, spec.key)
        ? asString(stored)
        : ''
  }
  values.value = next
  // A copy that goes all the way down, not one level.
  //
  // `{ ...next }` copies the keys and hands over the values themselves, which is
  // harmless for a string and fatal for a list: the array in `saved` is the same array
  // the form edits, so typing into a row changed the original as well, the comparison
  // found two identical things and the form decided nothing had happened. Every list
  // setting was therefore unsaveable — clearing a cluster's kubeconfig or its namespace
  // left the Save button grey, because from the form's point of view nothing had
  // changed. Structural cloning is what makes "what it was" and "what it is now" two
  // separate things, which is the only way telling them apart means anything.
  saved.value = clone(next)
}

/**
 * The fields a module declared, minus the ones this page must not show.
 *
 * A row's identity is one of those: it is the core's own field, written for the core and
 * not for a reader, and a page that showed it would be showing an internal detail where
 * a cluster's name belongs.
 */
function visibleFieldsOf(spec: SettingSpec): SettingSpec[] {
  const hidden = new Set([...(props.hideFields ?? []), 'dogit_row_id'])
  return (spec.items?.fields ?? []).filter((field) => !hidden.has(field.key))
}

function isList(spec: SettingSpec): boolean {
  return spec.type === 'list'
}

/**
 * The name that says which row this is, or nothing when the module names none.
 *
 * The same rule as the core's: a row with a half-written name is not a reference to
 * another row, and joining them would be a guess.
 */
function rowName(spec: SettingSpec, row: SettingEntry): string {
  const identify = spec.items?.identify ?? []
  if (!identify.length) return ''
  const parts: string[] = []
  for (const field of identify) {
    const text = asString(row[field]).trim()
    if (!text) {
      // A name this scope has not decided is not a name it holds — but the row is still
      // called something, and this is what it is called. Matching a row by that is how a
      // form about one place finds it when the place is somebody else's.
      return asString(row.dogit_row_name).trim()
    }
    parts.push(text)
  }
  return parts.join(' ')
}

/**
 * Whether one row is worth writing at this scope at all.
 *
 * A row arrives carrying its name and whatever this scope decided; a row that decided
 * nothing here is somebody else's row, and writing it down would say that it is this
 * scope's own. A list whose entries have no names cannot be told apart row by row, so
 * what is on screen is what is saved — as before.
 */
function rowIsOurs(spec: SettingSpec, row: SettingEntry, index?: number): boolean {
  if (!(spec.items?.identify?.length)) return true
  // A row that cannot be recognised at all is not a row: there is nothing to write it onto
  // and nothing to say it is anybody's. A row the core gave an identity to is a row,
  // named or not — the name may well be the level above's, and that is not a reason to
  // pretend this scope has decided nothing about it.
  if (!row.dogit_row_id && !rowName(spec, row)) return false
  if (decidedFields(spec, row).length > 0) return true
  if (index === undefined) return false
  // A row that was not here when the page was loaded is this scope's own: it is the one
  // somebody added. And a row whose name no longer matches the one it was loaded with is
  // this scope's own too, because a name is a field this scope may change — which is an
  // override of one field, not a second row.
  return !entriesOf(saved.value[spec.key])[index] || renamed(spec, row, index)
}

/** Whether a row's name is no longer the name it arrived with. */
function renamed(spec: SettingSpec, row: SettingEntry, index: number): boolean {
  const was = entriesOf(saved.value[spec.key])[index]
  if (!was) return false
  return rowName(spec, was) !== rowName(spec, row)
}

/**
 * Whether a field is this scope's to change.
 *
 * A row that is entirely inherited belongs to the level above, and its name with it:
 * renaming somebody else's row would leave two rows of the same cluster, one of them
 * still theirs. A row this scope has decided anything about can be renamed, because from
 * that moment it is its own row.
 */
function fieldLocked(spec: SettingSpec, row: SettingEntry, index: number, key: string): boolean {
  // Nothing is locked here except the whole form for somebody who may not change it. A
  // name, like any other field, is this project's to decide: writing a name of its own
  // makes a row of its own, and the place it was copied from is then one this project no
  // longer uses — which is what the row's In use switch is for.
  return !mayEdit.value
}

/**
 * The fields of a row this scope decided.
 *
 * A row comes from the core carrying only what is its own: the values a level above
 * holds are not sent to a page at all. So "has a value" and "decided it here" are one
 * question, which is what lets one form be the same form at every scope.
 */
function decidedFields(spec: SettingSpec, row: SettingEntry): string[] {
  const identify = new Set(spec.items?.identify ?? [])
  // A row's identity is not a decision: the core writes it, the page never shows it, and
  // counting it would make every inherited row look like this scope's own — which is how
  // an inherited row ended up with a Remove button. Neither is the core's word for
  // whose row a row is: it is bookkeeping, and a page that decided it could not be
  // believed if it did.
  const core = new Set([...coreRowFields(spec), ...coreBookkeeping])
  return Object.keys(row).filter((key) => !identify.has(key) && !core.has(key))
}


/**
 * A list as it is written at this scope: only what this scope decided.
 *
 * A row that matches what came from above is left out entirely, and a row that differs
 * keeps only the fields that differ. Two reasons, and the second is the important one:
 *
 * - saving here then changes what was meant to change and nothing else;
 * - a field copied down from above is a copy. The level above changes its cluster's
 *   kubeconfig tomorrow and the copy here is yesterday's, silently, and nobody editing
 *   this page would ever see that this project holds a value of its own.
 *
 * What the core sends back is the effective list, so the row that comes back still
 * shows every field. Nothing is lost; what is stored is only what was decided here.
 */
/**
 * A list as it is written at this scope.
 *
 * A field that has been emptied is left out rather than written as an empty value: a
 * cleared override is a decision to stop overriding, and what it goes back to is the
 * level above's — which the core knows and this page has never seen. Writing "" instead
 * would keep the override alive with nothing in it, and a deployment would then be told
 * the namespace is the empty string.
 */
function listSettable(spec: SettingSpec, raw: unknown): unknown {
  if (!isList(spec)) return settingValue(spec, raw)
  const rows: SettingEntry[] = []
  for (const [at, row] of entriesOf(raw).entries()) {
    if (!rowIsOurs(spec, row, at)) continue
    const kept: SettingEntry = {}
    for (const [key, value] of Object.entries(row)) {
      if (value === '' || value === undefined || value === null) continue
      kept[key] = value
    }
    if (Object.keys(kept).length) rows.push(coerceRow(spec, kept))
  }
  if (!rows.length) return undefined
  return rows
}

/**
 * One row as the setting wants it, for the fields it carries.
 *
 * Not listValue, which fills in every declared field: that is right for a row being
 * written whole and wrong here, where a field that was left out is a field this scope
 * has no opinion about, and writing it as an empty value would be an opinion.
 */
function coerceRow(spec: SettingSpec, row: SettingEntry): SettingEntry {
  const fields = spec.items?.fields ?? []
  const out: SettingEntry = {}
  for (const field of fields) {
    if (!(field.key in row)) continue
    const value = row[field.key]
    if (field.secret && value === secretMask) {
      // Kept as the mask, so the core leaves the stored value alone rather than
      // storing a row of asterisks over somebody's token.
      out[field.key] = secretMask
      continue
    }
    out[field.key] = fieldValue(field, value)
  }
  for (const key of spec.items?.identify ?? []) {
    if (key in out || !(key in row)) continue
    out[key] = row[key]
  }
  // The core's own row fields are not fields the module declared, so they are carried
  // by hand: without this, switching an inherited row off would save a row with
  // nothing in it and the switch would come undone on the next load.
  for (const key of coreRowFields(spec)) {
    if (!(key in row)) continue
    out[key] = row[key] === true
  }
  // A row's identity is carried through untouched: it is how the core knows this row is
  // the same row after its name has changed, and dropping it here would turn a renamed
  // row into a new row.
  if (typeof row.dogit_row_id === 'string') out.dogit_row_id = row.dogit_row_id
  return out
}

/**
 * The core's own bookkeeping on a row, written by the core and never sent back.
 *
 * `dogit_row_id` is how a row is recognised as itself after its name has changed,
 * `dogit_row_own` is the core's answer to "was this row written here" — the only thing
 * that decides whether the row may be deleted on this page — and `dogit_row_name` is what
 * the row is called where it was written, said so that a row with no name of its own here
 * can still be titled and found.
 */
const coreBookkeeping = ['dogit_row_id', 'dogit_row_own', 'dogit_row_name']

/** The two fields the core keeps in a list whose entries have names. */
function coreRowFields(spec: SettingSpec): string[] {
  return spec.items?.identify?.length ? ['enabled', 'auto_deploy'] : []
}

/** Two field values, compared as values rather than as strings. */
function sameValue(a: unknown, b: unknown): boolean {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null)
}

/**
 * The rows of a list setting, always as an array of objects.
 *
 * Anything that is not already a list of objects is read as no rows at all, rather
 * than as one row with the wrong fields: a half-recognised value would be shown as a
 * row somebody can edit, and saving it would turn a value this form did not
 * understand into one it wrote.
 */
function entriesOf(value: unknown): SettingEntry[] {
  if (!Array.isArray(value)) return []
  return value.filter((row): row is SettingEntry =>
    typeof row === 'object' && row !== null && !Array.isArray(row),
  )
}

async function load() {
  error.value = ''
  try {
    const answer = await api.get<{ own?: Record<string, unknown>; inherited?: Record<string, unknown> }>(
      `/modules/${props.module.id}/settings?${scopeQuery.value}`,
    )
    fill(answer)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  }
}

/** Coerces what the form holds back into what the setting expects. */
function settingValue(spec: SettingSpec, raw: unknown): unknown {
  if (isList(spec)) return listValue(spec, raw as SettingEntry[])

  const text = asString(raw)
  // A secret the core does not return comes back as a mask; submitting that would
  // store the word as the password.
  if (spec.secret && text === '********') return undefined
  if (spec.type === 'bool') return text === 'true'
  if (spec.type === 'int') {
    const parsed = Number.parseInt(text, 10)
    return Number.isNaN(parsed) ? text : parsed
  }
  return text
}

/**
 * A list as the module asked for it: each row carrying each of its declared fields.
 *
 * Fields the row does not have become an empty value rather than being absent, and
 * fields the module never declared are dropped. Both directions matter: an absent
 * boolean reads as "not decided" somewhere else, and a field this build does not know
 * is refused by the core rather than stored — so writing one would be a save that
 * fails for a reason the form could have prevented.
 */
function listValue(spec: SettingSpec, rows: SettingEntry[]): SettingEntry[] {
  const fields = spec.items?.fields ?? []
  return rows.map((row) => {
    const out: SettingEntry = {}
    for (const field of fields) {
      const value = row[field.key]
      if (field.secret && value === secretMask) {
        // Kept as the mask so the core leaves the stored value alone rather than
        // storing a row of asterisks over somebody's kubeconfig.
        out[field.key] = secretMask
        continue
      }
      out[field.key] = fieldValue(field, value)
    }
    return out
  })
}

function fieldValue(field: SettingSpec, value: unknown): unknown {
  if (field.type === 'bool') return value === true
  if (field.type === 'int') {
    const parsed = Number.parseInt(asString(value), 10)
    return Number.isNaN(parsed) ? 0 : parsed
  }
  const text = asString(value)
  return field.secret && text === '' ? undefined : text
}

/**
 * Saves one row, and only that row.
 *
 * A row is saved by itself because a row is what somebody decided: two clusters in one
 * list are two clusters with two kubeconfigs, and a Save that took both would make
 * changing one's namespace a chance to rewrite the other's credential. It also means an
 * edit nobody finished stays on the screen instead of being either saved or lost with
 * somebody else's change.
 *
 * What is sent is what this scope decides about that row — see listSettable — so
 * saving one row never touches the rows above it or the rows beside it.
 */
async function saveRow(spec: SettingSpec, row: SettingEntry) {
  if (isList(spec)) {
    // The whole list, every row this scope has an answer about, and not just the row
    // under the hand.
    //
    // One scope stores one list, so saving a list replaces it. Sending only the row that
    // was edited would drop every other row this scope has decided — which is how "save
    // one cluster" quietly deleted the cluster beside it. Each row still saves itself:
    // what is sent for a row is only what this scope decided about that row, and a card
    // about one place has the rest of the list already loaded to send with it.
    if (whereIs(spec, row) < 0) return

    const value = listSettable(spec, values.value[spec.key])
    await saveOne(spec.key, value === undefined ? [] : value)
    await settleRow(spec, row)
    emit('saved')
    return
  }
  const value = settingValue(spec, values.value[spec.key])
  if (value === undefined) return
  await saveOne(spec.key, value)
  settleSetting(spec)
}

/** The same for a setting that is one value rather than a list of rows. */
async function saveSetting(spec: SettingSpec) {
  if (isList(spec)) return
  await saveRow(spec, values.value[spec.key] as SettingEntry)
}

/** One setting at one scope. */
async function saveOne(key: string, value: unknown) {
  busy.value = true
  error.value = ''
  try {
    await api.put(`/modules/${props.module.id}/settings/bulk?${scopeQuery.value}`, {
      values: { [key]: value },
    })
    notify(`Saved ${key} for ${props.module.name}`, { type: 'success' })
  } catch (caught) {
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    busy.value = false
  }
}

/**
 * What a row looks like now that it has been saved.
 *
 * Read back rather than assumed: what is stored here is only what this scope decided,
 * and the row on screen is that merged with everything above it. The merge is done by
 * the core, so the row that comes back is the one to believe.
 *
 * Only this row is settled. Reloading the whole form would throw away the edits
 * somebody has half-made in the rows beside it.
 */
async function settleRow(spec: SettingSpec, row: SettingEntry) {
  const index = whereIs(spec, row)
  if (index < 0) return
  try {
    const answer = await api.get<{ own?: Record<string, unknown> }>(
      `/modules/${props.module.id}/settings?${scopeQuery.value}`,
    )
    const arrived = entriesOf(answer.own?.[spec.key])[index]
    const current = entriesOf(values.value[spec.key])
    if (!arrived || !current[index]) return
    current[index] = { ...arrived }
    values.value = { ...values.value, [spec.key]: current }
    const baseline = entriesOf(saved.value[spec.key])
    if (baseline[index]) baseline[index] = clone(arrived)
    saved.value = { ...saved.value, [spec.key]: baseline }
  } catch {
    // A row that could not be read back is left dirty rather than marked saved: it is
    // better to offer a save twice than to say it landed when nobody has said so.
  }
}

/** The same for a setting that is not a list. */
function settleSetting(spec: SettingSpec) {
  saved.value = { ...saved.value, [spec.key]: clone(values.value[spec.key]) }
}

/** Whether one row of a list is different from what is stored. */
/**
 * Whether one row is different from what is stored — including a row that has gone.
 *
 * A row that was removed is the last row's business: nothing else on the page is
 * different, and comparing only what is on screen with what is stored cannot see a row
 * that is no longer on screen at all. Without this, Remove removed the row from the form
 * and left the Save button grey, which is a deletion that silently does nothing.
 */
function rowChanged(spec: SettingSpec, row: SettingEntry): boolean {
  const index = whereIs(spec, row)
  if (index < 0) return false
  const before = entriesOf(saved.value[spec.key])[index]
  if (!before) return true
  return JSON.stringify(row) !== JSON.stringify(before)
}

/** Whether one setting is different from what is stored. */
function settingChanged(spec: SettingSpec): boolean {
  return JSON.stringify(values.value[spec.key]) !== JSON.stringify(saved.value[spec.key])
}

/** Puts one row back to what is stored, leaving the others as they are. */
function revertRow(spec: SettingSpec, row: SettingEntry) {
  const index = whereIs(spec, row)
  if (index < 0) return
  const current = entriesOf(values.value[spec.key])
  const was = entriesOf(saved.value[spec.key])[index]
  if (!was) return
  current[index] = clone(was)
  values.value = { ...values.value, [spec.key]: current }
}

/**
 * Where a row is in the whole list, asked of the row itself.
 *
 * The page may be showing one row of many — a card about one place shows that place's row
 * and no other — and a number taken from the loop over what is on screen is the number of
 * that place in a list it is not part of. Everything that reads or writes a row asks
 * where the row is, rather than being told.
 */
function whereIs(spec: SettingSpec, row: SettingEntry): number {
  return entriesOf(values.value[spec.key]).indexOf(row)
}

/** Puts one setting back to what is stored. */
function revertSetting(spec: SettingSpec) {
  values.value = { ...values.value, [spec.key]: clone(saved.value[spec.key]) }
}

/** Puts this scope back to inheriting, which is not the same as a default. */
async function reset(spec: SettingSpec) {
  busy.value = true
  error.value = ''
  try {
    await api.del(
      `/modules/${props.module.id}/settings?key=${encodeURIComponent(spec.key)}&${scopeQuery.value}`,
    )
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = false
  }
}

/**
 * What a field shows.
 *
 * Whatever this row holds, and a field the row does not hold is empty. The name of a
 * place is no exception: a name this scope has not decided is not written into the field,
 * so an empty field means the same thing here as anywhere else — the value comes from the
 * level above — and what the row is called is said once, in the row's own heading, where
 * it can be read without being saved back.
 */
function shownValue(spec: SettingSpec, row: SettingEntry, key: string): string {
  return asString(row[key])
}

/**
 * Whether a field of this row is not this scope's to say.
 *
 * On the field itself, from the row and the key, so every control asks the same question
 * the same way. A field the row does not carry is one nobody here has decided — the name
 * of a place included, which is why clearing it says "no name of my own" rather than
 * "a place called nothing", and the field above says so where it can be read.
 */
function inheritedNow(spec: SettingSpec, row: SettingEntry, key: string): boolean {
  // Nothing is above the instance, so a field the instance has not set is simply unset
  // there — and saying "inherited" on the top of the chain would point at nothing.
  if (props.scope === 'instance') return false
  return !(key in row)
}

/**
 * What the level above decided for one field of this row.
 *
 * Shown, and not written. A page that shows nothing where a place is configured says
 * nothing, and a reader who cannot see the value has no way to tell an unset field from one
 * that is already right — which is how a place ends up given a namespace it did not need.
 *
 * Shown as a placeholder rather than as the field's value, because that is what it is: a
 * value this scope has not decided, which typing over replaces and leaving alone keeps.
 * Nothing here goes into what a save sends — that is still built from what this scope
 * decided, so reading a value in cannot turn into writing it down.
 */
function inheritedOf(spec: SettingSpec, row: SettingEntry, key: string): string {
  if (props.scope === 'instance') return ''
  if (key in row) return ''
  return asString(inheritedField(spec, row, key))
}

/** The row the level above holds that this row is, and the field of it that was asked for. */
function inheritedField(spec: SettingSpec, row: SettingEntry, key: string): unknown {
  const above = entriesOf(inheritedValues.value[spec.key])
  const id = typeof row.dogit_row_id === 'string' ? row.dogit_row_id : ''
  const name = asString(row.dogit_row_name || '').trim() ||
    asString(row[spec.items?.identify?.[0] ?? '']).trim()

  for (const one of above) {
    const theirId = typeof one.dogit_row_id === 'string' ? one.dogit_row_id : ''
    if (id && theirId && id === theirId) return one[key]
    if (name && asString(one[spec.items?.identify?.[0] ?? 'name'] ?? '').trim() === name) {
      return one[key]
    }
  }
  return undefined
}

/**
 * Whether a registry field is showing what came from above.
 *
 * A field the level above decided and this scope has not is drawn in the dropdown as the
 * address it is, marked as inherited, and picked — so the reader sees where the images come
 * from without the form pretending this scope decided it.
 */
function registryInherited(spec: SettingSpec, row: SettingEntry, field: SettingSpec): string {
  return inheritedOf(spec, row, field.key)
}

function setField(spec: SettingSpec, row: SettingEntry, key: string, event: Event) {
  const target = event.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  row[key] = target.value
}

/**
 * A switch nobody here has decided.
 *
 * It shows neither position, because neither is true: the value is whatever the level
 * above holds, and this page has not been told what that is. The first click is the
 * decision, and it is "on"; the second is "off".
 */
function toggleBool(row: SettingEntry, key: string) {
  row[key] = inheritedNowBool(row, key) ? true : !row[key]
}

function inheritedNowBool(row: SettingEntry, key: string): boolean {
  return !(key in row)
}

/**
 * What the levels above decided, kept apart from what this scope decided.
 *
 * Two maps and not one on purpose: a value written into `values` is a decision, and what a
 * save sends is built from `values`. Mixing the two would let a reader who changed nothing
 * save somebody else's answer as their own.
 */
const inheritedValues = ref<Record<string, unknown>>({})

/** What the core sends back for a secret it holds rather than returns. */
const secretMask = '********'

/**
 * A new, empty row.
 *
 * Every declared field present with a blank value, so the row renders as a form
 * rather than growing fields one keystroke at a time as each is first typed into.
 *
 * The one field that does not start blank is the registry, which starts as whatever the
 * module's own registry setting says — found by its type rather than by its name, because
 * this form reads a manifest and does not know what any module calls its settings. It is a
 * value for a row being created and nothing else: no deployment reads it, so changing it
 * moves no place, and a place that names a registry keeps that one.
 */
function newEntry(spec: SettingSpec): SettingEntry {
  const row: SettingEntry = {}
  for (const field of spec.items?.fields ?? []) {
    if (field.type === 'bool') {
      row[field.key] = false
      continue
    }
    row[field.key] = field.type === 'registry' ? newRowRegistry() : ''
  }
  return row
}

/** What a module's own registry setting is set to, or nothing when it declares none. */
function newRowRegistry(): string {
  for (const spec of props.module.manifest?.settings ?? []) {
    if (spec.type !== 'registry') continue
    return asString(values.value[spec.key]).trim()
  }
  return ''
}

/**
 * Adds a row.
 *
 * Allowed on a place's own Settings tab as well as on the module's: a project may name a
 * cluster the instance has not got one for, and refusing to let it write one here would
 * mean the only place to add a place is the module's page — which is the arrangement the
 * rows were meant to end. A new row is this scope's own, so it is the one row on a
 * place's tab that can be deleted again.
 */
function addEntry(spec: SettingSpec) {
  const rows = entriesOf(values.value[spec.key])
  values.value[spec.key] = [...rows, newEntry(spec)]
}

/** Said when a row is saved, so the page above can put a new place on screen. */
const emit = defineEmits<{ saved: [] }>()

/**
 * Removes a row.
 *
 * On the last row, rather than leaving one behind as a blank: a settings page that
 * always has one empty row is a page where the empty row looks like a value, and
 * somebody reads an entry that was never filled in.
 *
 * A row that is not this scope's is not removable, and the caller does not offer it.
 * It was written by somebody else — the instance, or a group — and a Remove button on
 * it would either silently do nothing (a row left out of this scope's list is simply
 * inherited again) or, worse, delete somebody else's cluster by saving a shorter list.
 * The only thing a scope below the writer's may say about such a row is that it is not
 * in use here, and that is a switch rather than a deletion: the row stays written down
 * with its kubeconfig, and switching it back on brings it back.
 */
async function removeEntry(spec: SettingSpec, row: SettingEntry) {
  const rows = entriesOf(values.value[spec.key])
  const at = rows.indexOf(row)
  if (at < 0 || busy.value) return
  const title = entryTitle(spec, row)

  // Removed first on the page, and written down at once.
  //
  // A Remove that waits for a Save is a button that deletes a row from the form and
  // leaves the stored row where it is, and the Save it is waiting for is on the row that
  // was just deleted — so the deletion could never be saved from here at all. Same bargain
  // as the switches: a thing you do, not a thing you fill in and confirm.
  const before = rows.slice()
  rows.splice(at, 1)
  values.value = { ...values.value, [spec.key]: rows }
  busy.value = true
  error.value = ''
  try {
    const value = listSettable(spec, rows)
    await saveOne(spec.key, value === undefined ? [] : value)
    await load()
    notify(`${title} is no longer a row of this form`, { type: 'success', timer: 5 })
    emit('saved')
  } catch (caught) {
    values.value = { ...values.value, [spec.key]: before }
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    busy.value = false
  }
}

/**
 * Whether a row is one this scope wrote, and may therefore delete.
 *
 * A row that came from above has nothing here to delete: what this scope has to say
 * about it is whether it is in use, and everything else belongs to whoever wrote it.
 */
function rowIsOwn(spec: SettingSpec, row: SettingEntry): boolean {
  if (!spec.items?.identify?.length) return true
  // The core says which rows were written here, and it is the only thing that knows: a
  // row that decided nothing at this level — a cluster the instance configured, or a group
  // — arrives carrying nothing but its name, exactly like a row somebody added and left
  // alone. Guessing from the fields a row happens to carry is what put a Remove on an
  // inherited row and took it off an administrator's own cluster.
  if (row.dogit_row_own === true) return true
  const index = entriesOf(values.value[spec.key]).indexOf(row)
  if (index < 0) return false
  // And a row that was not on the page when it loaded is this scope's own: it is the one
  // somebody added just now.
  return !entriesOf(saved.value[spec.key])[index]
}

/** Whether a row is in use here. A row nobody said anything about is in use. */
function rowInUse(row: SettingEntry): boolean {
  return row.enabled !== false
}

/**
 * Whether a push may deploy to this row by itself. A row nobody said anything about
 * deploys by itself: the switch exists for somebody who wants a place to stop moving on
 * its own, and a row nobody has touched has never been asked to.
 */
function rowAutoDeploy(row: SettingEntry): boolean {
  return row.auto_deploy !== false
}

/**
 * What these two switches mean, in a tooltip, because two switches with two letters are
 * not enough to tell them apart and guessing is worse than reading.
 */
const inheritedNote = [
  'Autodeploy: whether a push or a tag may deploy to this place by itself.',
  'Off means the run still happens and the image is still built — only the',
  'deployment to this place waits for somebody to start it by hand.',
  'In use: whether this project may deploy here at all.',
].join(' ')

/**
 * The rows of a list setting, for the template to walk.
 *
 * One row when the page was asked about one place by name, and that row is looked up by
 * the same field the core uses to join a row to the one it was inherited from. The other
 * rows are not hidden here but absent: a card about a place has nothing to do with the
 * other places.
 */
function rowsOf(spec: SettingSpec): SettingEntry[] {
  const rows = entriesOf(values.value[spec.key])
  // Nothing to look a row up by: a list whose entries have no names is one list, and the
  // first row of it is as good a match as any.
  if (!props.onlyRow || !(spec.items?.identify?.length)) return rows
  // The row this page is about is remembered by where it was, not by what it is called.
  //
  // A name is what the page was asked about, so it is also the one thing that changes:
  // write a new one and the row no longer answers to the old, and a form that looked the
  // row up by name would show nothing at all — the row would disappear the moment it
  // was saved, which reads as a save that did nothing.
  const at = rows.findIndex((row) => rowName(spec, row) === props.onlyRow)
  const row = at >= 0 ? rows[at] : rows[0]
  return row ? [row] : []
}

/** What a row is called in the list: its first field, or "entry". */
function entryTitle(spec: SettingSpec, row: SettingEntry): string {
  for (const field of spec.items?.fields ?? []) {
    // A switch is not a name: a row nobody has named yet would otherwise be titled
    // "false", which is a thing a reader has to stop and think about.
    if (field.type === 'bool') continue
    const text = asString(row[field.key]).trim()
    if (text) return text
  }
  // Nothing here has a name of its own, so the row is titled by what the core says it is
  // called — the name it has where it was written, which is not a value to save here.
  const label = asString(row.dogit_row_name).trim()
  if (label) return label
  return 'a new row'
}

onMounted(() => {
  void load()
  void loadRegistries()
})
watch(() => [props.module.id, props.scope, props.scopeId], load)

/**
 * The registries this instance knows about, for a field that picks one.
 *
 * Fetched once, and only when the module actually declares such a field: the list is the
 * administrator's own address book, and a settings form for a module that asks for no
 * registry has no business asking for one. A fetch that fails, or an instance with nothing
 * written down yet, leaves the field a plain text input — still usable, because what a
 * registry needs is text and a mirror nobody wrote down is a legitimate thing to name.
 */
const registries = ref<DockerRegistry[]>([])

async function loadRegistries() {
  if (!declaresRegistryField(props.module.manifest?.settings ?? [])) return
  try {
    const answer = await api.get<DockerRegistryPage>('/registry/docker', { page: 1 })
    registries.value = answer.registries
  } catch {
    // Nothing said: the field falls back to text in the template, which is the honest
    // thing when the list could not be read, rather than an empty dropdown that looks
    // like an answer.
  }
}

/** Whether any of these settings, or any field of one of them, is a registry. */
function declaresRegistryField(specs: SettingSpec[]): boolean {
  return specs.some(
    (spec) => spec.type === 'registry' ||
      (spec.items?.fields ?? []).some((field) => field.type === 'registry'),
  )
}

/** What a text field shows when it holds nothing: the value from above, if there is one. */
function inheritedPlaceholder(spec: SettingSpec, row: SettingEntry, field: SettingSpec): string {
  const fromAbove = inheritedOf(spec, row, field.key)
  if (fromAbove) return `${fromAbove} — decided above`
  if (inheritedNow(spec, row, field.key)) return 'inherited'
  return field.default !== undefined ? String(field.default) : ''
}

/**
 * What a registry field offers, in the order a reader wants them.
 *
 * Empty first, and it says plainly that nothing is chosen: an empty row is a row a
 * deployment is refused over, so an option that read like a default would be promising
 * something the field cannot deliver. Then the ones on the list of registries — the module's
 * own among them, which is a registry like any other and belongs in the same list rather than
 * named twice somewhere else — and then "another address", for the address somebody knows and
 * has not written down yet. That last one is how a registry gets added to the list by the
 * people who use it: they type it here, and a deployment says which one is missing.
 *
 * An address already saved that is not on the list stays in the list, marked as what it is.
 * Dropping it would make the field show something else than the value being saved, and
 * saving that would quietly change where the images come from.
 */
const otherRegistryChoice = 'dogit:another-registry'

/**
 * How two addresses are compared, which is the way the core compares them too.
 *
 * A scheme and a trailing slash are how a person writes an address, and neither is part of
 * the host a credential is filed under or an image name carries. Compared as text, a value
 * saved as "https://harbor.example.com/" is not on a list holding "harbor.example.com" —
 * and the field then falls back to its first option and says a place is unchosen when it
 * names a registry quite happily.
 */
function registryAddress(address: string): string {
  return asString(address).trim().replace(/^https?:\/\//i, '').replace(/\/+$/, '')
}

/** Whether a saved value names one of the options, spelled the way the option is. */
function registrySpelling(options: { value: string; label: string }[], value: string): string {
  const wanted = registryAddress(value)
  if (wanted === '') return ''
  const match = options.find((option) => registryAddress(option.value) === wanted)
  return match?.value ?? value
}

function registryOptions(value: string, fromAbove = ''): { value: string; label: string }[] {
  const chosen = value.trim()
  const options = [
    { value: '', label: '— not chosen —' },
    ...registries.value.map((reg) => ({
      value: reg.url,
      label: reg.name ? `${reg.name} — ${reg.url}` : reg.url,
    })),
  ]

  // What this place pulls from, decided above. Offered before everything else and marked,
  // because it is the answer the deployment runs with right now: a reader who opens this
  // form has to be able to see it without going to the module's page to find out.
  const inherited = fromAbove.trim()
  if (inherited !== '') {
    options.unshift({ value: inherited, label: `${inherited} — decided above` })
  }

  const known = options.some((option) => registryAddress(option.value) === registryAddress(chosen))
  if (chosen !== '' && !known) {
    options.push({
      value: chosen,
      label: `${chosen} (not in the list of registries — deployments are refused)`,
    })
  }
  options.push({ value: otherRegistryChoice, label: 'another address…' })
  return options
}



/**
 * Which registry fields are showing the "type an address" box.
 *
 * Kept apart from the values themselves on purpose. The address lives in the row, where it
 * is saved and inherited like every other value; whether the box is open is a thing about
 * this browser's view of the form and nothing else. Folding the marker into the value would
 * make the field change the instant a character was typed — the first letter would replace
 * the marker and close the box under the reader's cursor.
 */
const typingRegistry = ref<Record<string, boolean>>({})

function registryKey(spec: SettingSpec, index: number | string, field: SettingSpec): string {
  return `${spec.key}-${index}-${field.key}`
}

function registryTyping(spec: SettingSpec, index: number | string, field: SettingSpec): boolean {
  return typingRegistry.value[registryKey(spec, index, field)] === true
}

/** What the dropdown shows: the address being typed, or whatever the row holds. */
function registryShown(spec: SettingSpec, row: SettingEntry, index: number, field: SettingSpec): string {
  if (registryTyping(spec, index, field)) return otherRegistryChoice
  const own = asString(row[field.key]).trim()
  if (own !== '') return registrySpelling(registryOptions(own), own)
  return inheritedOf(spec, row, field.key) || own
}

/** The same open-box state for a registry kept at the top level rather than per place. */
function plainRegistryTyping(spec: SettingSpec): boolean {
  return typingRegistry.value[`${spec.key}-top`] === true
}

function choosePlainRegistry(spec: SettingSpec, event: Event) {
  const chosen = (event.target as HTMLSelectElement).value
  const key = `${spec.key}-top`
  if (chosen === otherRegistryChoice) {
    typingRegistry.value = { ...typingRegistry.value, [key]: true }
    return
  }
  const stillTyping = { ...typingRegistry.value }
  delete stillTyping[key]
  typingRegistry.value = stillTyping
  values.value[spec.key] = chosen
}

function chooseRegistry(spec: SettingSpec, row: SettingEntry, index: number,
	field: SettingSpec, event: Event) {
  const chosen = (event.target as HTMLSelectElement).value
  if (chosen === otherRegistryChoice) {
    typingRegistry.value = { ...typingRegistry.value, [registryKey(spec, index, field)]: true }
    return
  }
  const key = registryKey(spec, index, field)
  const stillTyping = { ...typingRegistry.value }
  delete stillTyping[key]
  typingRegistry.value = stillTyping

  // Picking the address that came from above is picking "not here": the form must not turn
  // a reader reading the current value into a decision they did not mean to make, and a row
  // that saves an answer equal to the one above is a row that has decided nothing and says
  // it has.
  const fromAbove = inheritedOf(spec, row, field.key)
  const picked = (event.target as HTMLSelectElement).value
  if (fromAbove !== '' && picked === fromAbove.trim() && !(field.key in row)) {
    row[field.key] = ''
    return
  }
  setField(spec, row, field.key, event)
}
</script>

<template>
  <div>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="!module.manifest?.settings?.length" class="muted small">
      This module declared no settings.
    </div>

    <div v-else>
      <p v-if="note" class="muted small">{{ note }}</p>

      <div v-for="spec in module.manifest.settings" :key="spec.key" class="setting-row">
        <div class="setting-label">
          <div>{{ spec.label }}</div>
          <div v-if="spec.description" class="muted small">{{ spec.description }}</div>
        </div>

        <!-- A list is its own block rather than a control in the two-column row:
             one row of a list is already a group of values, and putting that inside a
             260px column would be unreadable. -->
        <div v-if="spec.type === 'list'" class="setting-list" :class="{ bare: onlyRow }">
          <div v-for="(row, index) in rowsOf(spec)" :key="index" class="setting-entry">
            <!-- No heading of its own on a place's tab: the row that names the place is
                 the one with the switches, a screen's width above this, and a second
                 heading saying the same name is the same fact twice. -->
            <div v-if="!onlyRow" class="setting-entry-head">
              <span class="setting-entry-name">{{ entryTitle(spec, row) }}</span>
              <!-- Which of these rows are this scope's own. Without it a page of
                   inherited rows looks like a page of things decided here, and the
                   first save writes all of them down as if they had been. -->
              <span
                v-if="spec.items?.identify?.length && scope !== 'instance'"
                class="setting-entry-origin"
                :class="{ ours: rowIsOurs(spec, row) }"
              >
                {{ rowIsOurs(spec, row) ? 'changed here' : 'inherited' }}
              </span>
              <!-- Two switches, and both are answers about this row alone.
                   "In use" asks whether this project may deploy here at all; "Autodeploy"
                   asks whether a push may do it by itself. They are different questions:
                   the first takes the place away, the second leaves it here for somebody
                   to deploy to on purpose. -->
              <div
                v-if="spec.items?.identify?.length && !onlyRow"
                class="row-switches"
                :title="inheritedNote"
              >
              <!-- The name sits after the switch, as it does everywhere else on the
                   page: a switch with a name on its left and one with a name on its
                   right are two rows that read as one, and a name is found in the same
                   place on every switch or in none. -->
              <label class="row-switch">
                <button
                  class="switch"
                  :class="{ on: rowAutoDeploy(row) }"
                  type="button"
                  role="switch"
                  :aria-checked="rowAutoDeploy(row)"
                  :disabled="busy"
                  @click="row.auto_deploy = !rowAutoDeploy(row)"
                >
                  <span class="knob" />
                </button>
                <span class="row-switch-name">Autodeploy</span>
              </label>
              <label class="row-switch">
                <button
                  class="switch"
                  :class="{ on: rowInUse(row) }"
                  type="button"
                  role="switch"
                  :aria-checked="rowInUse(row)"
                  :disabled="busy"
                  @click="row.enabled = !rowInUse(row)"
                >
                  <span class="knob" />
                </button>
                <span class="row-switch-name">In use</span>
              </label>
              </div>
              <button
                v-if="rowIsOwn(spec, row) || !spec.items?.identify?.length"
                class="link-button row-remove"
                type="button"
                :disabled="busy || !mayEdit"
                @click="removeEntry(spec, row)"
              >
                Remove
              </button>
              <span v-else-if="!onlyRow" class="row-inherited-note">
                written above — edit it there, or switch it off here
              </span>
            </div>

            <!-- This row's own Save, below its own fields: a row is saved by itself, so
                 changing one's namespace is never also a chance to rewrite the other's
                 kubeconfig. It is here, at the end, and not under the row's name where
                 it would be a button above the thing it acts on. -->


            <div
              v-for="field in visibleFieldsOf(spec)"
              :key="field.key"
              class="setting-field"
            >
              <!-- Label and description on separate lines: run together they read as
                   one sentence about a field that has two unrelated things said
                   about it, and the reader cannot tell where the name stops. -->
              <label :for="`${spec.key}-${index}-${field.key}`">
                <span class="field-name">{{ field.label }}</span>
                <span v-if="field.description" class="muted small field-hint">
                  {{ field.description }}
                </span>
              </label>
              <button
                v-if="field.type === 'bool'"
                class="switch"
                :class="{ on: row[field.key] === true, undecided: inheritedNow(spec, row, field.key) }"
                type="button"
                role="switch"
                :aria-checked="row[field.key] === true"
                :disabled="!mayEdit"
                :title="inheritedNow(spec, row, field.key)
                  ? 'not decided here — switch it on to decide it here'
                  : (row[field.key] === true ? 'On' : 'Off')"
                @click="toggleBool(row, field.key)"
              >
                <span class="knob" />
              </button>
              <select
                v-else-if="field.type === 'enum'"
                :disabled="fieldLocked(spec, row, index, field.key)"
                :id="`${spec.key}-${index}-${field.key}`"
                :value="shownValue(spec, row, field.key)"
                @change="setField(spec, row, field.key, $event)"
              >
                <option v-for="option in field.options ?? []" :key="option" :value="option">
                  {{ option }}
                </option>
              </select>
              <!-- A registry is picked rather than typed, because an address typed here
                   is an address that has to be right: one character out is a registry
                   nobody has, which pulls anonymously and fails on the rollout. The list
                   is this instance's own; the last option is always "type one", for the
                   mirror that was never written down. -->
              <template v-else-if="field.type === 'registry'">
                <select
                  v-if="registries.length > 0"
                  :disabled="fieldLocked(spec, row, index, field.key)"
                  :id="`${spec.key}-${index}-${field.key}`"
                  :value="registryShown(spec, row, index, field)"
                  @change="chooseRegistry(spec, row, index, field, $event)"
                >
                  <option
                    v-for="option in registryOptions(asString(row[field.key]), inheritedOf(spec, row, field.key))"
                    :key="option.value"
                    :value="option.value"
                  >
                    {{ option.label }}
                  </option>
                </select>
                <!-- Open while it is being typed, and closed again the moment an address
                     is chosen from the list: the box is here for the address, and leaving
                     it open under a select that already names one would be two answers to
                     one question. -->
                <input
                  v-if="registryTyping(spec, index, field)"
                  :id="`${spec.key}-${index}-${field.key}-address`"
                  :value="asString(row[field.key])"
                  :disabled="fieldLocked(spec, row, index, field.key)"
                  placeholder="harbor.example.com, or mirror.example.com"
                  @input="setField(spec, row, field.key, $event)"
                />
              </template>
              <!-- A document, not a word: a kubeconfig pasted into a one-line box
                   loses its newlines and stops being a kubeconfig at all. -->
              <textarea
                v-else-if="field.type === 'text'"
                :disabled="fieldLocked(spec, row, index, field.key)"
                :id="`${spec.key}-${index}-${field.key}`"
                :value="shownValue(spec, row, field.key)"
                @input="setField(spec, row, field.key, $event)"
                rows="6"
                spellcheck="false"
                :placeholder="inheritedNow(spec, row, field.key) ? 'inherited' : `Paste the file's contents here`"
              />
              <input
                v-else
                :disabled="fieldLocked(spec, row, index, field.key)"
                :id="`${spec.key}-${index}-${field.key}`"
                :value="shownValue(spec, row, field.key)"
                @input="setField(spec, row, field.key, $event)"
                :type="field.secret ? 'password' : 'text'"
                :title="inheritedNow(spec, row, field.key)
                  ? 'decided above, not here — type to decide it here'
                  : ''"
                :placeholder="inheritedPlaceholder(spec, row, field)"
              />
            </div>
            <div class="setting-entry-actions">
              <!-- The hint takes its room whether or not it has anything to say, and the
                   buttons keep their width while they are disabled: a row that changes
                   shape when a switch is flicked makes the switch feel like it moved
                   something. -->
              <span class="muted small pending" :class="{ show: rowChanged(spec, row) }">
                not saved yet
              </span>
              <!-- Where a row this scope wrote is removed from, when the row's heading
                   — which is where it is removed from everywhere else — is the place's
                   own heading above. -->
              <button
                v-if="onlyRow && (rowIsOwn(spec, row) || !spec.items?.identify?.length)"
                class="link-button row-remove"
                type="button"
                :disabled="busy || !mayEdit"
                @click="removeEntry(spec, row)"
              >
                Remove this place
              </button>
              <button
                class="btn btn-small"
                type="button"
                :disabled="busy || !mayEdit || !rowChanged(spec, row)"
                @click="revertRow(spec, row)"
              >
                Discard
              </button>
              <button
                class="btn btn-small btn-primary"
                type="button"
                :disabled="busy || !mayEdit || !rowChanged(spec, row)"
                @click="saveRow(spec, row)"
              >
                {{ busy ? 'Saving…' : 'Save' }}
              </button>
            </div>
          </div>

          <!-- Adding is not here: on a place's tab the rows are that one place, and the
               button that adds a place belongs below the list of places, where the list
               it adds to is. -->
          <button
            v-if="mayEdit && !onlyRow"
            class="btn btn-small"
            type="button"
            :disabled="busy"
            @click="addEntry(spec)"
          >
            {{ spec.items?.add_label ?? `Add ${spec.label.toLowerCase()}` }}
          </button>
        </div>

        <div v-else class="setting-control">
          <select v-if="spec.type === 'enum'" v-model="values[spec.key]">
            <option v-for="option in spec.options" :key="option" :value="option">
              {{ option }}
            </option>
          </select>
          <button
            v-else-if="spec.type === 'bool'"
            class="switch"
            :class="{ on: values[spec.key] === 'true' }"
            type="button"
            role="switch"
            :aria-checked="values[spec.key] === 'true'"
            :title="values[spec.key] === 'true' ? 'On' : 'Off'"
            @click="values[spec.key] = values[spec.key] === 'true' ? 'false' : 'true'"
          >
            <span class="knob" />
          </button>
          <textarea
            v-else-if="spec.type === 'text'"
            v-model="values[spec.key] as string"
            rows="6"
            spellcheck="false"
          />
          <!-- The same picker as inside a row, for a module that keeps its registry at the
               top level rather than per place. One control and one fallback, because two
               versions of "which registry" is one of them quietly out of step. -->
          <template v-else-if="spec.type === 'registry'">
            <select
              v-if="registries.length > 0"
              v-model="values[spec.key] as string"
              @change="choosePlainRegistry(spec, $event)"
            >
              <option
                v-for="option in registryOptions(String(values[spec.key] ?? ''))"
                :key="option.value"
                :value="option.value"
              >
                {{ option.label }}
              </option>
            </select>
            <input
              v-if="plainRegistryTyping(spec)"
              v-model="values[spec.key] as string"
              placeholder="harbor.example.com, or mirror.example.com"
            />
          </template>
          <input
            v-else
            v-model="values[spec.key]"
            :type="spec.secret ? 'password' : 'text'"
            :placeholder="spec.default !== undefined ? String(spec.default) : ''"
          />
        </div>

        <!-- Only said when a value has actually been set here: somebody looking at
             a form that matches the default needs nothing read out to them. -->
        <p
          v-if="
            spec.type !== 'list' &&
            spec.default !== undefined &&
            module.settings?.[spec.key] !== undefined
          "
          class="setting-hint"
        >
          Set to <span class="mono">{{ String(spec.default) }}</span>
          <template v-if="String(spec.default) !== String(module.settings?.[spec.key])">
            instead of the default.
          </template>
          <button class="link-button" type="button" :disabled="busy" @click="reset(spec)">
            {{ scope === 'instance' ? 'Reset to the default' : 'Use the inherited value' }}
          </button>
        </p>

        <!-- The same bargain as a row of a list: this setting saves itself. -->
        <div v-if="spec.type !== 'list'" class="setting-entry-actions">
          <span v-if="settingChanged(spec)" class="muted small">not saved yet</span>
          <button
            class="btn btn-small"
            type="button"
            :disabled="busy || !settingChanged(spec)"
            @click="revertSetting(spec)"
          >
            Discard
          </button>
          <button
            class="btn btn-small btn-primary"
            type="button"
            :disabled="busy || !settingChanged(spec)"
            @click="saveSetting(spec)"
          >
            {{ busy ? 'Saving…' : 'Save' }}
          </button>
        </div>
      </div>

    </div>
  </div>
</template>

<style scoped>
/* Two columns that hold their width.
   A description is prose: given any space at all, it wraps to one word per line,
   which is what a flex row will do to it when the control beside it takes a fixed
   width and the label is left with whatever is left over. The label therefore has a
   floor of its own rather than whatever remains. */
.setting-row {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: 8px 16px;
  padding: 14px 0;
  border-bottom: 1px solid var(--border);
}

/* The first row needs no room above it: the card's own padding already separates it
   from the note above, and padding twice reads as though the content were floating. */
.setting-row:first-of-type {
  padding-top: 4px;
}

.setting-label {
  flex: 1 1 320px;
  min-width: 240px;
}

.setting-control {
  width: 260px;
  flex: 0 0 260px;
}

.setting-control input,
.setting-control select,
.setting-control textarea {
  width: 100%;
  padding: 6px 8px;
  font: inherit;
  font-size: 13px;
  color: inherit;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
}

/* A list setting takes the full width: the row above it is a label and a column of
   controls, and a list of rows would have to be squeezed into that column. */
.setting-control textarea {
  font-family: var(--mono);
  font-size: 12px;
  resize: vertical;
}

.setting-list {
  flex: 1 1 100%;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 12px;
  margin-top: 4px;
}

.setting-entry {
  padding: 12px 14px 14px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-inset);
}

/* On a place's tab the fields are that place's own settings, one level up from here and
   already inside the place's block: a second box inside a box draws a line round a thing
   that is one thing, and the row above it already says which thing. */
.setting-list.bare .setting-entry {
  padding: 0;
  border: none;
  border-radius: 0;
  background: none;
}

.setting-list.bare .setting-entry-actions {
  border-top: none;
  padding-top: 0;
  margin-top: 12px;
}

.setting-entry-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border);
}

/* The name a row is known by: which cluster, which mirror, which feed. */
.setting-entry-name {
  font-weight: 600;
}

/*
 * Where a row comes from. Two words, and the difference between them is the whole
 * point of inheritance: an inherited row is somebody else's decision that this scope
 * has not touched, and a row marked as changed here is this scope's own.
 */
.setting-entry-origin {
  padding: 1px 8px;
  border: 1px solid var(--border);
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
  color: var(--text-muted);
  white-space: nowrap;
}

.setting-entry-origin.ours {
  border-color: var(--accent);
  color: var(--accent);
}

/* A row's own fates: remove it if this scope wrote it, switch it off if not. */
/* A row's own answers, in one group on the right: two switches and, for a row this
   scope wrote, its own Remove. They are laid out as a group so that the switches line up
   with each other and the row does not read as a sentence. */
.row-switches {
  display: flex;
  align-items: center;
  gap: 6px 18px;
  margin-left: auto;
}

.row-switch,
.row-remove {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  /* Labels carry a bottom margin from the page's form styles; a switch's label is not a
     form control's margin, and six pixels of it lifts the switch off its own row. */
  margin-bottom: 0;
}

.row-switch-name {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
  /* The width of the longest of these words, so that switching one on and off does not
     narrow the row it is in. */
  min-width: 72px;
}

/* Why an inherited row has no Remove button, said once where it is seen. */
.row-inherited-note {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
}

/* A row's own Save, under its own fields. */

.setting-entry-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  /* Buttons keep their width when disabled, so a row does not jump as it is saved. */
  min-height: 30px;
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid var(--border);
}

.setting-entry-actions .pending {
  margin-right: auto;
  /* Hidden rather than removed: a row's buttons sit still while a switch is flicked,
     instead of sliding one button-width to the left and back. */
  visibility: hidden;
}

.setting-entry-actions .pending.show {
  visibility: visible;
}

/* A switch nobody here has decided: shown with its knob in the middle, because it is
   in neither position — the value belongs to the level above and this page has not been
   told what it is. */
.switch.undecided .knob {
  left: 50%;
  transform: translateX(-50%);
  opacity: 0.6;
}

.setting-field {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 16px;
  padding: 7px 0;
}

.setting-field + /* A switch nobody here has decided: shown with its knob in the middle, because it is
   in neither position — the value belongs to the level above and this page has not been
   told what it is. */
.switch.undecided .knob {
  left: 50%;
  transform: translateX(-50%);
  opacity: 0.6;
}

.setting-field {
  border-top: 1px solid var(--border);
}

.setting-field label {
  flex: 1 1 240px;
  min-width: 200px;
  font-size: 13px;
  line-height: 1.45;
}

.field-name {
  display: block;
}

.field-hint {
  display: block;
  margin-top: 1px;
}

.setting-field input,
.setting-field select,
.setting-field textarea {
  width: 100%;
  max-width: 340px;
  padding: 6px 8px;
  font: inherit;
  font-size: 13px;
  color: inherit;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
}

.setting-hint {
  flex-basis: 100%;
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}

.form-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--border);
}

.link-button {
  background: none;
  border: none;
  padding: 0 0 0 8px;
  color: var(--text-muted);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  text-decoration: underline;
}

.small {
  font-size: 12px;
}
</style>