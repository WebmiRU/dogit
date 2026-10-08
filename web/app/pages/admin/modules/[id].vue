<script setup lang="ts">
/** One module: what it is, what it uses, what it holds, and how to remove it. */
import type { ModuleRow, ModuleStats } from '~/types/module'

const route = useRoute()
const { ensureLoaded } = useAuth()
await ensureLoaded()

const moduleId = computed(() => String(route.params.id ?? ''))

/**
 * Where this module's data lives, as the core puts it.
 *
 * Three shapes and none of them is a boolean, because "does this module have a database" has
 * three different answers and each of them needs somebody to do something: it is holding a
 * resource, it has a name nobody kept a resource for, or it declared from the start that it
 * keeps its data elsewhere. The address is not here and is not fetched — it is a password.
 */
interface ModuleResource {
  id: string
  kind: string
  software?: string
  version?: string
  name?: string
  origin: 'managed' | 'manual' | string
  /** Where it is. Never a secret — a page can show a host without opening a password. */
  parts?: Record<string, string>
  created_at: string
  released_at?: string
}

interface ModuleSlot {
  key: string
  kind: string
  software?: string
  required: boolean
  held?: ModuleResource | null
  free?: ModuleResource[]
}

interface ModuleDatabase {
  /** Whether the module asked the core for a database when it registered. */
  wants_one: boolean
  /** The name given to it, empty when it has none. */
  name: string
  role?: string
  /** The resource being held for it, absent when there is none. */
  resource?: ModuleResource
  /** One slot the module asked for, with what is in it and what could go in it. */
  slots?: ModuleSlot[]
}

/** Whether this module is one that asks for a resource at all, from its own manifest. */
const wantsResource = computed(() => Boolean(module.value?.manifest?.database))

const module = ref<ModuleRow | null>(null)
const database = ref<ModuleDatabase | null>(null)
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
  // What this module's data lives in. Its own tab rather than a line above the settings
  // form, because there is more to say about it than one line holds: what it is, where it
  // is, where it came from, what is waiting for it, and — the part that matters — that a
  // module which asked for a database and has none is a gap rather than a choice.
  ...(wantsResource.value || database.value?.slots?.length ? ['resources'] : []),
  'removal',
])

const tabTitles: Record<string, string> = {
  overview: 'Overview',
  images: 'Images',
  notifications: 'Notifications',
  deployments: 'Deployments',
  resources: 'Resources',
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
      api.get<{ module: ModuleRow; settings: Record<string, unknown>; database?: ModuleDatabase }>(`/modules/${moduleId.value}`),
      // A module that has never reported answers with nothing, which is not an
      // error: statistics are new, and a module may not have been restarted since.
      api.get<{ latest: ModuleStats | null; series?: ModuleStats[] }>(`/modules/${moduleId.value}/stats`, { series: 1 })
        .catch(() => ({ latest: null })),
    ])
    module.value = answer.module
    database.value = answer.database ?? null
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

/**
 * The database line, worded once so that the four cases cannot drift apart.
 *
 * Each carries its consequence rather than its state alone. "None" means two different things
 * depending on whether the module wanted one, and a page that said only the word would leave
 * the reader to work out which of them they are looking at — and the one where they are looking
 * at a gap is the one somebody has to go and fix.
 */
const databaseLine = computed(() => {
  const db = database.value
  if (!db) return null

  const held = db.resource
  if (held) {
    return {
      name: held.name || db.name || 'unnamed',
      badges: [held.kind, held.software, held.version].filter(Boolean).join(':') || '',
      // Short, because this sits in a row with other facts in it, and a badge that needs
      // two lines stops being a badge. The consequence goes in the note below instead.
      origin: held.origin === 'manual'
        ? 'described by an administrator'
        : 'made by this instance',
      since: held.created_at,
      link: held.id,
      // The difference decides who may act on it: this instance made the one it may destroy,
      // and may only forget the one somebody else owns.
      note: held.origin === 'manual'
        ? 'on a host this instance does not run, so it is never dropped from here — only forgotten'
        : '',
      warn: false,
    }
  }

  if (db.name) {
    // A database with no resource behind it: provisioned before this instance kept a list of
    // them. Said plainly rather than hidden, because it is the answer to "why is this one not
    // on the Resources page", and a reader who cannot get there will assume the page is broken.
    return {
      name: db.name,
      badges: '',
      origin: '',
      since: '',
      link: '',
      note: 'made before this instance kept a list of resources, so it is on no page; the '
          + 'module still uses it, and only the module decides to let go of it',
      warn: false,
    }
  }

  if (db.wants_one) {
    return {
      name: 'none',
      badges: '',
      origin: '',
      since: '',
      link: '',
      note: 'this module asked for a database when it registered and has none — a gap, not a '
          + 'choice, and worth telling somebody about',
      warn: true,
    }
  }

  return {
    name: 'none',
    badges: '',
    origin: '',
    since: '',
    link: '',
    note: 'this module declared from the start that it keeps its data elsewhere',
    warn: false,
  }
})

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
            <span v-if="!module.enabled" class="badge badge-warning">Forbidden</span>
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
              :can-edit="true"
              note="These are the instance-wide values. Group and project scopes override them and are configured on the group or project page."
            />
          </div>
        </div>
      </section>

      <!-- Where this module's data lives, and what is waiting for it. -->
      <section v-else-if="tab === 'resources'">
        <ModuleResources
          :needs="database?.slots ?? []"
          :wants-any="Boolean(database?.wants_one)"
          @reload="load(true)"
        />
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

/* The database line, set apart from the form below it without a card of its own: it is one
   fact about the module, not a section of its own, and a box around it would make the eye
   read it as something to be filled in. */
.facts-db {
  padding: 0 0 14px;
  margin: 0 0 16px;
  border-bottom: 1px solid var(--border);
}

.section-title {
  margin: 22px 0 10px;
}

.small {
  font-size: 12px;
}
</style>