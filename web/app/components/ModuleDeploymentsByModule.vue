<script setup lang="ts">
/**
 * Every deployment this module has made, across every project.
 *
 * What somebody who installed a deploy module wants on its page: not one project's
 * deployments but all of them, because a module is an instance-level thing and the
 * clusters it reaches are shared.
 *
 * The history is asked for project by project through the same endpoint the project's
 * own page uses. That is one request per project rather than one request for all of
 * them, and it is a deliberate trade: an instance-wide endpoint would mean the core
 * holding a list of projects in order to ask a module about a thing stored per project,
 * which is a second place to keep an answer the module already has.
 *
 * The heading and the explanation live here, once. The panel below says what it has to
 * say about the deployments and no more, because the same two paragraphs three times
 * on one page is a page nobody reads.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{ module: ModuleRow }>()

interface Project {
  id: string
  path: string
}

const projects = ref<Project[]>([])
const loading = ref(true)
const error = ref('')

/** Rolling back is the administrator's, and the control is hidden from anybody else
 *  rather than shown and refused: a button that cannot work makes the page look broken. */
const { user, ensureLoaded } = useAuth()
const mayUndo = computed(() => user.value?.is_admin === true)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ projects?: Project[] }>('/projects?limit=100')
    projects.value = answer.projects ?? []
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await ensureLoaded()
  await load()
})
</script>

<template>
  <div>
    <h2 class="page-title">Deployments</h2>
    <p class="page-subtitle">
      Everything <span class="mono">{{ props.module.kind }}</span> has deployed on this
      instance, per project.
    </p>

    <div v-if="loading" class="spinner">Loading…</div>
    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <div v-for="project in projects" :key="project.id" class="project-block">
      <h3 class="project-name mono">{{ project.path }}</h3>
      <ModuleDeployments
        :project-id="project.id"
        :project-path="project.path"
        :module="props.module"
        :can-manage="mayUndo"
      />
    </div>
  </div>
</template>

<style scoped>
.project-block {
  padding: 14px 0;
  border-bottom: 1px solid var(--border);
}

.project-name {
  margin: 0 0 6px;
  font-size: 13px;
}
</style>