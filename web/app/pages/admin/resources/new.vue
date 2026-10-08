<script setup lang="ts">
/**
 * Writing down a resource this instance did not create.
 *
 * This is the answer to a module that must not keep its state here: one running on another
 * host, one whose database belongs inside a network it can reach and this one cannot, one
 * an organisation already has. The alternative was "no", and "no" is the answer a module
 * cannot work with.
 *
 * The address is written once and sealed. There is no field that reads it back afterwards,
 * deliberately: an address in almost every form a database takes one carries a password in
 * it, and a form that will show it again is a form that will show it to whoever opens the
 * page next.
 */
import { ref } from 'vue'
import { api, ApiError } from '~/utils/api'
import { useNotifyPool } from '~/composables/useNotifyPool'

useHead({ title: 'Describe a resource' })

const { add: notify } = useNotifyPool()

const kind = ref('db')
const software = ref('postgresql')
const version = ref('')
const name = ref('')
const address = ref('')
const forModuleKind = ref('')
const busy = ref(false)
const error = ref('')

/** What a kind is called, so the field does not say `db` to somebody who does not know. */
const KINDS = [
  { value: 'db', label: 'Database — db', hint: 'PostgreSQL or MySQL, reached over the network.' },
  { value: 's3', label: 'Object store — s3', hint: 'Anything that speaks the S3 protocol.' },
]

const known = KINDS.find((one) => one.value === kind.value)
const canSave = computed(() => kind.value !== '' && address.value.trim() !== '' && !busy.value)

/**
 * Says what will happen, before it happens.
 *
 * A form that creates a database the administrator thinks this instance will make is a form
 * that will be filled in wrongly: the `origin` decides whether destroying the resource later
 * drops the thing or leaves it, and that is not a detail to discover afterwards.
 */
const originNote = computed(() =>
  kind.value === 'db'
    ? 'This instance makes databases of its own when a module asks for one and has none. ' +
      'Describing one here is for a database somewhere else — a host you already have, or ' +
      'one you would rather this instance could not reach.'
    : 'Object stores are only ever described here. This instance creates none.',
)

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
      address: address.value.trim(),
      for_module_kind: forModuleKind.value.trim(),
    })
    notify('Written down. It is sealed, and the list will not show it again.', { type: 'success' })
    await navigateTo('/admin/resources')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'it could not be written down'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <div class="block-head">
      <h1 class="block-title">Describe a resource</h1>
      <span class="muted small">
        for a module that keeps its state somewhere this instance did not make
      </span>
    </div>

    <form class="card" @submit.prevent="save">
      <div class="card-body">
        <label class="field">
          <span class="label">Kind</span>
          <select v-model="kind">
            <option v-for="one in KINDS" :key="one.value" :value="one.value">{{ one.label }}</option>
          </select>
          <span v-if="known" class="muted small">{{ known.hint }}</span>
        </label>

        <label class="field">
          <span class="label">Software</span>
          <input v-model="software" type="text" placeholder="postgresql, minio, mysql">
          <span class="muted small">
            The piece of software behind it. This is what a module's requirement names — it
            asks for <span class="mono">db:postgresql</span> and not for a database.
          </span>
        </label>

        <label class="field">
          <span class="label">Version</span>
          <input v-model="version" type="text" placeholder="19beta4, 2024">
          <span class="muted small">
            Optional. Recorded as written, because what matters is what this resource is, not
            what it would be found to be after an upgrade that has not happened.
          </span>
        </label>

        <label class="field">
          <span class="label">Name</span>
          <input v-model="name" type="text" placeholder="the deploy module's database">
          <span class="muted small">
            Optional, and for you. Nothing resolves through it, so two may share one.
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
          <span class="label">Address</span>
          <input
            v-model="address"
            type="text"
            placeholder="postgres://user:password@host:5432/database"
          >
          <span class="muted small">
            Written once and sealed. Nothing here will show it again afterwards — not this
            form, not the list, not a page of any kind. A password in it is why this table
            exists.
          </span>
        </label>

        <p class="muted small">{{ originNote }}</p>

        <p v-if="error" class="alert alert-error">{{ error }}</p>

        <div class="actions">
          <NuxtLink to="/admin/resources" class="btn btn-small">Cancel</NuxtLink>
          <button class="btn btn-small btn-primary" type="submit" :disabled="!canSave">
            {{ busy ? 'Writing…' : 'Write it down' }}
          </button>
        </div>
      </div>
    </form>
  </div>
</template>
<style scoped>
.field {
  display: block;
  margin-bottom: 14px;
}

.field .label {
  display: block;
  font-weight: 600;
  margin-bottom: 4px;
}

.field input,
.field select {
  width: 100%;
  padding: 7px 9px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--bg-inset);
  color: var(--text);
  font-size: 14px;
}

.actions {
  display: flex;
  gap: 8px;
  align-items: center;
}
</style>
