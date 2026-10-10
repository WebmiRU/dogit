<script setup lang="ts">
/** Branch list with a deletion action for maintainers. */
import type { RefsResponse } from '~/types/repository'
import { timeAgo } from '~/utils/format'

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
const open = ref<string | null>(null)
const panel = ref({ top: 0, left: 0, up: false })

const openedBranch = computed(() =>
  open.value ? branches.value.find((branch) => branch.name === open.value) ?? null : null,
)

function showMenu(name: string, event: MouseEvent) {
  if (open.value === name) {
    closeMenu()
    return
  }
  const box = (event.currentTarget as HTMLElement).getBoundingClientRect()
  panel.value = {
    top: box.bottom + 6,
    left: box.right - 190,
    up: window.innerHeight - box.bottom < 150,
  }
  open.value = name
}

function closeMenu() {
  open.value = null
}

function onPointerAway(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as HTMLElement | null
  if (target?.closest('.row-menu-panel') || target?.closest('.row-menu')) return
  closeMenu()
}

function onKeyAway(event: KeyboardEvent) {
  if (event.key === 'Escape') closeMenu()
}

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

onMounted(() => {
  window.addEventListener('pointerdown', onPointerAway, true)
  window.addEventListener('keydown', onKeyAway)
  window.addEventListener('scroll', closeMenu, true)
})

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onPointerAway, true)
  window.removeEventListener('keydown', onKeyAway)
  window.removeEventListener('scroll', closeMenu, true)
})

async function remove(name: string) {
  closeMenu()
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

    <div>
      <div class="toolbar repository-toolbar repository-page-toolbar">
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
      <div v-else class="repository-table-wrap">
        <table class="admin-table repository-flat-table branches-table">
          <thead><tr><th>Branch</th><th>Last commit</th><th>Updated</th><th class="numeric">Actions</th></tr></thead>
          <tbody>
            <tr v-for="branch in branches" :key="branch.name">
              <td>
                <div class="branch-name-line">
                  <NuxtLink class="name" :to="repoViewUrl(projectPath, 'tree', branch.name)">{{ branch.name }}</NuxtLink>
                  <span v-if="isDefault(branch.name)" class="badge badge-green badge-label">Default</span>
                  <span v-if="branch.name === refName" class="badge badge-neutral badge-label">Current</span>
                </div>
              </td>
              <td class="mono small">{{ branch.target.slice(0, 10) }}</td>
              <td class="muted small">{{ branch.created_at ? timeAgo(branch.created_at) : '—' }}</td>
              <td class="numeric">
                <button
                  class="row-menu"
                  type="button"
                  aria-haspopup="menu"
                  :aria-expanded="open === branch.name"
                  :aria-label="`Actions for branch ${branch.name}`"
                  @click="showMenu(branch.name, $event)"
                >⋯</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <Teleport to="#teleports">
        <div
          v-if="openedBranch"
          class="row-menu-panel"
          :class="{ up: panel.up }"
          :style="{ top: `${panel.top}px`, left: `${panel.left}px` }"
        >
          <NuxtLink :to="repoViewUrl(projectPath, 'tree', openedBranch.name)" @click="closeMenu">
            Open record
          </NuxtLink>
          <button
            v-if="!isDefault(openedBranch.name)"
            class="row-menu-danger"
            type="button"
            :disabled="pending === openedBranch.name"
            @click="remove(openedBranch.name)"
          >
            {{ pending === openedBranch.name ? 'Deleting…' : 'Delete' }}
          </button>
        </div>
      </Teleport>
    </div>
  </div>
</template>