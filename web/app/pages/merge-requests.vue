<script setup lang="ts">
import type { MergeRequest } from '~/types/merge_requests'

const route = useRoute()
const { ensureLoaded } = useAuth()
await ensureLoaded()

const states = [
  { value: 'opened', label: 'Open' },
  { value: 'merged', label: 'Merged' },
  { value: 'closed', label: 'Closed' },
  { value: 'all', label: 'All' },
] as const
const state = computed(() => {
  const raw = String(route.query.state ?? 'opened')
  return states.some(s => s.value === raw) ? raw : 'opened'
})
const requests = ref<MergeRequest[]>([])
const counts = ref<Record<string, number>>({ opened: 0, merged: 0, closed: 0, all: 0 })
const total = ref(0)
const loading = ref(true)
const loadError = ref('')
const query = ref(String(route.query.query ?? ''))
const sort = ref(String(route.query.sort ?? 'created'))
const direction = ref<'asc' | 'desc'>(route.query.direction === 'asc' ? 'asc' : 'desc')
const pageSize = ref(Number(route.query.limit ?? 20))
const offset = ref(0)
const since = ref('all')
const bulkMode = ref(false)
const selectedIds = ref<number[]>([])
const menuMessage = ref('')
const historyOpen = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | undefined

const historyOptions = [
  { value: 'all', label: 'Any time' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
  { value: '1y', label: 'Last year' },
]
const visibleRequests = computed(() => requests.value)
async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<{
      merge_requests: MergeRequest[]
      total?: number
      counts?: Record<string, number>
    }>('/merge_requests', {
      state: state.value,
      query: query.value.trim(),
      sort: sort.value,
      direction: direction.value,
      since: since.value,
      limit: pageSize.value,
      offset: offset.value,
    })
    requests.value = response.merge_requests ?? []
    total.value = response.total ?? requests.value.length
    counts.value = response.counts ?? counts.value
    selectedIds.value = selectedIds.value.filter(id => requests.value.some(mr => mr.id === id))
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'The merge request list could not be loaded.'
  } finally {
    loading.value = false
  }
}
function refresh() { menuMessage.value = ''; void load() }
function toggleAllSelected() {
  selectedIds.value = selectedIds.value.length === visibleRequests.value.length
    ? []
    : visibleRequests.value.map(mr => mr.id)
}
function chooseSince(value: string) { since.value = value; historyOpen.value = false }
function selectPageSize(event: Event) {
  pageSize.value = Number((event.target as HTMLSelectElement).value)
  offset.value = 0
}
function stateLabel(mr: MergeRequest) {
  if (mr.is_draft) return 'Draft'
  return mr.state === 'opened' ? 'Open' : mr.state === 'merged' ? 'Merged' : 'Closed'
}
function copyListLink() {
  if (typeof window !== 'undefined' && navigator.clipboard) {
    void navigator.clipboard.writeText(window.location.href)
    menuMessage.value = 'List link copied.'
  } else menuMessage.value = 'Copying is not supported by this browser.'
}
onMounted(load)
watch([state, sort, direction, since, pageSize, offset], load)
watch(query, () => {
  offset.value = 0
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    void navigateTo({ path: '/merge-requests', query: { state: state.value, query: query.value || undefined, sort: sort.value, direction: direction.value, limit: pageSize.value } }, { replace: true })
    void load()
  }, 250)
})

onBeforeUnmount(() => { if (searchTimer) clearTimeout(searchTimer) })
</script>

<template>
  <div class="mr-index">
    <nav class="mr-list-breadcrumb" aria-label="Breadcrumb">
      <NuxtLink to="/projects">Projects</NuxtLink><span aria-hidden="true">/</span><strong>Merge requests</strong>
    </nav>

    <div class="mr-list-topline">
      <nav class="mr-list-tabs" aria-label="Filter merge requests">
        <NuxtLink v-for="option in states" :key="option.value"
          :to="{ path: '/merge-requests', query: { state: option.value, query: query || undefined, sort, direction, limit: pageSize } }"
          :class="{ active: state === option.value }">
          {{ option.label }} <span class="mr-count">{{ counts[option.value] ?? 0 }}</span>
        </NuxtLink>
      </nav>
      <div class="mr-list-actions">
        <button type="button" class="mr-secondary-button" :aria-pressed="bulkMode"
          :class="{ selected: bulkMode }" title="Toggle selection for bulk actions"
          @click="bulkMode = !bulkMode; selectedIds = []">Bulk edit</button>
        <NuxtLink class="mr-primary-button" to="/projects"><span aria-hidden="true">＋</span> New merge request</NuxtLink>
        <details class="mr-more-menu">
          <summary aria-label="More merge request list actions" title="More actions">⋮</summary>
          <div class="mr-more-popover">
            <button type="button" @click="refresh">Refresh list</button>
            <button type="button" @click="copyListLink">Copy list link</button>
            <button type="button" disabled title="Bulk operations are not implemented yet">Export list</button>
            <p v-if="menuMessage">{{ menuMessage }}</p>
          </div>
        </details>
      </div>
    </div>

    <div class="mr-list-toolbar">
      <details class="mr-history-filter" :open="historyOpen" @toggle="historyOpen = ($event.target as HTMLDetailsElement).open">
        <summary title="Filter by activity date" aria-label="Filter by activity date">
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3.2 8A7 7 0 1 1 3 12M3 3.5V8h4.5M10 5.5V10l3 2" /></svg>
          <svg class="mr-chevron" viewBox="0 0 12 12" aria-hidden="true"><path d="m3 4.5 3 3 3-3" /></svg>
        </summary>
        <div class="mr-history-popover"><button v-for="option in historyOptions" :key="option.value" type="button" :class="{ active: since === option.value }" @click="chooseSince(option.value)">{{ option.label }}</button></div>
      </details>
      <div class="mr-searchbox">
        <input v-model="query" type="search" placeholder="Search or filter results..." aria-label="Search merge requests" @keydown.enter.prevent="load">
        <button type="button" title="Search" aria-label="Search merge requests" @click="load"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5" /><path d="m13 13 4 4" /></svg></button>
      </div>
      <div class="mr-sort-control">
        <select v-model="sort" aria-label="Sort merge requests"><option value="created">Created date</option><option value="updated">Updated date</option><option value="title">Title</option><option value="state">Status</option></select>
        <button type="button" class="mr-sort-direction" :title="direction === 'desc' ? 'Descending; click for ascending' : 'Ascending; click for descending'" :aria-label="direction === 'desc' ? 'Descending; click for ascending' : 'Ascending; click for descending'" @click="direction = direction === 'desc' ? 'asc' : 'desc'">
          <svg viewBox="0 0 20 20" aria-hidden="true" :class="{ ascending: direction === 'asc' }"><path d="M4 5h6M4 9h4M4 13h2M14 3v13m-4-4 4 4 4-4" /></svg>
        </button>
      </div>
    </div>

    <div v-if="bulkMode" class="mr-bulk-notice">
      <label><input type="checkbox" :checked="visibleRequests.length > 0 && selectedIds.length === visibleRequests.length" @change="toggleAllSelected"> Select all {{ visibleRequests.length }} shown</label>
      <span>{{ selectedIds.length }} selected</span><span class="mr-bulk-hint">Bulk updates are not available yet; selection is ready for review.</span>
    </div>

    <div class="mr-list-result-wrap">
      <div v-if="loading" class="mr-list-state">Loading merge requests…</div>
      <div v-else-if="loadError" class="mr-list-state error">{{ loadError }}</div>
      <div v-else-if="visibleRequests.length === 0" class="mr-list-state">No merge requests match these filters.</div>
      <ul v-else class="mr-result-list">
        <li v-for="mr in visibleRequests" :key="mr.id" class="mr-result-row">
          <input v-if="bulkMode" v-model="selectedIds" class="mr-row-select" type="checkbox" :value="mr.id" :aria-label="'Select merge request !' + mr.iid">
          <div class="mr-result-copy">
            <NuxtLink class="mr-result-title" :to="mr.url || '/p/' + (mr.project?.path ?? '') + '/-/merge_requests/' + mr.iid">
              <span v-if="mr.is_draft" class="mr-draft-tag">Draft</span>{{ mr.title }}
            </NuxtLink>
            <div class="mr-result-meta"><span>!{{ mr.iid }}</span><span aria-hidden="true">·</span><span>created {{ timeAgo(mr.created_at) }} by {{ mr.author_name || mr.author_username }}</span><span aria-hidden="true">·</span><code>{{ mr.project?.path }}</code></div>

          </div>
          <span class="mr-row-status" :class="'status-' + mr.state" :title="stateLabel(mr)" :aria-label="stateLabel(mr)">
            <svg v-if="mr.state !== 'closed'" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="m7.5 12.2 3 3 6-6.3" /></svg>
            <svg v-else viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="m9 9 6 6m0-6-6 6" /></svg>
          </span>
        </li>
      </ul>
    </div>

    <footer class="mr-list-footer">
      <span class="mr-total">{{ total }} merge request{{ total === 1 ? '' : 's' }}</span>
      <label class="mr-page-size">Show <select :value="pageSize" aria-label="Number of items to show" @change="selectPageSize"><option :value="20">20 items</option><option :value="50">50 items</option><option :value="100">100 items</option></select></label>
    </footer>
  </div>
</template>

<style scoped>
/* Both the global and project-scoped lists use the same row and toolbar layout. */
