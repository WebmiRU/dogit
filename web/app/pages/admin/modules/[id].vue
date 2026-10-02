<script setup lang="ts">
/** One module: what it is, what it uses, what it holds, and how to remove it. */
import type { ModuleRow, ModuleStats } from '~/types/module'

const route = useRoute()
const { ensureLoaded } = useAuth()
await ensureLoaded()

const moduleId = computed(() => String(route.params.id ?? ''))

const module = ref<ModuleRow | null>(null)
const stats = ref<ModuleStats | null>(null)
const series = ref<ModuleStats[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)

/** Tabs live in the URL so a link to "the settings" is a link somebody can send. */
const tab = computed(() => (['overview', 'settings', 'removal'] as const)
  .includes(String(route.query.tab)) ? String(route.query.tab) : 'overview')

function setTab(name: string) {
  navigateTo({ path: route.path, query: name === 'overview' ? {} : { tab: name } }, { replace: true })
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [answer, statsAnswer] = await Promise.all([
      api.get<{ module: ModuleRow; settings: Record<string, unknown> }>(`/modules/${moduleId.value}`),
      // A module that has never reported answers with nothing, which is not an
      // error: statistics are new, and a module may not have been restarted since.
      api.get<{ latest: ModuleStats | null; series?: ModuleStats[] }>(`/modules/${moduleId.value}/stats`, { series: 1 })
        .catch(() => ({ latest: null })),
    ])
    module.value = answer.module
    stats.value = statsAnswer.latest ?? null
    series.value = statsAnswer.series ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function setEnabled(enabled: boolean) {
  busy.value = true
  error.value = ''
  try {
    await api.put(`/modules/${moduleId.value}/state`, { enabled })
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = false
  }
}

const values = reactive<Record<string, string>>({})
watch(module, (current) => {
  if (!current) return
  for (const [key, value] of Object.entries(values)) delete values[key]
  for (const spec of current.manifest?.settings ?? []) {
    const stored = current.settings?.[spec.key]
    values[spec.key] = stored === undefined || stored === null ? '' : String(stored)
  }
}, { immediate: true })

/** Coerces the typed string back into what the setting expects. */
function settingValue(spec: { type: string; secret?: boolean }, raw: string): unknown {
  if (spec.secret && raw === '********') return undefined
  switch (spec.type) {
    case 'bool':
      return raw === 'true'
    case 'int': {
      const parsed = Number.parseInt(raw, 10)
      return Number.isNaN(parsed) ? raw : parsed
    }
    default:
      return raw
  }
}

async function saveSetting(spec: { key: string; type: string; secret?: boolean }) {
  busy.value = true
  error.value = ''
  try {
    const value = settingValue(spec, values[spec.key] ?? '')
    if (value === undefined) return // an unchanged secret is not rewritten
    await api.put(`/modules/${moduleId.value}/settings?key=${encodeURIComponent(spec.key)}`, { value })
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = false
  }
}

const storageFraction = computed(() => {
  const current = stats.value
  if (!current?.storage_total_bytes || current.storage_used_bytes === undefined) return null
  if (current.storage_total_bytes <= 0) return null
  return current.storage_used_bytes / current.storage_total_bytes
})
</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading…</div>
    <div v-else-if="error && !module" class="alert alert-error">{{ error }}</div>

    <template v-else-if="module">
      <div class="repo-head">
        <div class="title">
          <p class="crumb">
            <NuxtLink to="/admin/modules">Modules</NuxtLink>
          </p>
          <h1 class="page-title">{{ module.name }}</h1>
          <p class="page-subtitle mono">{{ module.kind }} · v{{ module.module_version || '?' }}</p>
        </div>
        <button
          class="btn"
          type="button"
          :disabled="busy"
          @click="setEnabled(!module.enabled)"
        >
          {{ module.enabled ? 'Forbid' : 'Allow' }}
        </button>
        <button class="btn" type="button" :disabled="loading" @click="load">Refresh</button>
      </div>

      <div v-if="error" class="alert alert-error">{{ error }}</div>
      <div v-if="!module.enabled" class="alert alert-warning">
        This module is forbidden: the core no longer tells it who is calling, so it
        cannot let anyone in. Nothing was deleted, and allowing it again restores
        the previous state exactly.
      </div>

      <nav class="tabs">
        <button
          v-for="name in ['overview', 'settings', 'removal']"
          :key="name"
          class="tab"
          :class="{ active: tab === name }"
          type="button"
          @click="setTab(name)"
        >
          {{ name === 'removal' ? 'Removal' : name.charAt(0).toUpperCase() + name.slice(1) }}
        </button>
      </nav>

      <section v-if="tab === 'overview'">
        <dl class="facts">
          <dt>Status</dt>
          <dd>
            <span class="badge" :class="module.status === 'online' ? 'badge-green' : 'badge-neutral'">
              {{ module.status }}
            </span>
            <span v-if="!module.enabled" class="badge badge-warning">forbidden</span>
          </dd>

          <dt>Address</dt>
          <dd>
            <template v-if="module.public_url">
              <span class="mono">{{ module.public_url }}</span>
              <span class="badge">{{ module.dedicated_host ? 'its own name' : 'under this instance' }}</span>
            </template>
            <span v-else class="muted">
              not published — reachable only inside the network
            </span>
          </dd>

          <dt>Endpoint</dt>
          <dd class="mono">{{ module.endpoint }}</dd>

          <dt>Last seen</dt>
          <dd>
            {{ module.last_seen_at ? timeAgo(module.last_seen_at) : 'never' }}
            <span v-if="module.last_seen_at" class="muted">· {{ formatDate(module.last_seen_at) }}</span>
          </dd>

          <dt v-if="storageFraction !== null">Storage</dt>
          <dd v-if="storageFraction !== null">
            {{ formatBytes(stats!.storage_used_bytes) }} of
            {{ formatBytes(stats!.storage_total_bytes) }}
            <span
              class="badge"
              :class="storageFraction > 0.9 ? 'badge-danger' : storageFraction > 0.7 ? 'badge-warning' : 'badge-green'"
            >
              {{ Math.round(storageFraction * 100) }}% used
            </span>
          </dd>

          <dt>Scopes</dt>
          <dd>
            <span v-for="scope in module.scopes" :key="scope" class="badge">{{ scope }}</span>
            <span v-if="!module.scopes?.length" class="muted">none declared</span>
          </dd>
        </dl>

        <h2 class="section-title">Reported</h2>
        <ModuleStats :stats="stats" :series="series" />
      </section>

      <section v-else-if="tab === 'settings'">
        <div v-if="!module.manifest?.settings?.length" class="card empty">
          This module declared no settings.
        </div>

        <div v-else class="card">
          <div class="card-body">
            <p class="muted" style="margin-top: 0">
              These are the instance-wide values. Group and project scopes override
              them and are configured on the group or project page.
            </p>

            <div v-for="spec in module.manifest.settings" :key="spec.key" class="setting-row">
              <div class="setting-label">
                <div>{{ spec.label }}</div>
                <div v-if="spec.description" class="muted">{{ spec.description }}</div>
              </div>

              <div class="setting-control">
                <select v-if="spec.type === 'enum'" v-model="values[spec.key]">
                  <option v-for="option in spec.options" :key="option" :value="option">{{ option }}</option>
                </select>
                <select v-else-if="spec.type === 'bool'" v-model="values[spec.key]">
                  <option value="true">enabled</option>
                  <option value="false">disabled</option>
                </select>
                <input
                  v-else
                  v-model="values[spec.key]"
                  :type="spec.secret ? 'password' : 'text'"
                  :placeholder="spec.default !== undefined ? String(spec.default) : ''"
                />
              </div>

              <button class="btn" type="button" :disabled="busy" @click="saveSetting(spec)">
                Save
              </button>
            </div>
          </div>
        </div>
      </section>

      <section v-else>
        <ModuleUninstall :module="module" />
      </section>
    </template>
  </div>
</template>

<style scoped>
.tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border);
  margin: 18px 0;
}

.tab {
  background: none;
  border: 0;
  border-bottom: 2px solid transparent;
  color: var(--text-muted);
  padding: 8px 14px;
  cursor: pointer;
  font-size: 14px;
}

.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
}

.facts {
  display: grid;
  grid-template-columns: 120px minmax(0, 1fr);
  gap: 8px 16px;
  margin: 0 0 8px;
  font-size: 13px;
}

.facts dt {
  color: var(--text-muted);
}

.facts dd {
  margin: 0;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
}

.section-title {
  margin: 22px 0 10px;
}

.setting-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 260px auto;
  gap: 12px;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
}

.setting-row:last-child {
  border-bottom: none;
}

.setting-label {
  font-size: 13px;
}

@media (max-width: 800px) {
  .setting-row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>