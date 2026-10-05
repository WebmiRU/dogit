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

/**
 * What the autodeploy switch actually does, for the tooltip.
 *
 * The column header says on or off and the cell says on or off, which is enough to read
 * the setting and not enough to know what it changes — "autodeploy: off" could mean
 * that pushes build nothing, or that they build but do not deploy, or that the next
 * deploy waits for a button. Those are three different installations. The answer
 * belongs in a tooltip rather than in the column, because a sentence in every row of
 * the table is worse than no sentence at all.
 */
const autodeployHint = computed(() =>
  autoPaused.value
    ? 'Off. A push builds the image and nothing more: the deployment waits for '
      + 'somebody to start it by hand.'
    : 'On. A push to a branch or a tag this repository deploys starts the deployment '
      + 'itself, without anybody pressing anything.',
)

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
      <!-- The head of the card: whether anything is happening. One lamp and one
           sentence. Everything else about the modules is a row of the table below, and
           the switch that decides whether the next push deploys anything is in it. -->
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

        </div>

      <table class="table">
        <thead>
          <tr>
            <th>Module</th>
            <th>State</th>
            <!-- Which way the switch is, said once at the top of the column. Putting the
                 same words in the header and in every row repeats the fact a row
                 already gives plainly; saying it in the header only is enough to
                 tell what the column is, and leaves one word in the cell. -->
            <!-- One line, one word. The phrase underneath it — "on a push or a tag" —
                 made this header three lines tall and broke the row it sits in, for a
                 piece of explanation that is worth reading once and not on every
                 glance. It is in the tooltip now, where nothing is pushed out of
                 shape by it. -->
            <th class="brake-col" :title="autodeployHint">
              <div>Autodeploy</div>
            </th>
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
              <!-- The brake, as a switch in the table rather than a paragraph above
                   it. Whether runs start by themselves is a standing fact about this
                   project, and it belongs beside the module it governs and the state
                   that module is in — where the eye goes when asking "what will happen
                   on the next push". -->
              <td class="brake-col">
                <!-- The same switch the notification table uses, and deliberately: two
                   controls that mean the same thing ought to be the same control, and
                   the eye should not have to learn a second one for the second setting.
                   A switch and not a choice between On and Off, because there is no third
                   answer here to make it a choice. -->
                  <button
                    class="switch"
                    :class="{ on: !autoPaused }"
                    type="button"
                    :disabled="savingPause || !props.canManage"
                    :aria-pressed="!autoPaused"
                    :title="autodeployHint"
                    @click="setAutoPaused(!autoPaused)"
                  >
                    <span class="knob" />
                  </button>
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

/* The head: what is deployed and where it is in its life. One line, because the lamp
   is the only thing left in it — the switch that used to be under it has moved into
   the table, where it sits beside the module it governs. */
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

/* The column the brake sits in. As narrow as the header's longest word and the
   switch together, which is not the module name's width — a table of modules should
   not have a column for a switch taking half the page. */
.brake-col {
  width: 1%;
  white-space: nowrap;
}

/* Not bold, because the words beside it — the module's name and the state — are not,
   and a header heavier than the thing it heads draws the eye to the column that matters
   least. */
.brake-col th {
  font-weight: 500;
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
</style>