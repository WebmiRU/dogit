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

/**
 * How this project may deploy, and what is happening where.
 *
 * One row per place and nothing else on this page: a row is a place, and the card under
 * it carries that place's own settings as well as its log and its history. A separate
 * page of settings would be settings about places, in one place, which is the thing the
 * rows stopped being.
 */
const view = ref<'places'>('places')
/** The module whose deployments are shown, or none. */

const modules = ref<ModuleRow[]>([])
const loading = ref(true)
const error = ref('')

/**
 * What is happening here, in one word and one lamp.
 *
 * Four states, because there are four things worth knowing and a page that shows three of
 * them at once is showing none: something is being deployed right now, something is waiting
 * its turn, the last thing that was deployed went well or went badly, or nothing has
 * happened yet.
 *
 * Waiting was folded into "under way" until a deployment could actually wait — which the
 * queue made true — and the header then claimed work was happening over a deployment that
 * was only standing in a queue, while the card right below it said "Waiting". Two answers to
 * one question, and the one higher up was the one that was wrong.
 *
 * The module's own badge cannot answer this — it says whether the module is answering, and
 * a module answers perfectly well while a rollout is failing.
 */
const deployBusy = ref(false)
const lastFailed = ref(false)
/** The deployments the core last listed as under way or waiting. */
const listed = ref<{ running?: boolean; queued?: boolean }[]>([])

const deployWaiting = computed(
  () => !deployBusy.value && listed.value.some((one) => one.queued === true),
)

const headState = computed<'working' | 'waiting' | 'bad' | 'ok'>(() => {
  if (deployBusy.value) return 'working'
  if (deployWaiting.value) return 'waiting'
  if (lastFailed.value) return 'bad'
  return 'ok'
})

const headStateText = computed(() => {
  if (!modules.value.some((one) => one.kind.startsWith('deploy:'))) return 'no deploy module'
  if (deployBusy.value) return 'a deployment is under way'
  if (deployWaiting.value) return 'a deployment is waiting its turn'
  if (lastFailed.value) return 'the last deployment did not finish'
  return 'nothing is deploying'
})

/**
 * Asked once, so that a page opened during a rollout does not say nothing is deploying.
 *
 * This used to be read off the socket alone, which is the same mistake the cards below had: an
 * event says what happened while somebody was watching, and a page opened halfway through a
 * deployment has seen none of them. It then printed "nothing is deploying" over a rollout in
 * progress, one line above the rows that show the rollout — the page contradicting itself about
 * the same fact, with the truth further down.
 *
 * One finished row is enough, and it is the newest, because the question is not "has anything
 * ever failed" but "how did the last one go".
 */
async function loadHead() {
  try {
    const answer = await api.get<{
      active?: unknown[]
      finished?: { status?: string }[]
    }>(`/projects/${props.projectId}/deploy-operations?finished=1`)
    listed.value = answer.active ?? []
    // Read off the list rather than off its length: the list holds deployments that are
    // under way and deployments that are waiting, and only the first is work happening.
    // Both are "not finished", and treating them as one thing is what made this header
    // say a deployment was under way while a queue stood still.
    deployBusy.value = listed.value.some((one) => one.running === true)
    lastFailed.value = answer.finished?.[0]?.status !== undefined &&
      answer.finished[0].status !== 'success'
  } catch {
    // Left as it is. A header that cannot be fetched says what it last knew, and a header that
    // says "nothing is deploying" because a request failed is a page making a claim it has no
    // evidence for — the failure is visible further down, where the rows are empty.
  }
}

/** Follows deployments only as far as "is one happening, is one waiting, did the last one work". */
const stopState = watchEvents({
  kinds: ['deploy.operation', 'deploy.history'],
  project: () => props.projectPath,
  onEvent: (event) => {
    if (event.kind === 'deploy.operation') {
      deployBusy.value = true
      // Whatever was listed is no longer what the page is drawing: something started.
      listed.value = []
      if (event.payload?.failed === true) lastFailed.value = true
      return
    }
    // The end of a deployment: whatever it ended as is the last word on it. And the
    // list is asked again rather than assumed, because the deployment that ended may
    // have handed its place to one that was waiting — which is a header reading
    // "under way" a moment later and cannot be worked out from here.
    deployBusy.value = false
    const status = String(event.payload?.status ?? '')
    if (status) lastFailed.value = status !== 'success'
    void loadHead()
  },
})

onBeforeUnmount(() => stopState())

const deployModules = computed(() =>
  modules.value.filter((module) => module.kind.startsWith('deploy:')),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ modules: ModuleRow[] }>('/modules')
    modules.value = answer.modules ?? []
    void loadHead()
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

    <div v-else-if="!loading && !error && deployModules.length" class="deploy-module-list">
      <!-- The live status is a separate summary; the module table below stays flat. -->
      <div class="deploy-module-state">
        <div class="deploy-module-state-main">
          <!-- The state of the whole thing, in one place.
               The badge in the table below says whether the module is answering, which
               is true while a deployment is failing as steadily as it is while one is
               succeeding — so this says what is actually happening, and it is the only
               indicator on the page that does. -->
          <span class="deploy-module-lamp" :class="headState"><span class="deploy-module-lamp-dot" /></span>
          <span class="deploy-module-state-label">{{ headStateText }}</span>
        </div>

        </div>

      <table class="admin-table deploy-page-table">
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
            </tr>

          </template>
        </tbody>
      </table>

    </div>

    <!-- Each place, and the card about that place. One row per place: a card drawn for
         the project as a whole merges two clusters into one, and a rollout on one of
         them is then shown next to a version of the other that has been running for a
         month. -->
    <ProjectPlaces
      v-for="module in deployModules"
      :key="`places-${module.id}`"
      :project-id="props.projectId"
      :project-path="props.projectPath"
      :module="module"
      :can-manage="props.canManage"
    />

  </div>
</template>

