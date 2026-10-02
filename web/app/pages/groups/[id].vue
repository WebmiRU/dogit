<script setup lang="ts">
/** A group and the projects inside it. */
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

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [details, listing] = await Promise.all([
      api.get<{ group: GroupSummary }>(`/groups/${groupId.value}`),
      api.get<{ projects: ProjectSummary[] }>(`/groups/${groupId.value}/projects`),
    ])
    group.value = details.group
    projects.value = listing.projects
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(groupId, load)
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

      <div class="card-header" style="margin-top: 16px">
        <strong>Projects</strong>
      </div>

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
    </template>
  </div>
</template>