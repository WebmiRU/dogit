<script setup lang="ts">
/**
 * The one list of where to work.
 *
 * Projects and groups are rows of one table, because to somebody looking for somewhere
 * to work they are the same kind of thing: a name, a path and an address. Two lists
 * would mean two searches and a page to find twice.
 *
 * The search and the filters are in the address bar rather than in this component.
 * That is not fussiness about links — a filtered list a person cannot send to somebody,
 * or get back to with the back button, is a list they will end up using the wrong.
 *
 * What comes back is one page, chosen by the database. Loading everything and cutting
 * it up here is how a page stops opening after a year, and the count beside the control
 * is the whole list so the control can say where the end is.
 */
import type { PagedPlaces, Place, PlaceFilters } from '~/types/place'
import { placeHref, placeQuery } from '~/types/place'

const props = withDefaults(
  defineProps<{
    /** Which kinds to show. Empty means both. */
    type?: '' | 'project' | 'group'
    /** A page heading of its own. */
    title?: string
    /** Whether the kind filter is offered. Off when the page is about one kind. */
    filterKind?: boolean
  }>(),
  { type: '', filterKind: true },
)

const route = useRoute()
const { add: notify } = useNotifyPool()

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function pipelineLabel(value: string): string {
  const labels: Record<string, string> = {
    success: 'Passed', failed: 'Failed', running: 'Running', pending: 'Pending',
    canceled: 'Canceled', interrupted: 'Interrupted',
  }
  return labels[value] ?? value
}

const places = ref<Place[]>([])
const total = ref(0)
const page = ref(1)
const pages = ref(1)
const loading = ref(true)
const error = ref('')

/** The word being typed, kept apart from what has been searched for yet. */
const typing = ref('')
const sortedPlaces = computed(() => places.value)

/** What is asked of the server, read out of the address bar. */
const filters = computed<Required<PlaceFilters>>(() => ({
  search: String(route.query.search ?? ''),
  type: (props.type || String(route.query.type ?? '')) as PlaceFilters['type'] as 'project' | 'group' | '',
  visibility: String(route.query.visibility ?? ''),
  scope: String(route.query.scope ?? '') as PlaceFilters['scope'],
  sort: (String(route.query.sort ?? 'name') || 'name') as PlaceFilters['sort'],
  direction: (String(route.query.direction ?? 'asc') || 'asc') as PlaceFilters['direction'],
  page: Math.max(1, Number(route.query.page ?? 1) || 1),
  per_page: 20,
}))

const query = computed(() =>
  placeQuery({ ...filters.value, type: filters.value.type }),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<PagedPlaces>(`/projects/places?${query.value}`)
    places.value = answer.places ?? []
    total.value = answer.total ?? 0
    page.value = answer.page ?? 1
    pages.value = Math.max(1, answer.pages ?? 1)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/** Goes to a filtered list, or to another page of this one. */
function go(change: Partial<PlaceFilters>) {
  const next: PlaceFilters = { ...filters.value, ...change }
  // Any narrowing starts again at the first page: page seven of a search that now
  // matches three things is a page about nothing.
  if (change.search !== undefined || change.type !== undefined || change.visibility !== undefined || change.scope !== undefined || change.sort !== undefined || change.direction !== undefined) {
    next.page = 1
  }
  const search = placeQuery(next)
  navigateTo({ path: route.path, query: Object.fromEntries(new URLSearchParams(search)) })
}

// Searching waits until the typing stops. Every keystroke is a request otherwise, and
// on a slow connection they arrive out of order: the list shows results for a prefix
// that was typed two words ago.
let wait: ReturnType<typeof setTimeout> | undefined
watch(typing, (word) => {
  clearTimeout(wait)
  wait = setTimeout(() => {
    if (word !== filters.value.search) go({ search: word })
  }, 300)
})

onMounted(() => {
  typing.value = filters.value.search
  void load()
})

watch(query, () => void load())
const closeMenus = (event: MouseEvent) => {
  const target = event.target as HTMLElement | null
  const clickedMenu = target?.closest<HTMLDetailsElement>('.places-row-menu')
  document.querySelectorAll<HTMLDetailsElement>('.places-row-menu[open]').forEach((menu) => {
    if (menu !== clickedMenu) menu.open = false
  })
}

onMounted(() => document.addEventListener('click', closeMenus))
onBeforeUnmount(() => {
  clearTimeout(wait)
  document.removeEventListener('click', closeMenus)
})

/** The range shown in the footer: "1–20 of 340". */
const range = computed(() => {
  if (total.value === 0) return 'nothing'
  const first = (page.value - 1) * filters.value.per_page + 1
  const last = Math.min(first + places.value.length - 1, total.value)
  return `${first}–${last} of ${total.value}`
})

const visibilities = [
  { value: '', label: 'Any visibility' },
  { value: 'private', label: 'Private' },
  { value: 'internal', label: 'Internal' },
  { value: 'public', label: 'Public' },
]
</script>

<template>
  <div>
    <div class="toolbar">
      <div class="places-search">
        <svg class="places-search-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
          <circle cx="10.8" cy="10.8" r="6.3" />
          <path d="m15.5 15.5 4.2 4.2" />
        </svg>
        <input v-model="typing" class="search" type="search" placeholder="Filter or search projects" aria-label="Search projects and groups">
      </div>

      <label class="places-sort">
        <span class="sr-only">Sort projects</span>
        <select :value="filters.sort" aria-label="Sort projects" @change="go({ sort: ($event.target as HTMLSelectElement).value as PlaceFilters['sort'] })">
          <option value="name">Name</option>
          <option value="created">Created date</option>
          <option value="last_activity">Last activity</option>
        </select>
      </label>
      <button class="places-sort-direction" type="button" :title="filters.direction === 'asc' ? 'Sort descending' : 'Sort ascending'" :aria-label="filters.direction === 'asc' ? 'Sort descending' : 'Sort ascending'" @click="go({ direction: filters.direction === 'asc' ? 'desc' : 'asc' })">
        <svg viewBox="0 0 16 16" aria-hidden="true" focusable="false" :class="{ descending: filters.direction === 'desc' }"><path d="M8 2v11M3.5 8.5 8 13l4.5-4.5" /></svg>
      </button>

      <select
        :value="filters.visibility"
        aria-label="Visibility"
        @change="go({ visibility: ($event.target as HTMLSelectElement).value })"
      >
        <option v-for="one in visibilities" :key="one.value" :value="one.value">{{ one.label }}</option>
      </select>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-else-if="loading" class="spinner">Loading…</div>

    <div v-else-if="places.length === 0" class="card empty">
      <template v-if="filters.search">
        Nothing here is called “{{ filters.search }}”.
      </template>
      <template v-else>
        Nothing here yet.
      </template>
    </div>

    <template v-else>
      <table class="table">
        <thead>
          <tr>
            <th>Project</th>
            <th>Tags</th>
            <th>Pipeline</th>
            <th>Activity</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="place in sortedPlaces" :key="place.id">
            <td>
              <div class="place-name-line">
                <span class="place-avatar" :class="place.kind === 'group' ? 'is-group' : 'is-project'">
                  {{ (place.name || place.path).slice(0, 1).toUpperCase() }}
                </span>
                <div class="place-name-content">
                  <NuxtLink :to="placeHref(place)" class="name">{{ place.name || place.path }}</NuxtLink>
                  <div class="muted small mono">{{ place.path }}</div>
                  <div v-if="place.description" class="muted small place-description">{{ place.description }}</div>
                </div>
              </div>
            </td>
            <td>
              <div class="places-tags">
                <span v-if="place.kind === 'group'" class="badge badge-neutral">Group</span>
                <span v-else-if="place.visibility" class="badge" :class="'badge-' + place.visibility">{{ place.visibility }}</span>
                <span v-if="place.access_name" class="badge badge-neutral">{{ place.access_name }}</span>
                <span v-if="place.kind === 'project' && filters.scope === 'inactive'" class="badge badge-neutral">Archived</span>
                <span v-if="place.kind === 'group' && place.project_count !== undefined" class="muted small">{{ place.project_count }} projects</span>
              </div>
            </td>
            <td class="places-pipeline-cell">
              <span v-if="place.kind === 'project' && place.latest_pipeline_status" class="pipeline-state" :class="'pipeline-state-' + place.latest_pipeline_status">
                <span class="pipeline-state-dot"></span>{{ pipelineLabel(place.latest_pipeline_status) }}
              </span>
              <span v-else class="muted small">—</span>
              <div v-if="place.latest_pipeline_at" class="muted small">{{ formatDate(place.latest_pipeline_at) }}</div>
            </td>
            <td class="places-activity-cell">
              <div class="places-activity-counts">
                <NuxtLink v-if="place.open_merge_requests > 0" :to="place.kind === 'project' ? '/p/' + place.path + '/-/merge_requests' : placeHref(place)" :title="place.open_merge_requests + ' open merge requests'" class="places-count">
                  <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2v12M4 4h5a3 3 0 0 1 3 3v2M9 12l3 3 3-3" /></svg>{{ place.open_merge_requests }}
                </NuxtLink>
                <span v-else class="places-count muted" title="Open merge requests: 0"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2v12M4 4h5a3 3 0 0 1 3 3v2M9 12l3 3 3-3" /></svg>0</span>
                <span class="places-count" :class="{ muted: !place.open_issues }" :title="place.open_issues + ' open issues'">
                  <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="5.5" /><path d="M8 5v3.4M8 11h.01" /></svg>{{ place.open_issues }}
                </span>
              </div>
              <div class="places-last-commit" v-if="place.last_commit_at">
                <span class="places-commit-message" :title="place.last_commit_message">{{ place.last_commit_message || 'Commit' }}</span>
                <time class="muted small" :datetime="place.last_commit_at">{{ formatDate(place.last_commit_at) }}</time>
              </div>
              <span v-else class="muted small">No commits yet</span>
            </td>
            <td class="places-actions-cell">
              <details class="places-row-menu">
                <summary aria-label="Row actions">···</summary>
                <div class="places-row-menu-popover">
                  <NuxtLink :to="placeHref(place)" class="menu-item">Open {{ place.kind }}</NuxtLink>
                  <NuxtLink v-if="place.kind === 'project'" :to="'/p/' + place.path + '/-/settings'" class="menu-item">Settings</NuxtLink>
                  <NuxtLink v-else :to="'/groups/' + place.id" class="menu-item">Group details</NuxtLink>
                </div>
              </details>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="pager">
        <span class="muted small">{{ range }}</span>
        <div class="spacer" />
        <button class="btn btn-small" type="button" :disabled="page <= 1" @click="go({ page: page - 1 })">
          Previous
        </button>
        <span class="muted small">page {{ page }} of {{ pages }}</span>
        <button
          class="btn btn-small"
          type="button"
          :disabled="page >= pages"
          @click="go({ page: page + 1 })"
        >
          Next
        </button>
      </div>
    </template>
  </div>
</template>

