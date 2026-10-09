<script setup lang="ts">
import type { MergeRequest } from '~/types/merge_requests'
import type { CommitInfo, DiffStat, FileChange, RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  projectName: string
  refs: RefsResponse | null
  defaultBranch: string
}>()
const emit = defineEmits<{ (event: 'created', mergeRequest: MergeRequest): void }>()
const { user } = useAuth()

type Member = { user: { id: string; username: string; name?: string } }
type CompareResponse = { files: FileChange[]; stats: DiffStat; commits: CommitInfo[]; commits_ahead: number; commits_behind: number }
const stage = ref<'branches' | 'details'>('branches')
const sourceProject = ref(props.projectId)
const targetProject = ref(props.projectId)
const branches = computed(() => props.refs?.branches ?? [])
const source = ref(branches.value.find(branch => branch.name !== props.defaultBranch)?.name ?? branches.value[0]?.name ?? '')
const target = ref(props.defaultBranch || branches.value[0]?.name || '')
const error = ref('')
const comparing = ref(false)
const submitting = ref(false)
watch(() => [props.refs, props.defaultBranch] as const, () => {
  const all = props.refs?.branches ?? []
  if (!all.some(branch => branch.name === source.value)) source.value = all.find(branch => branch.name !== props.defaultBranch)?.name ?? all[0]?.name ?? ''
  if (!all.some(branch => branch.name === target.value)) target.value = props.defaultBranch || all[0]?.name || ''
})
const compareResult = ref<CompareResponse | null>(null)
const commits = computed(() => compareResult.value?.commits ?? [])
const files = computed(() => compareResult.value?.files ?? [])
const stats = computed(() => compareResult.value?.stats ?? null)
const commitCount = computed(() => compareResult.value?.commits_ahead ?? commits.value.length)
const activeTab = ref<'commits' | 'changes'>('commits')
const diffMode = ref<'split' | 'unified'>('unified')
const fileMode = ref<'tree' | 'list'>('tree')
const wrapLines = ref(false)
const diffsExpanded = ref(true)
const filesPaneOpen = ref(true)
const fileSearch = ref('')
const visibleFiles = computed(() => {
  const needle = fileSearch.value.trim().toLocaleLowerCase()
  if (!needle) return files.value
  // A glob-like search supports examples such as "*.vue" and "src/*.go".
  const parts = needle.split('*').filter(Boolean)
  return files.value.filter(file => {
    const path = file.path.toLocaleLowerCase()
    if (!needle.includes('*')) return path.includes(needle)
    let cursor = 0
    for (const part of parts) {
      const found = path.indexOf(part, cursor)
      if (found < 0) return false
      cursor = found + part.length
    }
    return true
  })
})
const sourceCandidates = computed(() => branches.value.filter(branch => branch.name !== target.value))
const targetCandidates = computed(() => branches.value.filter(branch => branch.name !== source.value))

async function compareBranches() {
  error.value = ''
  if (!branches.value.length) { error.value = 'No branches are available. Push a branch to the repository first.'; return }
  if (!source.value || !target.value || source.value === target.value) { error.value = 'Choose two different branches to compare.'; return }
  comparing.value = true
  compareResult.value = null
  try {
    const response = await api.get<CompareResponse>('/projects/' + props.projectId + '/repository/compare', {
      from: target.value, to: source.value, three_dot: 'true',
    })
    compareResult.value = {
      files: response.files ?? [],
      stats: response.stats ?? { files_changed: 0, additions: 0, deletions: 0 },
      commits: response.commits ?? [],
      commits_ahead: response.commits_ahead ?? response.commits?.length ?? 0,
      commits_behind: response.commits_behind ?? 0,
    }
    if (compareResult.value.commits_ahead === 0 && compareResult.value.commits.length === 0) {
      error.value = 'The source branch has no commits that the target branch is missing.'
      compareResult.value = null
      return
    }
    if (!title.value.trim()) title.value = titlePlaceholder.value
    stage.value = 'details'
    activeTab.value = 'commits'
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'The branches could not be compared.'
  } finally { comparing.value = false }
}

const title = ref('')
const draft = ref(false)
const description = ref('')
const plainEditing = ref(false)
const descriptionEl = ref<HTMLTextAreaElement | null>(null)
const assigneeId = ref('')
const reviewerId = ref('')
const milestone = ref('')
const labelInput = ref('')
const labels = ref<string[]>([])
const removeSourceBranch = ref(true)
const pipelineRequired = ref(false)
const squash = ref(false)
const members = ref<Member[]>([])
const knownMilestones = ref<string[]>([])
const knownLabels = ref<string[]>([])
const titlePlaceholder = computed(() => source.value.split('/').pop()?.split(/[-_]/).filter(Boolean).map(word => word.charAt(0).toUpperCase() + word.slice(1)).join(' ') ?? '')
const currentUserId = computed(() => user.value?.id ?? '')

async function loadFormOptions() {
  try {
    const response = await api.get<{ members: Member[] }>('/projects/' + props.projectId + '/members')
    members.value = response.members ?? []
  } catch { members.value = [] }
  if (user.value?.id && !members.value.some(member => member.user.id === user.value?.id)) {
    members.value.unshift({ user: { id: user.value.id, username: user.value.username, name: user.value.name ?? '' } })
  }
  try {
    const response = await api.get<{ merge_requests: MergeRequest[] }>('/projects/' + props.projectId + '/merge_requests', { state: 'all', limit: 100 })
    const existing = response.merge_requests ?? []
    knownMilestones.value = [...new Set(existing.map(mr => mr.milestone).filter((value): value is string => Boolean(value)))]
    knownLabels.value = [...new Set(existing.flatMap(mr => mr.labels ?? []))]
  } catch { /* Option lists may be empty on a new project. */ }
}
onMounted(() => void loadFormOptions())

function addLabels() {
  const next = labelInput.value.split(',').map(value => value.trim()).filter(Boolean)
  labels.value = [...new Set([...labels.value, ...next])]
  labelInput.value = ''
}
function removeLabel(label: string) { labels.value = labels.value.filter(value => value !== label) }
function handleLabelKey(event: KeyboardEvent) {
  if (event.key === 'Enter' || event.key === ',') { event.preventDefault(); addLabels() }
}
function insertMarkdown(before: string, after = before) {
  const el = descriptionEl.value
  const start = el?.selectionStart ?? description.value.length
  const end = el?.selectionEnd ?? start
  const selected = description.value.slice(start, end)
  const inserted = before + selected + after
  description.value = description.value.slice(0, start) + inserted + description.value.slice(end)
  nextTick(() => {
    if (!el) return
    el.focus()
    const selectionStart = start + before.length
    const selectionEnd = selected ? selectionStart + selected.length : selectionStart
    el.setSelectionRange(selectionStart, selectionEnd)
  })
}
function prefixMarkdownLine(prefix: string) {
  const el = descriptionEl.value
  const cursor = el?.selectionStart ?? description.value.length
  const lineStart = description.value.lastIndexOf('\n', cursor - 1) + 1
  description.value = description.value.slice(0, lineStart) + prefix + description.value.slice(lineStart)
  nextTick(() => {
    if (!el) return
    el.focus()
    el.setSelectionRange(cursor + prefix.length, cursor + prefix.length)
  })
}
function insertLink() { insertMarkdown('[link text](', ')') }
function formatDate(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date)
}
function shortSha(commit: CommitInfo) { return commit.short_sha || commit.sha.slice(0, 8) }
function fileId(index: number) { return 'mr-preview-diff-' + index }
function jumpToFile(index: number) { document.getElementById(fileId(index))?.scrollIntoView({ behavior: 'smooth', block: 'start' }) }
const jumpIndex = ref(0)
function jumpToDiff(direction: number) {
  const list = visibleFiles.value
  if (!list.length) return
  jumpIndex.value = (jumpIndex.value + direction + list.length) % list.length
  jumpToFile(files.value.indexOf(list[jumpIndex.value]))
}

async function submit() {
  error.value = ''
  if (!source.value || !target.value || source.value === target.value) { error.value = 'Choose different source and target branches.'; stage.value = 'branches'; return }
  const requestTitle = title.value.trim()
  if (!requestTitle) { error.value = 'A title is required.'; return }
  submitting.value = true
  try {
    const response = await api.post<{ merge_request: MergeRequest; existing?: boolean }>('/projects/' + props.projectId + '/merge_requests', {
      source_branch: source.value,
      target_branch: target.value,
      title: requestTitle,
      description: description.value,
      squash: squash.value,
      is_draft: draft.value,
      assignee_id: assigneeId.value || null,
      reviewer_id: reviewerId.value || null,
      milestone: milestone.value.trim(),
      labels: labels.value,
      remove_source_branch: removeSourceBranch.value,
      pipeline_required: pipelineRequired.value,
    })
    emit('created', response.merge_request)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'The merge request could not be created.'
  } finally { submitting.value = false }
}
</script>

<template>
  <div class="mr-create-page">
    <nav class="mr-create-breadcrumb" aria-label="Breadcrumb">
      <NuxtLink to="/projects">Projects</NuxtLink><span aria-hidden="true">/</span>
      <NuxtLink :to="'/p/' + projectPath">{{ projectName }}</NuxtLink><span aria-hidden="true">/</span>
      <NuxtLink :to="'/p/' + projectPath + '/-/merge_requests'">Merge requests</NuxtLink><span aria-hidden="true">/</span>
      <strong>New merge request</strong>
    </nav>

    <section v-if="stage === 'branches'" class="mr-create-stage">
      <h1>New merge request</h1>
      <p class="mr-page-intro">Select the source and target branches to compare your changes.</p>
      <div class="mr-branch-select-grid">
        <section class="mr-branch-select-card">
          <div class="mr-field-heading"><span class="mr-branch-avatar">↗</span><div><h2>Source branch</h2><p>The branch containing your changes</p></div></div>
          <label for="mr-create-source-project">Source project</label>
          <select id="mr-create-source-project" v-model="sourceProject"><option :value="projectId">{{ projectName }} ({{ projectPath }})</option></select>
          <label for="mr-create-source-branch">Source branch</label>
          <select id="mr-create-source-branch" v-model="source" required>
            <option value="" disabled>Select source branch</option>
            <option v-for="branch in sourceCandidates" :key="branch.name" :value="branch.name">{{ branch.name }}</option>
          </select>
          <div class="mr-selected-branch"><span class="mr-source-dot"></span><span>{{ source || 'Choose a source branch' }}</span></div>
        </section>
        <div class="mr-branch-select-direction" aria-hidden="true">→</div>
        <section class="mr-branch-select-card">
          <div class="mr-field-heading"><span class="mr-branch-avatar target">↙</span><div><h2>Target branch</h2><p>The branch that will receive the changes</p></div></div>
          <label for="mr-create-target-project">Target project</label>
          <select id="mr-create-target-project" v-model="targetProject"><option :value="projectId">{{ projectName }} ({{ projectPath }})</option></select>
          <label for="mr-create-target-branch">Target branch</label>
          <select id="mr-create-target-branch" v-model="target" required>
            <option v-for="branch in targetCandidates" :key="branch.name" :value="branch.name">{{ branch.name }}</option>
          </select>
          <div class="mr-selected-branch"><span class="mr-target-dot"></span><span>{{ target || 'Choose a target branch' }}</span><span v-if="target === defaultBranch" class="mr-default-tag">Default</span></div>
        </section>
      </div>
      <div v-if="error" class="mr-inline-error" role="alert">{{ error }}</div>
      <div v-if="!branches.length" class="mr-inline-notice">No branches are available yet. Push at least one branch before opening a merge request.</div>
      <div v-else-if="branches.length < 2" class="mr-inline-notice">You need at least two branches to open a merge request.</div>
      <div class="mr-compare-actions">
        <NuxtLink class="btn" :to="'/p/' + projectPath + '/-/merge_requests'">Cancel</NuxtLink>
        <button class="btn btn-primary" type="button" :disabled="comparing || branches.length < 2 || !source || !target || source === target" @click="compareBranches">
          {{ comparing ? 'Comparing branches…' : 'Compare branches and continue' }}
        </button>
      </div>
    </section>

    <section v-else class="mr-create-stage mr-details-stage">
      <h1>New merge request</h1>
      <p class="mr-branch-summary">From <code>{{ source }}</code> into <code>{{ target }}</code>
        <button class="mr-text-button" type="button" @click="stage = 'branches'">Change branches</button>
      </p>

      <form class="mr-create-form" @submit.prevent="submit">
        <div class="mr-main-fields">
          <div class="mr-field">
            <label for="mr-title">Title <span class="required">*</span></label>
            <input id="mr-title" v-model="title" :placeholder="titlePlaceholder || 'Describe the change'" maxlength="255" required>
          </div>
          <label class="mr-draft-option"><input v-model="draft" type="checkbox"><span>Mark as draft</span><small>Drafts cannot be merged until marked ready.</small></label>

          <div class="mr-field">
            <div class="mr-label-row"><label for="mr-description">Description</label><button class="mr-text-button" type="button" @click="plainEditing = !plainEditing">{{ plainEditing ? 'Show formatting toolbar' : 'Switch to plain text editing' }}</button></div>
            <div class="mr-markdown-editor">
              <div v-if="!plainEditing" class="mr-markdown-toolbar" role="toolbar" aria-label="Description formatting">
                <button type="button" title="Bold" @click="insertMarkdown('**', '**')"><strong>B</strong></button>
                <button type="button" title="Italic" @click="insertMarkdown('*', '*')"><em>I</em></button>
                <button type="button" title="Strikethrough" @click="insertMarkdown('~~', '~~')"><s>S</s></button>
                <span class="mr-toolbar-separator"></span>
                <button type="button" title="Bulleted list" @click="prefixMarkdownLine('- ')">☷</button>
                <button type="button" title="Numbered list" @click="prefixMarkdownLine('1. ')">1.</button>
                <button type="button" title="Task list" @click="prefixMarkdownLine('- [ ] ')">☑</button>
                <span class="mr-toolbar-separator"></span>
                <button type="button" title="Code" @click="insertMarkdown(String.fromCharCode(96), String.fromCharCode(96))">‹›</button>
                <button type="button" title="Quote" @click="prefixMarkdownLine('&gt; ')">❝</button>
                <button type="button" title="Insert link" @click="insertLink">↗</button>
                <button type="button" title="Insert table" @click="insertMarkdown('| Column | Column |' + '\n' + '| --- | --- |' + '\n' + '| Value | Value |', '')">▦</button>
                <button type="button" title="Horizontal rule" @click="insertMarkdown('\n---\n', '')">＋</button>
              </div>
              <textarea id="mr-description" ref="descriptionEl" v-model="description" rows="8" placeholder="Write a description, explain the change, and add testing notes for reviewers."></textarea>
              <div class="mr-markdown-footer"><span>Markdown supported</span><span>⌘ Enter to submit</span></div>
            </div>
            <p class="mr-field-help">Add context, implementation notes, and test results for reviewers.</p>
          </div>

          <div class="mr-extra-grid">
            <div class="mr-field">
              <label for="mr-assignee">Assignee</label>
              <div class="mr-input-action-row">
                <select id="mr-assignee" v-model="assigneeId"><option value="">Unassigned</option><option v-for="member in members" :key="member.user.id" :value="member.user.id">{{ member.user.name || member.user.username }} (@{{ member.user.username }})</option></select>
                <button class="mr-text-button" type="button" :disabled="!currentUserId" @click="assigneeId = currentUserId">Assign to me</button>
              </div>
            </div>
            <div class="mr-field">
              <label for="mr-reviewer">Reviewer</label>
              <select id="mr-reviewer" v-model="reviewerId"><option value="">Unassigned</option><option v-for="member in members" :key="member.user.id" :value="member.user.id">{{ member.user.name || member.user.username }} (@{{ member.user.username }})</option></select>
            </div>
            <div class="mr-field">
              <label for="mr-milestone">Milestone</label>
              <input id="mr-milestone" v-model="milestone" list="mr-milestone-options" placeholder="Select milestone or enter a name">
              <datalist id="mr-milestone-options"><option v-for="option in knownMilestones" :key="option" :value="option"></option></datalist>
            </div>
            <div class="mr-field">
              <label for="mr-label-input">Labels</label>
              <div class="mr-label-input-row"><input id="mr-label-input" v-model="labelInput" list="mr-label-options" placeholder="Select or type a label" @keydown="handleLabelKey"><button type="button" class="btn mr-add-label" @click="addLabels">Add</button></div>
              <datalist id="mr-label-options"><option v-for="option in knownLabels" :key="option" :value="option"></option></datalist>
              <div v-if="labels.length" class="mr-label-chips"><span v-for="label in labels" :key="label" class="mr-label-chip">{{ label }}<button type="button" :aria-label="'Remove label ' + label" @click="removeLabel(label)">×</button></span></div>
            </div>
          </div>

          <div class="mr-field mr-pipeline-field">
            <label for="mr-pipeline-policy">Merge can start</label>
            <select id="mr-pipeline-policy" v-model="pipelineRequired"><option :value="false">Anytime</option><option :value="true">After the source branch pipeline succeeds</option></select>
            <p class="mr-field-help">When required, the merge action stays unavailable until the latest source-branch pipeline passes.</p>
          </div>

          <section class="mr-merge-options">
            <h2>Merge options</h2>
            <label class="mr-option-row"><input v-model="removeSourceBranch" type="checkbox"><span><strong>Delete source branch when merge request is accepted.</strong><small>Remove the source branch after a successful merge.</small></span></label>
            <label class="mr-option-row"><input v-model="squash" type="checkbox"><span><strong>Squash commits when merge request is accepted.</strong><small>Combine the source branch commits into one commit.</small></span></label>
          </section>
        </div>

        <div v-if="error" class="mr-inline-error" role="alert">{{ error }}</div>
        <footer class="mr-create-actions"><div class="mr-action-note">Changes will be reviewed before they are merged.</div><div class="mrc-action-buttons">
          <NuxtLink class="btn" :to="'/p/' + projectPath + '/-/merge_requests'">Cancel</NuxtLink>
          <button class="btn btn-primary" type="submit" :disabled="submitting || !title.trim() || !compareResult">{{ submitting ? 'Creating…' : 'Create merge request' }}</button>
        </div></footer>
      </form>

      <section class="mr-preview">
        <div class="mr-preview-tabs" role="tablist" aria-label="Merge request preview">
          <button type="button" role="tab" :aria-selected="activeTab === 'commits'" :class="{ active: activeTab === 'commits' }" @click="activeTab = 'commits'">Commits <span class="mr-tab-count">{{ commitCount }}</span></button>
          <button type="button" role="tab" :aria-selected="activeTab === 'changes'" :class="{ active: activeTab === 'changes' }" @click="activeTab = 'changes'">Changes <span class="mr-tab-count">{{ stats?.files_changed ?? files.length }}</span></button>
        </div>
        <div v-if="activeTab === 'commits'" class="mr-commit-preview">
          <div class="mr-preview-summary">{{ commitCount }} {{ commitCount === 1 ? 'commit' : 'commits' }} from <code>{{ source }}</code> into <code>{{ target }}</code></div>
          <div v-if="!commits.length" class="mr-preview-empty">No commits to show.</div>
          <article v-for="commit in commits" :key="commit.sha" class="mr-preview-commit">
            <span class="mr-commit-avatar">{{ (commit.author_name || '?').slice(0, 1).toUpperCase() }}</span>
            <div class="mr-commit-message"><strong>{{ commit.subject || commit.message }}</strong><small>{{ commit.author_name }} authored {{ formatDate(commit.timestamp) }}</small></div>
            <code class="mr-commit-sha">{{ shortSha(commit) }}</code>
          </article>
        </div>
        <div v-else class="mr-changes-preview">
          <div class="mr-diff-summary">
            <div class="mr-diff-total"><strong>{{ stats?.files_changed ?? files.length }}</strong> files changed <span class="mr-additions">+{{ stats?.additions ?? 0 }}</span> <span class="mr-deletions">−{{ stats?.deletions ?? 0 }}</span></div>
            <div class="mr-diff-actions"><button type="button" class="mr-icon-button" title="Previous changed file" @click="jumpToDiff(-1)">↑</button><button type="button" class="mr-icon-button" title="Next changed file" @click="jumpToDiff(1)">↓</button><button type="button" class="mr-icon-button" :title="diffsExpanded ? 'Collapse all diffs' : 'Expand all diffs'" @click="diffsExpanded = !diffsExpanded">{{ diffsExpanded ? '−' : '+' }}</button><button type="button" class="mr-icon-button" :aria-pressed="wrapLines" title="Wrap long lines" @click="wrapLines = !wrapLines">↩</button></div>
          </div>
          <div class="mr-diff-workspace">
            <aside v-if="filesPaneOpen" class="mr-files-pane">
              <div class="mr-files-heading"><strong>Files</strong><span class="mr-tab-count">{{ visibleFiles.length }}</span><div class="mr-fill"></div><button type="button" class="mr-icon-button" title="Hide file list" @click="filesPaneOpen = false">◧</button></div>
              <label class="mr-file-search"><span aria-hidden="true">⌕</span><input v-model="fileSearch" type="search" placeholder="Search (e.g. *.vue)"></label>
              <div class="mr-file-view-modes"><button type="button" :class="{ active: fileMode === 'list' }" title="List files" :aria-pressed="fileMode === 'list'" @click="fileMode = 'list'">☷</button><button type="button" :class="{ active: fileMode === 'tree' }" title="Tree view" :aria-pressed="fileMode === 'tree'" @click="fileMode = 'tree'">⑂</button></div>
              <div v-if="!visibleFiles.length" class="mr-no-files">No files match this search.</div>
              <button v-for="change in visibleFiles" :key="change.path" type="button" class="mr-file-row" :style="{ paddingLeft: fileMode === 'tree' ? (change.path.split('/').length - 1) * 12 + 9 + 'px' : '9px' }" @click="jumpToFile(files.indexOf(change))">
                <span class="mr-file-status" :class="'status-' + change.status">{{ change.status === 'added' ? '+' : change.status === 'deleted' ? '−' : change.status === 'renamed' ? '↪' : '•' }}</span>
                <span class="mr-file-name">{{ change.path }}</span><span class="mr-file-stats"><i>+{{ change.additions }}</i><b>−{{ change.deletions }}</b></span>
              </button>
            </aside>
            <div v-else class="mr-files-collapsed"><button type="button" class="mr-icon-button" title="Show file list" @click="filesPaneOpen = true">◧</button></div>
            <div class="mr-diff-content">
              <div class="mr-diff-toolbar"><div class="mr-fill"></div><button type="button" class="mr-diff-mode" :class="{ active: diffMode === 'split' }" :aria-pressed="diffMode === 'split'" @click="diffMode = 'split'">Split</button><button type="button" class="mr-diff-mode" :class="{ active: diffMode === 'unified' }" :aria-pressed="diffMode === 'unified'" @click="diffMode = 'unified'">Unified</button><button type="button" class="mr-icon-button" :aria-pressed="wrapLines" title="Wrap long lines" @click="wrapLines = !wrapLines">↩</button></div>
              <div v-if="!files.length" class="mr-preview-empty">No changes to show.</div>
              <div v-else-if="!diffsExpanded" class="mr-preview-empty">All diffs are collapsed. Use the expand button to show them.</div>
              <div v-else class="mr-diff-file-list">
                <div v-for="change in visibleFiles" :id="fileId(files.indexOf(change))" :key="change.path" class="mr-preview-diff-file"><DiffFile :change="change" :view-mode="diffMode" :wrap-lines="wrapLines"><template #title><span class="mono">{{ change.path }}</span></template></DiffFile></div>
              </div>
            </div>
          </div>
        </div>
      </section>
    </section>
  </div>
</template>

