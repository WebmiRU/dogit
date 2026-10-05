<script setup lang="ts">
/**
 * The deploy modules this project may use, as a list rather than as forms.
 *
 * A table with one row per module, the way the notification modules are shown, for
 * the same reason: it answers "what can deploy this project" in one glance, and it
 * leaves the editing to a place somebody goes deliberately. A page that opens with a
 * form per module reads as a page of things to fill in rather than a page of things to
 * choose from.
 *
 * The two buttons are deliberately different in kind and not in size:
 *
 *   Edit changes what this project is allowed to do. It is a setting, it is
 *   inherited, and it is the same change somebody makes on a group.
 *   Admin is about what has already happened — what is running, and undoing it.
 *   That is not a setting at all, and it belongs to whoever is allowed to change a
 *   running system rather than to whoever configures one.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{
  projectId: string
  projectPath: string
  canManage: boolean
}>()

/** Which module's settings are open. One at a time: two forms on a page of a list is a page nobody reads. */
const editing = ref<string>('')
/** The module whose deployments are shown, or none. */
const administering = ref<string>('')

/**
 * Whether a push or a tag may start a run by itself.
 *
 * A brake, not a policy. Which branch reaches which place is written in the repository,
 * because that is a claim about code and is reviewed with it. This is the other thing:
 * what somebody reaches for when the thing that is deploying has to stop now, and a
 * commit is not something anyone can safely push while it is happening.
 */
const autoPaused = ref(false)
const savingPause = ref(false)

const modules = ref<ModuleRow[]>([])
const loading = ref(true)
const error = ref('')

const deployModules = computed(() =>
  modules.value.filter((module) => module.kind.startsWith('deploy:')),
)

function toggleEdit(module: ModuleRow) {
  editing.value = editing.value === module.id ? '' : module.id
  if (editing.value) administering.value = ''
}

// Opens the record of what this module deployed, closing the settings.
//
// The two are the same row with different things behind it, so opening one closes
// the other: a page with a settings form and a deployment history open at once is a
// page where neither can be found.
function toggleAdmin(module: ModuleRow) {
  administering.value = administering.value === module.id ? '' : module.id
  if (administering.value) editing.value = ''
}

/** Pulls the brake, or lets it out again. */
async function setAutoPaused(paused: boolean) {
  savingPause.value = true
  try {
    await api.patch(`/projects/${props.projectId}`, { auto_deploy_paused: paused })
    autoPaused.value = paused
  } finally {
    savingPause.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ modules: ModuleRow[] }>('/modules')
    modules.value = answer.modules ?? []

    const project = await api.get<{ project: { auto_deploy_paused?: boolean } }>(
      `/projects/${props.projectId}`,
    )
    autoPaused.value = project.project?.auto_deploy_paused ?? false
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await load()

  // The first module's record is open before anything is clicked.
  //
  // An instance has one deploy module in practice, and this page is opened to find out
  // what it is doing: whether a rollout is moving, what it printed, what it did last
  // night. A panel behind a button turns that question into two — click, then look —
  // and the answer to the first question is usually already there. The button stays,
  // because it is how somebody gets at a second module, and it still closes.
  const deployable = deployModules.value[0]
  if (deployable) administering.value = deployable.id
})
</script>

<template>
  <div>
    <h2 class="page-title">Deploy</h2>
    <p class="page-subtitle">
      Where {{ props.projectPath }} is deployed. Which module deploys, and where, is
      written in the repository's <span class="mono">.dogit-ci.yml</span>; what is set
      here is which of the instance's targets this project may name.
    </p>

    <div v-if="loading" class="spinner">Loading…</div>
    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <div v-else-if="deployModules.length === 0" class="card empty">
      <p>No deploy module is installed on this instance, so nothing here deploys anything yet.</p>
      <p class="muted">
        A deploy module is what knows about clusters, namespaces and manifests. The core
        does not: it reads a <span class="mono">deploy:</span> block from the repository
        and hands it to whichever module was named there.
      </p>
    </div>

    <div v-else class="card">
      <!-- The brake, above the table it stops: it is about every place at once, so it
           does not belong to any one row. -->
      <div class="brake">
        <label class="brake-label">
          <input
            type="checkbox"
            :checked="autoPaused"
            :disabled="savingPause || !props.canManage"
            @change="setAutoPaused(($event.target as HTMLInputElement).checked)"
          />
          <span>
            <strong>Stop deploying automatically</strong>
            <span class="muted small block">
              A push or a tag would build nothing on its own. You can still start a run
              by hand — this only stops them from starting themselves.
            </span>
          </span>
        </label>
      </div>

      <table class="table">
        <thead>
          <tr>
            <th>Module</th>
            <th>State</th>
            <th class="actions-col">Actions</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="module in deployModules" :key="module.id">
            <tr>
              <td>
                <div class="module-name">{{ module.name }}</div>
                <div class="muted small mono">{{ module.kind }}</div>
              </td>
              <td>
                <!-- A module that is not answering is the one thing an operator has
                     to act on, so the colours are the ones used everywhere else rather
                     than three new ones that look nearly the same. -->
                <span v-if="!module.enabled" class="badge badge-neutral">Forbidden</span>
                <span v-else-if="module.status === 'online'" class="badge badge-green">Online</span>
                <span v-else class="badge badge-warning">{{ module.status }}</span>
              </td>
              <td class="actions-col">
                <button class="btn btn-small" type="button" @click="toggleEdit(module)">
                  {{ editing === module.id ? 'Done' : 'Edit' }}
                </button>
                <button class="btn btn-small" type="button" @click="toggleAdmin(module)">
                  Admin
                </button>
              </td>
            </tr>

            <!-- The row of a module that is open. Nothing about a table goes here:
                 a cell spanning every column makes the browser redistribute them, and
                 the buttons slide sideways at the moment somebody is reaching for one. -->
            <tr v-if="editing === module.id" class="detail-row">
              <td></td>
              <td></td>
              <td></td>
            </tr>
          </template>
        </tbody>
      </table>

      <div
        v-for="module in deployModules"
        v-show="editing === module.id"
        :key="`edit-${module.id}`"
        class="panel"
      >
        <ModuleSettingsForm
          :module="module"
          scope="project"
          :scope-id="props.projectId"
          :note="`These values are for ${props.projectPath} only. Anything left alone is inherited from the group or the instance.`"
        />
      </div>

      <div
        v-for="module in deployModules"
        v-show="administering === module.id"
        :key="`admin-${module.id}`"
        class="panel"
      >
        <ModuleDeployments
          :project-id="props.projectId"
          :project-path="props.projectPath"
          :module="module"
          :can-manage="props.canManage"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.table {
  width: 100%;
  border-collapse: collapse;
}

th,
td {
  text-align: left;
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}

.module-name {
  font-weight: 600;
}

/* The action buttons are a pair, and a pair that has drifted apart reads as a list of
   unrelated things: the same size, side by side, doing one job. */
.actions-col {
  width: 190px;
  white-space: nowrap;
}

.actions-col .btn + .btn {
  margin-left: 6px;
}

/* An open row, below the table rather than inside it, and given the same inset as the
   rest of an expanded thing so it reads as belonging to the row above. */
.panel {
  margin-top: 4px;
  padding: 14px 12px;
  background: var(--bg-inset);
  border-radius: 0 0 6px 6px;
}

.small {
  font-size: 12px;
}

.block {
  display: block;
}

.brake {
  padding: 12px;
  border-bottom: 1px solid var(--border);
}

.brake-label {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  cursor: pointer;
}

/* A checkbox is a fixed-size control, and as a flex item it will otherwise stretch to
   fill the row: what came out was a 564-pixel-wide box with the words pushed to the far
   side, which reads as a broken page and is a poor thing to aim at.
   flex-shrink keeps it from being squeezed in the other direction; flex-basis in its
   own column is what stops the growth. */
.brake-label input[type='checkbox'] {
  flex: 0 0 auto;
  width: auto;
  padding: 0;
  margin: 2px 0 0;
}
</style>