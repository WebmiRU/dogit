<script setup lang="ts">
/**
 * What a module's data lives in, and what could go there instead.
 *
 * Its own tab rather than a line above the settings form, because there is more to say about it
 * than one line holds: what it is, where it is, where it came from, which of the module's needs
 * it answers, and — the part that matters — that a module which asked for a database and has
 * none is a gap rather than a choice.
 *
 * Put and taken back from here rather than from the list of resources, because the question is
 * "what should this module use" and asked of a resource list it can only be answered by
 * somebody matching resources against needs by hand. A registry with a database and an object
 * store has no way to say which is which from the other side.
 *
 * No secret, and none fetched. A page that shows a host by opening a password is a page that
 * eventually prints one.
 */
import { ref, computed } from 'vue'
import { api, ApiError } from '~/utils/api'
import { useNotifyPool } from '~/composables/useNotifyPool'

interface Resource {
  id: string
  kind: string
  software?: string
  version?: string
  name?: string
  origin: 'managed' | 'manual' | string
  parts?: Record<string, string>
  created_at: string
}

/** One slot the module asked for, as the manifest named it. */
interface Need {
  key: string
  kind: string
  software?: string
  required: boolean
  /** The resource in it, null when the slot is empty. */
  held?: Resource | null
  /** Free resources that could go in it. */
  free?: Resource[]
}

const props = defineProps<{
  needs: Need[]
  /** Whether this module asked for anything at all. */
  wantsAny: boolean
}>()

const { add: notify } = useNotifyPool()
const busy = ref('')

/** What a thing is, the way a requirement names it: db:postgresql:19. */
function coordinate(one: Resource): string {
  let out = one.kind
  if (one.software) out += `:${one.software}`
  if (one.version) out += `:${one.version}`
  return out
}

/**
 * Where it is, in one cell.
 *
 * The user is part of a database's address and the database is not the same thing as the user:
 * one user owns several databases, and a module is given one of them. Written as
 * `user@host/database` so the cell says the same thing a connection string would, without the
 * password in it.
 */
function where(one: Resource): string {
  const parts = one.parts ?? {}
  if (one.kind === 'db') {
    const host = parts.host ?? ''
    const name = parts.database_name ?? ''
    const user = parts.username ? `${parts.username}@` : ''
    return `${user}${host}${name ? '/' + name : ''}`
  }
  if (one.kind === 's3') {
    return `${parts.endpoint ?? ''}${parts.bucket ? '/' + parts.bucket : ''}`
  }
  return parts.endpoint ?? ''
}

/** The parts worth a column of their own: the ones a reader compares by eye. */
function detail(one: Resource): string[] {
  const parts = one.parts ?? {}
  if (one.kind === 'db') {
    return [parts.port ? `port ${parts.port}` : ''].filter(Boolean)
  }
  return [parts.region, parts.access_key ? `key ${parts.access_key}` : ''].filter(Boolean)
}

function originWord(one: Resource): string {
  return one.origin === 'managed' ? 'Made here' : 'Described'
}

function originClass(one: Resource): string {
  return one.origin === 'managed' ? 'badge-blue' : 'badge-neutral'
}

function whatWanted(need: Need): string {
  return need.software ? `${need.kind}:${need.software}` : need.kind
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString()
}

/**
 * Putting another resource in a slot that already has one.
 *
 * Asked before, in words that name the one being replaced, because the old one is not deleted
 * — it goes back on the shelf with nothing holding it — and somebody who did not know that
 * would read this as losing a database.
 */
async function attach(need: Need, resource: Resource) {
  const was = need.held
  if (was && !globalThis.confirm(
    `Put ${coordinate(resource)} in place of ${coordinate(was)} for "${need.key}"?\n\n` +
      'The module is given the new one immediately. The one it had is not deleted — it goes ' +
      'back to the Resources page with nothing holding it, and can be given to somebody else.',
  )) return

  busy.value = `${need.key}:${resource.id}`
  try {
    const answer = await api.post<{ replaced?: string }>(
      `/modules/${routeModuleId()}/resources/attach`,
      { need_key: need.key, resource_id: resource.id },
    )
    notify(answer.replaced
      ? `${coordinate(resource)} is in place; ${answer.replaced} is on the Resources page`
      : `${coordinate(resource)} is now what "${need.key}" uses`, { type: 'success' })
    reload()
  } catch (caught) {
    notify(caught instanceof ApiError ? caught.message : 'it could not be attached',
      { type: 'error', timer: 0 })
  } finally {
    busy.value = ''
  }
}

/**
 * Taking a resource out of a slot.
 *
 * Only offered where the module said the need is optional. For a required one the core refuses
 * and the page says why before the button is pressed, rather than offering something that will
 * be declined.
 */
async function detach(need: Need) {
  const held = need.held
  if (!held) return
  if (!globalThis.confirm(
    `Take ${coordinate(held)} away from "${need.key}"?\n\n` +
      'The module says it does not need this one, and will carry on without it. The resource ' +
      'itself is not deleted — it goes back to the Resources page with nothing holding it.',
  )) return

  busy.value = `${need.key}:${held.id}`
  try {
    await api.post(`/modules/${routeModuleId()}/resources/${encodeURIComponent(need.key)}/detach`, {})
    notify(`${coordinate(held)} is no longer used by this module`, { type: 'success' })
    reload()
  } catch (caught) {
    notify(caught instanceof ApiError ? caught.message : 'it could not be taken away',
      { type: 'error', timer: 0 })
  } finally {
    busy.value = ''
  }
}

// The page owns reloading: this component is handed data, and a component that also fetched it
// would have two ways to be wrong about what is on screen.
const emit = defineEmits<{ reload: [] }>()
function reload() {
  emit('reload')
}

const routeModuleId = computed(() => String(useRoute().params.id ?? ''))
</script>

<template>
  <div class="module-resources">
    <!-- One section per slot the module named. A module wanting a database and an object store
         gets two, and they are told apart by the names the module gave them — which are its
         own words for its own internals and the only thing that can tell them apart. -->
    <section v-for="need in needs" :key="need.key" class="slot">
      <h2 class="section-title">
        {{ need.key }}
        <span class="muted small">{{ whatWanted(need) }}</span>
        <span v-if="need.required" class="badge badge-warning">required</span>
        <span v-else class="badge badge-neutral">optional</span>
      </h2>

      <table v-if="need.held" class="table">
        <thead>
          <tr>
            <th>What</th>
            <th>Where it is</th>
            <th>Detail</th>
            <th>Where it came from</th>
            <th>Since</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td class="nowrap"><span class="mono">{{ coordinate(need.held) }}</span></td>
            <td>
              <span class="mono">{{ where(need.held) }}</span>
              <span v-if="need.held.name" class="muted small">— {{ need.held.name }}</span>
            </td>
            <td>
              <span v-for="part in detail(need.held)" :key="part" class="muted small">{{ part }}</span>
            </td>
            <td><span class="badge" :class="originClass(need.held)">{{ originWord(need.held) }}</span></td>
            <td class="nowrap muted small">{{ formatDate(need.held.created_at) }}</td>
            <td class="right">
              <button
                v-if="!need.required"
                class="btn btn-small"
                type="button"
                :disabled="busy === `${need.key}:${need.held.id}`"
                @click="detach(need)"
              >
                Take it away
              </button>
              <span v-else class="muted small">
                cannot be taken away — this module would go on working and keep nothing
              </span>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- An empty required slot is the one thing on this page that is a fault, and it is
           worded as one. A module that asked for a database and has none does not stop; it
           deploys and remembers nothing, which looks from the outside exactly like a module
           that has never deployed anything. -->
      <div v-else class="card empty">
        <p v-if="need.required">
          <strong>Nothing here.</strong>
          <span class="muted">
            This module asked for this one and has none. It will carry on working and keep
            nothing — which from the outside is a module that has never deployed anything.
            Put something here.
          </span>
        </p>
        <p v-else class="muted">
          Empty, and that is as it should be: this module said it does not need one.
        </p>
      </div>

      <!-- What could go in it. Only where there is something, and never for a slot that is
           already filled from this list — replacing is done from the slot above, where the one
           being replaced can be named. -->
      <div v-if="!need.held && need.free?.length" class="free-list">
        <p class="muted small">Free resources that would fit:</p>
        <table class="table">
          <thead>
            <tr>
              <th>What</th>
              <th>Where it is</th>
              <th>Detail</th>
              <th>Where it came from</th>
              <th>Described</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="one in need.free" :key="one.id">
              <td class="nowrap"><span class="mono">{{ coordinate(one) }}</span></td>
              <td>
                <span class="mono">{{ where(one) }}</span>
                <span v-if="one.name" class="muted small">— {{ one.name }}</span>
              </td>
              <td>
                <span v-for="part in detail(one)" :key="part" class="muted small">{{ part }}</span>
              </td>
              <td><span class="badge" :class="originClass(one)">{{ originWord(one) }}</span></td>
              <td class="nowrap muted small">{{ formatDate(one.created_at) }}</td>
              <td class="right">
                <button
                  class="btn btn-small btn-primary"
                  type="button"
                  :disabled="busy === `${need.key}:${one.id}`"
                  @click="attach(need, one)"
                >
                  Use this one
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-else-if="!need.held && need.required" class="muted small">
        Nothing free would fit. Describe one on the
        <NuxtLink to="/admin/resources/new">resources page</NuxtLink>.
      </p>
    </section>

    <div v-if="!needs.length" class="card empty">
      <p class="muted">
        This module asked for no resources. It keeps whatever it keeps on its own host, and
        there is nothing here for it.
      </p>
    </div>
  </div>
</template>

<style scoped>
.module-resources {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.slot {
  margin-bottom: 8px;
}

.section-title {
  display: flex;
  align-items: center;
  gap: 8px;
}

.free-list {
  margin-top: 10px;
}

.table td {
  vertical-align: top;
}
</style>