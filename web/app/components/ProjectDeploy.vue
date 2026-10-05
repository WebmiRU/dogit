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
import type { ModuleRow as ModulePlaceRow } from '~/types/notification'

const props = defineProps<{
  projectId: string
  projectPath: string
  canManage: boolean
}>()

/** Which module's settings are open. One at a time: two forms on a page of a list is a page nobody reads. */
const editing = ref<string>('')
/** The module whose deployments are shown, or none. */

/**
 * Whether a push or a tag may start a run by itself.
 *
 * A brake, not a policy. Which branch reaches which place is written in the repository,
 * because that is a claim about code and is reviewed with it. This is the other thing:
 * what somebody reaches for when the thing that is deploying has to stop now, and a
 * commit is not something anyone can safely push while it is happening.
 */

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

/**
 * The places this project has, as the list above decided them.
 *
 * Kept because the blocks below are drawn per place. Without it the page has a list of
 * places and one set of contents for all of them, which is a page about a project
 * rather than about anywhere the project is deployed to.
 */
const places = ref<ModulePlaceRow[]>([])

/** The module whose job all of this is. There is one in practice; the list is a list. */
const deployModule = computed<ModuleRow | null>(() => deployModules.value[0] ?? null)

/**
 * The name a repository writes to reach one place, which is how the history and the
 * catalogue are asked about it.
 *
 * Taken from the row rather than from its label: the label is for people and may have
 * been changed by anybody, while the name is the word a repository says in `cluster:`
 * and is therefore the word the records were written under.
 */
const clusterOf = (row: ModulePlaceRow) =>
  String(row.values?.name ?? row.label ?? '')

function onPlaces(rows: ModulePlaceRow[]) {
  places.value = rows.filter((one) => one.module_kind.startsWith('deploy:'))
}

function toggleEdit(module: ModuleRow) {
  editing.value = editing.value === module.id ? '' : module.id
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

onMounted(async () => {
  await load()
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

      <!-- Where this project may deploy, and which of those places it may not.
           Its own block rather than part of the settings form, because a cluster is
           not a setting of this project: it is a row the instance shares with
           everybody else, with its own switch at this level. Saving it here cannot
           remove a cluster the instance put there, and switching one off here
           switches off that one and nothing else — which is the thing that was
           impossible before, and the reason this is a list of rows. -->
      <div class="places">
        <h3>Where this may deploy</h3>
        <p class="muted small">
          The clusters this instance may deploy to, inherited by every project. A
          cluster cannot be removed from here — only switched off for
          {{ props.projectPath }}, which leaves it where it is for everybody else.
        </p>
        <ModuleTargets
          kind="deploy:"
          scope="project"
          layout="blocks"
          :scope-id="props.projectId"
          :project-id="props.projectId"
          :can-manage="props.canManage"
          @rows="onPlaces"
        >
          <!-- What is under a place's row is that place's deployment and nothing
               else: its status, its log, what it has done and which images have ever
               been on it. Three places give three sets of these, each under its own
               row, which is the only way to open the second one. -->
          <template #row="{ row }">
            <ModuleDeployments
              v-if="deployModule"
              :project-id="props.projectId"
              :project-path="props.projectPath"
              :module="deployModule"
              :place="{ cluster: clusterOf(row), namespace: '' }"
              :can-manage="props.canManage"
              :show-repository="false"
            />
          </template>
        </ModuleTargets>
      </div>

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

/* The clusters, as a block of their own between the two tables it would otherwise
   be mistaken for a part of. It is a different kind of thing: the table above is which
   modules exist, this is where one of them may put something. */
.places {
  /* The same inset the cells have. Written as `12px 0` it looked correct in the source
     and ran every word and both rules to the edge of the card, which is the one thing a
     block inside a card must not do. */
  padding: 14px 12px;
  border-top: 1px solid var(--border);
}

.places h3 {
  margin: 0 0 2px;
  font-size: 13px;
}

.places p {
  margin: 0 0 10px;
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
