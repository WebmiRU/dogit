<script setup lang="ts">
/** Registered modules: what is installed, whether it is alive, and how full it is. */
import type { ModuleRow } from '~/types/module'

const { user, ensureLoaded } = useAuth()
await ensureLoaded()

const modules = ref<ModuleRow[]>([])
const loading = ref(true)
const error = ref('')

/**
 * Reads the list again.
 *
 * Modules change on their own: a runner starts, a heartbeat lapses and one goes
 * offline. Those moments happen with nobody in front of the browser, so this page
 * follows the instance's event feed rather than waiting to be asked.
 */
async function load(quiet = false) {
  // A reload that follows an event leaves the rows where they are. Rebuilding the
  // list every few seconds would move the row somebody is reading out from under
  // them, which is worse than a row a second out of date.
  if (!quiet) loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ modules: ModuleRow[] }>('/modules')
    modules.value = answer.modules
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

let stopWatching: (() => void) | undefined

onMounted(() => {
  void load()
  stopWatching = watchEvents({
    kinds: ['module.registered', 'module.updated', 'module.removed'],
    onChange: () => void load(true),
  })
})

onBeforeUnmount(() => stopWatching?.())

/** Storage as a fraction, or null when the module reported none of it. */
function storageFraction(module: ModuleRow): number | null {
  const stats = module.stats
  if (!stats?.storage_total_bytes || stats.storage_used_bytes === undefined) return null
  if (stats.storage_total_bytes <= 0) return null
  return stats.storage_used_bytes / stats.storage_total_bytes
}

function storageBadge(module: ModuleRow) {
  const fraction = storageFraction(module)
  if (fraction === null) return null
  const cls = fraction >= 0.9 ? 'badge-danger' : fraction >= 0.7 ? 'badge-warning' : 'badge-green'
  return { cls, text: `${Math.round(fraction * 100)}% used` }
}

const statusClass: Record<string, string> = {
  online: 'badge-green',
  offline: 'badge-private',
  pending: 'badge-private',
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title">Modules</h1>
        <p class="page-subtitle">
          Separate services that work with dogit's users and permissions. Each one
          declares the scopes and settings it accepts, and reports what it can see
          about itself.
        </p>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="loading && modules.length === 0" class="spinner">Loading modules…</div>
    <div v-else-if="modules.length === 0" class="card empty">
      No modules are registered. A module registers itself with an instance token:
      <code class="mono">dogit module token create</code>, then
      <code class="mono">curl -X POST /api/v1/modules/register</code> with it.
    </div>

    <table v-else class="admin-table modules-table">
        <thead>
          <tr>
            <th>Module</th>
            <th>Status</th>
            <th>Address</th>
            <th>Storage</th>
            <th>Channel</th>
            <th>Last seen</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="module in modules" :key="module.id">
            <td>
              <NuxtLink :to="`/admin/modules/${module.id}`" class="strong">
                {{ module.name }}
              </NuxtLink>
              <div class="mono muted small">{{ module.kind }} · v{{ module.module_version || '?' }}</div>
            </td>
            <td>
              <span class="badge" :class="statusClass[module.status] ?? 'badge-neutral'">
                {{ module.status }}
              </span>
              <span v-if="!module.enabled" class="badge badge-warning">Forbidden</span>
            </td>
            <td>
              <span v-if="module.public_url" class="mono small">{{ module.public_url }}</span>
              <span v-else class="muted small">internal</span>
            </td>
            <td>
              <template v-if="storageBadge(module)">
                <span class="badge" :class="storageBadge(module)!.cls">
                  {{ storageBadge(module)!.text }}
                </span>
              </template>
              <span v-else class="muted small">not reported</span>
            </td>
            <td class="small">
              <!-- Two numbers rather than one badge, because they answer different
                   questions: a module can be connected and still have nothing waiting, and
                   commands waiting for a connected module is the state that looks like a
                   module ignoring its core. -->
              <span
                class="badge"
                :class="module.channel_connections ? 'badge-green' : 'badge-warning'"
              >
                {{ module.channel_connections || 0 }}
              </span>
              <span v-if="module.channel_pending_commands" class="muted small">
                {{ module.channel_pending_commands }} waiting
              </span>
            </td>
            <td class="small">{{ module.last_seen_at ? timeAgo(module.last_seen_at) : 'never' }}</td>
          </tr>
        </tbody>
    </table>
  </div>
</template>

