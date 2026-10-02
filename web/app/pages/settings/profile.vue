<script setup lang="ts">
/** Profile settings: the display name and the email used for notifications. */
const { user } = useAuth()

const form = reactive({
  name: user.value?.name ?? '',
  email: user.value?.email ?? '',
})

const saving = ref(false)
const saved = ref(false)
const error = ref('')

async function save() {
  saving.value = true
  saved.value = false
  error.value = ''
  try {
    const response = await api.patch<{ user: unknown }>('/user', {
      name: form.name,
      email: form.email,
    })
    // The API returns the updated user; refreshing the shared state keeps the
    // header and avatar in sync without a reload.
    await useAuth().ensureLoaded(true)
    void response
    saved.value = true
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <h1 class="page-title">Profile</h1>
    <p class="page-subtitle">How you appear in commits, merge requests and comments.</p>

    <div v-if="!user" class="card empty">Not signed in.</div>
    <div v-else class="card" style="max-width: 520px">
      <div class="card-body">
        <div v-if="error" class="alert alert-error">{{ error }}</div>
        <div v-if="saved" class="alert alert-info">Profile updated.</div>

        <div style="display: flex; align-items: center; gap: 12px; margin-bottom: 18px">
          <UserAvatar :name="user.username" :size="56" />
          <div>
            <div style="font-weight: 600">{{ user.username }}</div>
            <div class="muted">{{ user.is_admin ? 'Administrator' : 'Member' }}</div>
          </div>
        </div>

        <form @submit.prevent="save">
          <div class="field">
            <label for="username">Username</label>
            <input id="username" :value="user.username" disabled />
          </div>
          <div class="field">
            <label for="name">Display name</label>
            <input id="name" v-model="form.name" placeholder="Alice Smith" />
          </div>
          <div class="field">
            <label for="email">Email</label>
            <input id="email" v-model="form.email" type="email" />
          </div>
          <button class="btn btn-primary" type="submit" :disabled="saving">
            {{ saving ? 'Saving…' : 'Save changes' }}
          </button>
        </form>
      </div>
    </div>
  </div>
</template>
