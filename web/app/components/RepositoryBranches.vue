<script setup lang="ts">
/** Branch list with a deletion action for maintainers. */
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  refName: string
  defaultBranch: string
}>()

// The list asks the page to reload the refs rather than keeping its own copy.
// A local copy would update the rows but not the branch selector in the header,
// which reads the same data from the page — the two would disagree until the
// next full reload.
const emit = defineEmits<{
  (event: 'refs-changed'): void
  (event: 'change-ref', ref: string): void
}>()

const error = ref('')
const pending = ref<string | null>(null)

const allBranches = computed(() => props.refs?.branches ?? [])
const filter = ref('')

const branches = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return allBranches.value
  return allBranches.value.filter((branch) => branch.name.toLowerCase().includes(needle))
})

/** The default branch is marked as such, and cannot be deleted. */
function isDefault(name: string) {
  return name === props.defaultBranch
}

async function remove(name: string) {
  if (!confirm(`Delete branch ${name}?`)) return
  pending.value = name
  error.value = ''
  try {
    await api.delete(`/projects/${props.projectId}/repository/branches/${encodeURIComponent(name)}`)
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

    <div class="card" style="margin-bottom: 16px">
      <div class="toolbar">
        <strong>Branches ({{ allBranches.length }})</strong>
        <div class="spacer" />
        <div class="field" style="max-width: 260px; margin: 0">
          <input v-model="filter" type="search" placeholder="Filter branches" />
        </div>
      </div>

      <div v-if="allBranches.length === 0" class="empty">No branches yet.</div>
      <div v-else-if="branches.length === 0" class="empty">
        No branch matches “{{ filter }}”.
      </div>
      <ul v-else class="tree-list">
        <li v-for="branch in branches" :key="branch.name">
          <span class="icon">⑂</span>
          <NuxtLink
            class="name"
            :to="repoViewUrl(projectPath, 'tree', branch.name)"
          >
            {{ branch.name }}
          </NuxtLink>
          <span v-if="isDefault(branch.name)" class="badge">Default</span>
          <span v-if="branch.name === refName" class="badge">Current</span>
          <span class="meta mono">{{ branch.target.slice(0, 8) }}</span>
          <!-- The default branch is not offered: the server refuses it, and a
               button that always fails reads as a broken page. -->
          <button
            v-if="!isDefault(branch.name)"
            class="btn"
            type="button"
            :disabled="pending === branch.name"
            @click="remove(branch.name)"
          >
            {{ pending === branch.name ? 'Deleting…' : 'Delete' }}
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>