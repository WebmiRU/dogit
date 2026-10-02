<script setup lang="ts">
/**
 * Tag list.
 *
 * Tags are on their own page rather than under the branch list: a repository with
 * hundreds of branches would push every tag out of sight, and tags are looked up
 * by name far more often than they are scanned.
 */
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
}>()

const emit = defineEmits<{ (event: 'refs-changed'): void }>()

const error = ref('')
const pending = ref<string | null>(null)
const filter = ref('')

const allTags = computed(() => props.refs?.tags ?? [])

const tags = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return allTags.value
  return allTags.value.filter((tag) => tag.name.toLowerCase().includes(needle))
})

async function remove(name: string) {
  if (!confirm(`Delete tag ${name}?`)) return
  pending.value = name
  error.value = ''
  try {
    await api.delete(`/projects/${props.projectId}/repository/tags/${encodeURIComponent(name)}`)
    emit('refs-changed')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    pending.value = null
  }
}
</script>

<template>
  <div>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div class="card">
      <div class="toolbar">
        <strong>Tags ({{ allTags.length }})</strong>
        <div class="spacer" />
        <div class="field" style="max-width: 260px; margin: 0">
          <input v-model="filter" type="search" placeholder="Filter tags" />
        </div>
      </div>

      <div v-if="allTags.length === 0" class="empty">
        No tags yet. A tag is created from the API; nothing in this interface makes one yet.
      </div>
      <div v-else-if="tags.length === 0" class="empty">No tag matches “{{ filter }}”.</div>
      <ul v-else class="tree-list">
        <li v-for="tag in tags" :key="tag.name">
          <span class="icon">◈</span>
          <NuxtLink
            class="name"
            :to="repoViewUrl(projectPath, 'tree', tag.name)"
          >
            {{ tag.name }}
          </NuxtLink>
          <span class="meta mono">{{ tag.target.slice(0, 8) }}</span>
          <button
            class="btn"
            type="button"
            :disabled="pending === tag.name"
            @click="remove(tag.name)"
          >
            {{ pending === tag.name ? 'Deleting…' : 'Delete' }}
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>