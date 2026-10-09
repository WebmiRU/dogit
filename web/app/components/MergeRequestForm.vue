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

