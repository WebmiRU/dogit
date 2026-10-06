<script setup lang="ts">
/**
 * This project's places, one row each, and under each row the card about that place.
 *
 * A row is a place and the card under it is that place and nothing else: which pods came
 * up, what the module said, what has been deployed there and which images have ever been
 * there are four questions with four answers for a project that deploys to two
 * clusters. Drawn for the project as a whole they are merged into one, and a rollout on
 * one of them is shown next to a version of the other that has been running for a month
 * — which reads as one deployment that has been going for a month.
 *
 * What a row may change is what is this project's own answer about that place, and two
 * things: whether a push may deploy there by itself, and whether this project may deploy
 * there at all. Everything else about a cluster — where it is, what it is called, which
 * namespace, how long to wait — belongs to whoever installed the module and is on its
 * Options tab. A kubeconfig is a credential and this page is not the place for it: the
 * page is open to everybody who may read the project, and a cluster's address is not
 * theirs to read.
 *
 * Each row saves itself, because a row is what somebody decided: two clusters are two
 * credentials, and a Save that took both would make switching one switch a chance to
 * rewrite the other's answer.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{
  projectId: string
  projectPath: string
  module: ModuleRow
  canManage: boolean
}>()

/** One row of the clusters list, as this project sees it. */
interface Place {
  name: string
  namespace: string
  /** Whether this project may deploy here at all. Nobody having said means yes. */
  inUse: boolean
  /** Whether a push may do it by itself. Nobody having said means yes. */
  autodeploy: boolean
}

const places = ref<Place[]>([])
/** The same rows as they are stored at the levels above, keyed by name. */
const inherited = ref<Record<string, Record<string, unknown>>>({})
const loading = ref(true)
const saving = ref('')
const error = ref('')

/** What each row's two switches said when it was loaded, so a row knows whether it has changed. */
const loaded = ref<Record<string, { inUse: boolean; autodeploy: boolean }>>({})

async function load() {
  loading.value = true
  error.value = ''
  try {
    // "own" and not "effective": the values this instance wrote for the cluster are not
    // sent to a page, so this project sees the name of its place and what it has decided
    // about it, and nothing else.
    const answer = await api.get<{ own?: Record<string, unknown> }>(
      `/modules/${props.module.id}/settings?scope=project&projectID=${encodeURIComponent(props.projectId)}`,
    )
    const rows = Array.isArray(answer.own?.clusters)
      ? (answer.own!.clusters as Record<string, unknown>[])
      : []

    // A row with no name is a row somebody started filling in, and it is not a place.
    const seen: Place[] = []
    for (const row of rows) {
      const name = typeof row?.name === 'string' ? row.name.trim() : ''
      if (!name) continue
      seen.push({
        name,
        namespace: typeof row.default_namespace === 'string' ? row.default_namespace : '',
        inUse: row.enabled !== false,
        autodeploy: row.auto_deploy !== false,
      })
    }
    places.value = seen

    const baseline: Record<string, { inUse: boolean; autodeploy: boolean }> = {}
    for (const one of seen) baseline[one.name] = { inUse: one.inUse, autodeploy: one.autodeploy }
    loaded.value = baseline
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/** Whether a place's two switches are different from what is stored. */
function changed(place: Place): boolean {
  const was = loaded.value[place.name]
  if (!was) return false
  return was.inUse !== place.inUse || was.autodeploy !== place.autodeploy
}

/** What this project has decided about this place, and nothing else: a row that says the
 *  same as the level above is not a decision, and a copy stops following that level
 *  without anybody deciding that. */
function own(place: Place): Record<string, unknown> {
  const was = loaded.value[place.name]
  const row: Record<string, unknown> = { name: place.name }
  if (was && was.inUse !== place.inUse) row.enabled = place.inUse
  if (was && was.autodeploy !== place.autodeploy) row.auto_deploy = place.autodeploy
  return row
}

/** Saves one place, and only that place. */
async function saveRow(place: Place) {
  saving.value = place.name
  error.value = ''
  try {
    await api.put(
      `/modules/${props.module.id}/settings/bulk?scope=project&projectID=${encodeURIComponent(props.projectId)}`,
      { values: { clusters: [own(place)] } },
    )
    await load()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    saving.value = ''
  }
}

/** Puts one place's switches back to what is stored. */
function discard(place: Place) {
  const was = loaded.value[place.name]
  if (!was) return
  place.inUse = was.inUse
  place.autodeploy = was.autodeploy
}

onMounted(load)
watch(() => props.module.id, load)
</script>

<template>
  <section class="places">
    <h2 class="places-title">{{ module.name }} places</h2>
    <p class="muted small">
      One row per place {{ props.projectPath }} may deploy to, and under it everything
      about that place: what is happening there, what has been deployed there, which
      images have been there, and its own settings.
    </p>

    <p v-if="error" class="alert alert-error">{{ error }}</p>
    <p v-if="loading" class="spinner">Loading…</p>

    <p v-else-if="places.length === 0" class="muted small">
      This module has no place this project may name. Its Options tab is where they are
      written down.
    </p>

    <!-- One place, folded away. The name is the whole of what is shown until somebody
         asks for it, the same way the repository's own configuration folds: a page of
         places that is a page of open cards is a page where nothing stands out. -->
    <details v-for="(place, index) in places" :key="place.name" class="place">
      <summary class="place-head">
        <span class="place-name mono">{{ place.name }}</span>
        <span v-if="place.namespace" class="muted small">{{ place.namespace }}</span>
        <span class="place-note">
          {{ !place.inUse ? 'not in use here' : place.autodeploy ? 'a push deploys here' : 'only by hand' }}
        </span>
      </summary>

      <div class="place-body">
        <!-- What this project may say about this place, and nothing else: whether a push
             may deploy here by itself, and whether this project may deploy here at all.
             Where the cluster is and how it is reached is on the module's own page. -->
        <div class="place-actions">
          <label
            class="switch-pair"
            title="Autodeploy: whether a push or a tag may deploy here by itself. Off means the run still happens and the image is still built — only the deployment here waits for somebody to start it by hand."
          >
            <span class="switch-name">Autodeploy</span>
            <button
              class="switch"
              :class="{ on: place.autodeploy }"
              type="button"
              role="switch"
              :aria-checked="place.autodeploy"
              :disabled="saving === place.name || !props.canManage || !place.inUse"
              @click="place.autodeploy = !place.autodeploy"
            >
              <span class="knob" />
            </button>
          </label>

          <label
            class="switch-pair"
            title="In use: whether this project may deploy here at all. Off leaves the place configured on the module's own page and untouched here — switched off for this project, not deleted."
          >
            <span class="switch-name">In use</span>
            <button
              class="switch"
              :class="{ on: place.inUse }"
              type="button"
              role="switch"
              :aria-checked="place.inUse"
              :disabled="saving === place.name || !props.canManage"
              @click="place.inUse = !place.inUse"
            >
              <span class="knob" />
            </button>
          </label>

          <span v-if="changed(place)" class="muted small not-saved">not saved yet</span>
          <button
            class="btn btn-small"
            type="button"
            :disabled="saving === place.name || !changed(place)"
            @click="discard(place)"
          >
            Discard
          </button>
          <button
            class="btn btn-small btn-primary"
            type="button"
            :disabled="saving === place.name || !changed(place)"
            @click="saveRow(place)"
          >
            {{ saving === place.name ? 'Saving…' : 'Save this row' }}
          </button>
        </div>

        <!-- The card about this place and no other. -->
        <ModuleDeployments
          :project-id="props.projectId"
          :project-path="props.projectPath"
          :module="module"
          :can-manage="props.canManage"
          :place="{ cluster: place.name, namespace: place.namespace }"
          :show-repository="index === 0"
          @settings-changed="load"
        />
      </div>
    </details>
  </section>
</template>

<style scoped>
.places {
  margin-top: 28px;
}

.places-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 4px;
}

.place {
  margin-top: 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

/* The row that is always visible: the name, and one word about what happens here by
   itself. Everything else is inside, and comes out when the name is clicked. */
.place-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
  padding: 12px 14px;
  cursor: pointer;
  list-style: none;
}

.place-head::-webkit-details-marker {
  display: none;
}

.place-name {
  font-weight: 600;
  margin-right: auto;
}

.place-note {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

.place-body {
  padding: 0 14px 14px;
}

.place-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
  padding-bottom: 10px;
  margin-bottom: 10px;
  border-bottom: 1px solid var(--border);
}

.switch-pair {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
}

.switch-name {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

.not-saved {
  margin-right: auto;
}
</style>