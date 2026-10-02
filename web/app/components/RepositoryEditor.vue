<script setup lang="ts">
/**
 * File editor.
 *
 * Saving is a single request: the content becomes a blob, a tree and a commit on
 * the server, and the branch moves only if it has not moved since the file was
 * loaded. That is why the editor sends the blob id it started from — a conflict
 * is reported instead of silently overwriting someone else's work.
 */
import type { FileResponse, RefsResponse } from '~/types/repository'

const emit = defineEmits<{ (event: 'change-ref', ref: string): void }>()

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  refName: string
  path: string
  canPush: boolean
}>()

const original = ref('')
const blobSha = ref('')
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const saveError = ref('')
const conflict = ref('')

const content = ref('')
const message = ref('')
const targetBranch = ref('')
/** When set, the commit lands on a new branch instead of the one being viewed. */
const newBranch = ref('')

/** A new file has nothing to load; the path comes from the URL. */
const isNewFile = computed(() => props.path.endsWith('/-/new') || props.path === '')

onMounted(async () => {
  targetBranch.value = props.refName
  if (isNewFile.value) {
    loading.value = false
    message.value = `Add ${props.path}`
    return
  }
  await load()
})

watch(() => [props.refName, props.path], load)

async function load() {
  loading.value = true
  loadError.value = ''
  saveError.value = ''
  conflict.value = ''
  try {
    const file = await api.get<FileResponse>(`/projects/${props.projectId}/repository/file`, {
      ref: props.refName,
      path: props.path,
    })
    if (file.binary) {
      loadError.value = 'this file is binary and cannot be edited'
      return
    }
    original.value = file.content
    blobSha.value = file.sha
    content.value = file.content
    if (!message.value) message.value = `Update ${props.path}`
  } catch (caught) {
    // A file that is not there yet is not an error: it is a create.
    if (caught instanceof ApiError && caught.status === 404) {
      original.value = ''
      blobSha.value = ''
      content.value = ''
      if (!message.value) message.value = `Add ${props.path}`
      return
    }
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

const lineCount = computed(() => content.value.split('\n').length)

/** Tab indents instead of leaving the field, which is what an editor must do. */
function onTab(event: KeyboardEvent) {
  const el = event.target as HTMLTextAreaElement
  const start = el.selectionStart
  const end = el.selectionEnd

  event.preventDefault()
  content.value = `${content.value.slice(0, start)}  ${content.value.slice(end)}`
  nextTick(() => {
    el.selectionStart = el.selectionEnd = start + 2
  })
}

const dirty = computed(() => content.value !== original.value)

interface DiffLine {
  kind: ' ' | '+' | '-'
  text: string
}

/**
 * A line diff, computed in the browser so the preview needs no round trip.
 *
 * The common prefix and suffix are dropped first, which makes the usual case —
 * one file edited in a few places — cheap without needing a real diff algorithm.
 */
const diff = computed<DiffLine[]>(() => {
  const before = original.value.split('\n')
  const after = content.value.split('\n')

  let start = 0
  while (start < before.length && start < after.length && before[start] === after[start]) start++

  let end = 0
  while (
    end < before.length - start &&
    end < after.length - start &&
    before[before.length - 1 - end] === after[after.length - 1 - end]
  ) end++

  // The indexes are all within bounds of the array they index, but TypeScript
  // cannot see that, so each read is spelled out.
  const at = (lines: string[], index: number): string => lines[index] ?? ''

  const lines: DiffLine[] = []
  for (let i = 0; i < start; i++) lines.push({ kind: ' ', text: at(before, i) })
  for (let i = start; i < before.length - end; i++) lines.push({ kind: '-', text: at(before, i) })
  for (let i = start; i < after.length - end; i++) lines.push({ kind: '+', text: at(after, i) })
  for (let i = 0; i < end; i++) {
    lines.push({ kind: ' ', text: at(before, before.length - 1 - i) })
  }
  return lines
})

const changes = computed(() => ({
  additions: diff.value.filter((l) => l.kind === '+').length,
  deletions: diff.value.filter((l) => l.kind === '-').length,
}))

async function save() {
  saveError.value = ''
  conflict.value = ''
  saving.value = true
  try {
    const response = await api.post<{ commit_sha: string; branch: string }>(
      `/projects/${props.projectId}/repository/files`,
      {
        branch: targetBranch.value,
        new_branch: newBranch.value.trim(),
        path: props.path,
        content: content.value,
        message: message.value.trim(),
        start_sha: blobSha.value,
      },
    )
    await navigateTo(`/p/${props.projectPath}/-/commit/${response.commit_sha}`)
  } catch (caught) {
    if (caught instanceof ApiError && caught.status === 409) {
      conflict.value = caught.message
    } else {
      saveError.value = caught instanceof ApiError ? caught.message : 'the request failed'
    }
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="toolbar">
      <BranchSelector v-if="!isNewFile" :refs="refs" :ref-name="refName" @change="emit('change-ref', $event)" />

      <div class="breadcrumbs">
        <NuxtLink :to="`/p/${projectPath}/-/tree/${encodeURIComponent(refName)}`">{{ projectPath }}</NuxtLink>
        <span class="sep">/</span>
        <span>{{ path }}</span>
      </div>

      <div class="spacer" />
      <NuxtLink
        v-if="!isNewFile"
        class="btn"
        :to="`/p/${projectPath}/-/blob/${encodeURIComponent(refName)}/${path}`"
      >
        Cancel
      </NuxtLink>
    </div>

    <div v-if="loading" class="spinner">Loading file…</div>
    <div v-else-if="loadError" class="alert alert-error" style="margin: 16px">{{ loadError }}</div>

    <template v-else>
      <div v-if="!canPush" class="alert" style="margin: 16px">
        You have read access to this project, so you cannot commit here.
      </div>

      <div v-else class="editor-body">
        <div class="editor-area">
          <div class="gutter" aria-hidden="true">
            <span v-for="n in lineCount" :key="n">{{ n }}</span>
          </div>
          <textarea
            v-model="content"
            class="editor-input"
            spellcheck="false"
            autocomplete="off"
            autocapitalize="off"
            @keydown.tab="onTab"
          />
        </div>

        <details class="preview" :open="dirty">
          <summary>
            Changes
            <span class="delta-add">+{{ changes.additions }}</span>
            <span class="delta-del">−{{ changes.deletions }}</span>
          </summary>
          <div class="diff">
            <div v-for="(line, index) in diff" :key="index" class="diff-line" :class="line.kind === '+' ? 'diff-add' : line.kind === '-' ? 'diff-del' : 'diff-ctx'">
              {{ line.text }}
            </div>
          </div>
        </details>

        <form class="commit-box" @submit.prevent="save">
          <div class="field">
            <label for="commit-branch">Commit to</label>
            <select id="commit-branch" v-model="targetBranch" :disabled="!!newBranch.trim()">
              <option v-for="branch in refs?.branches ?? []" :key="branch.name" :value="branch.name">
                {{ branch.name }}
              </option>
            </select>
          </div>

          <div class="field">
            <label for="new-branch">or a new branch</label>
            <input id="new-branch" v-model="newBranch" placeholder="leave empty to commit to the branch above" />
          </div>

          <div class="field">
            <label for="commit-message">Commit message</label>
            <input id="commit-message" v-model="message" required />
          </div>

          <div v-if="conflict" class="alert alert-error">
            {{ conflict }}
            <div class="conflict-actions">
              <button class="btn" type="button" @click="load">Discard my changes and reload</button>
            </div>
          </div>
          <div v-else-if="saveError" class="alert alert-error">{{ saveError }}</div>

          <button
            class="btn btn-primary"
            type="submit"
            :disabled="saving || !dirty || !message.trim()"
          >
            {{ saving ? 'Committing…' : 'Commit changes' }}
          </button>
        </form>
      </div>
    </template>
  </div>
</template>

<style scoped>
.editor-body {
  padding: 16px;
}

.editor-area {
  display: flex;
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  background: var(--bg-code, #0d1117);
}

/* The gutter scrolls with the text, so the two are one scrolling box. */
.gutter {
  display: flex;
  flex-direction: column;
  padding: 12px 8px;
  text-align: right;
  font: 13px/1.5 var(--mono);
  color: #6e7681;
  user-select: none;
  overflow: hidden;
}

.editor-input {
  flex: 1;
  min-height: 420px;
  padding: 12px;
  border: 0;
  background: transparent;
  color: inherit;
  font: 13px/1.5 var(--mono);
  resize: vertical;
}

.preview {
  margin-top: 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 12px;
}

.summary {
  cursor: pointer;
  font-size: 13px;
  color: var(--muted);
}

.delta-add {
  color: #3fb950;
  margin-left: 8px;
}

.delta-del {
  color: #f85149;
  margin-left: 8px;
}

.diff {
  margin-top: 8px;
  max-height: 320px;
  overflow: auto;
  font: 12px/1.5 var(--mono);
  white-space: pre-wrap;
}

.diff-line {
  padding: 0 6px;
}

/* The class names spell the kind out rather than using a sign, because "+" and
   "-" are not usable in a CSS selector. */
.diff-add {
  background: rgba(63, 185, 80, 0.15);
}

.diff-del {
  background: rgba(248, 81, 73, 0.15);
}

.diff-ctx {
  color: var(--muted);
}

.commit-box {
  margin-top: 16px;
  display: grid;
  gap: 12px;
  max-width: 520px;
}

.conflict-actions {
  margin-top: 8px;
}
</style>