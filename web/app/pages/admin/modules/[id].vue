<script setup lang="ts">
/** One module: what it is, what it uses, what it holds, and how to remove it. */
import type { ModuleRow, ModuleStats, SettingSpec } from '~/types/module'

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

/**
 * The settings form.
 *
 * One form with one button, not a button per row. A settings page where every line
 * is its own little action reads as a set of separate tasks, and saving four of
 * them means four requests that each reload the page under the operator's hands.
 * Everything is submitted in one request, checked in one go, and either lands or
 * does not.
 */
const values = reactive<Record<string, string>>({})
const saved = ref<Record<string, string>>({})

watch(module, (current) => {
  if (!current) return
  for (const key of Object.keys(values)) delete values[key]
  for (const key of Object.keys(saved)) delete saved[key]

  for (const spec of current.manifest?.settings ?? []) {
    const stored = current.settings?.[spec.key]
    const value = stored === undefined || stored === null ? '' : String(stored)
    values[spec.key] = value
    saved[spec.key] = value
  }
}, { immediate: true })

/** What the operator has changed since the last save. */
const changed = computed(() => {
  const dirty: Record<string, string> = {}
  for (const [key, value] of Object.entries(values)) {
    if (saved[key] !== value) dirty[key] = value
  }
  return dirty
})

const changedCount = computed(() => Object.keys(changed.value).length)

/**
 * Puts one setting back to what the module declared.
 *
 * A separate action from discarding edits, because the two are not the same: this
 * one changes what is stored, and it is what an operator reaches for after
 * setting a value they did not mean.
 */
async function resetSetting(spec: SettingSpec) {
  busy.value = true
  error.value = ''
  try {
    await api.del(`/modules/${moduleId.value}/settings?key=${encodeURIComponent(spec.key)}`)
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    busy.value = false
  }
}

/** Returns the form to what is actually stored. */
function revert() {
  for (const [key, value] of Object.entries(saved)) values[key] = value
}

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

/**
 * Saves every change in one request.
 *
 * An unchanged secret is left out rather than written back: the API never returns
 * a secret, so submitting the placeholder would store the word "********" as the
 * password.
 */
async function saveSettings() {
  const dirty = changed.value
  if (changedCount.value === 0) return

  const payload: Record<string, unknown> = {}
  for (const spec of module.value?.manifest?.settings ?? []) {
    if (!(spec.key in dirty)) continue
    const value = settingValue(spec, dirty[spec.key])
    if (value === undefined) continue
    payload[spec.key] = value
  }

  busy.value = true
  error.value = ''
  try {
    await api.put(`/modules/${moduleId.value}/settings/bulk`, { values: payload })
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

        <form v-else class="card" @submit.prevent="saveSettings">
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

              <!-- Shown only when a value has actually been set: an operator looking
                   at a form that matches the default needs nothing said about it. -->
              <p
                v-if="spec.default !== undefined && module.settings?.[spec.key] !== undefined"
                class="setting-hint"
              >
                Set to <span class="mono">{{ String(spec.default) }}</span>
                <template v-if="String(spec.default) !== String(module.settings?.[spec.key])">
                  instead of the default.
                </template>
                <button
                  class="link-button"
                  type="button"
                  :disabled="busy"
                  @click="resetSetting(spec)"
                >
                  Reset to the default
                </button>
              </p>
            </div>

            <div class="form-actions">
              <button class="btn btn-primary" type="submit" :disabled="busy || changedCount === 0">
                {{ busy ? 'Saving…' : 'Save changes' }}
              </button>
              <button
                class="btn"
                type="button"
                :disabled="busy || changedCount === 0"
                @click="revert"
              >
                Discard
              </button>
              <span class="muted small">
                {{ changedCount }} unsaved change{{ changedCount === 1 ? '' : 's' }}
              </span>
            </div>
          </div>
        </form>
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
  grid-template-columns: minmax(0, 1fr) 280px;
  row-gap: 4px;
  gap: 12px;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
}

.setting-row:last-of-type {
  border-bottom: none;
}

.setting-label {
  font-size: 13px;
}

.setting-hint {
  grid-column: 1 / -1;
  margin: 0 0 10px;
  font-size: 12px;
  color: var(--text-muted);
}

/* A link rather than a button: it resets one setting, which reads as an action on
   that line rather than as a fourth button in a row of controls. */
.link-button {
  background: none;
  border: 0;
  padding: 0;
  color: var(--accent);
  cursor: pointer;
  font-size: 12px;
  text-decoration: underline;
}

.link-button:disabled {
  cursor: default;
  opacity: 0.5;
  text-decoration: none;
}

.form-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--border);
}

.small {
  font-size: 12px;
}

@media (max-width: 800px) {
  .setting-row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>