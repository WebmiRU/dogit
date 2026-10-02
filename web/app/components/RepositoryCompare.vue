<script setup lang="ts">
/**
 * Compare two refs.
 *
 * This is the diff on its own, without a merge request attached to it: the answer
 * to "what is on this branch that is not on that one", which is worth asking on
 * its own before anyone opens a request about it.
 */
import type { FileChange, RefsResponse } from '~/types/repository'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  defaultBranch: string
}>()

const emit = defineEmits<{ (event: 'refs-changed'): void }>()

const allRefs = computed(() => [
  ...(props.refs?.branches ?? []).map((r) => ({ name: r.name, target: r.target, type: 'branch' as const })),
  ...(props.refs?.tags ?? []).map((r) => ({ name: r.name, target: r.target, type: 'tag' as const })),
])

// The chosen refs live in the URL, so a comparison can be linked to and comes
// back the way it was left. The URL wins over the defaults once it is present.
const route = useRoute()
const router = useRouter()

const from = ref('')
const to = ref('')

watch(
  [allRefs, () => route.query.from, () => route.query.to],
  ([refs, queryFrom, queryTo]) => {
    const names = refs.map((r) => r.name)
    from.value = names.includes(String(queryFrom))
      ? String(queryFrom)
      : props.defaultBranch || names[0] || ''
    to.value = names.includes(String(queryTo))
      ? String(queryTo)
      : names.find((name) => name !== from.value) ?? ''
  },
  { immediate: true },
)

watch([from, to], ([nextFrom, nextTo]) => {
  router
    .replace({ query: { ...route.query, from: nextFrom, to: nextTo } })
    .catch(() => {})
})

// Comparing on load means arriving at the tab with two refs chosen in the URL
// already shows the answer, rather than an empty page and a button.
watch(
  [from, to],
  ([nextFrom, nextTo]) => {
    if (nextFrom && nextTo && nextFrom !== nextTo) compare()
  },
  { immediate: true },
)

const files = ref<FileChange[]>([])
const stats = ref<{ files_changed: number; additions: number; deletions: number } | null>(null)
const loading = ref(false)
const error = ref('')
const searched = ref(false)

async function compare() {
  if (!from.value || !to.value || from.value === to.value) {
    error.value = 'Pick two different refs.'
    return
  }

  loading.value = true
  error.value = ''
  try {
    const response = await api.get<{ files: FileChange[]; stats: typeof stats.value }>(
      `/projects/${props.projectId}/repository/compare`,
      { from: from.value, to: to.value },
    )
    files.value = response.files ?? []
    stats.value = response.stats ?? null
    searched.value = true
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
    files.value = []
    stats.value = null
  } finally {
    loading.value = false
  }
}

</script>

<template>
  <div>
    <div class="card" style="margin-bottom: 16px">
      <div class="card-header"><strong>Compare</strong></div>
      <div class="card-body">
        <form class="compare-form" @submit.prevent="compare">
          <div class="field">
            <label for="compare-from">Compare</label>
            <select id="compare-from" v-model="from">
              <option v-for="ref in allRefs" :key="`from-${ref.name}`" :value="ref.name">
                {{ ref.name }}
              </option>
            </select>
          </div>

          <div class="arrow" aria-hidden="true">→</div>

          <div class="field">
            <label for="compare-to">with</label>
            <select id="compare-to" v-model="to">
              <option v-for="ref in allRefs" :key="`to-${ref.name}`" :value="ref.name">
                {{ ref.name }}
              </option>
            </select>
          </div>

          <button class="btn btn-primary" type="submit" :disabled="loading">
            {{ loading ? 'Comparing…' : 'Compare' }}
          </button>
        </form>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-else-if="searched" class="card">
      <div class="toolbar">
        <strong>{{ from }} → {{ to }}</strong>
        <div class="spacer" />
        <template v-if="stats">
          <span class="muted">{{ stats.files_changed }} {{ stats.files_changed === 1 ? 'file' : 'files' }}</span>
          <span class="add">+{{ stats.additions }}</span>
          <span class="del">−{{ stats.deletions }}</span>
        </template>
      </div>

      <div v-if="files.length === 0" class="empty">
        These refs point at the same content.
      </div>
      <div v-else class="card-body">
        <div v-for="change in files" :key="change.path" class="diff-file">
          <div class="toolbar">
            <NuxtLink
              class="name mono"
              :to="repoViewUrl(projectPath, 'blob', to, change.path)"
            >
              {{ change.path }}
            </NuxtLink>
            <span class="badge">{{ change.status }}</span>
            <span class="add">+{{ change.additions }}</span>
            <span class="del">−{{ change.deletions }}</span>
          </div>
          <pre class="patch">{{ change.patch || 'No textual changes' }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.compare-form {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  flex-wrap: wrap;
}

.compare-form .field {
  min-width: 200px;
}

.arrow {
  padding-bottom: 8px;
  color: var(--muted);
}

.diff-file + .diff-file {
  margin-top: 16px;
  border-top: 1px solid var(--border);
  padding-top: 12px;
}

.patch {
  margin: 8px 0 0;
  padding: 10px;
  background: var(--bg-code, #0d1117);
  border-radius: 6px;
  overflow-x: auto;
  font: 12px/1.5 var(--mono);
  white-space: pre;
}

.add {
  color: #3fb950;
}

.del {
  color: #f85149;
}
</style>