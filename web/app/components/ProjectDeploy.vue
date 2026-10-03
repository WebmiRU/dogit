<script setup lang="ts">
/**
 * Where this project deploys to, and what has been deployed.
 *
 * Its own tab rather than a block in settings, for the same reason notifications have
 * one: this is the answer to "what is running, and where did it come from", and it is
 * worth being able to look at it without scrolling past something else first.
 *
 * The tab exists whether or not a deploy module is installed. An empty tab says what
 * is missing and what to do about it, which is the only way somebody who has never
 * heard of a deploy module finds out that the question has an answer.
 *
 * What it deliberately does not do is decide anything. Which module deploys, and
 * where, is in the repository's .dogit-ci.yml; what this page can change is the
 * module's own configuration for this project — which cluster name from the instance's
 * rows this project is allowed to name.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{
  projectId: string
  projectPath: string
  canManage: boolean
}>()

const modules = ref<ModuleRow[]>([])
const loading = ref(true)
const error = ref('')

/**
 * The modules that deploy, by what their kind says.
 *
 * A kind starting with "deploy:" is the whole of the test, and it is the core's test:
 * it knows the shape of the name and nothing about Kubernetes, Nomad or what the
 * module behind either one does. That is what lets a second kind appear later with
 * nothing changed here.
 */
const deployModules = computed(() =>
  modules.value.filter((module) => module.kind.startsWith('deploy:')),
)

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

onMounted(load)
</script>

<template>
  <div>
    <h2 class="page-title">Deploy</h2>
    <p class="page-subtitle">
      Where {{ props.projectPath }} is deployed. The deployment itself is described in
      the repository's <span class="mono">.dogit-ci.yml</span>; what is set here is
      which of the instance's targets this project may name.
    </p>

    <div v-if="loading" class="spinner">Loading…</div>
    <div v-else-if="error" class="alert alert-error">{{ error }}</div>

    <div v-else-if="deployModules.length === 0" class="card empty">
      <p>
        No deploy module is installed on this instance, so nothing here deploys
        anything yet.
      </p>
      <p class="muted">
        A deploy module is what knows about clusters, namespaces and manifests. The
        core does not: it reads a <span class="mono">deploy:</span> block from the
        repository and hands it to whichever module was named there.
      </p>
    </div>

    <div v-for="module in deployModules" :key="module.id" class="card module-block">
      <div class="card-body">
        <h3 class="module-name">
          {{ module.name }}
          <span class="muted mono">{{ module.kind }}</span>
        </h3>
        <p class="muted small">
          <template v-if="module.enabled">Enabled.</template>
          <template v-else>
            Forbidden on this instance, so this project deploys nowhere at the moment.
          </template>
        </p>

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
.module-block {
  margin-top: 16px;
}

.module-name {
  margin: 0 0 4px;
  font-size: 15px;
}

.small {
  font-size: 12px;
}
</style>