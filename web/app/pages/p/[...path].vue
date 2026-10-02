<script setup lang="ts">
/**
 * Repository view.
 *
 * The URL carries both the project path and the view, mirroring the familiar
 * forge layout: /p/<owner>/<project>/-/tree/<ref>/<dir>, /-/blob/<ref>/<file>,
 * /-/commits, /-/commit/<sha>. A single catch-all page parses that structure so
 * nested group paths keep working.
 */
import type {
  BlameLine,
  CommitDiffResponse,
  CommitResponse,
  CommitsResponse,
  FileResponse,
  ProjectSummary,
  RefsResponse,
  TreeResponse,
} from '~/types/repository'

interface ProjectResponse {
  project: ProjectSummary
}

const route = useRoute()
const { ensureLoaded } = useAuth()
await ensureLoaded()

/** Splits the catch-all route into its parts. */
const segments = computed(() => {
  const raw = route.params.path
  const value = Array.isArray(raw) ? raw.join('/') : String(raw ?? '')
  return value.split('/').filter(Boolean)
})

/** Everything before the "-/" marker is the project path. */
const projectPath = computed(() => {
  const dashIndex = segments.value.indexOf('-')
  const parts = dashIndex === -1 ? segments.value : segments.value.slice(0, dashIndex)
  return parts.join('/')
})

/** Everything after "-/", with the leading view name removed. */
const view = computed(() => {
  const dashIndex = segments.value.indexOf('-')
  if (dashIndex === -1) return { name: 'tree', rest: [] as string[] }
  return {
    name: segments.value[dashIndex + 1] ?? 'tree',
    rest: segments.value.slice(dashIndex + 2),
  }
})

/** A ref may contain slashes, so it is taken from the query when present. */
const currentRef = computed(() => {
  const fromQuery = route.query.ref
  if (typeof fromQuery === 'string' && fromQuery) return fromQuery
  if (view.value.name === 'tree' || view.value.name === 'blob') return view.value.rest[0] ?? ''
  return ''
})

const restPath = computed(() => {
  if (view.value.name === 'blob') return decodeURIComponent(view.value.rest.slice(1).join('/'))
  if (view.value.name === 'tree') return decodeURIComponent(view.value.rest.slice(1).join('/'))
  return ''
})

const project = ref<ProjectSummary | null>(null)
const refs = ref<RefsResponse | null>(null)
const loadError = ref('')
const loading = ref(true)

async function loadProject() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<ProjectResponse>(
      `/projects/${encodeURIComponent(projectPath.value)}`,
    )
    project.value = response.project

    refs.value = await api.get<RefsResponse>(`/projects/${response.project.id}/repository/refs`)
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(loadProject)
watch(projectPath, loadProject)

const projectId = computed(() => project.value?.id ?? '')

const activeRef = computed(() => {
  if (currentRef.value) return currentRef.value
  return project.value?.default_branch || 'main'
})

const tabs = computed(() => {
  if (!project.value) return []
  const base = `/p/${project.value.path}`
  return [
    { label: 'Code', to: `${base}/-/tree/${encodeURIComponent(activeRef.value)}`, match: 'tree' },
    { label: 'Commits', to: `${base}/-/commits`, match: 'commits' },
    { label: 'Branches', to: `${base}/-/branches`, match: 'branches' },
  ]
})

/** Clone URL shown in the header. */
const cloneUrl = computed(() => project.value?.ssh_url ?? '')

const copyState = ref('')
async function copyCloneUrl() {
  try {
    await navigator.clipboard.writeText(cloneUrl.value)
    copyState.value = 'copied'
    setTimeout(() => (copyState.value = ''), 1500)
  } catch {
    copyState.value = 'copy failed'
  }
}
</script>

<template>
  <div>
    <div v-if="loading" class="spinner">Loading project…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <template v-else-if="project">
      <div class="repo-head">
        <div class="title">
          <h1 class="page-title">
            {{ project.name || project.path }}
            <span class="badge" :class="`badge-${project.visibility}`">{{ project.visibility }}</span>
            <span class="badge">{{ project.access_name }}</span>
          </h1>
          <p v-if="project.description" class="page-subtitle">{{ project.description }}</p>
        </div>
        <div class="repo-clone">
          <span>{{ cloneUrl }}</span>
          <button class="btn" type="button" @click="copyCloneUrl">
            {{ copyState || 'Copy' }}
          </button>
        </div>
      </div>

      <nav class="repo-tabs">
        <NuxtLink
          v-for="tab in tabs"
          :key="tab.match"
          :to="tab.to"
          :class="{ active: view.name === tab.match }"
        >
          {{ tab.label }}
        </NuxtLink>
      </nav>

      <div style="margin-top: 16px">
        <RepositoryTree
          v-if="view.name === 'tree'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
          :path="restPath"
        />
        <RepositoryBlob
          v-else-if="view.name === 'blob'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
          :path="restPath"
        />
        <RepositoryCommits
          v-else-if="view.name === 'commits'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
        />
        <RepositoryCommitView
          v-else-if="view.name === 'commit'"
          :project-id="projectId"
          :project-path="project.path"
          :sha="view.rest[0] ?? ''"
        />
        <RepositoryBranches
          v-else-if="view.name === 'branches'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
        />
        <div v-else class="card empty">Unknown view “{{ view.name }}”.</div>
      </div>
    </template>
  </div>
</template>
