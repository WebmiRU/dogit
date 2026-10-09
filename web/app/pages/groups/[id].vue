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

function formatProjectDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date)
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
  <div class="project-index group-detail-index">
    <div v-if="loading" class="spinner">Loading group…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <template v-else-if="group">
      <div class="repo-head project-index-head group-detail-head">
        <div class="title">
          <h1 class="page-title">{{ group.name || group.full_path }}</h1>
          <p class="page-subtitle">
            <span class="mono">{{ group.full_path }}</span>
            <span class="badge">{{ group.access_name }}</span>
          </p>
        </div>
        <NuxtLink class="btn group-back-button" to="/groups">
          <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
            <path d="m14 18-6-6 6-6M8 12h12" />
          </svg>
          <span>All groups</span>
        </NuxtLink>
      </div>

      <nav class="repo-tabs">
        <button
          v-for="one in tabs"
          :key="one.match"
          class="group-tab"
          :class="{ active: tab === one.match }"
          type="button"
          @click="setTab(one.match)"
        >
          {{ one.label }}
        </button>
      </nav>

      <div v-if="tab === 'projects'" class="group-projects">
        <div v-if="projects.length === 0" class="card empty group-projects-empty">
          No projects in this group yet. Create one from the
          <NuxtLink to="/projects">projects page</NuxtLink> with this namespace.
        </div>

        <div v-else class="group-projects-table-wrap">
          <table class="admin-table places-table group-projects-table">
            <thead>
              <tr>
                <th>Project</th>
                <th>Visibility</th>
                <th>Default branch</th>
                <th>Access</th>
                <th>Created</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="project in projects" :key="project.id">
                <td>
                  <div class="place-name-line">
                    <span class="place-avatar is-project">
                      {{ (project.name || project.path).slice(0, 1).toUpperCase() }}
                    </span>
                    <div class="place-name-content">
                      <NuxtLink :to="'/p/' + project.path" class="name">
                        {{ project.name || project.path }}
                      </NuxtLink>
                      <div class="muted small mono group-project-path">{{ project.path }}</div>
                      <div v-if="project.description" class="muted small place-description">
                        {{ project.description }}
                      </div>
                    </div>
                  </div>
                </td>
                <td>
                  <span class="badge badge-label" :class="'badge-' + project.visibility">
                    {{ project.visibility }}
                  </span>
                  <span v-if="project.archived_at" class="badge badge-neutral badge-label">Archived</span>
                </td>
                <td>
                  <span v-if="project.default_branch" class="mono small">{{ project.default_branch }}</span>
                  <span v-else class="muted small">—</span>
                </td>
                <td>
                  <span v-if="project.access_name" class="places-access-value">{{ project.access_name }}</span>
                  <span v-else class="muted small">—</span>
                </td>
                <td class="muted small">{{ formatProjectDate(project.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Who is told about this group's builds, and what each one says. -->
      <div v-else>
        <h2 class="page-title">Notifications</h2>
        <p class="page-subtitle">
          Who is told when something in this group happens. Every project below inherits
          these unless it says otherwise for itself.
        </p>

        <ModuleTargets scope="group" :scope-id="groupId" :can-manage="canManage" />
      </div>
    </template>
  </div>
</template>

