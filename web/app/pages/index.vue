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

/**
 * The counters, with the mark each one wears.
 *
 * A mark rather than a bare number because five numbers in a row are five numbers with
 * nothing to tell them apart: which of these is the one about projects and which is
 * about keys is something a reader should not have to recall from the label underneath.
 */
const tiles = computed(() => {
  if (!stats.value) return []
  return [
    { label: 'Projects', value: stats.value.projects, icon: 'repo' },
    { label: 'Owned', value: stats.value.owned_projects, icon: 'owner' },
    { label: 'Maintainer', value: stats.value.maintainer_projects, icon: 'maint' },
    { label: 'Groups', value: stats.value.groups, icon: 'group' },
    { label: 'SSH keys', value: stats.value.ssh_keys, icon: 'key' },
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

/**
 * The name of a project, when there is one.
 *
 * A feed row without one says "in " and stops, which reads as a line that failed to load
 * rather than a project the reader is not allowed to see. These are events on projects
 * this user can read, so the name is there — but the path is the fallback rather than the
 * heading, because a path is what a person can paste into a URL and a name is what a
 * person recognises.
 */
function projectLabel(entry: { project_name: string; project_path: string }): string {
  return entry.project_name || entry.project_path
}
</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading your dashboard…</div>
    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <template v-else>
      <!-- The same header card the project pages open with: a mark, where you are,
           what this page is, and one line about it. It is here so that arriving at
           the dashboard and arriving at a project are two arrivals in one building
           rather than two interfaces that happen to share a colour scheme. -->
      <div class="repo-head dashboard-head">
        <div class="project-identity">
          <div class="project-mark" aria-hidden="true">◆</div>
          <div class="title project-title-block">
            <div class="project-path dashboard-mark-label">
              {{ greeting }}, {{ user?.username }}
            </div>
            <h1 class="page-title project-page-title">Dashboard</h1>
            <p class="page-subtitle">
              What has been happening across your projects.
            </p>
          </div>
        </div>
      </div>

      <section class="dashboard-section">
        <h2 class="dashboard-section-title">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <rect x="1.8" y="2.2" width="5.4" height="5.4" rx="1" />
            <rect x="8.8" y="8.4" width="5.4" height="5.4" rx="1" />
            <path d="M7.2 4.9h3.4v3.5M4.5 7.6v3.5h3.5" />
          </svg>
          <span>At a glance</span>
        </h2>

        <div class="dashboard-stats">
          <div v-for="tile in tiles" :key="tile.label" class="dashboard-stat">
            <span class="dashboard-stat-icon" aria-hidden="true">
              <svg viewBox="0 0 16 16">
                <template v-if="tile.icon === 'repo'">
                  <path d="m2 4 6-2.5L14 4v8l-6 2.5L2 12z" />
                  <path d="M2 4l6 3 6-3M8 7v7.5" />
                </template>
                <template v-else-if="tile.icon === 'owner'">
                  <circle cx="8" cy="5.5" r="2.6" />
                  <path d="M2.8 14c0-2.8 2.3-4.4 5.2-4.4s5.2 1.6 5.2 4.4" />
                </template>
                <template v-else-if="tile.icon === 'maint'">
                  <path d="M8 1.6 9.9 4l2.8.4-2 2 .5 2.8L9 8l-1.2 1.2.5 2.8-2-2L4.2 11l.5-2.8-2-2L5.4 4z" />
                </template>
                <template v-else-if="tile.icon === 'group'">
                  <circle cx="5.6" cy="5.4" r="2.3" />
                  <circle cx="11.4" cy="6" r="1.8" />
                  <path d="M1.6 13.4c0-2.4 1.8-3.6 4-3.6s4 1.2 4 3.6M10.2 9.9c2 .1 4.2.8 4.2 3.5" />
                </template>
                <template v-else>
                  <circle cx="4.4" cy="8.4" r="2.6" />
                  <path d="M7 8.4h6.4M11.4 6.2l2.2 2.2-2.2 2.2" />
                </template>
              </svg>
            </span>
            <div class="dashboard-stat-text">
              <div class="dashboard-stat-value">{{ tile.value }}</div>
              <div class="dashboard-stat-label">{{ tile.label }}</div>
            </div>
          </div>
        </div>
      </section>

      <section class="dashboard-section">
        <h2 class="dashboard-section-title">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <circle cx="8" cy="8" r="5.6" />
            <path d="M8 4.4v3.8l2.6 1.5M2.4 4.4v-2M2.4 4.4h2" />
          </svg>
          <span>Recent</span>
        </h2>

        <div class="dashboard-columns">
          <section class="card">
            <div class="dashboard-card-header">
              <span class="dashboard-card-title">
                <svg viewBox="0 0 16 16" aria-hidden="true">
                  <path d="M12 6.6a4 4 0 0 0-8 0c0 4.4-1.5 4.4-1.5 5.4h11c0-1-1.5-1-1.5-5.4ZM6.4 14h3.2" />
                </svg>
                Activity
              </span>
            </div>
            <div v-if="dashboard?.activity.length">
              <div v-for="entry in dashboard.activity" :key="entry.id" class="dashboard-row">
                <UserAvatar :name="entry.actor_name" :size="26" />
                <div class="dashboard-row-text">
                  <div>{{ entry.actor_name }} {{ entry.summary }}</div>
                  <div class="dashboard-row-meta">
                    <NuxtLink :to="`/p/${entry.project_path}`">
                      {{ projectLabel(entry) }}
                    </NuxtLink>
                    <span v-if="entry.detail"> · {{ entry.detail }}</span>
                    <span> · {{ timeAgo(entry.created_at) }}</span>
                  </div>
                </div>
              </div>
            </div>
            <div v-else class="dashboard-empty">
              Nothing yet. Push something to a project and it will show up here.
            </div>
          </section>

          <div class="dashboard-side">
            <section class="card">
              <div class="dashboard-card-header">
                <span class="dashboard-card-title">
                  <svg viewBox="0 0 16 16" aria-hidden="true">
                    <circle cx="4" cy="3.2" r="1.5" />
                    <circle cx="4" cy="12.8" r="1.5" />
                    <circle cx="12" cy="8" r="1.5" />
                    <path d="M4 4.7v6.6M4 8c0-2.1 1.8-3 4-3h2.5" />
                  </svg>
                  Latest commits
                </span>
                <NuxtLink to="/activity" class="muted dashboard-card-link">all activity</NuxtLink>
              </div>
              <div v-if="dashboard?.commits.length">
                <div v-for="entry in dashboard.commits" :key="entry.sha" class="dashboard-row">
                  <div class="dashboard-row-text">
                    <div class="truncate">{{ firstLine(entry.message) }}</div>
                    <div class="dashboard-row-meta">
                      <NuxtLink :to="`/p/${entry.project_path}/-/commit/${entry.sha}`">
                        <span class="sha">{{ entry.sha.slice(0, 8) }}</span>
                      </NuxtLink>
                      <NuxtLink :to="`/p/${entry.project_path}`">
                        {{ projectLabel(entry) }}
                      </NuxtLink>
                      <span> · {{ entry.author_name }}</span>
                      <span> · {{ timeAgo(entry.timestamp) }}</span>
                    </div>
                  </div>
                </div>
              </div>
              <div v-else class="dashboard-empty">No commits recorded yet.</div>
            </section>

            <section class="card">
              <div class="dashboard-card-header">
                <span class="dashboard-card-title">
                  <svg viewBox="0 0 16 16" aria-hidden="true">
                    <path d="m8 1.8 5.5 2.8v6.8L8 14.2l-5.5-2.8V4.6z" />
                    <path d="M2.5 4.6 8 7.5l5.5-2.9M8 7.5v6.7" />
                  </svg>
                  Your projects
                </span>
                <NuxtLink to="/projects" class="muted dashboard-card-link">view all</NuxtLink>
              </div>
              <div v-if="dashboard?.projects.length">
                <div
                  v-for="project in dashboard.projects.slice(0, 6)"
                  :key="project.id"
                  class="dashboard-project-row"
                >
                  <span class="dashboard-project-name">
                    <NuxtLink :to="`/p/${project.path}`">
                      {{ project.name || project.path }}
                    </NuxtLink>
                  </span>
                  <span class="badge" :class="`badge-${project.visibility}`">
                    {{ project.visibility }}
                  </span>
                </div>
              </div>
              <div v-else class="dashboard-empty">No projects yet.</div>
            </section>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

