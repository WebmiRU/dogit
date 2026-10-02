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

/** One diff line, already classified for colouring. */
interface DiffLine {
  kind: 'add' | 'del' | 'hunk' | 'context'
  text: string
  oldNo: number
  newNo: number
}

function splitPatch(change: FileChange): DiffLine[] {
  const out: DiffLine[] = []
  let oldNo = 0
  let newNo = 0

  for (const raw of change.patch.split('\n')) {
    if (raw.startsWith('@@')) {
      const match = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw)
      if (match) {
        oldNo = Number(match[1])
        newNo = Number(match[2])
      }
      out.push({ kind: 'hunk', text: raw, oldNo: 0, newNo: 0 })
      continue
    }
    if (raw.startsWith('diff ') || raw.startsWith('index ') || raw.startsWith('--- ') || raw.startsWith('+++ ')) {
      continue
    }
    if (raw.startsWith('+')) {
      out.push({ kind: 'add', text: raw.slice(1), oldNo: 0, newNo: newNo++ })
    } else if (raw.startsWith('-')) {
      out.push({ kind: 'del', text: raw.slice(1), oldNo: oldNo++, newNo: 0 })
    } else if (raw.startsWith(' ')) {
      out.push({ kind: 'context', text: raw.slice(1), oldNo: oldNo++, newNo: newNo++ })
    }
  }
  return out
}

const collapsed = ref<Record<string, boolean>>({})

function toggle(path: string) {
  collapsed.value[path] = !collapsed.value[path]
}
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

        <section v-for="change in diff.files" :key="change.path" class="file-change">
          <header>
            <span class="badge">{{ change.status }}</span>
            <span>{{ change.old_path ? `${change.old_path} → ${change.path}` : change.path }}</span>
            <span class="stat">
              <span class="stat-add">+{{ change.additions }}</span>
              <span class="stat-del">−{{ change.deletions }}</span>
              <button class="btn" style="margin-left: 10px" type="button" @click="toggle(change.path)">
                {{ collapsed[change.path] ? 'Expand' : 'Collapse' }}
              </button>
            </span>
          </header>
          <div v-if="!collapsed[change.path]" class="code-view">
            <template v-if="change.binary">
              <div class="code-line"><span class="ln">—</span><span class="lc muted">Binary file not shown</span></div>
            </template>
            <div v-for="(line, index) in splitPatch(change)" :key="index" class="code-line" :class="line.kind">
              <span class="ln">{{ line.oldNo || '' }}</span>
              <span class="ln">{{ line.newNo || '' }}</span>
              <span class="lc">{{ line.text }}</span>
            </div>
          </div>
        </section>
      </div>
    </template>
  </div>
</template>
