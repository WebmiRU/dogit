<script setup lang="ts">
/**
 * Conflict resolution for a merge request.
 *
 * git leaves three versions of a conflicted file and refuses to choose. This shows
 * them one after another, full width — code is wider than a third of a column —
 * and lets a person pick one by clicking it, or write something else.
 *
 * Nothing is written until the merge happens, so looking costs nothing.
 */
import type { MergeRequest } from '~/types/merge_requests'

const props = defineProps<{
  projectId: string
  projectPath: string
  mr: MergeRequest
}>()

const emit = defineEmits<{ (event: 'resolved'): void }>()

interface ConflictFile {
  path: string
  status: string
  base: string
  ours: string
  theirs: string
  base_text: string
  ours_text: string
  theirs_text: string
}

/** ours | theirs | base | manual */
type Choice = 'ours' | 'theirs' | 'base' | 'manual'

const files = ref<ConflictFile[]>([])
const loading = ref(true)
const error = ref('')
const choices = ref<Record<string, Choice>>({})
const manual = ref<Record<string, string>>({})
const resolving = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const response = await api.get<{ conflicts: ConflictFile[] }>(
      `/projects/${props.projectId}/merge_requests/${props.mr.iid}/conflicts`,
    )
    files.value = response.conflicts ?? []
    // The target branch's side is the default: it is the history this merge is
    // joining, so a person who wanted the other side has to say so deliberately.
    for (const file of files.value) choices.value[file.path] = 'ours'
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.mr.iid, load)

/** Which side is in the merge for this file right now. */
function pick(file: ConflictFile, choice: Choice) {
  choices.value[file.path] = choice
}

async function resolveAndMerge() {
  resolving.value = true
  error.value = ''
  try {
    const resolutions = files.value.map((file) => {
      const choice = choices.value[file.path] ?? 'ours'
      if (choice === 'manual') return { path: file.path, text: manual.value[file.path] ?? '' }
      return { path: file.path, stage: choice }
    })

    await api.post(`/projects/${props.projectId}/merge_requests/${props.mr.iid}/merge`, {
      method: 'merge',
      resolutions,
    })
    emit('resolved')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    resolving.value = false
  }
}

/**
 * The three versions, in the order they are offered.
 *
 * The names say what each one is rather than which side won: "theirs" is
 * meaningless to anyone who has not read a git manual.
 */
function versions(file: ConflictFile) {
  return [
    {
      key: 'ours' as Choice,
      title: `The target branch, ${props.mr.target_branch}`,
      note: 'What the branch being merged into contains today. Picking this keeps the target as it is for this file.',
      text: file.ours_text,
      exists: !!file.ours,
    },
    {
      key: 'theirs' as Choice,
      title: `The source branch, ${props.mr.source_branch}`,
      note: 'What the branch asking to be merged brings. Picking this brings this file’s changes across.',
      text: file.theirs_text,
      exists: !!file.theirs,
    },
    {
      key: 'base' as Choice,
      title: 'Before either side changed it',
      note: 'The file as it was where the two branches parted. Picking this discards both changes to this file — usually it means this file has no reason to be in the merge at all.',
      text: file.base_text,
      exists: !!file.base,
    },
  ]
}
</script>

<template>
  <div class="card">
    <div class="card-header"><strong>Resolve conflicts</strong></div>

    <div class="card-body">
      <div v-if="loading" class="muted">Reading the conflicted files…</div>
      <div v-else-if="error" class="alert alert-error">{{ error }}</div>
      <div v-else-if="files.length === 0" class="muted">Nothing to resolve.</div>

      <template v-else>
        <p class="muted resolver-note">
          git left {{ files.length }} {{ files.length === 1 ? 'file' : 'files' }} with
          changes on both sides and would not choose. Pick what each file should
          contain in the result, or write it yourself. Nothing is committed until
          you merge.
        </p>

        <div v-for="file in files" :key="file.path" class="conflict">
          <div class="toolbar">
            <NuxtLink
              class="mono"
              :to="repoViewUrl(projectPath, 'blob', mr.target_branch, file.path)"
            >
              {{ file.path }}
            </NuxtLink>
            <span class="badge">{{ file.status }}</span>
            <div class="spacer" />
            <span class="choice-label">
              In the result:
              <strong>
                {{
                  choices[file.path] === 'manual'
                    ? 'written by hand'
                    : choices[file.path] === 'ours'
                      ? `the ${mr.target_branch} version`
                      : choices[file.path] === 'theirs'
                        ? `the ${mr.source_branch} version`
                        : 'the original version'
                }}
              </strong>
            </span>
          </div>

          <button
            v-for="version in versions(file)"
            :key="version.key"
            class="version"
            type="button"
            :class="{ picked: choices[file.path] === version.key }"
            :disabled="!version.exists"
            @click="pick(file, version.key)"
          >
            <header>
              <span class="title">{{ version.title }}</span>
              <span v-if="choices[file.path] === version.key" class="marker">✓ in the result</span>
            </header>
            <p class="note">{{ version.note }}</p>
            <pre v-if="version.exists">{{ version.text }}</pre>
            <p v-else class="absent">This side does not have the file at all.</p>
          </button>

          <div class="field">
            <label class="write-label">
              <input
                type="checkbox"
                :checked="choices[file.path] === 'manual'"
                @change="pick(file, ($event.target as HTMLInputElement).checked ? 'manual' : 'ours')"
              />
              Write the result myself
            </label>
            <textarea
              v-if="choices[file.path] === 'manual'"
              v-model="manual[file.path]"
              rows="10"
              spellcheck="false"
              :placeholder="`The content ${file.path} should have after the merge`"
            />
          </div>
        </div>

        <button class="btn btn-primary" type="button" :disabled="resolving" @click="resolveAndMerge">
          {{ resolving ? 'Merging…' : 'Merge with these resolutions' }}
        </button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.resolver-note {
  margin: 0 0 16px;
  font-size: 13px;
}

.conflict {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 20px;
}

.choice-label {
  font-size: 12px;
  color: var(--muted);
}

.conflict .toolbar strong {
  color: var(--text);
}

/* Each version is a button, not a panel with a button in it: the whole card is
   the target, which is both easier to hit and impossible to half-click. */
.version {
  display: block;
  width: 100%;
  text-align: left;
  margin-top: 10px;
  padding: 0;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: transparent;
  color: inherit;
  cursor: pointer;
  overflow: hidden;
}

.version:hover:not(:disabled) {
  border-color: var(--accent);
}

.version:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.version.picked {
  border-color: var(--accent);
  box-shadow: inset 3px 0 0 var(--accent);
}

.version header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  background: rgba(110, 118, 129, 0.08);
  font-size: 13px;
}

.version .title {
  font-weight: 600;
}

.marker {
  margin-left: auto;
  color: var(--accent);
  font-size: 12px;
}

.note {
  margin: 8px 12px;
  font-size: 12px;
  color: var(--muted);
}

.version pre {
  margin: 0;
  padding: 12px;
  background: var(--bg-code, #0d1117);
  border-top: 1px solid var(--border);
  font: 12px/1.5 var(--mono);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
}

.absent {
  margin: 0;
  padding: 12px;
  border-top: 1px solid var(--border);
  font-size: 12px;
  color: var(--muted);
}

.write-label {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
  font-size: 13px;
}

.write-label input {
  width: auto;
}

textarea {
  margin-top: 8px;
  font: 12px/1.5 var(--mono);
}
</style>