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
    /** Whose values this is: instance, group or project. */
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
function fill(answer: { effective?: Record<string, unknown> }) {
  const next: Record<string, unknown> = {}
  for (const spec of props.module.manifest?.settings ?? []) {
    const stored = answer.effective?.[spec.key]
    next[spec.key] = isList(spec)
      ? entriesOf(stored)
      : asString(stored ?? spec.default ?? '')
  }
  values.value = next
  saved.value = { ...next }
}

function isList(spec: SettingSpec): boolean {
  return spec.type === 'list'
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

const changed = computed(() => {
  const dirty: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(values.value)) {
    // Compared as JSON, because a list is a value rather than a reference: two arrays
    // with the same rows are the same setting, and an identity comparison would say
    // every list was edited the moment it was loaded.
    if (JSON.stringify(saved.value[key]) !== JSON.stringify(value)) dirty[key] = value
  }
  return dirty
})
const changedCount = computed(() => Object.keys(changed.value).length)

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
 * Saves every change in one request.
 *
 * One button for the whole form, and the whole form or nothing: a settings page
 * that saves one field at a time can leave half of somebody's configuration behind,
 * and the half that did save is the half they were not looking at.
 */
async function save() {
  const dirty = changed.value
  if (changedCount.value === 0) return

  const payload: Record<string, unknown> = {}
  for (const spec of props.module.manifest?.settings ?? []) {
    if (!(spec.key in dirty)) continue
    const value = settingValue(spec, dirty[spec.key])
    if (value === undefined) continue
    payload[spec.key] = value
  }

  busy.value = true
  error.value = ''
  try {
    await api.put(`/modules/${props.module.id}/settings/bulk?${scopeQuery.value}`, {
      values: payload,
    })
    await load()
    notify(`Settings saved for ${props.module.name}`, { type: 'success' })
  } catch (caught) {
    const message = caught instanceof ApiError ? caught.message : 'the request failed'
    error.value = message
    notify(message, { type: 'error', timer: 0 })
  } finally {
    busy.value = false
  }
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

function revert() {
  values.value = { ...saved.value }
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

function addEntry(spec: SettingSpec) {
  const rows = entriesOf(values.value[spec.key])
  values.value[spec.key] = [...rows, newEntry(spec)]
}

/**
 * Removes a row.
 *
 * On the last row, rather than leaving one behind as a blank: a settings page that
 * always has one empty row is a page where the empty row looks like a value, and
 * somebody reads an entry that was never filled in.
 */
function removeEntry(spec: SettingSpec, index: number) {
  const rows = entriesOf(values.value[spec.key])
  rows.splice(index, 1)
  values.value[spec.key] = rows
}

/** The rows of a list setting, for the template to walk. */
function rowsOf(spec: SettingSpec): SettingEntry[] {
  return entriesOf(values.value[spec.key])
}

/** What a row is called in the list: its first field, or "entry". */
function entryTitle(spec: SettingSpec, row: SettingEntry): string {
  for (const field of spec.items?.fields ?? []) {
    const text = asString(row[field.key]).trim()
    if (text) return text
  }
  return 'entry'
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

    <form v-else @submit.prevent="save">
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
              <button
                class="link-button"
                type="button"
                :disabled="busy"
                @click="removeEntry(spec, index)"
              >
                Remove
              </button>
            </div>

            <div v-for="field in spec.items?.fields ?? []" :key="field.key" class="setting-field">
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
                :title="row[field.key] === true ? 'On' : 'Off'"
                @click="row[field.key] = row[field.key] !== true"
              >
                <span class="knob" />
              </button>
              <select
                v-else-if="field.type === 'enum'"
                :id="`${spec.key}-${index}-${field.key}`"
                v-model="row[field.key]"
              >
                <option v-for="option in field.options ?? []" :key="option" :value="option">
                  {{ option }}
                </option>
              </select>
              <!-- A document, not a word: a kubeconfig pasted into a one-line box
                   loses its newlines and stops being a kubeconfig at all. -->
              <textarea
                v-else-if="field.type === 'text'"
                :id="`${spec.key}-${index}-${field.key}`"
                v-model="row[field.key]"
                rows="6"
                spellcheck="false"
                placeholder="Paste the file's contents here"
              />
              <input
                v-else
                :id="`${spec.key}-${index}-${field.key}`"
                v-model="row[field.key]"
                :type="field.secret ? 'password' : 'text'"
                :placeholder="field.default !== undefined ? String(field.default) : ''"
              />
            </div>
          </div>

          <button class="btn btn-small" type="button" :disabled="busy" @click="addEntry(spec)">
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
      </div>

      <div class="form-actions">
        <button class="btn btn-primary" type="submit" :disabled="busy || changedCount === 0">
          {{ busy ? 'Saving…' : 'Save changes' }}
        </button>
        <button class="btn" type="button" :disabled="busy || changedCount === 0" @click="revert">
          Discard
        </button>
        <span v-if="changedCount > 0" class="muted small">{{ changedCount }} changed</span>
      </div>
    </form>
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
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border);
}

/* The name a row is known by: which cluster, which mirror, which feed. */
.setting-entry-name {
  font-weight: 600;
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