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
  <div style="margin-bottom: 16px">
    <button class="btn btn-primary" type="button" @click="open = !open">
      {{ open ? 'Cancel' : 'New merge request' }}
    </button>

    <div v-if="open" class="card" style="margin-top: 12px">
      <div class="card-header"><strong>New merge request</strong></div>
      <div class="card-body">
        <div v-if="error" class="alert alert-error">{{ error }}</div>

        <form @submit.prevent="submit">
          <div class="field">
            <label for="mr-source">Source branch</label>
            <select id="mr-source" v-model="source">
              <option v-for="branch in candidates" :key="branch.name" :value="branch.name">
                {{ branch.name }}
              </option>
            </select>
          </div>

          <div class="field">
            <label for="mr-target">Target branch</label>
            <select id="mr-target" v-model="target">
              <option v-for="branch in branches" :key="branch.name" :value="branch.name">
                {{ branch.name }}
              </option>
            </select>
          </div>

          <div class="field">
            <label for="mr-title">Title</label>
            <input id="mr-title" v-model="title" :placeholder="titlePlaceholder" />
          </div>

          <div class="field">
            <label for="mr-description">Description</label>
            <textarea
              id="mr-description"
              v-model="description"
              rows="4"
              placeholder="What does this change, and why?"
            />
          </div>

          <div class="field">
            <label class="checkbox">
              <input v-model="squash" type="checkbox" />
              Squash into a single commit
            </label>
          </div>

          <button
            class="btn btn-primary"
            type="submit"
            :disabled="submitting || !source || !target || source === target"
          >
            {{ submitting ? 'Opening…' : 'Create merge request' }}
          </button>
        </form>
      </div>
    </div>
  </div>
</template>

<style scoped>
.checkbox {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text);
  font-size: 13px;
}

.checkbox input {
  width: auto;
}
</style>