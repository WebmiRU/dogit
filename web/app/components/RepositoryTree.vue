<script setup lang="ts">
/** Directory listing of a repository at a ref. */
import type { RefsResponse, TreeEntry, TreeResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  refName: string
  path: string
}>()

const tree = ref<TreeResponse | null>(null)
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    tree.value = await api.get<TreeResponse>(
      `/projects/${props.projectId}/repository/tree`,
      { ref: props.refName, path: props.path },
    )
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => [props.refName, props.path], load)

/** Directories first, then files, each alphabetically. */
const entries = computed<TreeEntry[]>(() => {
  const items = [...(tree.value?.entries ?? [])]
  return items.sort((a, b) => {
    const aDir = a.type === 'tree'
    const bDir = b.type === 'tree'
    if (aDir !== bDir) return aDir ? -1 : 1
    return a.path.localeCompare(b.path)
  })
})

const isEmpty = computed(
  () => !loading.value && !loadError.value && entries.value.length === 0,
)

const treeUrl = (path: string, ref: string) =>
  `/p/${props.projectPath}/-/tree/${encodeURIComponent(ref)}/${path}`

const fileUrl = (path: string, ref: string) =>
  `/p/${props.projectPath}/-/blob/${encodeURIComponent(ref)}/${path}`

function entryLink(entry: TreeEntry) {
  const fullPath = props.path ? `${props.path}/${entry.path}` : entry.path
  return entry.type === 'tree' ? treeUrl(fullPath, props.refName) : fileUrl(fullPath, props.refName)
}

function icon(entry: TreeEntry) {
  if (entry.type === 'tree') return '▸'
  if (entry.type === 'commit') return '◈'
  const name = entry.path.toLowerCase()
  if (name === 'readme.md' || name.startsWith('readme')) return '📄'
  return '·'
}
</script>

<template>
  <div class="card">
    <div class="toolbar">
      <BranchSelector :refs="refs" :project-path="projectPath" :ref-name="refName" />

      <div class="breadcrumbs" v-if="tree">
        <NuxtLink :to="treeUrl('', refName)">{{ tree.breadcrumbs[0]?.name ?? projectPath }}</NuxtLink>
        <template v-for="(crumb, index) in tree.breadcrumbs.slice(1)" :key="crumb.path">
          <span class="sep">/</span>
          <NuxtLink :to="treeUrl(crumb.path, refName)">{{ crumb.name }}</NuxtLink>
        </template>
      </div>

      <div class="spacer" />
      <span v-if="tree" class="muted mono">{{ tree.sha.slice(0, 8) }}</span>
    </div>

    <div v-if="loading" class="spinner">Loading files…</div>
    <div v-else-if="loadError" class="alert alert-error" style="margin: 16px">{{ loadError }}</div>
    <div v-else-if="isEmpty" class="empty">
      This directory is empty.
    </div>
    <ul v-else class="tree-list">
      <li v-if="path" class="is-dir">
        <span class="icon">↰</span>
        <NuxtLink class="name" :to="treeUrl(path.split('/').slice(0, -1).join('/'), refName)">
          ..
        </NuxtLink>
      </li>
      <li
        v-for="entry in entries"
        :key="entry.path"
        :class="{ 'is-dir': entry.type === 'tree' }"
      >
        <span class="icon">{{ icon(entry) }}</span>
        <NuxtLink class="name" :to="entryLink(entry)">{{ entry.path }}</NuxtLink>
        <span class="meta">{{ entry.type === 'tree' ? '' : formatBytes(entry.size) }}</span>
      </li>
    </ul>
  </div>
</template>
