<script setup lang="ts">
/**
 * One merge request.
 *
 * The state shown here — can it merge, does it conflict — is recomputed by the
 * server on every read, because it depends on where the branches are now rather
 * than on when the request was opened.
 */
import type { MergeRequest, MergeRequestNote } from '~/types/merge_requests'
import type { FileChange } from '~/types/repository'

const route = useRoute()
const { ensureLoaded } = useAuth()
await ensureLoaded()

const segments = computed(() => {
  const raw = route.params.path
  const value = Array.isArray(raw) ? raw.join('/') : String(raw ?? '')
  return value.split('/').filter(Boolean)
})

const projectPath = computed(() => {
  const dash = segments.value.indexOf('-')
  return (dash === -1 ? segments.value : segments.value.slice(0, dash)).join('/')
})

const dashIndex = computed(() => segments.value.indexOf('-'))
const iid = computed(() => segments.value[dashIndex.value + 2] ?? '')

interface PageProject {
  id: string
  path: string
  name: string
}

const project = ref<PageProject | null>(null)
const mr = ref<MergeRequest | null>(null)
const notes = ref<MergeRequestNote[]>([])
const files = ref<FileChange[]>([])
const stats = ref<{ additions: number; deletions: number; files_changed: number } | null>(null)
const loading = ref(true)
const loadError = ref('')
const actionError = ref('')
const working = ref('')
// The diff is loaded apart from the request, so a missing branch costs the diff
// and not the page.
const diffError = ref('')
const branchesGone = ref(false)

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const encoded = projectPath.value.split('/').map(encodeURIComponent).join('/')
    const details = await api.get<{ project: PageProject }>(`/projects/${encoded}`)
    if (!details.project) {
      throw new Error('the project could not be read')
    }
    project.value = details.project

    const response = await api.get<{
      merge_request: MergeRequest
      notes?: MergeRequestNote[]
      diff_url?: string
      branches_gone?: boolean
    }>(`/projects/${details.project.id}/merge_requests/${iid.value}`)
    mr.value = response.merge_request
    notes.value = response.notes ?? []

    // The diff comes from the same compare endpoint the compare page uses, so a
    // merge request and a branch comparison read history the same way.
    branchesGone.value = Boolean((response as { branches_gone?: boolean }).branches_gone)
    diffError.value = ''
    files.value = []
    stats.value = null

    if (response.diff_url) {
      try {
        const diff = await api.get<{ files: FileChange[]; stats: typeof stats.value }>(response.diff_url)
        files.value = diff.files ?? []
        stats.value = diff.stats ?? null
      } catch (caught) {
        diffError.value = caught instanceof ApiError ? caught.message : 'the diff could not be loaded'
      }
    }
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => [projectPath.value, iid.value], load)

/**
 * What the merge button says and whether it can be pressed.
 *
 * A button that is simply absent is worse than one that is disabled with a
 * reason: "there is nothing here to merge" and "this is not mergeable" look the
 * same from the outside, and both look like a broken page.
 */
const mergeState = computed(() => {
  const request = mr.value
  if (!request) return { label: 'Merge', enabled: false, reason: '' }

  if (request.state === 'merged') {
    return { label: 'Merged', enabled: false, reason: '' }
  }
  if (request.state === 'closed') {
    return { label: 'Reopen', enabled: true, reason: '' }
  }
  if (request.has_conflicts) {
    // The button stays disabled because the conflict has to be answered first;
    // the resolver below is what makes it answerable without a local clone.
    return {
      label: 'Merge',
      enabled: false,
      reason: 'The branches conflict. Choose what each file should contain below, then merge.',
    }
  }
  if (request.merge_status === 'up_to_date') {
    return {
      label: 'Nothing to merge',
      enabled: false,
      reason: 'The target branch already contains everything from the source branch.',
    }
  }
  if (request.merge_status === 'can_fast_forward') {
    return { label: 'Merge (fast-forward)', enabled: true, reason: '' }
  }
  return { label: 'Merge', enabled: true, reason: '' }
})

async function merge() {
  actionError.value = ''
  working.value = 'merge'
  try {
    await api.post(`/projects/${project.value?.id}/merge_requests/${iid.value}/merge`, {
      method: 'merge',
    })
    await load()
  } catch (caught) {
    actionError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    working.value = ''
  }
}

async function changeState(state: 'opened' | 'closed') {
  actionError.value = ''
  working.value = state
  try {
    await api.put(`/projects/${project.value?.id}/merge_requests/${iid.value}/state`, { state })
    await load()
  } catch (caught) {
    actionError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    working.value = ''
  }
}

/** The merge state in words, rather than the label on the button. */
const statusText = computed(() => {
  const request = mr.value
  if (!request) return ''
  switch (request.merge_status) {
    case 'conflicts':
      return 'Has conflicts'
    case 'can_fast_forward':
      return 'Ready to merge, no merge commit needed'
    case 'can_merge':
      return 'Ready to merge'
    case 'up_to_date':
      return 'Nothing to merge'
    case 'branches_gone':
      return 'The branches are gone'
    default:
      return request.state
  }
})

const comment = ref('')
const commenting = ref(false)

async function addComment() {
  const body = comment.value.trim()
  if (!body) return

  commenting.value = true
  actionError.value = ''
  try {
    const response = await api.post<{ note: MergeRequestNote }>(
      `/projects/${project.value?.id}/merge_requests/${iid.value}/notes`,
      { body },
    )
    notes.value.push(response.note)
    comment.value = ''
  } catch (caught) {
    actionError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    commenting.value = false
  }
}

const patch = computed(() => {
  const text = mr.value?.description || ''
  return text.split('\n').map((line) => ({ kind: 'text' as const, text: line }))
})
</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading merge request…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <template v-else-if="mr">
      <div class="repo-head">
        <div class="title">
          <h1 class="page-title">
            <span class="mono">!{{ mr.iid }}</span> {{ mr.title }}
            <span class="badge" :class="`state-${mr.state}`">{{ mr.state }}</span>
          </h1>
          <p class="page-subtitle">
            {{ mr.author_name || mr.author_username }} wants to merge
            <span class="mono">{{ mr.source_branch }}</span> into
            <span class="mono">{{ mr.target_branch }}</span>
            <span>· {{ timeAgo(mr.created_at) }}</span>
          </p>
        </div>

        <div class="repo-clone">
          <button
            class="btn btn-primary"
            type="button"
            :disabled="!!working || !mergeState.enabled"
            :title="mergeState.reason"
            @click="mr?.state === 'closed' ? changeState('opened') : merge()"
          >
            {{ working ? 'Working…' : mergeState.label }}
          </button>
          <button
            v-if="mr.state === 'opened'"
            class="btn"
            type="button"
            :disabled="!!working"
            @click="changeState('closed')"
          >
            Close
          </button>
        </div>
      </div>

      <div v-if="actionError" class="alert alert-error">{{ actionError }}</div>
      <div v-if="mergeState.reason" class="alert" style="margin-bottom: 16px">
        {{ mergeState.reason }}
      </div>

      <div style="display: grid; grid-template-columns: 1fr 320px; gap: 20px; align-items: start">
        <div>
          <div class="card" style="margin-bottom: 16px">
            <div class="card-header"><strong>Description</strong></div>
            <div class="card-body">
              <p v-if="!mr.description" class="muted">No description.</p>
              <p v-for="(line, index) in patch" :key="index" class="mr-line">{{ line.text }}</p>
            </div>
          </div>

          <MergeConflictResolver
            v-if="mr.has_conflicts && mr.state === 'opened' && project"
            :project-id="project.id"
            :project-path="projectPath"
            :mr="mr"
            @resolved="load"
          />

          <div class="card" style="margin-bottom: 16px">
            <div class="card-header">
              <strong>Changes</strong>
              <span v-if="stats" class="muted" style="margin-left: 8px; font-weight: 400">
                {{ stats.files_changed }} {{ stats.files_changed === 1 ? 'file' : 'files' }},
                <span class="add">+{{ stats.additions }}</span>
                <span class="del">−{{ stats.deletions }}</span>
              </span>
            </div>
            <div class="card-body">
              <div v-if="diffError" class="alert">{{ diffError }}</div>
              <div v-else-if="branchesGone" class="muted">
                The branches are gone, so there is no diff to show. What was merged is
                already part of the target branch.
              </div>
              <div v-else-if="files.length === 0" class="muted">No changes to show.</div>
              <!-- A merge request is read through its diff, so it opens
                   expanded; the commit view is the one that collapses files. -->
              <DiffFile v-for="change in files" :key="change.path" :change="change" />
            </div>
          </div>

          <div class="card">
            <div class="card-header"><strong>Comments</strong></div>
            <div class="card-body">
              <div v-if="notes.length === 0" class="muted">No comments yet.</div>
              <div v-for="note in notes" :key="note.id" class="note">
                <div class="meta">
                  <strong>{{ note.author_name || note.author_username }}</strong>
                  <span>{{ timeAgo(note.created_at) }}</span>
                </div>
                <p class="mr-line">{{ note.body }}</p>
              </div>

              <form class="comment-form" @submit.prevent="addComment">
                <textarea v-model="comment" rows="3" placeholder="Leave a comment" />
                <button class="btn btn-primary" type="submit" :disabled="commenting || !comment.trim()">
                  {{ commenting ? 'Posting…' : 'Comment' }}
                </button>
              </form>
            </div>
          </div>
        </div>

        <aside class="card">
          <div class="card-header"><strong>Details</strong></div>
          <div class="card-body">
            <dl class="details">
              <dt>Source</dt>
              <dd>
                <NuxtLink class="mono" :to="repoViewUrl(projectPath, 'tree', mr.source_branch)">
                  {{ mr.source_branch }}
                </NuxtLink>
              </dd>
              <dt>Target</dt>
              <dd>
                <NuxtLink class="mono" :to="repoViewUrl(projectPath, 'tree', mr.target_branch)">
                  {{ mr.target_branch }}
                </NuxtLink>
              </dd>
              <dt>Status</dt>
              <dd>{{ statusText }}</dd>
              <dt v-if="mr.diff_stats">Changes</dt>
              <dd v-if="mr.diff_stats">
                {{ mr.diff_stats.files_changed }} files,
                <span class="add">+{{ mr.diff_stats.additions }}</span>
                <span class="del">−{{ mr.diff_stats.deletions }}</span>
              </dd>
              <dt v-if="mr.merged_at">Merged</dt>
              <dd v-if="mr.merged_at">
                {{ mr.merged_by_name || 'someone' }} · {{ timeAgo(mr.merged_at) }}
              </dd>
              <dt v-if="mr.merge_commit_sha">Commit</dt>
              <dd v-if="mr.merge_commit_sha">
                <NuxtLink
                  class="mono"
                  :to="`/p/${projectPath}/-/commit/${mr.merge_commit_sha}`"
                >
                  {{ mr.merge_commit_sha.slice(0, 8) }}
                </NuxtLink>
              </dd>
            </dl>
          </div>
        </aside>
      </div>
    </template>
  </div>
</template>

<style scoped>
.mr-line {
  white-space: pre-wrap;
  margin: 0 0 4px;
}

.diff-file + .diff-file {
  margin-top: 16px;
  border-top: 1px solid var(--border);
  padding-top: 12px;
}

.patch {
  margin: 8px 0 0;
  padding: 10px;
  background: var(--bg-code, #0d1117);
  border-radius: 6px;
  overflow-x: auto;
  font: 12px/1.5 var(--mono);
  white-space: pre;
}

.note + .note {
  margin-top: 12px;
  border-top: 1px solid var(--border);
  padding-top: 12px;
}

.comment-form {
  margin-top: 16px;
  display: grid;
  gap: 8px;
  justify-items: start;
}

.details {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 6px 12px;
  margin: 0;
  font-size: 13px;
}

.details dt {
  color: var(--muted);
}

.details dd {
  margin: 0;
}

.add {
  color: #3fb950;
}

.del {
  color: #f85149;
}


</style>