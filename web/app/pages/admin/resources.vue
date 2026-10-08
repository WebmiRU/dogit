<script setup lang="ts">
/**
 * The resources this instance has given to modules, and the ones it is holding for nobody.
 *
 * One page for both, because the question behind either is the same — what does this instance
 * have — and two pages would have to be read together to answer it. Issued ones can only be
 * looked at: a resource in use is a module's business, and this page's job is to say so
 * rather than to offer a button. Free ones can be taken away from a module or destroyed,
 * because a resource nobody holds is this instance's to close.
 *
 * No address is shown here and none is fetched for the list. An address is a password in
 * almost every form a database takes one, and a page of twenty things and their holders is a
 * page that ends up pasted into a ticket.
 */
import { onMounted, ref, computed } from 'vue'
import { api, ApiError } from '~/utils/api'
import { useNotifyPool } from '~/composables/useNotifyPool'

interface Resource {
  id: string
  kind: string
  software?: string
  version?: string
  name?: string
  origin: 'managed' | 'manual' | string
  integration_id?: string
  released_at?: string
  module_kind?: string
  module_name?: string
  created_at: string
  updated_at: string
}

const { add: notify } = useNotifyPool()

const resources = ref<Resource[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref('')

/** Issued first, then the free ones, each newest first — the list as the store orders it. */
const held = computed(() => resources.value.filter((one) => one.integration_id))
const free = computed(() => resources.value.filter((one) => !one.integration_id))

/** What a thing is, the way a requirement names it: db:postgresql:19.1. */
function coordinate(one: Resource): string {
  let out = one.kind
  if (one.software) out += `:${one.software}`
  if (one.version) out += `:${one.version}`
  return out
}

/**
 * Where a resource came from, in the two words that decide what may be done to it.
 *
 * Its own column rather than a badge beside the name, for one reason and then the rest: a
 * badge sitting next to `db:postgresql` squeezes that column until the coordinate is clipped
 * mid-word, and a coordinate that reads `db:postgresq` is not a thing a reader can look up.
 */
function originWord(one: Resource): string {
  return one.origin === 'managed' ? 'made here' : 'described'
}

/** Blue for one this instance made and may destroy; grey for one it may only forget. */
function originClass(one: Resource): string {
  return one.origin === 'managed' ? 'badge-blue' : 'badge-neutral'
}

/** Who has it, in a sentence that never leaves an empty name behind. */
function holder(one: Resource): string {
  return one.module_name || one.module_kind || 'a module'
}

function when(text?: string): string {
  if (!text) return ''
  const at = new Date(text)
  if (Number.isNaN(at.getTime())) return ''
  return at.toLocaleString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' })
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ resources?: Resource[] }>('/resources')
    resources.value = answer.resources ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the resources could not be read'
  } finally {
    loading.value = false
  }
}

/**
 * Takes a resource back from the module holding it.
 *
 * Not deletion, and the wording says so: the resource stays, sealed, with the module it
 * belonged to written beside it. Somebody looking at it in a month is going to want to know
 * whose it was, and that module is gone by then.
 */
async function release(one: Resource) {
  if (!globalThis.confirm(
    `Take ${coordinate(one)} away from ${holder(one)}?\n\n` +
      'The resource itself is not deleted. It will sit here with nothing holding it, and the ' +
      'module it belonged to is written beside it.',
  )) return

  busy.value = one.id
  try {
    await api.post(`/resources/${one.id}/release`, {})
    notify(`${coordinate(one)} is no longer in use`, { type: 'success' })
    await load()
  } catch (caught) {
    notify(caught instanceof ApiError ? caught.message : 'it could not be taken back',
      { type: 'error', timer: 0 })
  } finally {
    busy.value = ''
  }
}

/** Forgets a resource: the record goes, the thing stays where it is. */
async function forget(one: Resource) {
  if (!globalThis.confirm(
    `Forget ${coordinate(one)}${one.name ? ` ("${one.name}")` : ''}?\n\n` +
      (one.origin === 'managed'
        ? 'The record goes. The database stays, because dropping it is a separate thing you ' +
          'have to ask for on purpose.'
        : 'The record goes. It was described by somebody, so it is left exactly where it is.'),
  )) return

  busy.value = one.id
  try {
    await api.delete(`/resources/${one.id}`)
    notify(`${coordinate(one)} is forgotten`, { type: 'success' })
    await load()
  } catch (caught) {
    notify(caught instanceof ApiError ? caught.message : 'it could not be forgotten',
      { type: 'error', timer: 0 })
  } finally {
    busy.value = ''
  }
}

/** Destroys a managed resource and drops what this instance created for it. */
async function destroy(one: Resource) {
  if (!globalThis.confirm(
    `Drop the database behind ${coordinate(one)}${one.name ? ` ("${one.name}")` : ''}?\n\n` +
      'This one is not undoable. The database and the role that owns it are dropped and the ' +
      'record is forgotten.',
  )) return

  busy.value = one.id
  try {
    const answer = await api.post<{ database?: string }>(`/resources/${one.id}/destroy`, {})
    notify(answer.database ? `Dropped ${answer.database}` : 'Destroyed', { type: 'success' })
    await load()
  } catch (caught) {
    notify(caught instanceof ApiError ? caught.message : 'it could not be destroyed',
      { type: 'error', timer: 0 })
  } finally {
    busy.value = ''
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="block-head">
      <h1 class="block-title">Resources</h1>
      <span class="muted small">
        what this instance has given to modules, and what it is holding for nobody
      </span>
    </div>

    <div class="card">
      <div class="card-body">
        <NuxtLink to="/admin/resources/new" class="btn btn-small">
          Describe a resource
        </NuxtLink>
        <p class="muted small" style="margin-top: 0.5rem">
          For a module that must not keep its state here — one that runs on another host, or
          one whose database you would rather not have inside this cluster. You write down how
          it is reached; this instance records it sealed and never shows it again in a list.
        </p>
      </div>
    </div>

    <p v-if="loading" class="muted">Reading…</p>
    <p v-else-if="error" class="alert alert-error">{{ error }}</p>

    <template v-else>
      <section class="block">
        <h3 class="block-title">In use ({{ held.length }})</h3>
        <p v-if="!held.length" class="muted small">
          Nothing is in use. A module is given a resource when it registers, and it says so on
          its own page.
        </p>
        <table v-else class="table">
          <thead>
            <tr>
              <th>What</th>
              <th>Name</th>
              <th>Where it came from</th>
              <th>Held by</th>
              <th>Since</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="one in held" :key="one.id">
              <td class="nowrap"><span class="mono">{{ coordinate(one) }}</span></td>
              <td>{{ one.name || '—' }}</td>
              <td><span class="badge" :class="originClass(one)">{{ originWord(one) }}</span></td>
              <td>{{ holder(one) }}</td>
              <td class="muted small">{{ when(one.created_at) }}</td>
              <td class="right">
                <button
                  class="btn btn-small"
                  type="button"
                  :disabled="busy === one.id"
                  @click="release(one)"
                >
                  Take it back
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="block">
        <h3 class="block-title">Nobody holds these ({{ free.length }})</h3>
        <p v-if="!free.length" class="muted small">
          Nothing is sitting here. A module that is removed leaves its resource behind rather
          than destroying it, and this is where it waits.
        </p>
        <table v-else class="table">
          <thead>
            <tr>
              <th>What</th>
              <th>Name</th>
              <th>Where it came from</th>
              <th>Whose it was</th>
              <th>Left</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="one in free" :key="one.id">
              <td class="nowrap"><span class="mono">{{ coordinate(one) }}</span></td>
              <td>{{ one.name || '—' }}</td>
              <td><span class="badge" :class="originClass(one)">{{ originWord(one) }}</span></td>
              <td class="muted small">{{ one.module_name || one.module_kind || 'never given' }}</td>
              <td class="muted small">{{ when(one.released_at || one.created_at) }}</td>
              <td class="right">
                <button
                  v-if="one.origin === 'managed' && one.name"
                  class="btn btn-small"
                  type="button"
                  :disabled="busy === one.id"
                  @click="destroy(one)"
                >
                  Drop the database
                </button>
                <button
                  class="btn btn-small"
                  type="button"
                  :disabled="busy === one.id"
                  @click="forget(one)"
                >
                  Forget
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>
  </div>
</template>
<style scoped>
/* Copied from the other tables rather than invented: a table that looks like a different
   table from the one two pages up does not read as "this one has fewer columns", it reads as
   "this one is somewhere else". */
.table {
  width: 100%;
  border-collapse: collapse;
  margin-bottom: 16px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
}

.table th {
  text-align: left;
  padding: 8px 12px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border);
}

/* Height 1px is what makes every cell take the height of the tallest one in its row, which is
   the whole of what keeps a row of buttons from pushing its own border out of line. */
.table td {
  height: 1px;
  padding: 10px 12px;
  vertical-align: middle;
  border-bottom: 1px solid var(--border);
}

.table tbody tr:last-child td {
  border-bottom: none;
}

/* The buttons are the widest thing in the row, and without this the column they sit in eats
   the one before it until a coordinate reads `db:postgresq`. */
.table td.right {
  text-align: right;
  white-space: nowrap;
  width: 1%;
}

.nowrap {
  white-space: nowrap;
}

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
