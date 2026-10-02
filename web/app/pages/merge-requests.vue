<script setup lang="ts">
/**
 * Merge request list across every project the user may read.
 *
 * The state is part of the URL rather than a local toggle, so a link to "the open
 * ones" is a link someone can send.
 */
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
  return states.some((s) => s.value === raw) ? raw : 'opened'
})

const requests = ref<MergeRequest[]>([])
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<{ merge_requests: MergeRequest[] }>('/merge_requests', {
      state: state.value,
    })
    requests.value = response.merge_requests
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(state, load)

</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title">Merge requests</h1>
        <p class="page-subtitle">Requests to bring one branch into another.</p>
      </div>
      <nav class="repo-tabs">
        <!-- The page marks the current filter itself. These links differ only by
             query, which the router does not take into account when deciding what
             is active, so it would call all of them current at once. -->
        <NuxtLink
          v-for="option in states"
          :key="option.value"
          :to="{ path: '/merge-requests', query: { state: option.value } }"
          :class="{ active: state === option.value }"
        >
          {{ option.label }}
        </NuxtLink>
      </nav>
    </div>

    <div v-if="loading" class="spinner">Loading merge requests…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>
    <div v-else-if="requests.length === 0" class="card empty">
      Nothing here. A merge request is opened from a project's
      <NuxtLink to="/projects">branches page</NuxtLink>.
    </div>

    <div v-else class="card">
      <ul class="tree-list">
        <li v-for="mr in requests" :key="mr.id">
          <span class="icon">⑂</span>
          <div class="mr-body">
            <NuxtLink class="name" :to="mr.url || `/p/${mr.project?.path}/-/merge_requests/${mr.iid}`">
              {{ mr.title }}
            </NuxtLink>
            <div class="meta">
              <span class="mono">{{ mr.source_branch }}</span>
              <span>into</span>
              <span class="mono">{{ mr.target_branch }}</span>
              <span>·</span>
              <span class="mono">{{ mr.project?.path }}</span>
            </div>
            <div class="meta">
              <span>opened by {{ mr.author_name || mr.author_username }}</span>
              <span>·</span>
              <span>{{ timeAgo(mr.updated_at) }}</span>
              <span
                v-if="mr.diff_stats"
                class="diff-stat"
              >
                <span class="add">+{{ mr.diff_stats.additions }}</span>
                <span class="del">−{{ mr.diff_stats.deletions }}</span>
              </span>
            </div>
          </div>
          <span class="badge" :class="`state-${mr.state}`">{{ mr.state }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.mr-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

/* The meta line is a row of words, so it is laid out as one: without this the
   spans sit against each other and "into main" reads as "intom ain". */
.meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.diff-stat .add {
  color: #3fb950;
}

.diff-stat .del {
  color: #f85149;
}



</style>