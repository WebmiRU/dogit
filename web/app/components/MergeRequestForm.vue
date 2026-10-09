<script setup lang="ts">
/**
 * The form for opening a merge request from the branches page.
 *
 * The source branch is chosen here rather than on the branch row because opening
 * one is a decision about a pair of branches, and the list of branches is also
 * where a person looks to find out what is there.
 */
import type { RefsResponse } from '~/types/repository'
import type { MergeRequest } from '~/types/merge_requests'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  defaultBranch: string
}>()

const emit = defineEmits<{ (event: 'created', mergeRequest: MergeRequest): void }>()

const open = ref(false)
const source = ref('')
const target = ref(props.defaultBranch)
const title = ref('')
const description = ref('')
const squash = ref(false)
const submitting = ref(false)
const error = ref('')

const branches = computed(() => props.refs?.branches ?? [])

// Only branches that are not the target can be the source, and the source must
// not be the default branch when something else is being merged into it.
const candidates = computed(() => branches.value.filter((b) => b.name !== target.value))

watch(open, (now) => {
  if (!now) return
  error.value = ''
  source.value = candidates.value[0]?.name ?? ''
  target.value = props.defaultBranch
  title.value = ''
})

/** A title is derived from the branch until the person writes their own. */
// The server derives a title from the branch name too; this is the same guess, so
// the field shows what will be used instead of leaving the person surprised.
const titlePlaceholder = computed(() => {
  const name = source.value.split('/').pop() ?? ''
  return name
    .split(/[-_]/)
    .filter(Boolean)
    .map((word) => {
      const first = word.charAt(0)
      return first.toUpperCase() + word.slice(1)
    })
    .join(' ')
})

async function submit() {
  error.value = ''
  submitting.value = true
  try {
    const response = await api.post<{ merge_request: MergeRequest }>(
      `/projects/${props.projectId}/merge_requests`,
      {
        source_branch: source.value,
        target_branch: target.value,
        title: title.value.trim(),
        description: description.value,
        squash: squash.value,
      },
    )
    emit('created', response.merge_request)
    open.value = false
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section class="mr-create">
    <button class="btn btn-primary mr-open" type="button" @click="open = !open">
      <span class="mr-open-icon" aria-hidden="true">{{ open ? '×' : '+' }}</span>
      {{ open ? 'Cancel' : 'New merge request' }}
    </button>

    <div v-if="open" class="mr-panel">
      <header class="mr-heading">
        <nav class="mr-breadcrumb" aria-label="Breadcrumb">
          <NuxtLink :to="`/p/${projectPath}/-/merge_requests`">Merge requests</NuxtLink>
          <span aria-hidden="true">/</span>
          <span>New merge request</span>
        </nav>
        <div class="mr-heading-main">
          <div class="mr-heading-icon" aria-hidden="true">⇄</div>
          <div>
            <h2>New merge request</h2>
            <p>Compare branches and propose your changes for review.</p>
          </div>
        </div>
      </header>

      <div v-if="error" class="alert alert-error mr-error" role="alert">
        <span class="mr-error-icon" aria-hidden="true">!</span>
        <div>
          <strong>Could not create merge request</strong>
          <p>{{ error }}</p>
        </div>
      </div>

      <form class="mr-form" @submit.prevent="submit">
        <div class="mr-branch-grid">
          <section class="mr-branch-card">
            <div class="mr-branch-heading">
              <span class="mr-branch-symbol source" aria-hidden="true">↗</span>
              <div>
                <h3>Source branch</h3>
                <p>The changes you want to bring in</p>
              </div>
            </div>
            <label for="mr-source">From</label>
            <select id="mr-source" v-model="source" required>
              <option value="" disabled>Select source branch</option>
              <option v-for="branch in candidates" :key="branch.name" :value="branch.name">
                {{ branch.name }}
              </option>
            </select>
            <div class="mr-branch-foot">
              <span class="mr-dot source-dot" />
              <span>{{ source || 'Choose a source branch' }}</span>
            </div>
          </section>

          <div class="mr-direction" aria-hidden="true">
            <span>→</span>
          </div>

          <section class="mr-branch-card target-card">
            <div class="mr-branch-heading">
              <span class="mr-branch-symbol target" aria-hidden="true">↙</span>
              <div>
                <h3>Target branch</h3>
                <p>The branch that receives the changes</p>
              </div>
            </div>
            <label for="mr-target">Into</label>
            <select id="mr-target" v-model="target" required>
              <option v-for="branch in branches" :key="branch.name" :value="branch.name">
                {{ branch.name }}
              </option>
            </select>
            <div class="mr-branch-foot">
              <span class="mr-dot target-dot" />
              <span>{{ target || 'Choose a target branch' }}</span>
              <span v-if="target === props.defaultBranch" class="mr-default">Default</span>
            </div>
          </section>
        </div>

        <div v-if="!branches.length" class="mr-notice">
          No branches are available yet. Push a branch to the repository before opening a merge request.
        </div>
        <div v-else-if="!candidates.length" class="mr-notice">
          You need at least two different branches to open a merge request.
        </div>

        <div class="mr-divider" />

        <div class="mr-details-layout">
          <section class="mr-details">
            <div class="mr-section-title">
              <h3>Merge request details</h3>
              <p>Give reviewers enough context to understand the change.</p>
            </div>
            <div class="field">
              <label for="mr-title">Title <span class="required">*</span></label>
              <input
                id="mr-title"
                v-model="title"
                :placeholder="titlePlaceholder || 'Describe the change'"
                maxlength="255"
                required
              />
              <p class="mr-hint">A short, specific summary works best.</p>
            </div>
            <div class="field">
              <label for="mr-description">Description <span class="optional">Optional</span></label>
              <textarea
                id="mr-description"
                v-model="description"
                rows="8"
                placeholder="What does this change, and why? Add context or testing notes for reviewers."
              />
            </div>
          </section>

          <aside class="mr-options">
            <div class="mr-section-title">
              <h3>Merge options</h3>
              <p>Choose how the commits are recorded.</p>
            </div>
            <label class="mr-squash">
              <input v-model="squash" type="checkbox" />
              <span class="mr-check-custom" aria-hidden="true" />
              <span class="mr-squash-copy">
                <strong>Squash commits</strong>
                <small>Combine the source branch commits into one commit when merging.</small>
              </span>
            </label>
          </aside>
        </div>

        <footer class="mr-actions">
          <div class="mr-action-note">
            <span class="mr-action-lock" aria-hidden="true">↳</span>
            Changes will be reviewed before they are merged.
          </div>
          <div class="mr-action-buttons">
            <button class="btn" type="button" :disabled="submitting" @click="open = false">
              Cancel
            </button>
            <button
              class="btn btn-primary mr-submit"
              type="submit"
              :disabled="submitting || !source || !target || source === target || !branches.length"
            >
              <span v-if="!submitting" aria-hidden="true">＋</span>
              {{ submitting ? 'Creating…' : 'Create merge request' }}
              <span v-if="!submitting" class="mr-submit-arrow" aria-hidden="true">→</span>
            </button>
          </div>
        </footer>
      </form>
    </div>
  </section>
</template>

<style scoped>
.mr-create { margin: 0 0 20px; }
.mr-open {
  display:inline-flex; align-items:center; gap:8px; min-height:36px; padding:7px 12px;
  border-radius:5px; font-size:13px; font-weight:600;
}
.mr-open-icon { font-size:17px; line-height:1; font-weight:400; }
.mr-panel {
  width:100%; max-width:1240px; margin-top:14px; overflow:visible;
  border:0; border-radius:0; background:transparent; box-shadow:none;
}
.mr-heading {
  padding:7px 0 20px; border-bottom:1px solid var(--border); background:transparent;
}
.mr-breadcrumb {
  display:flex; flex-wrap:wrap; align-items:center; gap:8px; margin-bottom:15px;
  color:var(--text-muted); font-size:12px;
}
.mr-breadcrumb a { color:var(--text-muted); }
.mr-breadcrumb a:hover { color:var(--text); }
.mr-breadcrumb > span:last-child { color:var(--text); }
.mr-heading-main { display:flex; align-items:center; gap:13px; }
.mr-heading-icon {
  display:grid; place-items:center; flex:0 0 42px; width:42px; height:42px;
  border:1px solid var(--border-strong); border-radius:7px; background:#22242a;
  color:#d5d9e2; font-size:23px;
}
.mr-heading h2 {
  margin:0 0 4px; color:var(--text); font-size:24px; font-weight:650;
  letter-spacing:-.45px; line-height:1.25;
}
.mr-heading p,.mr-section-title p { margin:0; color:var(--text-muted); font-size:13px; line-height:1.5; }
.mr-form { padding:20px 0 0; }
.mr-branch-grid {
  display:grid; grid-template-columns:minmax(0,1fr) 32px minmax(0,1fr);
  align-items:stretch; gap:10px;
}
.mr-branch-card {
  min-width:0; padding:16px 17px 13px; border:1px solid var(--border);
  border-radius:6px; background:var(--bg-elevated);
}
.target-card { background:var(--bg-elevated); }
.mr-branch-heading { display:flex; align-items:center; gap:10px; margin-bottom:15px; }
.mr-branch-symbol {
  display:grid; place-items:center; flex:0 0 30px; width:30px; height:30px;
  border:1px solid var(--border); border-radius:6px; background:#282a30;
  color:#d4d6dc; font-size:16px; font-weight:600;
}
.mr-branch-heading h3 { margin:0 0 3px; color:var(--text); font-size:13px; font-weight:650; }
.mr-branch-heading p { margin:0; color:var(--text-muted); font-size:12px; line-height:1.4; }
.mr-branch-card label { display:block; margin-bottom:6px; color:var(--text-muted); font-size:12px; font-weight:600; }
.mr-branch-card select {
  display:block; width:100%; min-height:39px; padding:8px 32px 8px 10px;
  border:1px solid var(--border-strong); border-radius:5px; background:var(--bg-inset);
  color:var(--text); font-family:var(--mono); font-size:12px;
}
.mr-branch-card select:focus-visible,.mr-details input:focus-visible,.mr-details textarea:focus-visible {
  outline:2px solid var(--accent); outline-offset:1px;
}
.mr-branch-foot {
  display:flex; align-items:center; gap:7px; min-height:18px; margin-top:10px;
  overflow:hidden; color:var(--text-muted); font-family:var(--mono); font-size:11px;
}
.mr-branch-foot > span:nth-child(2) { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.mr-dot { flex:0 0 7px; width:7px; height:7px; border-radius:50%; }
.source-dot { background:#78a8e8; }
.target-dot { background:#8b919d; }
.mr-default { margin-left:auto; padding:2px 6px; border:1px solid var(--border-strong); border-radius:4px; color:var(--text-muted); font-family:inherit; font-size:10px; }
.mr-direction { display:grid; place-items:center; color:var(--text-muted); font-size:22px; }
.mr-notice {
  margin-top:12px; padding:11px 13px; border:1px solid var(--border);
  border-radius:5px; background:var(--bg-elevated); color:var(--text-muted); font-size:13px;
}
.mr-divider { height:1px; margin:22px 0 21px; background:var(--border); }
.mr-details-layout { display:grid; grid-template-columns:minmax(0,2fr) minmax(220px,.85fr); align-items:start; gap:26px; }
.mr-details,.mr-options { min-width:0; }
.mr-section-title { margin-bottom:17px; }
.mr-section-title h3 { margin:0 0 4px; color:var(--text); font-size:16px; font-weight:650; }
.mr-details .field { margin-bottom:19px; }
.mr-details label { display:block; margin-bottom:7px; color:var(--text); font-size:13px; font-weight:600; }
.mr-details input:not([type='checkbox']),.mr-details textarea {
  display:block; width:100%; box-sizing:border-box; border:1px solid var(--border-strong);
  border-radius:5px; background:var(--bg-inset); color:var(--text); font:inherit; font-size:13px;
}
.mr-details input:not([type='checkbox']) { min-height:39px; padding:8px 10px; }
.mr-details textarea { min-height:190px; padding:10px; resize:vertical; line-height:1.55; }
.mr-details input::placeholder,.mr-details textarea::placeholder { color:var(--text-muted); opacity:.85; }
.required { color:#f28b82; }
.optional { margin-left:5px; color:var(--text-muted); font-size:11px; font-weight:400; }
.mr-hint { margin:6px 0 0; color:var(--text-muted); font-size:11px; }
.mr-options {
  padding:16px; border:1px solid var(--border); border-radius:6px;
  background:var(--bg-elevated);
}
.mr-options .mr-section-title { margin-bottom:17px; }
.mr-squash { display:flex; align-items:flex-start; gap:10px; margin:0; cursor:pointer; }
.mr-squash input { position:absolute; width:1px; height:1px; margin:0; opacity:0; }
.mr-check-custom {
  display:grid; place-items:center; flex:0 0 16px; width:16px; height:16px; margin-top:2px;
  border:1px solid var(--border-strong); border-radius:3px; background:var(--bg-inset);
}
.mr-squash input:checked + .mr-check-custom { border-color:#4389eb; background:#4389eb; }
.mr-squash input:checked + .mr-check-custom::after { content:'✓'; color:#fff; font-size:11px; font-weight:800; }
.mr-squash input:focus-visible + .mr-check-custom { outline:2px solid var(--accent); outline-offset:2px; }
.mr-squash-copy { display:flex; flex-direction:column; gap:5px; min-width:0; }
.mr-squash-copy strong { color:var(--text); font-size:13px; font-weight:600; }
.mr-squash-copy small { color:var(--text-muted); font-size:12px; font-weight:400; line-height:1.5; }
.mr-actions {
  display:flex; align-items:center; justify-content:space-between; gap:16px;
  margin:22px 0 0; padding:16px 0; border-top:1px solid var(--border); background:transparent;
}
.mr-action-note { display:flex; align-items:center; gap:8px; color:var(--text-muted); font-size:12px; line-height:1.4; }
.mr-action-lock { color:var(--text-muted); font-size:16px; }
.mr-action-buttons { display:flex; align-items:center; justify-content:flex-end; gap:8px; }
.mr-action-buttons .btn { min-height:36px; padding:7px 13px; border-radius:5px; font-size:13px; }
.mr-action-buttons .mr-submit {
  gap:8px; border-color:#3678d7; background:#3678d7; color:#fff; font-weight:600;
}
.mr-action-buttons .mr-submit:hover:not(:disabled) { border-color:#4889e8; background:#4889e8; color:#fff; }
.mr-submit-arrow { font-size:15px; }
.mr-error {
  display:flex; align-items:flex-start; gap:10px; margin:16px 0 0;
  padding:12px 14px; border-radius:5px;
}
.mr-error-icon {
  display:grid; place-items:center; flex:0 0 20px; width:20px; height:20px;
  border:1px solid currentColor; border-radius:50%; font-size:12px; font-weight:800;
}
.mr-error strong { display:block; margin-bottom:2px; }
.mr-error p { margin:0; }
@media(max-width:850px) {
  .mr-details-layout { grid-template-columns:minmax(0,1fr); gap:16px; }
  .mr-options { grid-row:auto; }
}
@media(max-width:700px) {
  .mr-heading h2 { font-size:21px; }
  .mr-branch-grid { grid-template-columns:minmax(0,1fr); gap:8px; }
  .mr-direction { height:20px; transform:rotate(90deg); }
  .mr-branch-card { padding:14px; }
  .mr-details textarea { min-height:155px; }
  .mr-actions { align-items:stretch; flex-direction:column; }
  .mr-action-buttons { width:100%; }
  .mr-action-buttons .btn { flex:1; justify-content:center; }
}
@media(prefers-reduced-motion:no-preference) {
  .mr-panel { animation:mr-enter 150ms ease-out; }
  @keyframes mr-enter { from { opacity:0; transform:translateY(-3px); } to { opacity:1; transform:translateY(0); } }
}
</style>