<script setup lang="ts">
/**
 * Who pushes to this registry, at one scope.
 *
 * A registry's own row answers for the whole instance. This is everything narrower than
 * that: a group that pushes somewhere else, a project with an account of its own, a
 * password rotated for one team and not the rest. Each scope changes only what it names,
 * so a project can set a password and inherit the login — which is the arrangement people
 * actually want, and the reason a form here sends only the fields that were touched.
 *
 * The distinction this component is careful about: what a scope has *written* is not what
 * a build would *use*. Both are shown, and the form is filled from the first. Filling it
 * from the second would put an inherited login into the field as though the group had
 * chosen it, and saving would freeze an inheritance into a copy that no longer follows
 * the thing it came from.
 */
import type { GroupSummary } from '~/types/dashboard'
import type { RegistryCredentials, RegistryCredentialsInput } from '~/types/registry'

const props = defineProps<{ registryId: string }>()

const { user } = useAuth()

type Scope = 'instance' | 'group' | 'project'
const scope = ref<Scope>('instance')

const groups = ref<GroupSummary[]>([])
const projects = ref<{ id: string; path: string }[]>([])
const groupId = ref('')
const projectId = ref('')

/** Lists are fetched the first time a scope needs one, and then kept. */
const listsLoaded = ref({ group: false, project: false })

const loaded = ref<RegistryCredentials | undefined>()
const form = reactive({ credential_source: '', login: '', password: '' })
/** Whether the reader has said they mean to remove the password this scope set. */
const clearPassword = ref(false)

const loading = ref(false)
const saving = ref(false)
const removing = ref(false)
const error = ref('')
const saved = ref(false)

const SCOPES: { key: Scope; label: string; hint: string }[] = [
  {
    key: 'instance',
    label: 'Instance',
    hint: 'What every group and project inherits unless it says otherwise.',
  },
  { key: 'group', label: 'Group', hint: 'One group, and the projects under it.' },
  { key: 'project', label: 'Project', hint: 'One project.' },
]

const scopeHint = computed(() => SCOPES.find((one) => one.key === scope.value)?.hint ?? '')

const mayEdit = computed(() => user.value?.is_admin === true)

/**
 * What the reader has chosen that is worth sending.
 *
 * Only fields that differ from what is already written, because every field of this form
 * is an override rather than a value: sending a field that was not touched would write
 * down an inherited login as though this scope had chosen it, and sending an untouched
 * password field would delete the password the scope already has.
 */
function payload(): RegistryCredentialsInput {
  const body: RegistryCredentialsInput = {}
  const was = loaded.value
  if (form.credential_source !== (was?.credential_source ?? '')) {
    body.credential_source = form.credential_source
  }
  if (form.login !== (was?.login ?? '')) {
    body.login = form.login
  }
  if (form.password !== '') {
    body.password = form.password
  } else if (clearPassword.value && was?.has_password) {
    body.password = ''
  }
  return body
}

/** Nothing was touched, so there is nothing to say. */
const changed = computed(() => Object.keys(payload()).length > 0)

/** The scope this form is about, as the address the core is asked for. */
function query(): string {
  if (scope.value === 'group') return '?scope=group&groupID=' + encodeURIComponent(groupId.value)
  if (scope.value === 'project') {
    return '?scope=project&projectID=' + encodeURIComponent(projectId.value)
  }
  return '?scope=instance'
}

/** Whether the scope chosen is one that can be asked about yet. */
const ready = computed(() => {
  if (scope.value === 'group') return groupId.value !== ''
  if (scope.value === 'project') return projectId.value !== ''
  return true
})

async function loadLists(which: 'group' | 'project') {
  if (listsLoaded.value[which]) return
  try {
    if (which === 'group') {
      const answer = await api.get<{ groups: GroupSummary[] }>('/groups')
      groups.value = answer.groups ?? []
    } else {
      const answer = await api.get<{ projects?: { id: string; path: string }[] }>(
        '/projects?limit=200',
      )
      projects.value = answer.projects ?? []
    }
    listsLoaded.value[which] = true
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  }
}

async function read() {
  if (!mayEdit.value || !ready.value) return
  loading.value = true
  error.value = ''
  saved.value = false
  clearPassword.value = false
  form.password = ''
  try {
    const answer = await api.get<RegistryCredentials>(
      `/registry/docker/${props.registryId}/credentials${query()}`,
    )
    loaded.value = answer
    // Filled from what this scope wrote, never from what it would resolve to. The empty
    // fields are the point: they are the overrides this scope has not chosen.
    form.credential_source = answer.credential_source
    form.login = answer.login
  } catch (caught) {
    loaded.value = undefined
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

async function save() {
  if (saving.value || !ready.value) return
  saving.value = true
  error.value = ''
  saved.value = false
  try {
    await api.put(`/registry/docker/${props.registryId}/credentials${query()}`, payload())
    clearPassword.value = false
    form.password = ''
    await read()
    saved.value = true
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    saving.value = false
  }
}

/**
 * Takes this scope's own answer away, leaving whatever it inherited.
 *
 * Not the same as saving an empty form, which is why it is a separate button with its
 * own words: one says "this scope has said nothing", the other would save three empty
 * fields and leave the reader guessing whether the inherited pair survived.
 */
async function forget() {
  if (removing.value || !loaded.value?.written) return
  removing.value = true
  error.value = ''
  saved.value = false
  try {
    await api.put(`/registry/docker/${props.registryId}/credentials${query()}`, {
      credential_source: '',
      login: '',
      password: '',
    })
    clearPassword.value = false
    form.password = ''
    await read()
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    removing.value = false
  }
}

watch(scope, async (which) => {
  await loadLists(which)
  await read()
})
watch([groupId, projectId], read)

onMounted(read)
</script>

<template>
  <section v-if="mayEdit" class="registry-credentials">
    <div class="field">
      <label class="checkbox registry-credentials-toggle">
        <input v-model="scope" type="radio" value="instance" />
        Instance
      </label>
      <label class="checkbox registry-credentials-toggle">
        <input v-model="scope" type="radio" value="group" />
        Group
      </label>
      <label class="checkbox registry-credentials-toggle">
        <input v-model="scope" type="radio" value="project" />
        Project
      </label>
    </div>

    <div v-if="scope !== 'instance'" class="field">
      <label :for="'registry-credentials-scope'">{{ scope === 'group' ? 'Group' : 'Project' }}</label>
      <select
        v-if="scope === 'group'"
        id="registry-credentials-scope"
        v-model="groupId"
      >
        <option value="">Choose a group…</option>
        <option v-for="one in groups" :key="one.id" :value="one.id">
          {{ one.full_path || one.name }}
        </option>
      </select>
      <select v-else id="registry-credentials-scope" v-model="projectId">
        <option value="">Choose a project…</option>
        <option v-for="one in projects" :key="one.id" :value="one.id">{{ one.path }}</option>
      </select>
      <p class="hint">{{ scopeHint }}</p>
    </div>
    <p v-else class="hint registry-credentials-hint">{{ scopeHint }}</p>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div v-if="loading" class="spinner">Loading…</div>

    <div v-else-if="!ready" class="alert alert-info">
      Choose {{ scope === 'group' ? 'a group' : 'a project' }} to see what it has written.
    </div>

    <div v-else-if="loaded" class="card registry-credentials-card">
      <div class="card-body">
        <p v-if="loaded.resolved_error" class="alert alert-warning">
          {{ loaded.resolved_error }}
        </p>
        <p v-else class="registry-credentials-effective">
          A build for this {{ scope }} pushes as
          <strong>{{ loaded.resolved.login || 'nobody — no login is set anywhere' }}</strong>
          <template v-if="loaded.resolved.has_password">with a password</template>
          <template v-else>with no password</template>,
          from source <strong>{{ loaded.resolved.credential_source || 'static' }}</strong>.
          <span v-if="!loaded.written" class="registry-credentials-inherited">
            Nothing here overrides anything, so this is what it inherits.
          </span>
        </p>

        <div class="field">
          <label for="registry-credentials-source">Source</label>
          <select id="registry-credentials-source" v-model="form.credential_source">
            <option value="">Inherit from the scope above</option>
            <option value="static">Static — the account written down here</option>
            <option value="user">User — a dogit account of the same name</option>
          </select>
          <p class="hint">
            Empty leaves it to the scope above. A source set here overrides only the source;
            the login and password are still inherited unless you name them below.
          </p>
        </div>

        <div class="field">
          <label for="registry-credentials-login">Login</label>
          <input
            id="registry-credentials-login"
            v-model="form.login"
            autocomplete="off"
            placeholder="inherited"
          />
          <p class="hint">
            Empty inherits the login from the scope above. Naming one here copies it into
            this scope, and it will stop following the one it came from.
          </p>
        </div>

        <div class="field">
          <label for="registry-credentials-password">Password</label>
          <input
            id="registry-credentials-password"
            v-model="form.password"
            type="password"
            autocomplete="new-password"
            :placeholder="loaded.has_password ? 'set here — leave empty to keep' : 'inherited'"
          />
          <p class="hint">
            Stored and never shown. Empty keeps whatever this scope already has, or
            inherits when it has none.
          </p>
          <label v-if="loaded.has_password && !form.password" class="checkbox">
            <input v-model="clearPassword" type="checkbox" />
            Remove this scope's own password when I save, so it inherits again
          </label>
        </div>

        <div v-if="saved" class="alert alert-info">Saved. What a build would use is above.</div>

        <div class="docker-actions">
          <button class="btn btn-primary" type="button" :disabled="saving || !changed" @click="save">
            {{ saving ? 'Saving…' : 'Save' }}
          </button>
          <button
            v-if="loaded.written"
            class="btn btn-danger"
            type="button"
            :disabled="removing"
            @click="forget"
          >
            {{ removing ? 'Removing…' : 'Forget this scope' }}
          </button>
        </div>
      </div>
    </div>
  </section>
</template>