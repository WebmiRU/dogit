<script setup lang="ts">
/**
 * Where this project deploys to, as its repository says so.
 *
 * Read-only, and deliberately a second copy of nothing. The rules live in the file,
 * because a rule about what reaches production is a claim about code and is reviewed
 * with it. What this shows is that same policy read back in words, because "which
 * branch reaches production" is a question people ask of the interface rather than of a
 * file they have to go and find.
 *
 * A page that let these be edited here would be a second answer to the same question,
 * and the two would disagree the moment either changed alone.
 */
interface Place {
  name: string
  target: string
  cluster: string
  namespace: string
  tag_only: boolean
  rollout: boolean
  rules: string[]
}

const props = defineProps<{
  projectId: string
  branch: string
}>()

const places = ref<Place[]>([])
const loading = ref(true)
const error = ref('')

/** What a place listens for, in one sentence. */
function listensTo(place: Place): string {
  if (place.rules.length === 0) return 'every run'
  return place.rules.join(' · ')
}

onMounted(async () => {
  try {
    const answer = await api.get<{ places?: Place[] }>(
      `/projects/${props.projectId}/deploy-places`,
    )
    places.value = answer.places ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <section class="places">
    <div class="block-head">
      <h3 class="block-title">Where this deploys</h3>
      <span class="muted small">
        written in {{ props.branch }}'s .dogit-ci.yml, shown here to be read
      </span>
    </div>

    <div v-if="loading" class="spinner">Loading…</div>
    <p v-else-if="error" class="muted small">{{ error }}</p>
    <p v-else-if="places.length === 0" class="muted small">
      This project says nothing about deploying anywhere. A pipeline that builds an image
      is a pipeline that builds.
    </p>

    <table v-else class="places-table">
      <thead>
        <tr>
          <th>Place</th>
          <th>Goes to</th>
          <th>When</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="place in places" :key="place.name || place.cluster">
          <td>
            <span class="place-name">{{ place.name || '—' }}</span>
            <!-- A place that only a tag can reach says so here rather than by its
                 absence: "every run" next to production would be the wrong thing to
                 read, and the rule is what stops it, not the prose. -->
            <span v-if="place.tag_only" class="badge badge-neutral">by tag only</span>
          </td>
          <td class="small">
            <span class="mono">{{ place.target }}</span>
            <span class="muted"> · {{ place.cluster }}</span>
            <span v-if="place.namespace" class="muted"> · {{ place.namespace }}</span>
          </td>
          <td class="mono small">{{ listensTo(place) }}</td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.places {
  margin-top: 1rem;
}

.places-table {
  width: 100%;
  border-collapse: collapse;
}

th,
td {
  text-align: left;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
}

.place-name {
  font-weight: 600;
  margin-right: 6px;
}

.small {
  font-size: 12px;
}
</style>