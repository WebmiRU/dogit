<script setup lang="ts">
/** Commit list for a ref. */
import type { CommitInfo, CommitsResponse, RefsResponse } from '~/types/repository'

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
  <div class="card">
    <div class="toolbar">
      <BranchSelector :refs="refs" :project-path="projectPath" :ref-name="refName" />
      <div class="spacer" />
      <span v-if="response" class="muted">{{ response.total }} commits</span>
    </div>

    <div v-if="loading" class="spinner">Loading commits…</div>
    <div v-else-if="loadError" class="alert alert-error" style="margin: 16px">{{ loadError }}</div>
    <div v-else-if="commits.length === 0" class="empty">No commits on this branch yet.</div>
    <ul v-else class="commit-list">
      <li v-for="commit in commits" :key="commit.sha">
        <div class="msg">
          <NuxtLink :to="`/p/${projectPath}/-/commit/${commit.sha}`">
            {{ commit.subject }}
          </NuxtLink>
          <div class="sub">
            {{ commit.author_name }} committed {{ timeAgo(commit.timestamp) }}
          </div>
        </div>
        <div class="sub mono">{{ commit.sha }}</div>
      </li>
    </ul>

    <div v-if="response" class="toolbar" style="border-top: 1px solid var(--border); border-bottom: none">
      <button class="btn" type="button" :disabled="page === 1" @click="page -= 1">Newer</button>
      <span class="muted">Page {{ response.page }}</span>
      <button class="btn" type="button" :disabled="!response.has_more" @click="page += 1">Older</button>
    </div>
  </div>
</template>
