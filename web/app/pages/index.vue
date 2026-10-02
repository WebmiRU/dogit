<script setup lang="ts">
/** Project list and creation form. */
import type { ProjectSummary } from '~/types/repository'

const projects = ref<ProjectSummary[]>([])
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<{ projects: ProjectSummary[] }>('/projects')
    projects.value = response.projects
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const showForm = ref(false)
const creating = ref(false)
const createError = ref('')
const form = reactive({ path: '', name: '', description: '', visibility: 'private' })

async function createProject() {
  createError.value = ''
  creating.value = true
  try {
    const response = await api.post<{ project: ProjectSummary }>('/projects', { ...form })
    await navigateTo(`/p/${response.project.path}`)
  } catch (caught) {
    createError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title">Projects</h1>
        <p class="page-subtitle">
          {{ projects.length }} {{ projects.length === 1 ? 'project' : 'projects' }}
        </p>
      </div>
      <button class="btn btn-primary" type="button" @click="showForm = !showForm">
        {{ showForm ? 'Cancel' : 'New project' }}
      </button>
    </div>

    <div v-if="showForm" class="card" style="margin-bottom: 20px">
      <div class="card-header"><strong>New project</strong></div>
      <div class="card-body">
        <div v-if="createError" class="alert alert-error">{{ createError }}</div>

        <form @submit.prevent="createProject">
          <div class="field">
            <label for="path">Path</label>
            <input id="path" v-model="form.path" placeholder="my-project or team/my-project" required />
          </div>
          <div class="field">
            <label for="name">Name</label>
            <input id="name" v-model="form.name" placeholder="My project" />
          </div>
          <div class="field">
            <label for="description">Description</label>
            <input id="description" v-model="form.description" />
          </div>
          <div class="field">
            <label for="visibility">Visibility</label>
            <select id="visibility" v-model="form.visibility">
              <option value="private">Private</option>
              <option value="internal">Internal</option>
              <option value="public">Public</option>
            </select>
          </div>
          <button class="btn btn-primary" type="submit" :disabled="creating">
            {{ creating ? 'Creating…' : 'Create project' }}
          </button>
        </form>
      </div>
    </div>

    <div v-if="loading" class="spinner">Loading projects…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>
    <div v-else-if="projects.length === 0" class="card empty">
      No projects yet. Create the first one above.
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
          <span class="badge">{{ project.access_name }}</span>
        </div>
      </article>
    </div>
  </div>
</template>
