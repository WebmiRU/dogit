<script setup lang="ts">
/** A single commit with its file diff. */
import type { CommitDiffResponse, CommitResponse, FileChange } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  sha: string
}>()

const commit = ref<CommitResponse | null>(null)
const diff = ref<CommitDiffResponse | null>(null)
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    commit.value = await api.get<CommitResponse>(
      `/projects/${props.projectId}/repository/commits/${props.sha}`,
    )
    diff.value = await api.get<CommitDiffResponse>(
      `/projects/${props.projectId}/repository/commits/${props.sha}/diff`,
    )
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.sha, load)

</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading commit…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <template v-else-if="commit && diff">
      <div class="card" style="margin-bottom: 16px">
        <div class="card-body">
          <h2 class="page-title" style="font-size: 16px">{{ commit.commit.subject }}</h2>
          <p class="muted" style="margin: 6px 0 0">
            {{ commit.commit.author_name }} committed {{ timeAgo(commit.commit.timestamp) }} ·
            <span class="sha mono">{{ commit.commit.sha.slice(0, 10) }}</span>
            <span v-for="branch in commit.branches" :key="branch" class="badge">{{ branch }}</span>
          </p>
          <pre
            v-if="commit.commit.message !== commit.commit.subject"
            class="muted"
            style="white-space: pre-wrap; margin: 12px 0 0"
          >{{ commit.commit.message }}</pre>
        </div>
      </div>

      <div class="card">
        <div class="toolbar">
          <strong>{{ diff.stats.files_changed }} files changed</strong>
          <span class="stat-add">+{{ diff.stats.additions }}</span>
          <span class="stat-del">−{{ diff.stats.deletions }}</span>
        </div>

        <div v-if="diff.files.length === 0" class="empty">No file changes.</div>

        <DiffFile
          v-for="change in diff.files"
          :key="change.path"
          :change="change"
          collapsible
          :title="change.old_path ? `${change.old_path} → ${change.path}` : ''"
        />
      </div>
    </template>
  </div>
</template>
