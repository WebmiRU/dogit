<script setup lang="ts">
import type { MergeRequest } from '~/types/merge_requests'
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  defaultBranch: string
  canCreate: boolean
}>()

const states = [
  { value: 'opened', label: 'Open' },
  { value: 'merged', label: 'Merged' },
  { value: 'closed', label: 'Closed' },
  { value: 'all', label: 'All' },
] as const
const state = ref<(typeof states)[number]['value']>('opened')
const requests = ref<MergeRequest[]>([])
const counts = ref<Record<string, number>>({ opened: 0, merged: 0, closed: 0, all: 0 })
const total = ref(0)
const loading = ref(true)
const loadError = ref('')
const query = ref('')
const sort = ref('created')
const direction = ref<'asc' | 'desc'>('desc')
const pageSize = ref(20)
const offset = ref(0)
const since = ref('all')
const bulkMode = ref(false)
const selectedIds = ref<number[]>([])
const menuMessage = ref('')
const historyOpen = ref(false)
const menuOpen = ref(false)
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
    }>('/projects/' + props.projectId + '/merge_requests', {
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
function refresh() {
  menuMessage.value = ''
  void load()
}
function toggleAllSelected() {
  selectedIds.value = selectedIds.value.length === visibleRequests.value.length
    ? []
    : visibleRequests.value.map(mr => mr.id)
}
function chooseSince(value: string) {
  since.value = value
  historyOpen.value = false
}
function selectPageSize(event: Event) {
  pageSize.value = Number((event.target as HTMLSelectElement).value)
  offset.value = 0
}
function formatDate(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date)
}
function stateLabel(mr: MergeRequest) {
  if (mr.is_draft) return 'Draft'
  return mr.state === 'opened' ? 'Open' : mr.state === 'merged' ? 'Merged' : 'Closed'
}
function copyListLink() {
  if (typeof window !== 'undefined' && navigator.clipboard) {
    void navigator.clipboard.writeText(window.location.href)
    menuMessage.value = 'List link copied.'
  } else {
    menuMessage.value = 'Copying is not supported by this browser.'
  }
}
function onHistoryToggle(event: Event) {
  historyOpen.value = (event.currentTarget as HTMLDetailsElement).open
}
onMounted(load)
watch([state, sort, direction, since, pageSize, offset], load)
watch(query, () => {
  offset.value = 0
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void load(), 250)
})
onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <section class="mr-list-page">
    <div class="mr-list-topline">
      <nav class="mr-list-tabs" aria-label="Filter merge requests">
        <button v-for="option in states" :key="option.value" type="button"
          :class="{ active: state === option.value }" @click="state = option.value">
          {{ option.label }} <span class="mr-count">{{ counts[option.value] ?? 0 }}</span>
        </button>
      </nav>
      <div class="mr-list-actions">
        <button type="button" class="mr-secondary-button" :aria-pressed="bulkMode"
          :class="{ selected: bulkMode }" title="Toggle selection for bulk actions"
          @click="bulkMode = !bulkMode; selectedIds = []">Bulk edit</button>
        <NuxtLink v-if="canCreate" class="mr-primary-button" :to="'/p/' + projectPath + '/-/merge_requests/new'">
          <span aria-hidden="true">＋</span> New merge request
        </NuxtLink>
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
      <details class="mr-history-filter" :open="historyOpen" @toggle="onHistoryToggle">
        <summary title="Filter by activity date" aria-label="Filter by activity date">
          <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3.2 8A7 7 0 1 1 3 12M3 3.5V8h4.5M10 5.5V10l3 2" /></svg>
          <svg class="mr-chevron" viewBox="0 0 12 12" aria-hidden="true"><path d="m3 4.5 3 3 3-3" /></svg>
        </summary>
        <div class="mr-history-popover">
          <button v-for="option in historyOptions" :key="option.value" type="button"
            :class="{ active: since === option.value }" @click="chooseSince(option.value)">{{ option.label }}</button>
        </div>
      </details>
      <div class="mr-searchbox">
        <input v-model="query" type="search" placeholder="Search or filter results..." aria-label="Search merge requests"
          @keydown.enter.prevent="load">
        <button type="button" title="Search" aria-label="Search merge requests" @click="load">
          <svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5" /><path d="m13 13 4 4" /></svg>
        </button>
      </div>
      <div class="mr-sort-control">
        <select v-model="sort" aria-label="Sort merge requests">
          <option value="created">Created date</option>
          <option value="updated">Updated date</option>
          <option value="title">Title</option>
          <option value="state">Status</option>
        </select>
        <button type="button" class="mr-sort-direction" :title="direction === 'desc' ? 'Descending; click for ascending' : 'Ascending; click for descending'"
          :aria-label="direction === 'desc' ? 'Descending; click for ascending' : 'Ascending; click for descending'"
          @click="direction = direction === 'desc' ? 'asc' : 'desc'">
          <svg viewBox="0 0 20 20" aria-hidden="true" :class="{ ascending: direction === 'asc' }"><path d="M4 5h6M4 9h4M4 13h2M14 3v13m-4-4 4 4 4-4" /></svg>
        </button>
      </div>
    </div>

    <div v-if="bulkMode" class="mr-bulk-notice">
      <label><input type="checkbox" :checked="visibleRequests.length > 0 && selectedIds.length === visibleRequests.length" @change="toggleAllSelected"> Select all {{ visibleRequests.length }} shown</label>
      <span>{{ selectedIds.length }} selected</span>
      <span class="mr-bulk-hint">Bulk updates are not available yet; selection is ready for review.</span>
    </div>

    <div class="mr-list-result-wrap">
      <div v-if="loading" class="mr-list-state">Loading merge requests…</div>
      <div v-else-if="loadError" class="mr-list-state error">{{ loadError }}</div>
      <div v-else-if="visibleRequests.length === 0" class="mr-list-state">
        No merge requests match these filters.
      </div>
      <ul v-else class="mr-result-list">
        <li v-for="mr in visibleRequests" :key="mr.id" class="mr-result-row">
          <input v-if="bulkMode" v-model="selectedIds" class="mr-row-select" type="checkbox" :value="mr.id"
            :aria-label="'Select merge request !' + mr.iid">
          <div class="mr-result-copy">
            <NuxtLink class="mr-result-title" :to="'/p/' + projectPath + '/-/merge_requests/' + mr.iid">
              <span v-if="mr.is_draft" class="mr-draft-tag">Draft</span>{{ mr.title }}
            </NuxtLink>
            <div class="mr-result-meta">
              <span>!{{ mr.iid }}</span><span aria-hidden="true">·</span>
              <span>created {{ timeAgo(mr.created_at) }} by {{ mr.author_name || mr.author_username }}</span>

            </div>
          </div>
          <div class="mr-result-status">
            <span class="mr-row-status" :class="'status-' + mr.state" :title="stateLabel(mr)" :aria-label="stateLabel(mr)">
              <svg v-if="mr.state !== 'closed'" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="m7.5 12.2 3 3 6-6.3" /></svg>
              <svg v-else viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="m9 9 6 6m0-6-6 6" /></svg>
            </span>
            <span class="mr-row-updated">updated {{ timeAgo(mr.updated_at) }}</span>
          </div>
        </li>
      </ul>
    </div>

    <footer class="mr-list-footer">
      <label class="mr-page-size">Show
        <select :value="pageSize" aria-label="Number of items to show" @change="selectPageSize">
          <option :value="20">20 items</option><option :value="50">50 items</option><option :value="100">100 items</option>
        </select>
      </label>
    </footer>
  </section>
</template>

<style scoped>
.mr-list-page { width:100%; min-width:0; color:var(--text); }
.mr-list-breadcrumb { display:flex; flex-wrap:wrap; align-items:center; gap:9px; min-height:37px; margin:0 0 12px; padding:0 0 12px; border-bottom:1px solid var(--border); color:var(--text-muted); font-size:12px; }
.mr-list-breadcrumb a { color:var(--text-muted); }.mr-list-breadcrumb a:hover { color:var(--text); }.mr-list-breadcrumb strong { color:var(--text); font-weight:600; }
.mr-list-topline { display:flex; align-items:stretch; justify-content:space-between; flex-wrap:wrap; gap:12px; min-height:48px; border-bottom:1px solid var(--border); }
.mr-list-tabs { display:flex; align-items:stretch; flex-wrap:wrap; gap:0; min-width:0; }
.mr-list-tabs button { display:flex; align-items:center; gap:5px; padding:10px 13px 9px; border:0; border-bottom:2px solid transparent; background:transparent; color:var(--text-muted); font:inherit; font-size:13px; cursor:pointer; white-space:nowrap; }
.mr-list-tabs button:hover { color:var(--text); }.mr-list-tabs button.active { border-bottom-color:#8d9cb4; color:var(--text); font-weight:650; }
.mr-count { display:inline-flex; justify-content:center; align-items:center; min-width:22px; height:21px; padding:0 6px; border-radius:12px; background:#37383f; color:#d8d9df; font-size:11px; font-weight:500; }
.mr-list-actions { display:flex; align-items:center; gap:8px; padding-bottom:6px; }
.mr-secondary-button,.mr-primary-button { display:inline-flex; align-items:center; justify-content:center; gap:7px; min-height:34px; padding:6px 12px; border:1px solid var(--border-strong); border-radius:5px; font:inherit; font-size:12px; white-space:nowrap; cursor:pointer; text-decoration:none; }
.mr-secondary-button { background:#34353b; color:var(--text); }.mr-secondary-button:hover,.mr-secondary-button.selected { background:#42434a; color:#fff; text-decoration:none; }
.mr-primary-button { background:var(--button-primary-bg); border-color:var(--button-primary-border); color:var(--button-primary-text); font-weight:600; }
.mr-primary-button:hover { background:var(--button-primary-hover); border-color:var(--button-primary-hover); color:var(--button-primary-text); text-decoration:none; }
.mr-more-menu { position:relative; }.mr-more-menu summary { display:grid; place-items:center; width:31px; height:34px; border:1px solid transparent; border-radius:5px; color:var(--text-muted); cursor:pointer; font-size:21px; line-height:1; list-style:none; }.mr-more-menu summary::-webkit-details-marker { display:none; }.mr-more-menu summary:hover,.mr-more-menu[open] summary { background:var(--bg-elevated); border-color:var(--border); color:var(--text); }
.mr-more-popover,.mr-history-popover { position:absolute; z-index:35; top:calc(100% + 5px); right:0; min-width:190px; padding:5px; border:1px solid var(--border-strong); border-radius:7px; background:var(--bg-elevated); box-shadow:0 10px 25px rgba(0,0,0,.38); }
.mr-more-popover button,.mr-history-popover button { display:block; width:100%; padding:8px 10px; border:0; border-radius:4px; background:transparent; color:var(--text); text-align:left; font:inherit; font-size:12px; cursor:pointer; }.mr-more-popover button:hover:not(:disabled),.mr-history-popover button:hover { background:var(--bg-inset); }.mr-more-popover button:disabled { color:var(--text-muted); cursor:not-allowed; }.mr-more-popover p { margin:4px 8px; color:var(--text-muted); font-size:11px; }
.mr-list-toolbar { display:flex; align-items:center; gap:0; width:100%; margin:0 0 0; padding:10px; border:1px solid var(--border); border-top:0; background:#222328; }
.mr-history-filter { position:relative; flex:0 0 auto; }.mr-history-filter summary { display:flex; align-items:center; justify-content:center; gap:5px; width:58px; height:38px; border:1px solid var(--border-strong); border-radius:6px 0 0 6px; background:#18191d; color:var(--text-muted); cursor:pointer; list-style:none; }.mr-history-filter summary::-webkit-details-marker { display:none; }.mr-history-filter summary svg:first-child { width:19px; height:19px; fill:none; stroke:currentColor; stroke-width:1.65; stroke-linecap:round; stroke-linejoin:round; }.mr-history-filter .mr-chevron { width:11px; height:11px; fill:none; stroke:currentColor; stroke-width:1.5; }.mr-history-popover { left:0; right:auto; }
.mr-history-popover button.active { background:#303136; color:var(--text); }
.mr-searchbox { display:flex; flex:1 1 250px; min-width:120px; height:38px; }.mr-searchbox input { min-width:0; width:100%; height:38px; padding:8px 12px; border:1px solid var(--border-strong); border-left:0; border-radius:0; outline:none; background:#18191d; color:var(--text); font:inherit; font-size:13px; }.mr-searchbox input:focus { border-color:#626773; box-shadow:inset 0 0 0 1px #626773; }.mr-searchbox button { display:grid; place-items:center; flex:0 0 40px; width:40px; height:38px; border:1px solid var(--border-strong); border-left:0; border-radius:0 6px 6px 0; background:#18191d; color:var(--text); cursor:pointer; }.mr-searchbox button:hover { background:#303136; }.mr-searchbox button svg { width:18px; height:18px; fill:none; stroke:currentColor; stroke-width:1.7; stroke-linecap:round; }
.mr-sort-control { display:flex; flex:0 0 auto; height:38px; margin-left:10px; }.mr-sort-control select { width:auto; min-width:143px; height:38px; margin:0; padding:0 28px 0 11px; border:1px solid var(--border-strong); border-radius:6px 0 0 6px; background:#34353b; color:var(--text); font:inherit; font-size:12px; }.mr-sort-direction { display:grid; place-items:center; width:38px; height:38px; padding:0; border:1px solid var(--border-strong); border-left:0; border-radius:0 6px 6px 0; background:#34353b; color:var(--text); cursor:pointer; }.mr-sort-direction:hover { background:#46474e; }.mr-sort-direction svg { width:17px; height:17px; fill:none; stroke:currentColor; stroke-width:1.5; stroke-linecap:round; stroke-linejoin:round; }.mr-sort-direction svg.ascending { transform:rotate(180deg); }
.mr-bulk-notice { display:flex; align-items:center; flex-wrap:wrap; gap:12px; padding:9px 12px; border:1px solid var(--border); border-top:0; background:var(--bg-elevated); color:var(--text-muted); font-size:12px; }.mr-bulk-notice label { display:flex; align-items:center; gap:7px; margin:0; color:var(--text); }.mr-bulk-notice input,.mr-row-select { width:15px; height:15px; margin:0; accent-color:#8d9cb4; }.mr-bulk-hint { margin-left:auto; }
.mr-list-result-wrap { border-bottom:1px solid var(--border); background:transparent; }.mr-list-state { padding:34px 18px; color:var(--text-muted); text-align:center; font-size:13px; }.mr-list-state.error { color:#ffb4ae; }
.mr-result-list { list-style:none; margin:0; padding:0; }.mr-result-row { display:flex; align-items:center; gap:12px; min-width:0; padding:13px 10px 13px 14px; border-bottom:1px solid #292a2f; background:transparent; }.mr-result-row:hover { background:rgba(255,255,255,.018); }.mr-result-row:last-child { border-bottom:0; }.mr-result-copy { flex:1 1 auto; min-width:0; }.mr-result-title { display:block; overflow:hidden; margin-bottom:3px; color:var(--text); font-size:13px; font-weight:650; line-height:1.45; text-overflow:ellipsis; }.mr-result-title:hover { color:#d6dce7; text-decoration:underline; }.mr-result-meta { display:flex; flex-wrap:wrap; align-items:center; gap:5px; color:var(--text-muted); font-size:11px; line-height:1.55; }.mr-branch-pair { display:inline-flex; align-items:center; gap:5px; }.mr-branch-pair code { color:#b8bac3; font-size:11px; }.mr-draft-tag { margin-right:6px; padding:2px 5px; border:1px solid var(--border-strong); border-radius:3px; color:var(--text-muted); font-size:9px; font-weight:700; text-transform:uppercase; vertical-align:1px; }
.mr-row-status { display:grid; place-items:center; flex:0 0 30px; width:30px; height:30px; color:#43b581; }.mr-row-status svg { display:block; width:27px; height:27px; fill:none; stroke:currentColor; stroke-width:2.2; stroke-linecap:round; stroke-linejoin:round; }.mr-row-status.status-closed { color:#8a8d96; }.mr-row-status.status-merged { color:#43b581; }
.mr-list-footer { display:flex; align-items:center; justify-content:flex-end; gap:12px; min-height:60px; padding:11px 0; }.mr-total { margin-right:auto; color:var(--text-muted); font-size:11px; }.mr-page-size { display:flex; align-items:center; gap:8px; margin:0; color:var(--text-muted); font-size:12px; }.mr-page-size select { width:auto; min-width:124px; height:37px; margin:0; padding:5px 27px 5px 10px; border:1px solid var(--border-strong); border-radius:6px; background:#34353b; color:var(--text); font:inherit; font-size:12px; }
@media(max-width:850px) { .mr-list-topline { align-items:flex-start; flex-direction:column; }.mr-list-actions { width:100%; justify-content:flex-end; }.mr-list-toolbar { flex-wrap:wrap; gap:8px; }.mr-history-filter summary { border-radius:6px; }.mr-searchbox { flex-basis:calc(100% - 66px); }.mr-sort-control { margin-left:0; }.mr-sort-control select { border-radius:6px 0 0 6px; }.mr-list-actions { padding-bottom:9px; } }
@media(max-width:520px) { .mr-list-breadcrumb { gap:6px; }.mr-list-actions { flex-wrap:wrap; }.mr-primary-button { flex:1 1 auto; }.mr-sort-control { flex:1 1 auto; }.mr-sort-control select { flex:1 1 auto; min-width:0; }.mr-row-status { flex-basis:24px; width:24px; }.mr-row-status svg { width:23px; height:23px; }.mr-result-row { padding:11px 7px; } }
.mr-index,.mr-list-page { width:100%; min-width:0; color:var(--text); }
.mr-list-breadcrumb { display:flex; flex-wrap:wrap; align-items:center; gap:9px; min-height:37px; margin:0 0 12px; padding:0 0 12px; border-bottom:1px solid var(--border); color:var(--text-muted); font-size:12px; }
.mr-list-breadcrumb a { color:var(--text-muted); }.mr-list-breadcrumb a:hover { color:var(--text); }.mr-list-breadcrumb strong { color:var(--text); font-weight:600; }
.mr-list-topline { display:flex; align-items:stretch; justify-content:space-between; flex-wrap:wrap; gap:12px; min-height:48px; border-bottom:1px solid var(--border); }
.mr-list-tabs { display:flex; align-items:stretch; flex-wrap:wrap; gap:0; min-width:0; }
.mr-list-tabs button,.mr-list-tabs a { display:flex; align-items:center; gap:5px; padding:10px 13px 9px; border:0; border-bottom:2px solid transparent; background:transparent; color:var(--text-muted); font:inherit; font-size:13px; cursor:pointer; white-space:nowrap; text-decoration:none; }
.mr-list-tabs button:hover,.mr-list-tabs a:hover { color:var(--text); }.mr-list-tabs button.active,.mr-list-tabs a.active { border-bottom-color:#8d9cb4; color:var(--text); font-weight:650; }
.mr-count { display:inline-flex; justify-content:center; align-items:center; min-width:22px; height:21px; padding:0 6px; border-radius:12px; background:#37383f; color:#d8d9df; font-size:11px; font-weight:500; }
.mr-list-actions { display:flex; align-items:center; gap:8px; padding-bottom:6px; }
.mr-secondary-button,.mr-primary-button { display:inline-flex; align-items:center; justify-content:center; gap:7px; min-height:34px; padding:6px 12px; border:1px solid var(--border-strong); border-radius:5px; font:inherit; font-size:12px; white-space:nowrap; cursor:pointer; text-decoration:none; }
.mr-secondary-button { background:#34353b; color:var(--text); }.mr-secondary-button:hover,.mr-secondary-button.selected { background:#42434a; color:#fff; text-decoration:none; }
.mr-primary-button { background:var(--button-primary-bg); border-color:var(--button-primary-border); color:var(--button-primary-text); font-weight:600; }.mr-primary-button:hover { background:var(--button-primary-hover); border-color:var(--button-primary-hover); color:var(--button-primary-text); text-decoration:none; }
.mr-more-menu { position:relative; }.mr-more-menu summary { display:grid; place-items:center; width:31px; height:34px; border:1px solid transparent; border-radius:5px; color:var(--text-muted); cursor:pointer; font-size:21px; line-height:1; list-style:none; }.mr-more-menu summary::-webkit-details-marker { display:none; }.mr-more-menu summary:hover,.mr-more-menu[open] summary { background:var(--bg-elevated); border-color:var(--border); color:var(--text); }
.mr-more-popover,.mr-history-popover { position:absolute; z-index:35; top:calc(100% + 5px); right:0; min-width:190px; padding:5px; border:1px solid var(--border-strong); border-radius:7px; background:var(--bg-elevated); box-shadow:0 10px 25px rgba(0,0,0,.38); }
.mr-more-popover button,.mr-history-popover button { display:block; width:100%; padding:8px 10px; border:0; border-radius:4px; background:transparent; color:var(--text); text-align:left; font:inherit; font-size:12px; cursor:pointer; }.mr-more-popover button:hover:not(:disabled),.mr-history-popover button:hover { background:var(--bg-inset); }.mr-more-popover button:disabled { color:var(--text-muted); cursor:not-allowed; }.mr-more-popover p { margin:4px 8px; color:var(--text-muted); font-size:11px; }
.mr-list-toolbar { display:flex; align-items:center; gap:0; width:100%; margin:0; padding:10px; border:1px solid var(--border); border-top:0; background:#222328; }
.mr-history-filter { position:relative; flex:0 0 auto; }.mr-history-filter summary { display:flex; align-items:center; justify-content:center; gap:5px; width:58px; height:38px; border:1px solid var(--border-strong); border-radius:6px 0 0 6px; background:#18191d; color:var(--text-muted); cursor:pointer; list-style:none; }.mr-history-filter summary::-webkit-details-marker { display:none; }.mr-history-filter summary svg:first-child { width:19px; height:19px; fill:none; stroke:currentColor; stroke-width:1.65; stroke-linecap:round; stroke-linejoin:round; }.mr-history-filter .mr-chevron { width:11px; height:11px; fill:none; stroke:currentColor; stroke-width:1.5; }.mr-history-popover { left:0; right:auto; }
.mr-history-popover button.active { background:#303136; color:var(--text); }
.mr-searchbox { display:flex; flex:1 1 250px; min-width:120px; height:38px; }.mr-searchbox input { min-width:0; width:100%; height:38px; padding:8px 12px; border:1px solid var(--border-strong); border-left:0; border-radius:0; outline:none; background:#18191d; color:var(--text); font:inherit; font-size:13px; }.mr-searchbox input:focus { border-color:#626773; box-shadow:inset 0 0 0 1px #626773; }.mr-searchbox button { display:grid; place-items:center; flex:0 0 40px; width:40px; height:38px; border:1px solid var(--border-strong); border-left:0; border-radius:0 6px 6px 0; background:#18191d; color:var(--text); cursor:pointer; }.mr-searchbox button:hover { background:#303136; }.mr-searchbox button svg { width:18px; height:18px; fill:none; stroke:currentColor; stroke-width:1.7; stroke-linecap:round; }
.mr-sort-control { display:flex; flex:0 0 auto; height:38px; margin-left:10px; }.mr-sort-control select { width:auto; min-width:143px; height:38px; margin:0; padding:0 28px 0 11px; border:1px solid var(--border-strong); border-radius:6px 0 0 6px; background:#34353b; color:var(--text); font:inherit; font-size:12px; }.mr-sort-direction { display:grid; place-items:center; width:38px; height:38px; padding:0; border:1px solid var(--border-strong); border-left:0; border-radius:0 6px 6px 0; background:#34353b; color:var(--text); cursor:pointer; }.mr-sort-direction:hover { background:#46474e; }.mr-sort-direction svg { width:17px; height:17px; fill:none; stroke:currentColor; stroke-width:1.5; stroke-linecap:round; stroke-linejoin:round; }.mr-sort-direction svg.ascending { transform:rotate(180deg); }
.mr-bulk-notice { display:flex; align-items:center; flex-wrap:wrap; gap:12px; padding:9px 12px; border:1px solid var(--border); border-top:0; background:var(--bg-elevated); color:var(--text-muted); font-size:12px; }.mr-bulk-notice label { display:flex; align-items:center; gap:7px; margin:0; color:var(--text); }.mr-bulk-notice input,.mr-row-select { width:15px; height:15px; margin:0; accent-color:#8d9cb4; }.mr-bulk-hint { margin-left:auto; }
.mr-list-result-wrap { border-bottom:1px solid var(--border); background:transparent; }.mr-list-state { padding:34px 18px; color:var(--text-muted); text-align:center; font-size:13px; }.mr-list-state.error { color:#ffb4ae; }
.mr-result-list { list-style:none; margin:0; padding:0; }.mr-result-row { display:flex; align-items:center; gap:12px; min-width:0; padding:13px 10px 13px 14px; border-bottom:1px solid #292a2f; background:transparent; }.mr-result-row:hover { background:rgba(255,255,255,.018); }.mr-result-row:last-child { border-bottom:0; }.mr-result-copy { flex:1 1 auto; min-width:0; }.mr-result-title { display:block; overflow:hidden; margin-bottom:3px; color:var(--text); font-size:13px; font-weight:650; line-height:1.45; text-overflow:ellipsis; }.mr-result-title:hover { color:#d6dce7; text-decoration:underline; }.mr-result-meta { display:flex; flex-wrap:wrap; align-items:center; gap:5px; color:var(--text-muted); font-size:11px; line-height:1.55; }.mr-branch-pair { display:inline-flex; align-items:center; gap:5px; }.mr-branch-pair code { color:#b8bac3; font-size:11px; }.mr-draft-tag { margin-right:6px; padding:2px 5px; border:1px solid var(--border-strong); border-radius:3px; color:var(--text-muted); font-size:9px; font-weight:700; text-transform:uppercase; vertical-align:1px; }
.mr-row-status { display:grid; place-items:center; flex:0 0 30px; width:30px; height:30px; color:#43b581; }.mr-row-status svg { display:block; width:27px; height:27px; fill:none; stroke:currentColor; stroke-width:2.2; stroke-linecap:round; stroke-linejoin:round; }.mr-row-status.status-closed { color:#8a8d96; }.mr-row-status.status-merged { color:#43b581; }
.mr-list-footer { display:flex; align-items:center; justify-content:flex-end; gap:12px; min-height:60px; padding:11px 0; }.mr-total { margin-right:auto; color:var(--text-muted); font-size:11px; }.mr-page-size { display:flex; align-items:center; gap:8px; margin:0; color:var(--text-muted); font-size:12px; }.mr-page-size select { width:auto; min-width:124px; height:37px; margin:0; padding:5px 27px 5px 10px; border:1px solid var(--border-strong); border-radius:6px; background:#34353b; color:var(--text); font:inherit; font-size:12px; }
@media(max-width:850px) { .mr-list-topline { align-items:flex-start; flex-direction:column; }.mr-list-actions { width:100%; justify-content:flex-end; }.mr-list-toolbar { flex-wrap:wrap; gap:8px; }.mr-history-filter summary { border-radius:6px; }.mr-searchbox { flex-basis:calc(100% - 66px); }.mr-sort-control { margin-left:0; }.mr-sort-control select { border-radius:6px 0 0 6px; }.mr-list-actions { padding-bottom:9px; } }
@media(max-width:520px) { .mr-list-breadcrumb { gap:6px; }.mr-list-actions { flex-wrap:wrap; }.mr-primary-button { flex:1 1 auto; }.mr-sort-control { flex:1 1 auto; }.mr-sort-control select { flex:1 1 auto; min-width:0; }.mr-row-status { flex-basis:24px; width:24px; }.mr-row-status svg { width:23px; height:23px; }.mr-result-row { padding:11px 7px; } }

/* The reference is a 4K screenshot displayed at 50% scale: use CSS-pixel sizes. */
.mr-list-topline { min-height:37px; gap:0; }
.mr-list-tabs button,.mr-list-tabs a { gap:4px; padding:7px 10px 6px; font-size:12px; }
.mr-list-tabs button.active,.mr-list-tabs a.active { border-bottom-color:#579ce6; }
.mr-count { min-width:16px; height:16px; padding:0 4px; border-radius:8px; font-size:11px; }
.mr-list-actions { gap:5px; padding:0; }
.mr-secondary-button,.mr-primary-button { min-height:25px; padding:4px 10px; border-radius:6px; font-size:12px; font-weight:500; }
.mr-primary-button { background:#619fe2; border-color:#619fe2; color:#111827; }
.mr-primary-button:hover { background:#75afea; border-color:#75afea; color:#111827; }
.mr-more-menu summary { width:20px; height:25px; border-radius:4px; font-size:17px; }
.mr-list-toolbar { margin:0; padding:13px 12px; }
.mr-history-filter summary { width:45px; height:25px; gap:3px; border-radius:6px 0 0 6px; }
.mr-history-filter summary svg:first-child { width:13px; height:13px; }
.mr-history-filter .mr-chevron { width:8px; height:8px; }
.mr-searchbox { height:25px; }
.mr-searchbox input { height:25px; padding:4px 8px; font-size:12px; }
.mr-searchbox button { flex-basis:25px; width:25px; height:25px; }
.mr-searchbox button svg { width:14px; height:14px; }
.mr-sort-control { height:25px; margin-left:6px; }
.mr-sort-control select { min-width:100px; height:25px; padding:0 17px 0 9px; border-radius:6px 0 0 6px; font-size:12px; }
.mr-sort-direction { width:25px; height:25px; border-radius:0 6px 6px 0; }
.mr-sort-direction svg { width:13px; height:13px; }
.mr-result-row { align-items:flex-start; gap:8px; padding:7px 12px 9px; }
.mr-result-title { margin-bottom:2px; font-size:12px; font-weight:650; line-height:1.4; }
.mr-result-meta { gap:4px; font-size:10px; line-height:1.5; }
.mr-result-status { display:flex; flex:0 0 110px; min-width:85px; flex-direction:column; align-items:flex-end; gap:2px; }
.mr-row-status { flex:0 0 19px; width:19px; height:19px; }
.mr-row-status svg { width:19px; height:19px; stroke-width:2.2; }
.mr-row-updated { color:var(--text-muted); font-size:10px; line-height:1.4; white-space:nowrap; }
.mr-list-footer { justify-content:flex-end; min-height:50px; padding:19px 0 0; }
.mr-page-size { justify-content:space-between; gap:4px; width:110px; min-width:110px; height:25px; padding:0 6px 0 10px; border:1px solid var(--border-strong); border-radius:6px; background:#34353b; color:var(--text); font-size:12px; }
.mr-page-size select { flex:1 1 auto; width:100%; min-width:0; height:23px; margin:0; padding:2px 12px 2px 0; border:0; border-radius:0; background:transparent; color:var(--text); font-size:12px; }
@media(max-width:850px) {
  .mr-list-topline { align-items:flex-start; flex-direction:column; gap:4px; }
  .mr-list-tabs button,.mr-list-tabs a { padding:6px 8px 5px; font-size:12px; }
  .mr-list-actions { width:100%; justify-content:flex-end; }
  .mr-list-toolbar { flex-wrap:wrap; gap:4px; padding:8px; }
  .mr-history-filter summary { border-radius:5px; }
  .mr-searchbox { flex-basis:calc(100% - 49px); }
  .mr-sort-control { margin-left:0; }
  .mr-sort-control select { min-width:75px; border-radius:5px 0 0 5px; }
  .mr-result-status { flex-basis:75px; min-width:62px; }
  .mr-result-title { font-size:12px; }
  .mr-result-meta,.mr-row-updated { font-size:10px; }
  .mr-list-footer { padding-top:14px; }
}
@media(max-width:520px) {
  .mr-list-topline { min-height:0; }
  .mr-list-tabs { width:100%; overflow-x:auto; }
  .mr-list-tabs button,.mr-list-tabs a { padding:6px 5px 5px; font-size:11px; }
  .mr-count { min-width:14px; height:14px; font-size:10px; }
  .mr-list-actions { flex-wrap:wrap; }
  .mr-secondary-button,.mr-primary-button { min-height:23px; padding:4px 7px; font-size:11px; }
  .mr-list-toolbar { padding:6px; }
  .mr-searchbox { flex:1 1 calc(100% - 49px); min-width:0; }
  .mr-searchbox input { font-size:11px; }
  .mr-sort-control { flex:1 1 auto; min-width:0; }
  .mr-sort-control select { flex:1 1 auto; min-width:0; }
  .mr-result-row { padding:6px 5px; }
  .mr-result-title { font-size:11px; }
  .mr-result-meta { font-size:10px; }
  .mr-result-status { flex-basis:auto; min-width:0; }
  .mr-row-status,.mr-row-status svg { width:16px; height:16px; }
  .mr-row-status { flex-basis:16px; }
  .mr-row-updated { font-size:9px; }
  .mr-page-size { width:95px; min-width:95px; height:23px; font-size:11px; }
  .mr-page-size select { height:21px; font-size:11px; }
}

</style>