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

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{
      effective?: Record<string, unknown>
      inherited?: Record<string, unknown>
    }>(`/modules/${props.module.id}/settings?scope=project&projectID=${encodeURIComponent(props.projectId)}`)

    const rows = Array.isArray(answer.effective?.clusters)
      ? (answer.effective!.clusters as Record<string, unknown>[])
      : []
    const above = Array.isArray(answer.inherited?.clusters)
      ? (answer.inherited!.clusters as Record<string, unknown>[])
      : []

    const inheritedByName: Record<string, Record<string, unknown>> = {}
    for (const row of above) {
      if (typeof row?.name === 'string') inheritedByName[row.name] = row
    }
    inherited.value = inheritedByName

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
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
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

    <article v-for="(place, index) in places" :key="place.name" class="place">
      <header class="place-head">
        <div class="place-name">
          <span class="mono">{{ place.name }}</span>
          <span v-if="place.namespace" class="muted small">· {{ place.namespace }}</span>
        </div>

      </header>

      <!-- The card about this place and no other. -->
      <ModuleDeployments
        :project-id="props.projectId"
        :project-path="props.projectPath"
        :module="module"
        :can-manage="props.canManage"
        :place="{ cluster: place.name, namespace: place.namespace }"
        :show-repository="index === 0"
      />
    </article>
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
  margin-top: 16px;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

.place-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
  padding-bottom: 10px;
  margin-bottom: 10px;
  border-bottom: 1px solid var(--border);
}

.place-name {
  font-weight: 600;
  margin-right: auto;
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