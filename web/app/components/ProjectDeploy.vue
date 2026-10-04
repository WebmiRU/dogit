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

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ modules: ModuleRow[] }>('/modules')
    modules.value = answer.modules ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
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
                <span v-if="!module.enabled" class="badge badge-muted">Forbidden</span>
                <span v-else-if="module.status === 'online'" class="badge badge-ok">Online</span>
                <span v-else class="badge badge-warn">{{ module.status }}</span>
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

            <tr v-if="editing === module.id" class="detail-row">
              <td colspan="3">
                <ModuleSettingsForm
                  :module="module"
                  scope="project"
                  :scope-id="props.projectId"
                  :note="`These values are for ${props.projectPath} only. Anything left alone is inherited from the group or the instance.`"
                />
              </td>
            </tr>

            <tr v-if="administering === module.id" class="detail-row">
              <td colspan="3" class="admin-cell">
                <ModuleDeployments
                  :project-id="props.projectId"
                  :project-path="props.projectPath"
                  :module="module"
                  :can-manage="props.canManage"
                />
              </td>
            </tr>
          </template>
        </tbody>
      </table>
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

.detail-row td {
  background: var(--bg-inset);
  padding: 14px 12px;
}

/* The admin panel has a table of its own, a place list and a list of images. It is
   given room rather than a fixed column, because a history squeezed into a card reads
   as a summary and somebody will act on it. */
.admin-cell {
  width: 100%;
}

.small {
  font-size: 12px;
}
</style>