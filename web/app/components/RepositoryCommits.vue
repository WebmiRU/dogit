<script setup lang="ts">
/** Commit list for a ref. */
import type { CommitInfo, CommitsResponse, RefsResponse } from '~/types/repository'

const emit = defineEmits<{ (event: 'change-ref', ref: string): void }>()

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  refName: string
}>()

const page = ref(1)
const response = ref<CommitsResponse | null>(null)
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    response.value = await api.get<CommitsResponse>(
      `/projects/${props.projectId}/repository/commits`,
      { ref: props.refName, page: page.value, limit: 30 },
    )
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => [props.refName, page.value], load)

const commits = computed<CommitInfo[]>(() => response.value?.commits ?? [])
</script>

<template>
  <div>
    <div class="toolbar repository-toolbar repository-page-toolbar">
      <BranchSelector :refs="refs" :ref-name="refName" @change="emit('change-ref', $event)" />
      <div class="spacer" />
      <span v-if="response" class="muted">{{ response.total }} commits</span>
    </div>

    <div v-if="loading" class="spinner">Loading commits…</div>
    <div v-else-if="loadError" class="alert alert-error" style="margin: 16px">{{ loadError }}</div>
    <div v-else-if="commits.length === 0" class="empty">No commits on this branch yet.</div>
    <div v-else class="repository-table-wrap">
      <table class="admin-table repository-flat-table commits-table">
        <thead>
          <tr><th>Commit</th><th>Author</th><th>Committed</th><th>SHA</th></tr>
        </thead>
        <tbody>
          <tr v-for="commit in commits" :key="commit.sha">
            <td><NuxtLink :to="`/p/${projectPath}/-/commit/${commit.sha}`">{{ commit.subject }}</NuxtLink></td>
            <td class="muted">{{ commit.author_name }}</td>
            <td class="muted small">{{ timeAgo(commit.timestamp) }}</td>
            <td class="mono small">{{ commit.sha.slice(0, 10) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="response" class="toolbar repository-toolbar repository-page-toolbar repository-pager">
      <button class="btn" type="button" :disabled="page === 1" @click="page -= 1">Newer</button>
      <span class="muted">Page {{ response.page }}</span>
      <button class="btn" type="button" :disabled="!response.has_more" @click="page += 1">Older</button>
    </div>
  </div>
</template>
