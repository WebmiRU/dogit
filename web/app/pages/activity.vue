<script setup lang="ts">
/** Instance-wide activity feed. */
import type { ActivityEntry } from '~/types/dashboard'

const entries = ref<ActivityEntry[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const response = await api.get<{ activity: ActivityEntry[] }>('/dashboard', { activity: 100 })
    entries.value = response.activity
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

// Pushes arrive while this page is open, so a slow poll keeps it current without
// a websocket for what is, at this point, a short list.
const timer = setInterval(load, 15_000)
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <div class="activity-page">
    <header class="repo-head activity-heading">
      <div class="title">
        <h1 class="page-title">Activity</h1>
        <p class="page-subtitle">Everything that happened across your projects.</p>
      </div>
    </header>

    <div class="activity-feed">
      <div v-if="loading && entries.length === 0" class="spinner">Loading activity…</div>
      <div v-else-if="error" class="alert alert-error activity-error">{{ error }}</div>
      <div v-else-if="entries.length === 0" class="feed-empty">Nothing has happened yet.</div>
      <div v-else class="feed">
        <div v-for="entry in entries" :key="entry.id" class="activity-feed-row">
          <UserAvatar :name="entry.actor_name" :size="28" />
          <div class="feed-text">
            <div class="activity-summary">{{ entry.actor_name }} {{ entry.summary }} in <NuxtLink :to="`/p/${entry.project_path}`">{{ entry.project_path }}</NuxtLink></div>
            <div class="feed-meta">
              <NuxtLink :to="`/p/${entry.project_path}`">{{ entry.project_name }}</NuxtLink>
              <span v-if="entry.detail"> · {{ entry.detail }}</span>
              · {{ timeAgo(entry.created_at) }}
              <span v-if="!entry.created_at">· </span>
              <span v-if="entry.created_at" class="feed-when">{{ formatDate(entry.created_at) }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

