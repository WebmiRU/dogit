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
        <div class="mr-heading-icon" aria-hidden="true">⇄</div>
        <div>
          <h2>New merge request</h2>
          <p>Compare branches and propose your changes for review.</p>
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

        <div class="mr-details">
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
              rows="5"
              placeholder="What does this change, and why? Add context or testing notes for reviewers."
            />
          </div>
          <label class="mr-squash">
            <input v-model="squash" type="checkbox" />
            <span class="mr-check-custom" aria-hidden="true" />
            <span class="mr-squash-copy">
              <strong>Squash commits</strong>
              <small>Combine the source branch commits into one commit when merging.</small>
            </span>
          </label>
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
.mr-create {
  margin-bottom: 18px;
}

.mr-open {
  min-height: 38px;
  padding: 8px 13px;
  gap: 8px;
  border-radius: 6px;
  background: #45464b;
  border-color: #55565c;
  color: #f4f4f5;
  font-weight: 600;
  box-shadow: 0 1px 2px rgba(0, 0, 0, .18);
}

.mr-open:hover:not(:disabled) {
  background: #53545a;
  border-color: #686970;
  color: #fff;
}

.mr-open-icon {
  font-size: 18px;
  line-height: 1;
  font-weight: 400;
}

.mr-panel {
  margin-top: 14px;
  overflow: hidden;
  border: 1px solid #35363c;
  border-radius: 10px;
  background: #1b1b20;
  box-shadow: 0 12px 34px rgba(0, 0, 0, .18);
}

.mr-heading {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 23px 26px;
  background: #202025;
  border-bottom: 1px solid #35363c;
}

.mr-heading-icon {
  display: grid;
  place-items: center;
  flex: 0 0 42px;
  width: 42px;
  height: 42px;
  border: 1px solid #55565c;
  border-radius: 9px;
  background: #303036;
  color: #e2e2e5;
  font-size: 24px;
}

.mr-heading h2 {
  margin: 0 0 3px;
  color: #f1f1f3;
  font-size: 20px;
  font-weight: 650;
  letter-spacing: -.025em;
}

.mr-heading p,
.mr-section-title p {
  margin: 0;
  color: #a2a2aa;
  font-size: 13px;
}

.mr-form {
  padding: 24px 26px 0;
}

.mr-branch-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 34px minmax(0, 1fr);
  align-items: center;
  gap: 12px;
}

.mr-branch-card {
  min-width: 0;
  padding: 18px;
  border: 1px solid #3b3b43;
  border-radius: 8px;
  background: #242429;
}

.target-card {
  background: #222328;
}

.mr-branch-heading {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 20px;
}

.mr-branch-symbol {
  display: grid;
  place-items: center;
  flex: 0 0 34px;
  width: 34px;
  height: 34px;
  border-radius: 7px;
  font-size: 17px;
  font-weight: 650;
}

.mr-branch-symbol.source {
  background: #38383f;
  color: #e4e4e8;
}

.mr-branch-symbol.target {
  background: #34353a;
  color: #d0d0d5;
}

.mr-branch-heading h3 {
  margin: 0 0 2px;
  color: #eeeef0;
  font-size: 14px;
  font-weight: 650;
}

.mr-branch-heading p {
  margin: 0;
  color: #96969f;
  font-size: 12px;
}

.mr-branch-card label {
  margin-bottom: 7px;
  color: #b8b8bf;
  font-size: 12px;
  font-weight: 600;
}

.mr-branch-card select,
.mr-details input:not([type='checkbox']),
.mr-details textarea {
  border-color: #4a4a52;
  border-radius: 6px;
  background: #17171b;
  color: #ededf0;
}

.mr-branch-card select {
  min-height: 40px;
  font-family: var(--mono);
  font-size: 12px;
}

.mr-branch-card select:focus,
.mr-details input:focus,
.mr-details textarea:focus {
  border-color: #92929b;
  box-shadow: 0 0 0 3px rgba(170, 170, 180, .12);
}

.mr-branch-foot {
  display: flex;
  align-items: center;
  gap: 7px;
  min-height: 20px;
  margin-top: 12px;
  overflow: hidden;
  color: #a5a5ad;
  font-family: var(--mono);
  font-size: 11px;
}

.mr-branch-foot > span:nth-child(2) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mr-dot {
  flex: 0 0 7px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
}

.source-dot {
  background: #b6b6bf;
}

.target-dot {
  background: #8b8b95;
}

.mr-default {
  margin-left: auto;
  padding: 2px 6px;
  border: 1px solid #4a4a52;
  border-radius: 4px;
  color: #b9b9c1;
  font-family: var(--sans);
  font-size: 10px;
}

.mr-direction {
  display: grid;
  place-items: center;
  color: #a7a7af;
  font-size: 22px;
}

.mr-notice {
  margin-top: 14px;
  padding: 11px 13px;
  border: 1px solid #46464e;
  border-radius: 6px;
  background: #26262c;
  color: #c2c2c9;
  font-size: 13px;
}

.mr-divider {
  height: 1px;
  margin: 26px 0 24px;
  background: #36363d;
}

.mr-section-title {
  margin-bottom: 20px;
}

.mr-section-title h3 {
  margin: 0 0 4px;
  color: #f0f0f2;
  font-size: 16px;
  font-weight: 650;
}

.mr-details .field {
  margin-bottom: 19px;
}

.mr-details label {
  color: #c6c6cd;
  font-size: 12px;
  font-weight: 600;
}

.mr-details input:not([type='checkbox']) {
  min-height: 40px;
}

.mr-details textarea {
  min-height: 116px;
  resize: vertical;
  line-height: 1.55;
}

.mr-details input::placeholder,
.mr-details textarea::placeholder {
  color: #777780;
}

.required {
  color: #e1e1e5;
}

.optional {
  margin-left: 5px;
  color: #85858f;
  font-weight: 400;
}

.mr-hint {
  margin: 6px 0 0;
  color: #85858f;
  font-size: 11px;
}

.mr-squash {
  display: flex;
  align-items: flex-start;
  gap: 11px;
  margin: 2px 0 26px;
  cursor: pointer;
}

.mr-squash input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}

.mr-check-custom {
  display: grid;
  place-items: center;
  flex: 0 0 17px;
  width: 17px;
  height: 17px;
  margin-top: 2px;
  border: 1px solid #62626b;
  border-radius: 4px;
  background: #19191e;
}

.mr-squash input:checked + .mr-check-custom {
  background: #d0d0d5;
  border-color: #d0d0d5;
}

.mr-squash input:checked + .mr-check-custom::after {
  content: '✓';
  color: #222228;
  font-size: 12px;
  font-weight: 800;
}

.mr-squash input:focus-visible + .mr-check-custom {
  outline: 2px solid #aaaab3;
  outline-offset: 2px;
}

.mr-squash-copy {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.mr-squash-copy strong {
  color: #e4e4e8;
  font-size: 13px;
  font-weight: 600;
}

.mr-squash-copy small {
  color: #9999a2;
  font-size: 12px;
  font-weight: 400;
}

.mr-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin: 0 -26px;
  padding: 17px 26px;
  border-top: 1px solid #36363d;
  background: #202025;
}

.mr-action-note {
  display: flex;
  align-items: center;
  gap: 8px;
  color: #92929b;
  font-size: 11px;
}

.mr-action-lock {
  color: #b4b4bc;
  font-size: 17px;
}

.mr-action-buttons {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 9px;
}

.mr-action-buttons .btn {
  min-height: 36px;
  padding: 7px 12px;
  border-color: #4b4b53;
  background: #303036;
  color: #e5e5e9;
}

.mr-action-buttons .btn:hover:not(:disabled) {
  border-color: #707079;
  background: #3b3b42;
  color: #fff;
}

.mr-action-buttons .mr-submit {
  gap: 8px;
  border-color: #d0d0d5;
  background: #d0d0d5;
  color: #222228;
  font-weight: 650;
}

.mr-action-buttons .mr-submit:hover:not(:disabled) {
  background: #e1e1e5;
  color: #17171b;
}

.mr-submit-arrow {
  font-size: 15px;
}

.mr-error {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin: 18px 26px 0;
}

.mr-error-icon {
  display: grid;
  place-items: center;
  flex: 0 0 20px;
  width: 20px;
  height: 20px;
  border: 1px solid currentColor;
  border-radius: 50%;
  font-size: 12px;
  font-weight: 800;
}

.mr-error strong {
  display: block;
  margin-bottom: 2px;
}

.mr-error p {
  margin: 0;
}

@media (max-width: 760px) {
  .mr-heading {
    padding: 18px;
  }

  .mr-form {
    padding: 18px 18px 0;
  }

  .mr-branch-grid {
    grid-template-columns: minmax(0, 1fr);
    gap: 8px;
  }

  .mr-direction {
    height: 22px;
    transform: rotate(90deg);
  }

  .mr-branch-card {
    padding: 15px;
  }

  .mr-actions {
    align-items: stretch;
    flex-direction: column;
    margin: 0 -18px;
    padding: 16px 18px;
  }

  .mr-action-note {
    line-height: 1.4;
  }

  .mr-action-buttons {
    width: 100%;
  }

  .mr-action-buttons .btn {
    flex: 1;
    justify-content: center;
  }

  .mr-error {
    margin: 16px 18px 0;
  }
}

@media (prefers-reduced-motion: no-preference) {
  .mr-panel {
    animation: mr-enter 160ms ease-out;
  }

  @keyframes mr-enter {
    from { opacity: 0; transform: translateY(-4px); }
    to { opacity: 1; transform: translateY(0); }
  }
}
</style>