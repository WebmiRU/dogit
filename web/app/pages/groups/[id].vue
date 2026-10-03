<script setup lang="ts">
/**
 * A group: what it holds, and where it sends notifications.
 *
 * Two tabs, and the second one exists for the same reason the project's does. A group
 * is the level between the instance and the repositories, so it is where the middle of
 * the hierarchy has to be visible: a team with a shared chat of its own sets it here,
 * and every project below inherits it without being touched.
 *
 * Both tabs are here whether or not anything is configured. A group whose owner has
 * never heard of notification modules should be able to open this and find out that
 * they exist, which is the only way anybody finds out.
 */
import type { GroupSummary } from '~/types/dashboard'
import type { ProjectSummary } from '~/types/repository'

const route = useRoute()
const { ensureLoaded } = useAuth()
await ensureLoaded()

const groupId = computed(() => String(route.params.id ?? ''))

const group = ref<GroupSummary | null>(null)
const projects = ref<ProjectSummary[]>([])
const loading = ref(true)
const loadError = ref('')
const canManage = ref(false)

const tabs = [
  { label: 'Projects', match: 'projects' },
  { label: 'Notifications', match: 'notifications' },
]

const tab = computed(() => {
  const asked = String(route.query.tab ?? 'projects')
  return tabs.some((one) => one.match === asked) ? asked : 'projects'
})

function setTab(name: string) {
  navigateTo({ path: route.path, query: name === 'projects' ? {} : { tab: name } }, { replace: true })
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const details = await api.get<{ group: GroupSummary; can_manage?: boolean }>(
      `/groups/${groupId.value}`,
    )
    group.value = details.group
    canManage.value = details.can_manage ?? false

    // The project list is only asked for when its tab is the one being shown: the
    // other tab has its own list to fetch, and asking for both on every visit would
    // fetch one of them for nobody.
    if (tab.value === 'projects') {
      const listing = await api.get<{ projects: ProjectSummary[] }>(`/groups/${groupId.value}/projects`)
      projects.value = listing.projects ?? []
    }
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch([groupId, tab], load)
</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading group…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <template v-else-if="group">
      <div class="repo-head">
        <div class="title">
          <h1 class="page-title">{{ group.name || group.full_path }}</h1>
          <p class="page-subtitle">
            <span class="mono">{{ group.full_path }}</span>
            <span class="badge">{{ group.access_name }}</span>
          </p>
        </div>
        <NuxtLink class="btn" to="/groups">All groups</NuxtLink>
      </div>

      <nav class="repo-tabs">
        <button
          v-for="one in tabs"
          :key="one.match"
          class="tab"
          :class="{ active: tab === one.match }"
          type="button"
          @click="setTab(one.match)"
        >
          {{ one.label }}
        </button>
      </nav>

      <div v-if="tab === 'projects'">
        <div v-if="projects.length === 0" class="card empty">
          No projects in this group yet. Create one from the
          <NuxtLink to="/projects">projects page</NuxtLink> with this namespace.
        </div>
        <div v-else class="project-grid">
          <article v-for="project in projects" :key="project.id" class="project-card">
            <h3>
              <NuxtLink :to="`/p/${project.path}`">{{ project.name || project.path }}</NuxtLink>
            </h3>
            <p>{{ project.description || 'No description' }}</p>
            <div class="meta">
              <span class="mono">{{ project.path }}</span>
              <span class="badge" :class="`badge-${project.visibility}`">{{ project.visibility }}</span>
            </div>
          </article>
        </div>
      </div>

      <!-- Who is told about this group's builds, and what each one says. -->
      <div v-else>
        <h2 class="page-title">Notifications</h2>
        <p class="page-subtitle">
          Who is told when something in this group happens. Every project below inherits
          these unless it says otherwise for itself.
        </p>

        <NotificationRecipients scope="group" :scope-id="groupId" :can-manage="canManage" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.tab {
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-muted);
  cursor: pointer;
  font: inherit;
  padding: 10px 14px;
}

.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
}
</style>