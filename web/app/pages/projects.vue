<script setup lang="ts">
/** Project list with a creation form. */
import type { GroupSummary } from '~/types/dashboard'
import type { ProjectSummary } from '~/types/repository'

// The groups the user may put a project into. A project created under a group gets
// that group's namespace in its path and inherits the group's members.
const groups = ref<GroupSummary[]>([])

async function loadGroups() {
  try {
    const answer = await api.get<{ groups: GroupSummary[] }>('/groups')
    groups.value = answer.groups ?? []
  } catch {
    // A missing list of namespaces is not worth an error above the form: the project
    // can still be created without one.
    groups.value = []
  }
}

onMounted(loadGroups)

const showForm = ref(false)
const creating = ref(false)
const createError = ref('')
const form = reactive({
  group_path: '',
  path: '',
  name: '',
  description: '',
  visibility: 'private',
  initialize_with_readme: true,
})

async function createProject() {
  createError.value = ''
  creating.value = true
  try {
    const response = await api.post<{ project: ProjectSummary }>('/projects', {
      ...form,
      // An empty namespace means the project is not in a group.
      group_path: form.group_path || undefined,
    })
    await navigateTo(`/p/${response.project.path}`)
  } catch (caught) {
    createError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <div class="project-index">
    <div class="repo-head project-index-head">
      <div class="title">
        <h1 class="page-title">Projects and groups</h1>
        
      </div>
      <button class="btn project-create-button" type="button" @click="showForm = !showForm">
        <span class="project-create-icon" aria-hidden="true">{{ showForm ? '−' : '+' }}</span>
        {{ showForm ? 'Cancel' : 'New project' }}
      </button>
    </div>

    <div v-if="showForm" class="card" style="margin-bottom: 20px">
      <div class="card-header"><strong>New project</strong></div>
      <div class="card-body">
        <div v-if="createError" class="alert alert-error">{{ createError }}</div>

        <form @submit.prevent="createProject">
          <div class="field">
            <label for="group">Namespace</label>
            <select id="group" v-model="form.group_path">
              <option value="">— no group —</option>
              <option v-for="group in groups" :key="group.id" :value="group.full_path">
                {{ group.full_path }}
              </option>
            </select>
          </div>
          <div class="field">
            <label for="path">Path</label>
            <input
              id="path"
              v-model="form.path"
              :placeholder="form.group_path ? 'my-project' : 'my-project or team/my-project'"
              required
            />
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
          <div class="field">
            <label class="checkbox">
              <input v-model="form.initialize_with_readme" type="checkbox" />
              Initialise with a README
            </label>
          </div>
          <button class="btn btn-primary" type="submit" :disabled="creating">
            {{ creating ? 'Creating…' : 'Create project' }}
          </button>
        </form>
      </div>
    </div>

    <nav class="project-index-tabs" aria-label="Project views">
      <NuxtLink to="/projects" :class="{ active: !$route.query.type }">All</NuxtLink>
      <NuxtLink to="/projects?type=project" :class="{ active: $route.query.type === 'project' }">Projects</NuxtLink>
      <NuxtLink to="/projects?type=group" :class="{ active: $route.query.type === 'group' }">Groups</NuxtLink>
    </nav>

    <PlacesTable />

  </div>
</template>

<style scoped>
.checkbox {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text);
  font-size: 13px;
}

.checkbox input {
  width: auto;
}
</style>
