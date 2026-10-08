<script setup lang="ts">
/**
 * Writing down a resource this instance did not create.
 *
 * This is the answer a module has been getting "no" to: a database on a host this instance
 * does not run, reached with credentials that are not ours to create. The alternative was "no",
 * and "no" is the answer a module cannot work with.
 *
 * The fields come from the core rather than being written here. What a database is made of and
 * what an object store is made of are two different lists, and a form holding its own copy of
 * them is a form that asks for a port it will then refuse, or one that asks for a host and
 * stores nothing for it — while the core checks a third list and the table has a fourth.
 *
 * The secrets among these fields are written once and sealed. There is no field that reads them
 * back afterwards, deliberately: a password in almost every form a database takes one is a
 * password, and a form that will show it again is a form that will show it to whoever opens the
 * page next.
 */
import { ref, computed } from 'vue'
import { api, ApiError } from '~/utils/api'
import { useNotifyPool } from '~/composables/useNotifyPool'

useHead({ title: 'Describe a resource' })

const { add: notify } = useNotifyPool()

interface Field {
  key: string
  label: string
  hint: string
  secret: boolean
  required: boolean
  port: boolean
  default: string
}

interface Kind {
  key: string
  label: string
  note: string
  software: string[]
  fields: Field[]
}

/**
 * The kinds, as the core describes them.
 *
 * Asked for rather than written down here, and there is a route for it. This form used to hold
 * its own list of kinds and its own list of fields per kind, and the two lists were the only
 * thing on the page nobody could check against anything: the core checked its own, the table had
 * its own columns, and a part this form forgot was refused by the core with a message about a
 * field the page had never shown.
 */
const kinds = ref<Kind[]>([])
const loading = ref(true)
const loadError = ref('')

const kind = ref('')
const software = ref('')
const version = ref('')
const name = ref('')
const forModuleKind = ref('')
const parts = ref<Record<string, string>>({})
const busy = ref(false)
const error = ref('')

/**
 * The chosen kind, and its fields, defensively.
 *
 * `software` is read as a list and not assumed to be one: an object store has no software to
 * choose from, and a core that reports that as `null` instead of an empty list would take the
 * form down on the one kind that has the least to fill in. The page a kind that has nothing to
 * ask about is the page that must not break.
 */
const known = computed(() => kinds.value.find((one) => one.key === kind.value) ?? null)
const fields = computed(() => known.value?.fields ?? [])
const softwareOptions = computed(() => known.value?.software ?? [])

/** The fields that are filled in, and only those: an untouched field must not be sent. */
const filledParts = computed(() => {
  const out: Record<string, string> = {}
  for (const field of fields.value) {
    const value = (parts.value[field.key] ?? '').trim()
    if (value !== '') out[field.key] = value
  }
  return out
})

/** The first missing required field, named. Checked here so the button explains itself. */
const firstMissing = computed(() => {
  for (const field of fields.value) {
    if (field.required && (parts.value[field.key] ?? '').trim() === '') return field.label
  }
  return ''
})

const canSave = computed(() => kind.value !== '' && firstMissing.value === '' && !busy.value)

/**
 * The password field's value, and whether it is filled.
 *
 * Held apart because it must never be readable afterwards and there is nothing else on this
 * page that may be read once and forgotten: if the save fails and the value stays in the field,
 * the administrator can correct one line and try again rather than finding the password in
 * somewhere they are not looking and having nowhere to put it.
 */
const secretsPresent = computed(() =>
  fields.value.filter((f) => f.secret && (parts.value[f.key] ?? '').trim() !== '').map((f) => f.label),
)

function valueOf(key: string, fallback = ''): string {
  return parts.value[key] ?? fallback
}

function setValue(key: string, value: string) {
  parts.value = { ...parts.value, [key]: value }
}

function onKindChosen(next: string) {
  kind.value = next
  const chosen = kinds.value.find((one) => one.key === next)
  software.value = chosen?.software?.[0] ?? ''
  // Each kind has its own fields, so what is left over from the previous one is not kept:
  // sending a bucket along with a database is a record of two things.
  parts.value = {}
}

/** A password field is emptied once the save has gone through, whatever the outcome said. */
function forgetSecrets() {
  const next = { ...parts.value }
  for (const field of fields.value) {
    if (field.secret) delete next[field.key]
  }
  parts.value = next
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const answer = await api.get<{ kinds: Kind[] }>('/resources/kinds')
    kinds.value = answer.kinds
    if (answer.kinds.length && !answer.kinds.some((one) => one.key === kind.value)) {
      onKindChosen(answer.kinds[0].key)
    }
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

async function save() {
  busy.value = true
  error.value = ''
  try {
    await api.post('/resources', {
      kind: kind.value,
      software: software.value.trim(),
      version: version.value.trim(),
      name: name.value.trim(),
      origin: 'manual',
      parts: filledParts.value,
      for_module_kind: forModuleKind.value.trim(),
    })
    // Before the navigation and before the failure path, because the form is torn down by the
    // first and a password left in it after the second would still be in the browser's memory
    // for as long as the tab is open.
    forgetSecrets()
    notify('Written down. The secrets are sealed, and nothing here will show them again.', {
      type: 'success',
    })
    await navigateTo('/admin/resources')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'it could not be written down'
  } finally {
    busy.value = false
  }
}

onMounted(() => void load())
</script>

<template>
  <div>
    <div class="block-head">
      <h1 class="block-title">Describe a resource</h1>
      <span class="muted small">for a module that keeps its state somewhere this instance did not make</span>
    </div>

    <div v-if="loading" class="spinner">Loading…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <template v-else>
      <div v-if="error" class="alert alert-error">{{ error }}</div>

      <form class="card" @submit.prevent="save">
        <div class="card-body">
          <!-- Which sort of thing this is, because the fields below change with it and a
               database is not an object store in more than its name. -->
          <label class="field">
            <span class="label">Kind</span>
            <select :value="kind" @change="onKindChosen(($event.target as HTMLSelectElement).value)">
              <option v-for="one in kinds" :key="one.key" :value="one.key">{{ one.label }} — {{ one.key }}</option>
            </select>
          </label>

          <p v-if="known" class="muted small">{{ known.note }}</p>

          <!-- What it is made of, in this kind's own order. -->
          <div v-for="field in fields" :key="field.key" class="field">
            <span class="label">
              {{ field.label }}
              <span v-if="field.required" class="required">required</span>
              <span v-else class="optional">optional</span>
            </span>

            <input
              v-if="field.port"
              type="text"
              inputmode="numeric"
              :value="valueOf(field.key, field.default)"
              :placeholder="field.default"
              @input="setValue(field.key, ($event.target as HTMLInputElement).value)"
            >
            <input
              v-else-if="field.secret"
              type="password"
              autocomplete="new-password"
              :value="valueOf(field.key)"
              @input="setValue(field.key, ($event.target as HTMLInputElement).value)"
            >
            <input
              v-else
              type="text"
              :value="valueOf(field.key)"
              @input="setValue(field.key, ($event.target as HTMLInputElement).value)"
            >

            <span class="muted small">{{ field.hint }}</span>
          </div>

          <!-- The software, where it is software. An object store is not one piece of
               software, so the field is not offered for it rather than offered and ignored. -->
          <label v-if="softwareOptions.length" class="field">
            <span class="label">Software</span>
            <select v-model="software">
              <option v-for="one in softwareOptions" :key="one" :value="one">{{ one }}</option>
            </select>
            <span class="muted small">
              This is what a module's requirement names — it asks for
              <span class="mono">{{ kind }}:{{ software || '…' }}</span> and not for a database.
            </span>
          </label>

          <label class="field">
            <span class="label">Version</span>
            <input v-model="version" type="text" placeholder="19, 8.0, 2024">
            <span class="muted small">
              Optional. Recorded as written, because what matters is what this resource is, not
              what it would be found to be after an upgrade that has not happened.
            </span>
          </label>

          <label class="field">
            <span class="label">For which module</span>
            <input v-model="forModuleKind" type="text" placeholder="deploy:kubernetes">
            <span class="muted small">
              Optional, and the most important field here. A module of this kind that has no
              database is given this one instead of a new one inside this cluster — which is
              the whole point of describing one. Left empty, it waits: a resource nobody has
              claimed is not handed to whatever module arrives next.
            </span>
          </label>

          <label class="field">
            <span class="label">Name</span>
            <input v-model="name" type="text" placeholder="the deploy module's database">
            <span class="muted small">
              Optional, and for you. Nothing resolves through it, so two may share one.
            </span>
          </label>

          <!-- What is sealed, said before anything is written rather than after. -->
          <p v-if="secretsPresent.length" class="muted small">
            {{ secretsPresent.join(' and ') }} will be sealed when this is saved, and no page
            will show {{ secretsPresent.length > 1 ? 'them' : 'it' }} again — including this one.
          </p>
        </div>

        <!-- The buttons, on the same line as the note about what is still missing.
             The note is on the left and the buttons on the right rather than the other way
             round because the note changes as the form is filled in: somebody halfway through
             is looking at what is left, not at the button, and a button that moved when they
             looked away is a button they have to find again. -->
        <div class="form-actions">
          <button class="btn btn-primary" type="submit" :disabled="!canSave">
            {{ busy ? 'Writing…' : 'Write it down' }}
          </button>
          <NuxtLink to="/admin/resources" class="btn">Cancel</NuxtLink>
          <span class="form-note">
            <template v-if="firstMissing">Fill in {{ firstMissing.toLowerCase() }} to go on.</template>
            <template v-else-if="known">Everything this kind needs is filled in.</template>
          </span>
        </div>
      </form>
    </template>
  </div>
</template>

<style scoped>
/* The buttons and the note, on one line, separated from the fields above by a rule rather
   than by a box. A form this tall needs to say where it ends, and a second border inside the
   card would draw it twice. */
.form-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 14px 16px;
  border-top: 1px solid var(--border);
}

/* Pushed to the right so it sits against the edge the eye finishes at. Sits after the buttons
   in the source so it is read last by a screen reader too, and reaches there with margin-left
   rather than order. */
.form-note {
  margin-left: auto;
  font-size: 12px;
  color: var(--text-muted);
}

.required {
  margin-left: 6px;
  font-size: 11px;
  color: var(--text-muted);
}

.optional {
  margin-left: 6px;
  font-size: 11px;
  color: var(--text-muted);
  opacity: 0.7;
}
</style>