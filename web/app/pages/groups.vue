<script setup lang="ts">
/** Group list and creation. */
import type { GroupSummary } from '~/types/dashboard'

const groups = ref<GroupSummary[]>([])
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<{ groups: GroupSummary[] }>('/groups')
    groups.value = response.groups
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
const form = reactive({ slug: '', name: '' })

async function createGroup() {
  createError.value = ''
  creating.value = true
  try {
    await api.post('/groups', { ...form })
    form.slug = ''
    form.name = ''
    showForm.value = false
    await load()
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
        <h1 class="page-title">Groups</h1>
        <p class="page-subtitle">
          Groups own projects and carry the access levels their members have.
        </p>
      </div>
      <button class="btn btn-primary" type="button" @click="showForm = !showForm">
        {{ showForm ? 'Cancel' : 'New group' }}
      </button>
    </div>

    <div v-if="showForm" class="card" style="margin-bottom: 20px">
      <div class="card-header"><strong>New group</strong></div>
      <div class="card-body">
        <div v-if="createError" class="alert alert-error">{{ createError }}</div>
        <form @submit.prevent="createGroup">
          <div class="field">
            <label for="slug">Path</label>
            <input id="slug" v-model="form.slug" placeholder="platform" required />
          </div>
          <div class="field">
            <label for="gname">Name</label>
            <input id="gname" v-model="form.name" placeholder="Platform team" />
          </div>
          <button class="btn btn-primary" type="submit" :disabled="creating">
            {{ creating ? 'Creating…' : 'Create group' }}
          </button>
        </form>
      </div>
    </div>

    <div v-if="loading" class="spinner">Loading groups…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>
    <div v-else-if="groups.length === 0" class="card empty">
      No groups yet. A group bundles projects and their members.
    </div>
    <div v-else class="project-grid">
      <article v-for="group in groups" :key="group.id" class="project-card">
        <h3>{{ group.name || group.slug }}</h3>
        <p class="mono">{{ group.full_path }}</p>
        <div class="meta">
          <span class="badge">{{ group.access_name }}</span>
        </div>
      </article>
    </div>
  </div>
</template>
