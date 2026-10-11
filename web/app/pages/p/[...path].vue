<script setup lang="ts">
/**
 * What identifies this page to Nuxt.
 *
 * By default a page is keyed by its whole address, so moving from Notifications to
 * Pipelines tore down this component and built it again — the header, navigation and
 * content — and every one of those was thrown away and redrawn in front of the reader.
 *
 * The key here is the repository and nothing below it. The project navigation and
 * its page content are one component: changing sections swaps the content without
 * rebuilding the project header and navigation, while another repository rebuilds it.
 */
definePageMeta({
  key: (route) => route.path.split('/-/')[0] ?? route.path,
})

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
import type { MergeRequest } from '~/types/merge_requests'

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

/**
 * Where the ref of a view lives.
 *
 * A ref with a slash cannot sit in the path — the router decodes %2F into a real
 * separator — so views either carry it in the query or, when it has no slash, in
 * the path. The two are told apart by the ref itself rather than by the view, so
 * a link and its page always agree.
 */
const refFromQuery = computed(() =>
  typeof route.query.ref === 'string' ? route.query.ref : '',
)

const refFromPath = computed(() => {
  if (!['tree', 'blob', 'edit'].includes(view.value.name)) return ''
  const first = view.value.rest[0] ?? ''
  return first ? decodeURIComponent(first) : ''
})

/** True while the ref is the first path segment and nothing was moved to the query. */
const refIsInPath = computed(
  () => !!refFromPath.value && !refFromQuery.value && refFitsInPath(refFromPath.value),
)

const currentRef = computed(
  () => refFromQuery.value || refFromPath.value || project.value?.default_branch || 'main',
)

/** The directory or file the view is about, which follows the ref if it is in the path. */
const restPath = computed(() => {
  if (!['blob', 'tree', 'edit'].includes(view.value.name)) return ''
  const rest = refIsInPath.value ? view.value.rest.slice(1) : view.value.rest
  return decodeURIComponent(rest.join('/'))
})

const project = ref<ProjectSummary | null>(null)
const refs = ref<RefsResponse | null>(null)
const loadError = ref('')
const loading = ref(true)

async function loadProject() {
  loading.value = true
  loadError.value = ''
  try {
    // Each segment is encoded on its own. Encoding the whole path turns the slash
    // of a grouped project into %2F, which arrives as one segment the server
    // cannot resolve.
    const encodedPath = projectPath.value.split('/').map(encodeURIComponent).join('/')

    const response = await api.get<ProjectResponse>(`/projects/${encodedPath}`)
    project.value = response.project

    await loadRefs()
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

// The refs live here rather than in the views, because more than one of them show
// the same list: the branch selector in the header, the branch page and the tag
// page. Reloading them in one place is what keeps those three from disagreeing
// after a branch is created or deleted.
async function loadRefs() {
  if (!project.value) return

  const response = await api.get<RefsResponse>(
    `/projects/${project.value.id}/repository/refs`,
  )
  refs.value = response

  // Deleting the branch the page is looking at leaves the page pointing at
  // something that no longer exists. Moving it to the default branch is better
  // than leaving an error in place of the content the person came for.
  const name = activeRef.value
  if (!name) return

  const exists =
    response.branches.some((branch) => branch.name === name) ||
    response.tags.some((tag) => tag.name === name)
  if (exists) return

  const fallback = project.value.default_branch || response.default_branch
  if (fallback && fallback !== name) {
    await switchRef(fallback)
  }
}

/** Reload the refs and, if the current one is gone, move off it. */
async function refsChanged() {
  try {
    await loadRefs()
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  }
}

// The page is one component for every view of a repository, so watching the project
// path rather than mounting is what keeps the content on screen when only the tab
// changed. Reloading on mount — which is what a mount happens on, because the
// catch-all route is re-created when its last segment changes — throws the whole
// page away and shows a spinner in place of a table somebody was reading.
watch(projectPath, loadProject, { immediate: true })

/**
 * A repository with nothing in it.
 *
 * Every tab below this point answers a question about content, and an empty
 * repository has none of it: showing a file tree or a commit list for it produces
 * errors that read like faults rather than the state the project is actually in.
 */
const repositoryIsEmpty = computed(() => {
  const list = refs.value
  if (!list) return false
  return list.branches.length === 0 && list.tags.length === 0
})

/** The merge request number in the address, when the address carries one. */
const mergeRequestNumber = computed(() => {
  const candidate = view.value.rest[0] ?? ''
  return /^\d+$/.test(candidate) ? candidate : ''
})

function mergeRequestCreated(mr: MergeRequest) {
  void navigateTo('/p/' + (project.value?.path ?? projectPath.value) + '/-/merge_requests/' + mr.iid)
}

const projectId = computed(() => project.value?.id ?? '')

/** Access levels, mirroring the values the API uses. */
const AccessLevel = {
  Guest: 10,
  Reporter: 20,
  Developer: 30,
  Maintainer: 40,
  Owner: 50,
} as const

const canPush = computed(() => (project.value?.access_level ?? 0) >= AccessLevel.Developer)
const canManage = computed(() => (project.value?.access_level ?? 0) >= AccessLevel.Maintainer)

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
  const known = ['tree', 'blob', 'edit', 'commits', 'branches', 'tags', 'compare']

  if (known.includes(view.value.name)) {
    // The file may not exist on the branch being switched to; the page reports
    // that itself rather than silently falling back to another file.
    await navigateTo(repoViewUrl(project.value.path, view.value.name, next, restPath.value))
    return
  }

  await navigateTo(repoViewUrl(project.value.path, 'tree', next))
}

interface ProjectNavigationItem {
  label: string
  to: string
  match: string
}

interface ProjectNavigationGroup {
  key: 'repository' | 'build' | 'operate' | 'project'
  label: string
  items: ProjectNavigationItem[]
}

const navigationGroups = computed<ProjectNavigationGroup[]>(() => {
  if (!project.value) return []

  const path = project.value.path
  const base = `/p/${path}`
  const repositoryItems: ProjectNavigationItem[] = repositoryIsEmpty.value ? [] : [
    { label: 'Code', to: repoViewUrl(path, 'tree', activeRef.value), match: 'tree' },
    { label: 'Commits', to: repoViewUrl(path, 'commits', activeRef.value), match: 'commits' },
    { label: 'Branches', to: repoViewUrl(path, 'branches', activeRef.value), match: 'branches' },
    { label: 'Tags', to: repoViewUrl(path, 'tags', activeRef.value), match: 'tags' },
    { label: 'Compare', to: repoViewUrl(path, 'compare', activeRef.value), match: 'compare' },
    { label: 'Merge requests', to: repoViewUrl(path, 'merge_requests', activeRef.value), match: 'merge_requests' },
  ]

  const groups: ProjectNavigationGroup[] = []
  if (repositoryItems.length) {
    groups.push({ key: 'repository', label: 'Repository', items: repositoryItems })
  }

  groups.push(
    {
      key: 'build',
      label: 'Build',
      items: [
        { label: 'Pipelines', to: `${base}/-/pipelines`, match: 'pipelines' },
        { label: 'Artifacts', to: `${base}/-/artifacts`, match: 'artifacts' },
        { label: 'Images', to: `${base}/-/packages`, match: 'packages' },
      ],
    },
    {
      key: 'operate',
      label: 'Operate',
      items: [
        { label: 'Deploy', to: `${base}/-/deploy`, match: 'deploy' },
        { label: 'Notifications', to: `${base}/-/notifications`, match: 'notifications' },
      ],
    },
    {
      key: 'project',
      label: 'Project',
      items: [
        { label: 'Settings', to: `${base}/-/settings`, match: 'settings' },
      ],
    },
  )
  return groups
})

function isNavigationItemActive(match: string) {
  if (match === 'tree') return ['tree', 'blob', 'edit'].includes(view.value.name)
  return view.value.name === match
}

/** Clone URL shown in the header. */
const cloneUrl = computed(() => project.value?.ssh_url ?? '')

/** The first thing to run, ready to copy. */
const cloneCommand = computed(() => `git clone ${cloneUrl.value}
cd ${project.value?.path.split('/').pop() ?? ''}
touch README.md
git add README.md
git commit -m "Initial commit"
git push -u origin HEAD`)

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
      <div class="repo-head project-page-head">
        <div class="project-identity">
          <div class="project-mark" aria-hidden="true">{{ (project.name || project.path).slice(0, 1).toUpperCase() }}</div>
          <div class="title project-title-block">
            <div class="project-path">{{ project.path.split('/').slice(0, -1).join(' / ') || 'Projects' }}</div>
            <h1 class="page-title project-page-title">
              {{ project.name || project.path.split('/').pop() }}
              <span class="badge badge-label" :class="`badge-${project.visibility}`">{{ project.visibility }}</span>
              <span class="badge badge-label">{{ project.access_name }}</span>
            </h1>
            <p class="page-subtitle">{{ project.description || 'No description yet.' }}</p>
          </div>
        </div>
        <div class="repo-clone">
          <span class="muted">git clone</span>
          <span class="clone-url">{{ cloneUrl }}</span>
          <button class="btn" type="button" @click="copyCloneUrl">
            {{ copyState || 'Copy' }}
          </button>
        </div>
      </div>

      <div class="project-workspace">
        <aside class="project-sidebar">
          <div class="project-sidebar-caption">Project</div>
          <div class="project-sidebar-project">
            <span class="project-sidebar-mark" aria-hidden="true">
              {{ (project.name || project.path).slice(0, 1).toUpperCase() }}
            </span>
            <span class="project-sidebar-project-name">
              <strong>{{ project.name || project.path.split('/').pop() }}</strong>
              <small>{{ project.path }}</small>
            </span>
          </div>

          <nav class="project-nav" aria-label="Project navigation">
            <section
              v-for="group in navigationGroups"
              :key="group.key"
              class="project-nav-group"
            >
              <h2 class="project-nav-group-title">
                <svg v-if="group.key === 'repository'" viewBox="0 0 16 16" aria-hidden="true">
                  <path d="m2 4 6-2.5L14 4v8l-6 2.5L2 12z M2 4l6 3 6-3 M8 7v7.5" />
                </svg>
                <svg v-else-if="group.key === 'build'" viewBox="0 0 16 16" aria-hidden="true">
                  <rect x="1.5" y="1.5" width="4" height="4" rx="0.7" />
                  <rect x="10.5" y="10.5" width="4" height="4" rx="0.7" />
                  <path d="M5.5 3.5h3v9h2" />
                </svg>
                <svg v-else-if="group.key === 'operate'" viewBox="0 0 16 16" aria-hidden="true">
                  <path d="M8 1.5v9m0 0 3-3m-3 3-3-3M2 13.5h12" />
                </svg>
                <svg v-else viewBox="0 0 16 16" aria-hidden="true">
                  <path d="M6 1.5h4l.6 2 1.6.9 2-.5 1.2 2.1-1.4 1.5v1.8l1.4 1.5-1.2 2.1-2-.5-1.6.9-.6 2H6l-.6-2-1.6-.9-2 .5-1.2-2.1L2 9.3V7.5L.6 6l1.2-2.1 2 .5 1.6-.9z" transform="translate(.5 0) scale(.94)" />
                  <circle cx="8" cy="8" r="2" />
                </svg>
                <span>{{ group.label }}</span>
              </h2>
              <div class="project-nav-links">
                <NuxtLink
                  v-for="item in group.items"
                  :key="item.match"
                  :to="item.to"
                  class="project-nav-item"
                  :class="{ 'is-active': isNavigationItemActive(item.match) }"
                  :aria-current="isNavigationItemActive(item.match) ? 'page' : undefined"
                >
                  {{ item.label }}
                </NuxtLink>
              </div>
            </section>
          </nav>
        </aside>

        <div class="project-workspace-main">


      <div v-if="repositoryIsEmpty" class="card empty-repo">
        <h2>This repository is empty</h2>
        <p class="muted">
          Nothing has been pushed to it yet, so there is no code to show. The
          quickest way to start is to push a commit from a machine you have an
          SSH key on.
        </p>
        <pre class="clone-command">{{ cloneCommand }}</pre>
        <p class="muted">
          Once there is a first commit, the code, commit and branch pages all
          start to mean something.
        </p>
        <NuxtLink class="btn" to="/projects">Back to projects</NuxtLink>
      </div>

      <div v-else class="project-view-content">
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
          :can-push="canPush"
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
          :default-branch="project.default_branch"
          @change-ref="switchRef"
          @refs-changed="refsChanged"
        />
        <!-- A number after the name makes it one request rather than the list. -->
        <RepositoryCompare
          v-else-if="view.name === 'compare'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :default-branch="project.default_branch"
          @refs-changed="refsChanged"
        />
        <MergeRequestCreatePage
          v-else-if="view.name === 'merge_requests' && view.rest[0] === 'new'"
          :project-id="projectId"
          :project-path="project.path"
          :project-name="project.name || project.path"
          :refs="refs"
          :default-branch="project.default_branch"
          @created="mergeRequestCreated"
        />
        <!-- Only a number after the name makes it one request rather than the
             list; anything else is a mistyped address, and showing the list beats
             an error about a merge request number. -->
        <MergeRequestView
          v-else-if="view.name === 'merge_requests' && mergeRequestNumber"
          :key="mergeRequestNumber"
        />
        <MergeRequestList
          v-else-if="view.name === 'merge_requests'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :default-branch="project.default_branch"
          :can-create="canPush"
          @refs-changed="refsChanged"
        />
        <RepositoryTags
          v-else-if="view.name === 'tags'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          @refs-changed="refsChanged"
        />
        <RepositoryEditor
          v-else-if="view.name === 'edit'"
          :project-id="projectId"
          :project-path="project.path"
          :refs="refs"
          :ref-name="activeRef"
          :path="restPath"
          :can-push="canPush"
          @change-ref="switchRef"
        />
        <!-- A run has its own page: watching the log is what people come for, and
             a list that also did that would do neither thing well. -->
        <ProjectPipelineRun
          v-else-if="view.name === 'pipelines' && view.rest[0]"
          :project-id="projectId"
          :project-path="project.path"
          :run-iid="Number(view.rest[0])"
          :job-iid="view.rest[1] === 'jobs' && view.rest[2] ? Number(view.rest[2]) : 0"
          :default-branch="project.default_branch"
        />
        <ProjectPipelines
          v-else-if="view.name === 'pipelines'"
          :project-id="projectId"
          :project-path="project.path"
          :default-branch="project.default_branch"
        />
        <ProjectPackages
          v-else-if="view.name === 'packages'"
          :project-id="projectId"
          :project-path="project.path"
        />
        <ProjectArtifacts
          v-else-if="view.name === 'artifacts'"
          :project-id="projectId"
          :project-path="project.path"
          :can-trigger="canPush"
        />
        <ProjectNotifications
          v-else-if="view.name === 'notifications'"
          :project-id="projectId"
          :project-path="project.path"
          :can-manage="canManage"
        />
        <ProjectDeploy
          v-else-if="view.name === 'deploy'"
          :project-id="projectId"
          :project-path="project.path"
          :can-manage="canManage"
        />
        <RepositorySettings
          v-else-if="view.name === 'settings'"
          :project="project"
          :project-id="projectId"
          :can-manage="canManage"
          @saved="project = $event"
        />
        <div v-else class="card empty">Unknown view “{{ view.name }}”.</div>
        </div>
      </div>
    </div>
    </template>
  </div>
</template>

