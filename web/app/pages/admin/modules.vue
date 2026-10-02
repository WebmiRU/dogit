<script setup lang="ts">
/** Registered modules: what is installed, whether it is alive, and its settings. */
interface SettingSpec {
  key: string
  label: string
  type: string
  default?: unknown
  options?: string[]
  description?: string
  secret?: boolean
}

interface ModuleManifest {
  version: string
  description?: string
  scopes: string[]
  settings: SettingSpec[]
  depends_on?: string[]
}

interface ModuleRow {
  id: string
  kind: string
  name: string
  endpoint: string
  module_version: string
  manifest: ModuleManifest
  status: string
  enabled: boolean
  last_seen_at?: string
  registered_at: string
  settings: Record<string, unknown>
  scopes: string[]
}

const { user } = useAuth()

const modules = ref<ModuleRow[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref<string | null>(null)
const form = reactive<Record<string, Record<string, string>>>({})

/** Settings of one module, keyed by setting key. */
function valuesOf(module: ModuleRow): Record<string, string> {
  let values = form[module.id]
  if (!values) {
    values = {}
    form[module.id] = values
  }
  return values
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const response = await api.get<{ modules: ModuleRow[] }>('/modules')
    modules.value = response.modules

    // Pre-fill the form with the effective values, so an administrator sees what
    // the module will actually use rather than a blank form.
    for (const module of response.modules) {
      const values = valuesOf(module)
      for (const spec of module.manifest?.settings ?? []) {
        const current = module.settings?.[spec.key]
        values[spec.key] = current === undefined || current === null ? '' : String(current)
      }
    }
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function toggle(module: ModuleRow) {
  busy.value = module.id
  error.value = ''
  try {
    await api.patch(`/modules/${module.id}`, { enabled: !module.enabled })
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = null
  }
}

/** Coerces the typed string back into what the setting expects. */
function settingValue(spec: SettingSpec, raw: string): unknown {
  if (spec.secret && raw === '********') return undefined
  switch (spec.type) {
    case 'bool':
      return raw === 'true' || raw === 'on'
    case 'int': {
      const parsed = Number.parseInt(raw, 10)
      return Number.isNaN(parsed) ? raw : parsed
    }
    default:
      return raw
  }
}

async function saveSetting(module: ModuleRow, spec: SettingSpec) {
  busy.value = module.id
  error.value = ''
  try {
    const value = settingValue(spec, valuesOf(module)[spec.key] ?? '')
    if (value === undefined) return // an unchanged secret is not rewritten
    await api.put(`/modules/${module.id}/settings?key=${encodeURIComponent(spec.key)}`, { value })
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = null
  }
}

async function remove(module: ModuleRow) {
  if (!confirm(`Remove module ${module.kind} (${module.name})?`)) return
  busy.value = module.id
  try {
    await api.delete(`/modules/${module.id}`)
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = null
  }
}

const statusClass: Record<string, string> = {
  online: 'badge-green',
  offline: 'badge-private',
  disabled: '',
  pending: 'badge-private',
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title">Modules</h1>
        <p class="page-subtitle">
          Modules are separate services that register with dogit and work with its
          users and permissions. Each one declares the scopes and settings it
          supports, so this page shows exactly what the installed module accepts.
        </p>
      </div>
      <button class="btn" type="button" :disabled="loading" @click="load">Refresh</button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="!user?.is_admin" class="alert alert-info">
      Viewing module status. Changing settings requires administrator rights.
    </div>

    <div v-if="loading && modules.length === 0" class="spinner">Loading modules…</div>
    <div v-else-if="modules.length === 0" class="card empty">
      No modules are registered.
    </div>

    <section v-for="module in modules" :key="module.id" class="card module-card">
      <div class="card-header">
        <strong class="mono">{{ module.kind }}</strong>
        <span class="badge" :class="statusClass[module.status]">{{ module.status }}</span>
        <span class="badge">v{{ module.module_version || '?' }}</span>
        <div class="spacer" />
        <button
          class="btn"
          type="button"
          :disabled="busy === module.id"
          @click="toggle(module)"
        >
          {{ module.enabled ? 'Disable' : 'Enable' }}
        </button>
        <button
          v-if="user?.is_admin"
          class="btn"
          type="button"
          :disabled="busy === module.id"
          @click="remove(module)"
        >
          Remove
        </button>
      </div>

      <div class="card-body">
        <p v-if="module.manifest?.description" class="muted" style="margin-top: 0">
          {{ module.manifest.description }}
        </p>

        <dl class="module-facts">
          <dt>Name</dt>
          <dd>{{ module.name }}</dd>
          <dt>Endpoint</dt>
          <dd class="mono">{{ module.endpoint }}</dd>
          <dt>Last seen</dt>
          <dd>
            {{ module.last_seen_at ? timeAgo(module.last_seen_at) : 'never' }}
            <span v-if="module.last_seen_at" class="muted">· {{ formatDate(module.last_seen_at) }}</span>
          </dd>
          <dt>Scopes</dt>
          <dd>
            <span v-for="scope in module.scopes" :key="scope" class="badge">{{ scope }}</span>
            <span v-if="!module.scopes?.length" class="muted">none declared</span>
          </dd>
        </dl>

        <template v-if="module.manifest?.settings?.length">
          <h3 class="section-title" style="margin: 18px 0 10px">
            Settings · instance scope
          </h3>

          <div v-for="spec in module.manifest.settings" :key="spec.key" class="setting-row">
            <div class="setting-label">
              <div>{{ spec.label }}</div>
              <div v-if="spec.description" class="muted">{{ spec.description }}</div>
            </div>

            <div class="setting-control">
              <select v-if="spec.type === 'enum'" v-model="valuesOf(module)[spec.key]">
                <option v-for="option in spec.options" :key="option" :value="option">
                  {{ option }}
                </option>
              </select>
              <select v-else-if="spec.type === 'bool'" v-model="valuesOf(module)[spec.key]">
                <option value="true">enabled</option>
                <option value="false">disabled</option>
              </select>
              <input
                v-else
                v-model="valuesOf(module)[spec.key]"
                :type="spec.secret ? 'password' : 'text'"
                :placeholder="spec.default !== undefined ? String(spec.default) : ''"
              />
            </div>

            <button
              class="btn"
              type="button"
              :disabled="busy === module.id"
              @click="saveSetting(module, spec)"
            >
              Save
            </button>
          </div>

          <p class="muted" style="font-size: 12px; margin-top: 10px">
            Instance settings apply everywhere. Group and project scopes override
            them and are configured on the group or project page.
          </p>
        </template>
      </div>
    </section>
  </div>
</template>

<style scoped>
.module-card {
  margin-bottom: 16px;
}

.card-header .spacer {
  flex: 1;
}

.module-facts {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: 6px 14px;
  margin: 0;
  font-size: 13px;
}

.module-facts dt {
  color: var(--text-muted);
}

.module-facts dd {
  margin: 0;
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
}

.setting-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 260px auto;
  gap: 12px;
  align-items: center;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
}

.setting-row:last-of-type {
  border-bottom: none;
}

.setting-label {
  font-size: 13px;
}

.setting-label .muted {
  font-size: 12px;
}

@media (max-width: 800px) {
  .setting-row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
