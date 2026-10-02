<script setup lang="ts">
/**
 * Project settings.
 *
 * Everything here is a property of the project itself rather than of a file, and
 * each field is sent on its own so an unchanged field never overwrites a change
 * someone else made while the form was open.
 */
import type { ProjectSummary } from '~/types/repository'

const props = defineProps<{
  project: ProjectSummary
  canManage: boolean
}>()

const emit = defineEmits<{ (event: 'saved', project: ProjectSummary): void }>()

const form = reactive({
  name: props.project.name ?? '',
  description: props.project.description ?? '',
  visibility: props.project.visibility,
  default_branch: props.project.default_branch,
})

watch(
  () => props.project,
  (next) => {
    form.name = next.name ?? ''
    form.description = next.description ?? ''
    form.visibility = next.visibility
    form.default_branch = next.default_branch
  },
)

const saving = ref(false)
const saveError = ref('')
const saved = ref(false)

async function save() {
  saveError.value = ''
  saved.value = false
  saving.value = true
  try {
    const response = await api.patch<{ project: ProjectSummary }>(`/projects/${props.project.id}`, {
      name: form.name,
      description: form.description,
      visibility: form.visibility,
      default_branch: form.default_branch,
    })
    emit('saved', response.project)
    saved.value = true
    setTimeout(() => (saved.value = false), 2000)
  } catch (caught) {
    saveError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-header"><strong>Settings</strong></div>
    <div class="card-body">
      <div v-if="!canManage" class="alert">
        Only a project owner or maintainer can change these.
      </div>

      <form v-else @submit.prevent="save">
        <div class="field">
          <label for="project-name">Name</label>
          <input id="project-name" v-model="form.name" />
        </div>

        <div class="field">
          <label for="project-description">Description</label>
          <textarea
            id="project-description"
            v-model="form.description"
            rows="3"
            placeholder="What is this project for?"
          />
        </div>

        <div class="field">
          <label for="project-visibility">Visibility</label>
          <select id="project-visibility" v-model="form.visibility">
            <option value="private">Private</option>
            <option value="internal">Internal</option>
            <option value="public">Public</option>
          </select>
        </div>

        <div class="field">
          <label for="default-branch">Default branch</label>
          <input id="default-branch" v-model="form.default_branch" />
        </div>

        <div v-if="saveError" class="alert alert-error">{{ saveError }}</div>
        <div v-else-if="saved" class="alert">Saved.</div>

        <button class="btn btn-primary" type="submit" :disabled="saving">
          {{ saving ? 'Saving…' : 'Save changes' }}
        </button>
      </form>
    </div>
  </div>
</template>