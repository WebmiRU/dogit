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
/**
 * The tabs on this module's page.
 *
 * A registry also shows what it holds, which is its own data: the core keeps
 * digests of images in passing and knows nothing about tags or sizes, so this is
 * asked of the module and every other module simply has one tab fewer.
 */
const moduleKind = ref('')

/**
 * Which tabs this module has.
 *
 * A module that announces things gets one more, called Notify, and it holds the list
 * of places it writes to — one row per destination. Those rows are what the module's
 * settings mean here: a Telegram module's settings are the address of one chat, so
 * asking for "two chats" means asking for two rows rather than for another setting.
 */
const tabNames = computed(() => [
  'overview',
  ...(moduleKind.value === 'registry:docker' ? ['images'] : []),
  'settings',
  // Where the module may act. A notification module's rows are the chats it writes to,
  // and they are rows rather than settings: a Telegram module's settings are the
  // address of one chat, so asking for two chats means asking for two rows. Each row
  // is inherited downwards and is changed where it is changed, not on one page.
  ...(moduleKind.value.startsWith('notify:') ? ['notifications'] : []),
  // The record of what it deployed, kept apart from what it is configured to do: one
  // is settings anybody inherits, the other is what happened to a running system and
  // belongs to whoever may change one.
  ...(moduleKind.value.startsWith('deploy:') ? ['deployments'] : []),
  'removal',
])

const tabTitles: Record<string, string> = {
  overview: 'Overview',
  images: 'Images',
  notifications: 'Notifications',
  deployments: 'Deployments',
  settings: 'Settings',
  removal: 'Removal',
}

// Links are not broken by a rename. This tab was called "Options" while it was being
// argued about, and somebody has that URL in a bookmark: it opens the same page as the
// name it had, rather than the overview.
const tabAliases: Record<string, string> = { options: 'settings' }

const tab = computed(() => {
  const asked = tabAliases[String(route.query.tab)] ?? String(route.query.tab)
  return (tabNames.value as string[]).includes(asked) ? asked : 'overview'
})

function setTab(name: string) {
  navigateTo({ path: route.path, query: name === 'overview' ? {} : { tab: name } }, { replace: true })
}

async function load(quiet = false) {
  if (!quiet) loading.value = true
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
    // Which tabs this module gets is its own kind's business: only a registry has
    // images, and the page says so rather than offering an empty tab to everything.
    moduleKind.value = answer.module.kind
    stats.value = statsAnswer.latest ?? null
    series.value = statsAnswer.series ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

let stopWatching: (() => void) | undefined

onMounted(() => {
  void load()
  // The page follows the same feed as the list it was opened from, so a module that
  // goes offline while this is open says so here without anybody pressing anything.
  stopWatching = watchEvents({
    kinds: ['module.registered', 'module.updated', 'module.removed'],
    onChange: () => void load(true),
  })
})

onBeforeUnmount(() => stopWatching?.())

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
      </div>

      <div v-if="error" class="alert alert-error">{{ error }}</div>
      <div v-if="!module.enabled" class="alert alert-warning">
        This module is forbidden: the core no longer tells it who is calling, so it
        cannot let anyone in. Nothing was deleted, and allowing it again restores
        the previous state exactly.
      </div>

      <nav class="tabs">
        <button
          v-for="name in tabNames"
          :key="name"
          class="tab"
          :class="{ active: tab === name }"
          type="button"
          @click="setTab(name)"
        >
          {{ tabTitles[name] ?? name }}
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

      <!-- What the module holds. Only a registry has images, and it knows what they
           are: the core stores digests in passing and nothing else. -->
      <ModuleRegistryImages v-else-if="tab === 'images'" />

      <!-- Where this module writes. On the instance, and so inherited by every group
           and project that has not said otherwise. -->
      <section v-else-if="tab === 'notifications'">
        <p class="muted">
          {{ module.manifest?.target?.description
            || 'One row per place this module writes to. Everything here is inherited by every group and project that has not changed it.' }}
        </p>

        <ModuleTargets
          scope="instance"
          :only-module="module.id"
          :default-module="module.id"
        />
      </section>

      <!-- The rows this module has, at the instance: where it may act and how it
           reaches each place. Shown for every kind of module that declares rows, and
           only those — a module with no rows has nothing here to say. -->
      <section v-else-if="tab === 'places'" class="card">
        <div class="card-body">
          <h3>{{ module.manifest?.target?.title || 'Where this module may act' }}</h3>
          <p class="muted">
            {{ module.manifest?.target?.description
              || 'One row per place this module acts on. Everything here is inherited by every group and project that has not changed it.' }}
          </p>
          <!-- The module's own kind, not the default. The list asks for rows of one
               kind of module: left to itself it asks about notifications, and a page
               about a deployment module then says no notification modules are
               installed while standing on top of a row that exists. -->
          <ModuleTargets
            scope="instance"
            :kind="module.kind"
            :only-module="module.id"
            :default-module="module.id"
          />
        </div>
      </section>

      <!-- What this module is configured with, at every scope it is configured at. -->
      <section v-else-if="tab === 'settings'">
        <div v-if="!module.manifest?.settings?.length" class="card empty">
          This module declared no settings.
        </div>

        <!-- The same form the group and project pages use.
             It used to be a copy of it, which is how a type of setting one page could
             render and the other could not — a list of clusters visible in one place
             and an unusable empty box in another. One form, three scopes. -->
        <div v-else class="card">
          <div class="card-body">
            <ModuleSettingsForm
              :module="module"
              scope="instance"
              note="These are the instance-wide values. Group and project scopes override them and are configured on the group or project page."
            />
          </div>
        </div>
      </section>

      <!-- Instance-wide: every project's deployments through this module, which is
           what somebody who installed it wants to see. A project is not in scope here
           because a project can be reached through any of them. -->
      <section v-else-if="tab === 'deployments'" class="card">
        <div class="card-body">
          <ModuleDeploymentsByModule :module="module" />
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

.small {
  font-size: 12px;
}
</style>