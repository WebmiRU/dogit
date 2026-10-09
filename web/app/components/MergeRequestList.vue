<script setup lang="ts">
/**
 * The merge requests of one project, with the form for opening one.
 *
 * The states are a filter rather than separate pages, because the question being
 * asked is usually "what is waiting for me", not "show me the closed ones".
 */
import type { MergeRequest } from '~/types/merge_requests'
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  defaultBranch: string
  canCreate: boolean
}>()

const emit = defineEmits<{ (event: 'refs-changed'): void }>()

const states = [
  { value: 'opened', label: 'Open' },
  { value: 'merged', label: 'Merged' },
  { value: 'closed', label: 'Closed' },
  { value: 'all', label: 'All' },
] as const

const state = ref<(typeof states)[number]['value']>('opened')
const requests = ref<MergeRequest[]>([])
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<{ merge_requests: MergeRequest[] }>(
      `/projects/${props.projectId}/merge_requests`,
      { state: state.value },
    )
    requests.value = response.merge_requests
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(state, load)

async function created(mr: MergeRequest) {
  // A new request is by definition an open one, so the list moves there rather
  // than leaving the person looking at a filtered view that does not contain it.
  state.value = 'opened'
  await navigateTo(`/p/${props.projectPath}/-/merge_requests/${mr.iid}`)
}
</script>

<template>
  <div>
    <MergeRequestForm
      v-if="canCreate"
      :project-id="projectId"
      :project-path="projectPath"
      :refs="refs"
      :default-branch="defaultBranch"
      @created="created"
    />

    <div class="card">
      <div class="toolbar">
        <nav class="mr-state-tabs" aria-label="Filter merge requests">
          <button
            v-for="option in states"
            :key="option.value"
            class="btn mr-state-tab"
            :class="{ active: state === option.value }"
            type="button"
            @click="state = option.value"
          >
            {{ option.label }}
          </button>
        </nav>
      </div>

      <div v-if="loading" class="empty">Loading merge requests…</div>
      <div v-else-if="loadError" class="empty">{{ loadError }}</div>
      <div v-else-if="requests.length === 0" class="empty">
        No {{ state === 'all' ? '' : state }} merge requests.
      </div>
      <ul v-else class="tree-list">
        <li v-for="mr in requests" :key="mr.id">
          <span class="icon">⑂</span>
          <div class="mr-body">
            <NuxtLink class="name" :to="`/p/${projectPath}/-/merge_requests/${mr.iid}`">
              {{ mr.title }}
            </NuxtLink>
            <div class="meta">
              <span class="mono">{{ mr.source_branch }}</span>
              <span>into</span>
              <span class="mono">{{ mr.target_branch }}</span>
              <span>·</span>
              <span>{{ mr.author_name || mr.author_username }}</span>
              <span>·</span>
              <span>{{ timeAgo(mr.updated_at) }}</span>
              <template v-if="mr.diff_stats">
                <span class="add">+{{ mr.diff_stats.additions }}</span>
                <span class="del">−{{ mr.diff_stats.deletions }}</span>
              </template>
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

.add {
  color: #3fb950;
}

.del {
  color: #f85149;
}



</style>