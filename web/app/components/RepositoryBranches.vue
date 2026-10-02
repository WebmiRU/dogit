<script setup lang="ts">
/** Branch list with a deletion action for maintainers. */
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  refName: string
}>()

const error = ref('')
const pending = ref<string | null>(null)

const branches = computed(() => props.refs?.branches ?? [])
const tags = computed(() => props.refs?.tags ?? [])

async function remove(name: string) {
  if (!confirm(`Delete branch ${name}?`)) return
  pending.value = name
  error.value = ''
  try {
    await api.delete(`/projects/${props.projectId}/repository/branches/${encodeURIComponent(name)}`)
    // Reload the ref list in place rather than reloading the whole page.
    refs.value = await api.get<RefsResponse>(`/projects/${props.projectId}/repository/refs`)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    pending.value = null
  }
}

const refs = defineModel<RefsResponse | null>('refs')
</script>

<template>
  <div>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div class="card" style="margin-bottom: 16px">
      <div class="toolbar"><strong>Branches ({{ branches.length }})</strong></div>
      <div v-if="branches.length === 0" class="empty">No branches yet.</div>
      <ul v-else class="tree-list">
        <li v-for="branch in branches" :key="branch.name">
          <span class="icon">⑂</span>
          <NuxtLink
            class="name"
            :to="`/p/${projectPath}/-/tree/${encodeURIComponent(branch.name)}`"
          >
            {{ branch.name }}
          </NuxtLink>
          <span v-if="branch.name === projectPath" class="badge">default</span>
          <span class="meta mono">{{ branch.target.slice(0, 8) }}</span>
          <button
            class="btn"
            type="button"
            :disabled="pending === branch.name"
            @click="remove(branch.name)"
          >
            Delete
          </button>
        </li>
      </ul>
    </div>

    <div class="card">
      <div class="toolbar"><strong>Tags ({{ tags.length }})</strong></div>
      <div v-if="tags.length === 0" class="empty">No tags yet.</div>
      <ul v-else class="tree-list">
        <li v-for="tag in tags" :key="tag.name">
          <span class="icon">◈</span>
          <NuxtLink
            class="name"
            :to="`/p/${projectPath}/-/tree/${encodeURIComponent(tag.name)}`"
          >
            {{ tag.name }}
          </NuxtLink>
          <span class="meta mono">{{ tag.target.slice(0, 8) }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>
