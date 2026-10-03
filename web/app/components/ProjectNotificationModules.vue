<script setup lang="ts">
/**
 * Where this project's notifications go.
 *
 * A project usually wants a different channel from the rest of the installation —
 * its own chat, its own bot — and having to ask an administrator to change
 * something instance-wide for that is the wrong way round. So the same modules are
 * offered here, and what is chosen is this project's own choice, overriding whatever
 * the group and the instance say.
 *
 * Only modules that announce things are listed: a registry has nothing to say
 * about a build, and offering its settings here would be a form nobody could fill
 * in meaningfully.
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

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<{ modules: ModuleRow[] }>('/modules')
    modules.value = (answer.modules ?? []).filter((one) => one.kind.startsWith('notify:'))
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div :data-project-id="projectId">
    <h3 class="section-title">Notifications</h3>
    <p class="muted small">
      Where this project's builds are announced. Only one module may be switched on
      here; anything left alone is inherited from the group or the instance.
    </p>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="!canManage" class="alert alert-info">
      Changing this needs rights to manage the project.
    </div>

    <div v-else-if="loading" class="spinner">Loading modules…</div>

    <div v-else-if="modules.length === 0" class="card empty">
      No notification modules are installed, so there is nothing to choose.
    </div>

    <div v-for="one in modules" v-else :key="one.id" class="card module-card">
      <div class="card-header">
        <strong>{{ one.name }}</strong>
        <span class="mono muted small">{{ one.kind }}</span>
        <div class="spacer" />
        <span
          class="badge"
          :class="one.status === 'online' ? 'badge-green' : 'badge-neutral'"
        >{{ one.status }}</span>
      </div>
      <div class="card-body">
        <ModuleSettingsForm
          :module="one"
          scope="project"
          :scope-id="props.projectId"
          note="These are this project's values. What is not set here is taken from the group, and then from the instance."
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.section-title {
  font-size: 16px;
}

.module-card {
  margin-bottom: 12px;
}

.card-header .spacer {
  flex: 1;
}

.small {
  font-size: 12px;
}
</style>