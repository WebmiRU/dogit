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
import type { ModuleRow, SettingSpec } from '~/types/module'

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

const values = ref<Record<string, string>>({})
const saved = ref<Record<string, string>>({})
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

function fill(answer: { settings?: Record<string, unknown> }) {
  const next: Record<string, string> = {}
  for (const spec of props.module.manifest?.settings ?? []) {
    next[spec.key] = asString(answer.settings?.[spec.key] ?? spec.default ?? '')
  }
  values.value = next
  saved.value = { ...next }
}

async function load() {
  error.value = ''
  try {
    const answer = await api.get<{ settings?: Record<string, unknown> }>(
      `/modules/${props.module.id}/settings?${scopeQuery.value}`,
    )
    fill(answer)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  }
}

const changed = computed(() => {
  const dirty: Record<string, string> = {}
  for (const [key, value] of Object.entries(values.value)) {
    if (saved.value[key] !== value) dirty[key] = value
  }
  return dirty
})
const changedCount = computed(() => Object.keys(changed.value).length)

/** Coerces the typed string back into what the setting expects. */
function settingValue(spec: SettingSpec, raw: string): unknown {
  // A secret the core does not return comes back as a mask; submitting that would
  // store the word as the password.
  if (spec.secret && raw === '********') return undefined
  if (spec.type === 'bool') return raw === 'true'
  if (spec.type === 'int') {
    const parsed = Number.parseInt(raw, 10)
    return Number.isNaN(parsed) ? raw : parsed
  }
  return raw
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

        <div class="setting-control">
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
          v-if="spec.default !== undefined && module.settings?.[spec.key] !== undefined"
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
  padding: 12px 0;
  border-bottom: 1px solid var(--border);
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
.setting-control select {
  width: 100%;
  padding: 6px 8px;
  font: inherit;
  font-size: 13px;
  color: inherit;
  background: var(--bg, transparent);
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
  margin-top: 14px;
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