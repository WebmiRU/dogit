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

/**
 * Switching a branch keeps the user where they are.
 *
 * The ref lives in a different place depending on the view: in the path for the
 * file views, in the query for the ones that list things. Rewriting only the ref
 * and leaving the rest of the route alone is what stops "switch to dev" from
 * throwing someone off the commits page and onto the code page.
 */
async function switchRef(next: string) {
  if (!project.value || next === activeRef.value) return

  const base = `/p/${project.value.path}`
  const encode = encodeURIComponent(next)

  switch (view.value.name) {
    case 'tree': {
      const path = view.value.rest.slice(1).join('/')
      await navigateTo(`${base}/-/tree/${encode}/${path}`)
      return
    }
    case 'blob': {
      // The file may not exist on the new branch; the page reports that itself
      // rather than silently falling back.
      const path = view.value.rest.slice(1).join('/')
      await navigateTo(`${base}/-/blob/${encode}/${path}`)
      return
    }
    case 'commits':
    case 'branches':
      await navigateTo({ path: `${base}/-/${view.value.name}`, query: { ref: next } })
      return
    default:
      await navigateTo(`${base}/-/tree/${encode}`)
  }
}

const tabs = computed(() => {
  if (!project.value) return []
  const base = `/p/${project.value.path}`
  const ref = encodeURIComponent(activeRef.value)

  // The ref travels differently per view: in the path for the file views, as a
  // query parameter for the listings. Carrying it into every tab is what keeps a
  // chosen branch from resetting when the user clicks between Code and Commits.
  return [
    { label: 'Code', to: `${base}/-/tree/${ref}`, match: 'tree' },
    { label: 'Commits', to: `${base}/-/commits?ref=${ref}`, match: 'commits' },
    { label: 'Branches', to: `${base}/-/branches?ref=${ref}`, match: 'branches' },
  ]
})

/** Clone URL shown in the header. */
const cloneUrl = computed(() => project.value?.ssh_url ?? '')

const copyState = ref('')
async function copyCloneUrl() {
  try {
    // The whole command is copied, because that is what the user then pastes.
    await navigator.clipboard.writeText(`git clone ${cloneUrl.value}`)
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
          <span class="muted">git clone</span>
          <span class="clone-url">{{ cloneUrl }}</span>
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
          @change-ref="switchRef"
        />
        <RepositoryBlob
          v-else-if="view.name === 'blob'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
          :path="restPath"
          @change-ref="switchRef"
        />
        <RepositoryCommits
          v-else-if="view.name === 'commits'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
          @change-ref="switchRef"
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
          @change-ref="switchRef"
        />
        <div v-else class="card empty">Unknown view “{{ view.name }}”.</div>
      </div>
    </template>
  </div>
</template>
