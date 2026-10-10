<script setup lang="ts">
/**
 * Project settings.
 *
 * Everything here is a property of the project itself rather than of a file, and
 * each field is sent on its own so an unchanged field never overwrites a change
 * someone else made while the form was open.
 */
import type { GroupSummary } from '~/types/dashboard'
import type { ProjectSummary } from '~/types/repository'

const props = defineProps<{
  project: ProjectSummary
  /** The project's own id, for the module settings that are addressed by id. */
  projectId: string
  canManage: boolean
}>()

const emit = defineEmits<{
  (event: 'saved', project: ProjectSummary): void
  (event: 'moved', path: string): void
}>()

const form = reactive({
  name: props.project.name ?? '',
  description: props.project.description ?? '',
  visibility: props.project.visibility,
  default_branch: props.project.default_branch,
})

watch(
  () => props.project,
  (next) => {
    form.name = next.name ?? ''
    form.description = next.description ?? ''
    form.visibility = next.visibility
    form.default_branch = next.default_branch
  },
)

const saving = ref(false)
const saveError = ref('')
const saved = ref(false)

/**
 * Moving a project between groups.
 *
 * It is a separate form from the fields above because it is a different kind of
 * change: a rename fixes a typo, while a move changes the address the project is
 * reached at and where its repository lives.
 */
const groups = ref<GroupSummary[]>([])
const targetGroup = ref('')
const newName = ref('')

onMounted(async () => {
  const response = await api.get<{ groups: GroupSummary[] }>('/groups')
  // Only a group this person owns can receive the project, so the list is not
  // filtered here: the server refuses the rest, and hiding them would hide why.
  groups.value = response.groups.filter((group) => group.access_level >= 50)
})

const pathParts = computed(() => props.project.path.split('/'))
const currentGroup = computed(() => (pathParts.value.length > 1 ? pathParts.value[0] : ''))

watch(
  () => props.project.path,
  (path) => {
    const parts = path.split('/')
    targetGroup.value = parts.length > 1 ? (parts[0] ?? '') : ''
    newName.value = parts[parts.length - 1] ?? ''
  },
  { immediate: true },
)

const moving = ref(false)
const moveError = ref('')

// The outcome goes through the notification pool: the page follows the project to
// its new address, and a message kept on this page would be gone before it had
// been read.
const { add: notify } = useNotifyPool()
const moveTarget = computed(() => {
  const name = newName.value.trim()
  return targetGroup.value ? `${targetGroup.value}/${name}` : name
})

const canMove = computed(
  () =>
    moveTarget.value.length > 0 &&
    moveTarget.value !== props.project.path &&
    !moving.value,
)

async function move() {
  moveError.value = ''
  moving.value = true
  try {
    await api.post(`/projects/${props.project.id}/move`, { group_path: targetGroup.value })

    // A move changes where the project lives, so the page follows it. Staying on
    // the old address would leave a form that claims to move something which has
    // already gone somewhere else.
    const from = props.project.path
    const to = moveTarget.value

    // The lines are the message; the link is where the project now lives.
    notify(`Project moved to ${to}\n${from} no longer resolves — clone URLs and links to it have to be updated.`, {
      links: [{ label: `Open ${to}`, to: `/p/${to}` }],
      timer: 12,
    })
    await navigateTo(`/p/${to}/-/settings`)
  } catch (caught) {
    moveError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    moving.value = false
  }
}

async function save() {
  saveError.value = ''
  saved.value = false
  saving.value = true
  try {
    const response = await api.patch<{ project: ProjectSummary }>(`/projects/${props.project.id}`, {
      name: form.name,
      description: form.description,
      visibility: form.visibility,
      default_branch: form.default_branch,
    })
    emit('saved', response.project)
    saved.value = true
    setTimeout(() => (saved.value = false), 2000)
  } catch (caught) {
    saveError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="repository-settings">
    <div v-if="!canManage" class="alert alert-info repository-settings-access">
      Only a project owner or maintainer can change these.
    </div>

    <div class="repository-settings-grid">
      <section class="repository-settings-section">
        <header class="repository-settings-heading">
          <div>
            <h2>General settings</h2>
            <p>Project identity, visibility and the default branch.</p>
          </div>
        </header>

        <form v-if="canManage" class="repository-settings-form" @submit.prevent="save">
          <div class="repository-settings-fields">
            <div class="field">
              <label for="project-name">Name</label>
              <input id="project-name" v-model="form.name" />
            </div>
            <div class="field">
              <label for="default-branch">Default branch</label>
              <input id="default-branch" v-model="form.default_branch" />
            </div>
            <div class="field repository-settings-field-wide">
              <label for="project-description">Description</label>
              <textarea
                id="project-description"
                v-model="form.description"
                rows="3"
                placeholder="What is this project for?"
              />
            </div>
            <div class="field">
              <label for="project-visibility">Visibility</label>
              <select id="project-visibility" v-model="form.visibility">
                <option value="private">Private</option>
                <option value="internal">Internal</option>
                <option value="public">Public</option>
              </select>
            </div>
          </div>

          <div v-if="saveError" class="alert alert-error repository-settings-message">
            {{ saveError }}
          </div>
          <div v-else-if="saved" class="alert alert-info repository-settings-message">
            Saved.
          </div>

          <footer class="repository-settings-form-footer">
            <span class="muted small">Changes apply to this project.</span>
            <button class="btn btn-primary" type="submit" :disabled="saving">
              {{ saving ? 'Saving…' : 'Save changes' }}
            </button>
          </footer>
        </form>
      </section>

      <section class="repository-settings-section repository-settings-move">
        <header class="repository-settings-heading">
          <div>
            <h2>Move project</h2>
            <p>Change its namespace and address.</p>
          </div>
        </header>

        <p class="muted repository-settings-section-note">
          A project inside a group is reached at
          <span class="mono">{{ currentGroup }}/{{ project.path.split('/').pop() }}</span>.
          Members of the destination group gain access. Moving is an owner's decision on both
          the project and the group it goes into.
        </p>

        <div class="repository-settings-fields">
          <div class="field">
            <label for="move-group">Namespace</label>
            <select id="move-group" v-model="targetGroup">
              <option value="">— no group —</option>
              <option v-for="group in groups" :key="group.id" :value="group.full_path">
                {{ group.full_path }}
              </option>
            </select>
          </div>
          <div class="field">
            <label for="move-name">Name</label>
            <input id="move-name" v-model="newName" />
          </div>
        </div>

        <div v-if="moveError" class="alert alert-error repository-settings-message">
          {{ moveError }}
        </div>

        <div class="repository-settings-move-footer">
          <button
            class="btn btn-primary"
            type="button"
            :disabled="!canMove || !canManage"
            @click="move"
          >
            {{ moving ? 'Moving…' : 'Move project' }}
          </button>
          <p class="repository-settings-move-note">
            This changes the project's address and where its repository is stored.
            Existing clone URLs stop working.
          </p>
        </div>
      </section>
    </div>
  </div>
</template>

