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

const places = ref<Place[]>([])
const total = ref(0)
const page = ref(1)
const pages = ref(1)
const loading = ref(true)
const error = ref('')

/** The word being typed, kept apart from what has been searched for yet. */
const typing = ref('')

/** What is asked of the server, read out of the address bar. */
const filters = computed<Required<PlaceFilters>>(() => ({
  search: String(route.query.search ?? ''),
  type: (props.type || String(route.query.type ?? '')) as PlaceFilters['type'] as 'project' | 'group' | '',
  visibility: String(route.query.visibility ?? ''),
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
  if (change.search !== undefined || change.type !== undefined || change.visibility !== undefined) {
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
onBeforeUnmount(() => clearTimeout(wait))

/** The range shown in the footer: "1–20 of 340". */
const range = computed(() => {
  if (total.value === 0) return 'nothing'
  const first = (page.value - 1) * filters.value.per_page + 1
  const last = Math.min(first + places.value.length - 1, total.value)
  return `${first}–${last} of ${total.value}`
})

const kinds = [
  { value: '', label: 'Everything' },
  { value: 'project', label: 'Projects' },
  { value: 'group', label: 'Groups' },
]

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
      <input
        v-model="typing"
        class="search"
        type="search"
        placeholder="Search by name, path or description"
        aria-label="Search projects and groups"
      >

      <select
        v-if="filterKind"
        :value="filters.type"
        aria-label="Which kind of place"
        @change="go({ type: ($event.target as HTMLSelectElement).value as PlaceFilters['type'] })"
      >
        <option v-for="one in kinds" :key="one.value" :value="one.value">{{ one.label }}</option>
      </select>

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
            <th>Name</th>
            <th>Kind</th>
            <th>Visibility</th>
            <th>Your access</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="place in places" :key="place.id">
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
              <span class="badge" :class="place.kind === 'group' ? 'badge-blue' : 'badge-neutral'">
                {{ place.kind }}
              </span>
              <div v-if="place.kind === 'group' && place.project_count !== undefined" class="muted small">
                {{ place.project_count }} {{ place.project_count === 1 ? 'project' : 'projects' }}
              </div>
            </td>
            <td>
              <span v-if="place.kind === 'project'" class="badge" :class="`badge-${place.visibility}`">
                {{ place.visibility }}
              </span>
              <span v-else class="muted small">inherited by its projects</span>
            </td>
            <td class="muted small">{{ place.access_name }}</td>
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

