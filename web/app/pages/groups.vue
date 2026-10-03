<script setup lang="ts">
/**
 * Groups, and their creation.
 *
 * The list is the shared one — the same table as the projects page, showing the group
 * rows of it. A group is a namespace holding projects, so a page that lists one kind
 * and not the other makes somebody look in two places for the same question.
 */
/** Redrawn after a group is created, which is the one thing the table cannot notice. */
const created = ref(0)

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
    created.value++
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

    <!-- Keyed on the create counter so a new group shows up without a reload. -->
    <PlacesTable :key="created" type="group" :filter-kind="false" />

  </div>
</template>
