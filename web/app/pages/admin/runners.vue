<script setup lang="ts">
/**
 * The machines that run builds.
 *
 * A runner is a module — it registers, it reports what it can see, it is forbidden
 * with the same button as anything else — so this is a filtered view of the module
 * list rather than a separate kind of thing. What it adds is the work: a runner
 * that is online and busy is a different fact from one that is online and idle, and
 * only the first explains why a pipeline is taking as long as it is.
 *
 * The page follows the instance's event feed. A runner that stops answering is
 * noticed by the core's housekeeping rather than by anybody here, and a page that
 * needs a button pressed to learn about it is a page nobody keeps open.
 */
import type { ModuleRow, ModuleStats } from '~/types/module'

interface RunnerRow extends ModuleRow {
  /** Jobs this machine is working on right now. */
  running_jobs: number
  /** How many it takes at once, as the machine itself declared. */
  concurrent_limit?: number
  /** Jobs waiting anywhere for a machine. */
  queue_depth?: number
  last_seen_ago?: string
}

const { user } = useAuth()

const runners = ref<RunnerRow[]>([])
const loading = ref(true)
const error = ref('')

async function load(quiet = false) {
  if (!quiet) loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ runners: RunnerRow[] }>('/admin/runners')
    runners.value = answer.runners
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

let stopWatching: (() => void) | undefined

onMounted(() => {
  if (!user.value?.is_admin) {
    loading.value = false
    return
  }
  void load()
  stopWatching = watchEvents({
    kinds: ['module.updated', 'module.registered', 'job.updated'],
    onChange: () => void load(true),
  })
})

onBeforeUnmount(() => stopWatching?.())

/**
 * How busy this machine is, as one word rather than a fraction.
 *
 * The class is a colour, not a badge class: the status above is already a badge,
 * and two of them stacked would say the same thing twice.
 */
function load_pressure(runner: RunnerRow): { label: string; colour: string } {
  const capacity = runner.concurrent_limit ?? 0
  if (capacity === 0) return { label: 'capacity unknown', colour: 'unknown' }
  if (runner.running_jobs === 0) return { label: 'idle', colour: 'green' }
  if (runner.running_jobs >= capacity) return { label: 'at capacity', colour: 'warn' }
  return { label: 'busy', colour: 'blue' }
}

/** How full this machine's own storage is, or nothing at all if it said. */
function storageOf(stats?: ModuleStats | null): string | null {
  if (!stats?.storage_total_bytes || stats.storage_used_bytes === undefined) return null
  return `${humanBytes(stats.storage_used_bytes)} of ${humanBytes(stats.storage_total_bytes)}`
}

/** How many processors it reported, if it reported any. */
function coresOf(stats?: ModuleStats | null): number | null {
  const extra = stats?.extra
  if (!extra) return null
  const value = extra.cores
  return value === undefined ? null : Number(value)
}

function humanBytes(bytes: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title" style="margin: 0">Runners</h1>
        <p class="page-subtitle">
          Machines that run jobs. Each one is a module, and is configured, forbidden
          and watched like any other.
        </p>
      </div>
    </div>

    <div v-if="!user?.is_admin" class="alert alert-info">
      This page requires administrator rights.
    </div>

    <template v-else>
      <div v-if="error" class="alert alert-error">{{ error }}</div>

      <div v-if="loading && runners.length === 0" class="spinner">Loading runners…</div>

      <div v-else-if="!loading && runners.length === 0" class="card empty">
        No runners are registered. A runner is its own binary: it registers with
        this instance and starts claiming jobs, from any machine that can reach it.
      </div>

      <table v-else class="table">
        <thead>
          <tr>
            <th>Runner</th>
            <th>Status</th>
            <th class="numeric">Jobs</th>
            <th>Capacity</th>
            <th>Storage</th>
            <th>Last seen</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="runner in runners" :key="runner.id">
            <td>
              <NuxtLink :to="`/admin/modules/${runner.id}`" class="runner-name">
                {{ runner.name }}
              </NuxtLink>
              <div class="muted small mono">{{ runner.endpoint }}</div>
            </td>
            <td>
              <span
                class="badge"
                :class="runner.status === 'online' ? 'badge-green' : 'badge-neutral'"
              >{{ runner.status }}</span>
              <span v-if="!runner.enabled" class="badge badge-warning">forbidden</span>
              <div class="pressure" :class="load_pressure(runner).colour">
                {{ load_pressure(runner).label }}
              </div>
            </td>
            <td class="numeric mono">
              {{ runner.running_jobs }}
              <span class="muted">running</span>
            </td>
            <td class="muted small">
              <template v-if="runner.concurrent_limit">
                {{ runner.concurrent_limit }} at a time
              </template>
              <template v-else>not declared</template>
              <div v-if="coresOf(runner.stats)" class="muted small">
                {{ coresOf(runner.stats) }} cores
              </div>
            </td>
            <td class="muted small">{{ storageOf(runner.stats) ?? 'not reported' }}</td>
            <td class="muted small">{{ runner.last_seen_ago ?? 'never' }}</td>
            <td class="numeric">
              <NuxtLink :to="`/admin/modules/${runner.id}`" class="btn small">Open</NuxtLink>
            </td>
          </tr>
        </tbody>
      </table>

      <p class="footnote">
        A runner that stops answering is marked offline by the core's housekeeping and
        the jobs it was holding are handed back — as interrupted, because nothing is
        known about how far they got.
      </p>
    </template>
  </div>
</template>

<style scoped>
.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.table th {
  text-align: left;
  font-weight: 600;
  font-size: 12px;
  color: var(--text-muted);
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
}

.table td {
  padding: 9px 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

.runner-name {
  color: inherit;
  font-weight: 500;
  text-decoration: none;
}

.runner-name:hover {
  text-decoration: underline;
}

.numeric {
  text-align: right;
}

.pressure {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-muted);
}

.pressure::before {
  content: '';
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.pressure.green { color: #3fb950; }
.pressure.warn { color: #d29922; }
.pressure.blue { color: #a371f7; }
.pressure.unknown { color: var(--text-muted); }

.small {
  font-size: 12px;
}

.btn.small {
  padding: 4px 10px;
  font-size: 12px;
}

.footnote {
  margin-top: 16px;
  font-size: 12px;
  color: var(--text-muted);
  max-width: 60ch;
}
</style>