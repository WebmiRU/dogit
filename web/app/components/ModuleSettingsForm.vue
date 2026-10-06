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
// The rows the levels above this scope decide, keyed by setting. Kept because a list
// that inherits entry by entry must not be written down whole at every level below:
// that would freeze somebody else's cluster in this project's settings the first time
// anybody opened this page and pressed Save.
const inherited = ref<Record<string, unknown>>({})
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
 * Fills the form from what the core says applies here.
 *
 * Read from "effective" rather than from "settings": the latter is a list of the rows
 * stored at this scope, each with its value wrapped in an object, which is not
 * something a form can read a value out of by key. Effective is what a job would
 * actually run with, which is also what should be shown.
 */
function fill(answer: {
  effective?: Record<string, unknown>
  inherited?: Record<string, unknown>
}) {
  const next: Record<string, unknown> = {}
  const above: Record<string, unknown> = {}
  for (const spec of props.module.manifest?.settings ?? []) {
    const stored = answer.effective?.[spec.key]
    next[spec.key] = isList(spec)
      ? entriesOf(stored)
      : asString(stored ?? spec.default ?? '')
    if (isList(spec)) above[spec.key] = entriesOf(answer.inherited?.[spec.key])
  }
  values.value = next
  // What the levels above this one decide, kept so that saving here does not write
  // their rows down again as if this scope had decided them.
  inherited.value = above
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
    if (!text) return ''
    parts.push(text)
  }
  return parts.join(' ')
}

/** Whether one row is worth writing at this scope at all. */
function rowIsOurs(spec: SettingSpec, row: SettingEntry): boolean {
  const above = entriesOf(inherited.value[spec.key])
  const name = rowName(spec, row)
  // A list the module does not name entries of cannot be told apart row by row, so
  // what is saved is what is on screen, as before.
  if (!name) return true
  const was = above.find((one) => rowName(spec, one) === name)
  if (!was) return true
  return JSON.stringify(orderFields(spec, was)) !== JSON.stringify(orderFields(spec, row))
}

/** A row with its fields in the module's own order, so two rows compare honestly. */
function orderFields(spec: SettingSpec, row: SettingEntry): SettingEntry {
  const out: SettingEntry = {}
  for (const field of spec.items?.fields ?? []) {
    if (field.key in row) out[field.key] = row[field.key]
  }
  for (const [key, value] of Object.entries(row)) {
    if (!(key in out)) out[key] = value
  }
  return out
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
function listSettable(spec: SettingSpec, raw: unknown): unknown {
  if (!isList(spec)) return settingValue(spec, raw)

  const rows: SettingEntry[] = []
  for (const row of entriesOf(raw)) {
    if (!rowIsOurs(spec, row)) continue
    const above = entriesOf(inherited.value[spec.key]).find(
      (one) => rowName(spec, one) === rowName(spec, row),
    )
    const own: SettingEntry = {}
    let kept = 0
    // The module's own fields, and then the two the core keeps in such a list: a
    // switch is not a field anybody declared, and without carrying it here a switched
    // row would be saved as nothing at all.
    const carry = [
      ...(spec.items?.fields ?? []).map((field) => field.key),
      ...coreRowFields(spec),
    ]
    for (const key of carry) {
      if (!(key in row)) continue
      if (above && sameValue(above[key], row[key])) continue
      own[key] = row[key]
      kept++
    }
    // A row whose name is what makes it a row at all: without it nothing above could
    // ever be matched to it.
    for (const key of spec.items?.identify ?? []) {
      own[key] = row[key]
      kept++
    }
    if (kept) rows.push(coerceRow(spec, own))
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
  return out
}

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
    const answer = await api.get<{ effective?: Record<string, unknown> }>(
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
async function saveRow(spec: SettingSpec, index: number) {
  if (isList(spec)) {
    const row = entriesOf(values.value[spec.key])[index]
    if (!row) return

    const value = listSettable(spec, [row])
    await saveOne(spec.key, value === undefined ? [] : value)
    await settleRow(spec, index)
    emit('saved')
    return
  }
  const value = settingValue(spec, values.value[spec.key])
  if (value === undefined) return
  await saveOne(spec.key, value)
  settleSetting(spec)
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
async function settleRow(spec: SettingSpec, index: number) {
  try {
    const answer = await api.get<{ effective?: Record<string, unknown> }>(
      `/modules/${props.module.id}/settings?${scopeQuery.value}`,
    )
    const row = entriesOf(answer.effective?.[spec.key])[index]
    const current = entriesOf(values.value[spec.key])
    if (!row || !current[index]) return
    current[index] = { ...row }
    values.value = { ...values.value, [spec.key]: current }
    const baseline = entriesOf(saved.value[spec.key])
    if (baseline[index]) baseline[index] = clone(row)
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
function rowChanged(spec: SettingSpec, index: number): boolean {
  const row = entriesOf(values.value[spec.key])[index]
  const was = entriesOf(saved.value[spec.key])[index]
  if (!row || !was) return true
  return JSON.stringify(row) !== JSON.stringify(was)
}

/** Whether one setting is different from what is stored. */
function settingChanged(spec: SettingSpec): boolean {
  return JSON.stringify(values.value[spec.key]) !== JSON.stringify(saved.value[spec.key])
}

/** Puts one row back to what is stored, leaving the others as they are. */
function revertRow(spec: SettingSpec, index: number) {
  const current = entriesOf(values.value[spec.key])
  const was = entriesOf(saved.value[spec.key])[index]
  if (!was) return
  current[index] = clone(was)
  values.value = { ...values.value, [spec.key]: current }
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

/** What the core sends back for a secret it holds rather than returns. */
const secretMask = '********'

/**
 * A new, empty row.
 *
 * Every declared field present with a blank value, so the row renders as a form
 * rather than growing fields one keystroke at a time as each is first typed into.
 */
function newEntry(spec: SettingSpec): SettingEntry {
  const row: SettingEntry = {}
  for (const field of spec.items?.fields ?? []) {
    row[field.key] = field.type === 'bool' ? false : ''
  }
  return row
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
function removeEntry(spec: SettingSpec, index: number) {
  const rows = entriesOf(values.value[spec.key])
  rows.splice(index, 1)
  values.value[spec.key] = rows
}

/** Whether a row is one this scope wrote, and may therefore delete. */
function rowIsOwn(spec: SettingSpec, row: SettingEntry): boolean {
  if (!spec.items?.identify?.length) return true
  const name = rowName(spec, row)
  if (!name) return true
  return !entriesOf(inherited.value[spec.key]).some((one) => rowName(spec, one) === name)
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

/** The rows of a list setting, for the template to walk. */
/**
 * The rows of a list setting, for the template to walk.
 *
 * One row when the page was asked about one place by name, and that row is looked up by
 * the same field the core uses to join inherited rows: by `name`. The other rows are not
 * hidden here but absent — a card about a place has nothing to do with the other places.
 */
/**
 * Whether this field's value is the one the level above wrote down.
 *
 * On the field itself, from the row and the key, so that every control asks the same
 * question the same way.
 */
function inheritedNow(spec: SettingSpec, row: SettingEntry, key: string): boolean {
  const above = entriesOf(inherited.value[spec.key]).find(
    (one) => rowName(spec, one) === rowName(spec, row),
  )
  if (!above) return false
  return sameValue(above[key], row[key])
}

/**
 * What a field shows.
 *
 * Nothing, where the value is inherited: the field is empty and says so in its
 * placeholder, because a value copied into the box would be read as this project's own
 * and saved as one — and would then stop following the level above without anybody
 * deciding that. Typing into it is what decides it here.
 */
function shownValue(spec: SettingSpec, row: SettingEntry, key: string): string {
  if (inheritedNow(spec, row, key)) return ''
  return asString(row[key])
}

function setField(row: SettingEntry, key: string, event: Event) {
  const target = event.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  row[key] = target.value
}


function rowsOf(spec: SettingSpec): SettingEntry[] {
  const rows = entriesOf(values.value[spec.key])
  if (!props.onlyRow || !rowName(spec, {})) return rows
  return rows.filter((row) => rowName(spec, row) === props.onlyRow)
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
  return 'a new row'
}

onMounted(load)
watch(() => [props.module.id, props.scope, props.scopeID], load)
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
        <div v-if="spec.type === 'list'" class="setting-list">
          <div v-for="(row, index) in rowsOf(spec)" :key="index" class="setting-entry">
            <div class="setting-entry-head">
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
              <div v-if="spec.items?.identify?.length" class="row-switches" :title="inheritedNote">
              <label
                class="row-switch"
              >
                <span class="row-switch-name">Autodeploy</span>
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
              </label>
              <label class="row-switch">
                <span class="row-switch-name">In use</span>
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
              </label>
              </div>
              <button
                v-if="rowIsOwn(spec, row) || !spec.items?.identify?.length"
                class="link-button row-remove"
                type="button"
                :disabled="busy || !mayEdit"
                @click="removeEntry(spec, index)"
              >
                Remove
              </button>
              <span v-else class="row-inherited-note">
                written above — edit it there, or switch it off here
              </span>
            </div>

            <!-- This row's own Save, below its own fields: a row is saved by itself, so
                 changing one's namespace is never also a chance to rewrite the other's
                 kubeconfig. It is here, at the end, and not under the row's name where
                 it would be a button above the thing it acts on. -->


            <div
              v-for="field in (spec.items?.fields ?? []).filter((one) => !(props.hideFields ?? []).includes(one.key))"
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
                :class="{ on: row[field.key] === true }"
                type="button"
                role="switch"
                :aria-checked="row[field.key] === true"
                :disabled="!mayEdit || inheritedNow(spec, row, field.key)"
                :title="inheritedNow(spec, row, field.key)
                  ? 'inherited — switch it to decide it here'
                  : (row[field.key] === true ? 'On' : 'Off')"
                @click="row[field.key] = row[field.key] !== true"
              >
                <span class="knob" />
              </button>
              <select
                v-else-if="field.type === 'enum'"
                :disabled="!mayEdit"
                :id="`${spec.key}-${index}-${field.key}`"
                :value="shownValue(spec, row, field.key)"
                @change="setField(row, field.key, $event)"
              >
                <option v-for="option in field.options ?? []" :key="option" :value="option">
                  {{ option }}
                </option>
              </select>
              <!-- A document, not a word: a kubeconfig pasted into a one-line box
                   loses its newlines and stops being a kubeconfig at all. -->
              <textarea
                v-else-if="field.type === 'text'"
                :disabled="!mayEdit"
                :id="`${spec.key}-${index}-${field.key}`"
                :value="shownValue(spec, row, field.key)"
                @input="setField(row, field.key, $event)"
                rows="6"
                spellcheck="false"
                :placeholder="inheritedNow(spec, row, field.key) ? 'inherited' : `Paste the file's contents here`"
              />
              <input
                v-else
                :disabled="!mayEdit"
                :id="`${spec.key}-${index}-${field.key}`"
                :value="shownValue(spec, row, field.key)"
                @input="setField(row, field.key, $event)"
                :type="field.secret ? 'password' : 'text'"
                :placeholder="inheritedNow(spec, row, field.key)
                  ? 'inherited'
                  : (field.default !== undefined ? String(field.default) : '')"
              />
            </div>
            <div class="setting-entry-actions">
              <!-- The hint takes its room whether or not it has anything to say, and the
                   buttons keep their width while they are disabled: a row that changes
                   shape when a switch is flicked makes the switch feel like it moved
                   something. -->
              <span class="muted small pending" :class="{ show: rowChanged(spec, index) }">
                not saved yet
              </span>
              <button
                class="btn btn-small"
                type="button"
                :disabled="busy || !mayEdit || !rowChanged(spec, index)"
                @click="revertRow(spec, index)"
              >
                Discard
              </button>
              <button
                class="btn btn-small btn-primary"
                type="button"
                :disabled="busy || !mayEdit || !rowChanged(spec, index)"
                @click="saveRow(spec, index)"
              >
                {{ busy ? 'Saving…' : 'Save this row' }}
              </button>
            </div>
          </div>

          <!-- Adding is allowed on a place's tab too: a project may name a cluster the
               instance has none of, and the only place to add a place should be the page
               that is about places. The row it adds is this project's own, and is the one
               row on this tab that can be deleted again. -->
          <button
            v-if="mayEdit"
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
            @click="saveRow(spec, -1)"
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

.setting-field {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 16px;
  padding: 7px 0;
}

.setting-field + .setting-field {
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