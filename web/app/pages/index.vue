<script setup lang="ts">
/** Home page: counters, recent activity and the projects the user can work in. */
import type { DashboardResponse } from '~/types/dashboard'

const { user } = useAuth()

const dashboard = ref<DashboardResponse | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    dashboard.value = await api.get<DashboardResponse>('/dashboard')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const stats = computed(() => dashboard.value?.stats)

const tiles = computed(() => {
  if (!stats.value) return []
  return [
    { label: 'Projects', value: stats.value.projects },
    { label: 'Owned', value: stats.value.owned_projects },
    { label: 'Maintainer', value: stats.value.maintainer_projects },
    { label: 'Groups', value: stats.value.groups },
    { label: 'SSH keys', value: stats.value.ssh_keys },
  ]
})

const greeting = computed(() => {
  const hour = new Date().getHours()
  if (hour < 5) return 'Good evening'
  if (hour < 12) return 'Good morning'
  if (hour < 18) return 'Good afternoon'
  return 'Good evening'
})

/** Feed entries are rendered as "<actor> <summary> in <project>". */
function feedText(entry: { actor_name: string; summary: string; project_path: string }): string {
  return `${entry.actor_name} ${entry.summary} in ${entry.project_path}`
}
</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading your dashboard…</div>
    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <template v-else>
      <h1 class="page-title">{{ greeting }}, {{ user?.username }}</h1>
      <p class="page-subtitle">What has been happening across your projects.</p>

      <div class="stat-grid">
        <div v-for="tile in tiles" :key="tile.label" class="stat-tile">
          <div class="value">{{ tile.value }}</div>
          <div class="label">{{ tile.label }}</div>
        </div>
      </div>

      <div class="dashboard-columns">
        <section class="card">
          <div class="card-header"><strong>Recent activity</strong></div>
          <div v-if="dashboard?.activity.length" class="feed">
            <div v-for="entry in dashboard.activity" :key="entry.id" class="index-feed-row">
              <UserAvatar :name="entry.actor_name" :size="26" />
              <div class="feed-text">
                <div>{{ feedText(entry) }}</div>
                <div class="feed-meta">
                  <NuxtLink :to="`/p/${entry.project_path}`">{{ entry.project_name }}</NuxtLink>
                  <span v-if="entry.detail"> · {{ entry.detail }}</span>
                  · {{ timeAgo(entry.created_at) }}
                </div>
              </div>
            </div>
          </div>
          <div v-else class="feed-empty">
            Nothing yet. Push something to a project and it will show up here.
          </div>
        </section>

        <div style="display: grid; gap: 16px">
          <section class="card">
            <div class="card-header">
              <strong>Latest commits</strong>
              <NuxtLink to="/activity" class="muted" style="margin-left: auto; font-size: 12px">
                all activity
              </NuxtLink>
            </div>
            <div v-if="dashboard?.commits.length" class="feed">
              <div v-for="entry in dashboard.commits" :key="entry.sha" class="index-feed-row">
                <div class="feed-text">
                  <div class="truncate">{{ firstLine(entry.message) }}</div>
                  <div class="feed-meta">
                    <NuxtLink :to="`/p/${entry.project_path}/-/commit/${entry.sha}`">
                      <span class="sha">{{ entry.sha.slice(0, 8) }}</span>
                    </NuxtLink>
                    · {{ entry.author_name }} · {{ timeAgo(entry.timestamp) }}
                  </div>
                </div>
              </div>
            </div>
            <div v-else class="feed-empty">No commits recorded yet.</div>
          </section>

          <section class="card">
            <div class="card-header">
              <strong>Your projects</strong>
              <NuxtLink to="/projects" class="muted" style="margin-left: auto; font-size: 12px">
                view all
              </NuxtLink>
            </div>
            <div v-if="dashboard?.projects.length">
              <div v-for="project in dashboard.projects.slice(0, 6)" :key="project.id" class="project-row">
                <span class="name">
                  <NuxtLink :to="`/p/${project.path}`">{{ project.name || project.path }}</NuxtLink>
                </span>
                <span class="badge" :class="`badge-${project.visibility}`">{{ project.visibility }}</span>
              </div>
            </div>
            <div v-else class="feed-empty">No projects yet.</div>
          </section>
        </div>
      </div>
    </template>
  </div>
</template>

