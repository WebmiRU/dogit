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

/**
 * What is happening here, in one word and one lamp.
 *
 * Three states, because there are three things worth knowing and a page that shows two
 * of them at once is showing neither: something is being deployed right now, the last
 * thing that was deployed went well or went badly, or nothing has happened yet. The
 * module's own badge cannot answer this — it says whether the module is answering, and
 * a module answers perfectly well while a rollout is failing.
 */
const deployBusy = ref(false)
const lastFailed = ref(false)

const headState = computed<'working' | 'bad' | 'ok'>(() => {
  if (deployBusy.value) return 'working'
  if (lastFailed.value) return 'bad'
  return 'ok'
})

const headStateText = computed(() => {
  if (!modules.value.some((one) => one.kind.startsWith('deploy:'))) return 'no deploy module'
  if (deployBusy.value) return 'a deployment is under way'
  if (lastFailed.value) return 'the last deployment did not finish'
  return 'nothing is deploying'
})

/** Follows deployments only as far as "is one happening, and did the last one work". */
const stopState = watchEvents({
  kinds: ['deploy.operation', 'deploy.history'],
  project: () => props.projectPath,
  onEvent: (event) => {
    if (event.kind === 'deploy.operation') {
      deployBusy.value = true
      if (event.payload?.failed === true) lastFailed.value = true
      return
    }
    // The end of a deployment: whatever it ended as is the last word on it.
    deployBusy.value = false
    const status = String(event.payload?.status ?? '')
    if (status) lastFailed.value = status !== 'success'
  },
})

onBeforeUnmount(() => stopState())

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
      <!-- The head of the card, and the whole of its state.
           Two lines: what is deployed and where it is in its life, then the one switch
           that changes what will happen next. Everything below is detail, and detail is
           what the tables underneath are for. -->
      <div class="head">
        <div class="head-main">
          <!-- The state of the whole thing, in one place.
               The badge in the table below says whether the module is answering, which
               is true while a deployment is failing as steadily as it is while one is
               succeeding — so this says what is actually happening, and it is the only
               indicator on the page that does. -->
          <span class="lamp" :class="headState" />
          <span class="head-state">{{ headStateText }}</span>
        </div>

        <!-- The brake, as a switch and beside the state it changes: whether runs start
             by themselves is a standing fact about this project, not a paragraph. -->
        <label class="brake">
          <input
            type="checkbox"
            class="toggle"
            :checked="!autoPaused"
            :disabled="savingPause || !props.canManage"
            @change="setAutoPaused(!($event.target as HTMLInputElement).checked)"
          />
          <span class="brake-label">
            {{ autoPaused ? 'Deploys start only by hand' : 'Deploys start on a push or a tag' }}
          </span>
          <span class="muted small">
            {{ autoPaused ? 'a push builds nothing by itself' : 'this project deploys itself' }}
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

/* The head: what is deployed and how it is going, and the one switch that changes what
   happens next. Two lines, because the lamp answers "now" and the switch answers
   "next time", and putting them side by side would ask the reader to hold two different
   tenses in one glance. */
.head {
  padding: 12px;
  border-bottom: 1px solid var(--border);
}

.head-main {
  display: flex;
  align-items: center;
  gap: 9px;
}

.head-state {
  font-weight: 600;
  font-size: 13px;
}

/* One lamp, three states, the same three colours the steps use beside them.
   A dot and not a badge: it says the state of a thing rather than naming it, and the
   sentence beside it does the naming. */
.lamp {
  display: inline-flex;
  width: 14px;
  height: 14px;
  flex: 0 0 auto;
}

.lamp-dot {
  width: 100%;
  height: 100%;
  border-radius: 50%;
  background: var(--green);
  /* A ring, so the lamp is a lamp and not a dot: at this size a filled circle with no
     edge disappears against a dark card, which is precisely when the state matters. */
  box-shadow: 0 0 0 2px var(--bg-inset);
}

.lamp.working .lamp-dot {
  background: var(--yellow);
}

.lamp.bad .lamp-dot {
  background: var(--red);
}

/* The brake as a switch, in the head rather than in a block of its own: whether runs
   start by themselves is a standing fact about this project, and it belongs beside the
   state it is a statement about. */
.head .brake {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 10px;
  margin-top: 10px;
  padding: 0;
  border: none;
}

.brake-label {
  cursor: pointer;
}

/* A switch, drawn rather than a box to be ticked.
   Whether deploys start by themselves is a standing setting — on or off, nothing to do
   inside it — and a checkbox asks a question about a moment while a switch states a
   fact about the project. It is still the same input underneath, so it keeps its label,
   its keyboard behaviour and its disabled state. */
input.toggle {
  appearance: none;
  -webkit-appearance: none;
  position: relative;
  width: 34px;
  height: 19px;
  flex: 0 0 auto;
  margin: 0;
  border-radius: 10px;
  background: var(--border);
  cursor: pointer;
  transition: background 0.15s ease;
}

input.toggle::before {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 15px;
  height: 15px;
  border-radius: 50%;
  background: #fff;
  transition: transform 0.15s ease;
}

input.toggle:checked {
  background: var(--green);
}

/* Off is the state worth stopping on: a project that has taken its own foot off is
   not doing anything, and that should be the thing that reads as a deliberate choice. */
input.toggle:checked::before {
  transform: translateX(15px);
}

input.toggle:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Folded away by default. */
.places-fold {
  padding: 0 12px;
  border-bottom: 1px solid var(--border);
}

.places-fold > summary {
  padding: 9px 0;
  cursor: pointer;
  user-select: none;
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