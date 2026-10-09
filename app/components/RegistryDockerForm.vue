<script setup lang="ts">
/**
 * One registry, written down or edited.
 *
 * Both halves of the job in one component, because they are the same form with or without
 * a record behind it, and two copies of a form are two copies of a rule — and the rules
 * here are the ones that are easy to get wrong. A password field that is never given a
 * value must not blank the stored one; every switch is a switch, so editing a name must not
 * turn any of them off; and an empty address on an edit is a mistake to report rather than
 * a registry with nothing in it.
 *
 * So the form sends only what the reader changed, and says what an empty password means
 * instead of guessing: leaving it alone keeps the stored one, typing a new one replaces it,
 * and the button below it clears it on purpose.
 */
import type { DockerRegistryInput, WrittenDockerRegistry } from '~/types/registry'

const props = defineProps<{ registryId?: string }>()
const editing = computed(() => Boolean(props.registryId))

const { user } = useAuth()

const form = reactive({
  name: '',
  url: '',
  login: '',
  password: '',
  insecure_tls: false,
  read_only: false,
  note: '',
  enabled: true,
})

/**
 * Whether a record is still on its way.
 *
 * False to begin with, and true only for as long as a record is actually being fetched: a
 * form with nothing behind it has nothing to wait for, and a form that shows a spinner
 * until something arrives is a spinner that arrives for a form that was already there.
 */
const loading = ref(false)
const saving = ref(false)
const removing = ref(false)
const error = ref('')
const loaded = ref<WrittenDockerRegistry | undefined>()
/** Whether the reader has said they mean to remove the stored password. */
const clearPassword = ref(false)

onMounted(async () => {
  if (!user.value?.is_admin || !props.registryId) return
  loading.value = true
  try {
    const answer = await api.get<WrittenDockerRegistry>(`/registry/docker/${props.registryId}`)
    loaded.value = answer
    form.name = answer.name
    form.url = answer.url
    form.login = answer.login
    form.password = ''
    form.insecure_tls = answer.insecure_tls
    form.read_only = answer.read_only
    form.note = answer.note
    form.enabled = answer.enabled
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
})

/**
 * What the form sends.
 *
 * The password only if the field says something about it, which is the one field that
 * cannot be treated as "whatever is in the box": an empty field nobody typed into is not
 * the same as an empty field they emptied on purpose, and only the second one may clear
 * what is stored. A new record starts with no password at all, so there an empty field
 * needs no saying.
 */
function payload(): DockerRegistryInput {
  const body: DockerRegistryInput = {
    name: form.name,
    url: form.url,
    login: form.login,
    insecure_tls: form.insecure_tls,
    read_only: form.read_only,
    note: form.note,
    enabled: form.enabled,
  }
  if (form.password !== '' || (editing.value && clearPassword.value)) {
    body.password = form.password
  }
  return body
}

/** The stored password is about to go, and the reader asked for it to. */
const removingPassword = computed(
  () => editing.value && clearPassword.value && form.password === '',
)

async function save() {
  if (saving.value) return
  saving.value = true
  error.value = ''
  try {
    const body = payload()
    if (editing.value && props.registryId) {
      await api.patch(`/registry/docker/${props.registryId}`, body)
    } else {
      await api.post('/registry/docker', body)
    }
    await navigateTo('/registry/docker')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    saving.value = false
  }
}

async function remove() {
  if (!props.registryId || removing.value) return
  // Named out loud, because this button is next to Save and beside Delete, and the three
  // of them do three different things to the same record.
  const reg = loaded.value
  const which = reg ? ` ${reg.name || reg.url}` : ' this registry'
  if (!confirm(`Delete${which}? This removes the record only — nothing inside the registry is touched.`)) {
    return
  }
  removing.value = true
  error.value = ''
  try {
    await api.delete(`/registry/docker/${props.registryId}`)
    await navigateTo('/registry/docker')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    removing.value = false
  }
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title" style="margin: 0">
          {{ editing ? 'Registry' : 'New registry' }}
        </h1>
        <p class="page-subtitle">
          <NuxtLink to="/registry/docker">Registries</NuxtLink>
          <template v-if="editing"> · Docker</template>
        </p>
      </div>
      <button class="btn" type="button" @click="navigateTo('/registry/docker')">Back</button>
    </div>

    <div v-if="!user?.is_admin" class="alert alert-info">
      This page requires administrator rights.
    </div>

    <template v-else>
      <div v-if="error" class="alert alert-error">{{ error }}</div>
      <div v-if="loading" class="spinner">Loading…</div>

      <form v-else class="card" @submit.prevent="save">
        <div class="card-body">
          <div class="field">
            <label for="url">Address</label>
            <input
              id="url"
              v-model="form.url"
              placeholder="registry.example.com:5000, or 192.168.1.103:8091"
              required
            />
            <p class="hint">
              Host with a port if it has one, and a path if the registry is served under
              one. Whether the name resolves is not checked here.
            </p>
          </div>

          <div class="field">
            <label for="name">Name</label>
            <input id="name" v-model="form.name" placeholder="internal mirror" />
            <p class="hint">For you. The list falls back to the address when this is empty.</p>
          </div>

          <div class="field">
            <label for="login">Login</label>
            <input id="login" v-model="form.login" autocomplete="off" />
          </div>

          <div class="field">
            <label for="password">Password</label>
            <input
              id="password"
              v-model="form.password"
              type="password"
              autocomplete="new-password"
              :placeholder="loaded?.has_password ? 'a password is set' : 'none'"
            />
            <p class="hint">
              <template v-if="loaded?.has_password">
                Stored and never shown here. Leave this empty to keep it.
              </template>
              <template v-else>Left empty if the registry needs no credential.</template>
            </p>
            <label v-if="loaded?.has_password && !form.password" class="checkbox">
              <input v-model="clearPassword" type="checkbox" />
              Remove the stored password when I save
            </label>
          </div>

          <div class="field">
            <label class="checkbox">
              <input v-model="form.insecure_tls" type="checkbox" />
              Do not check the certificate (a self-signed registry)
            </label>
            <label class="checkbox">
              <input v-model="form.read_only" type="checkbox" />
              Pull only — images may not be pushed here
            </label>
            <label class="checkbox">
              <input v-model="form.enabled" type="checkbox" />
              In use
            </label>
          </div>

          <div class="field">
            <label for="note">Note</label>
            <textarea id="note" v-model="form.note" rows="2" />
          </div>

          <div v-if="removingPassword" class="alert alert-info">
            The stored password will be removed when you save.
          </div>

          <div class="actions">
            <button class="btn btn-primary" type="submit" :disabled="saving">
              {{ saving ? 'Saving…' : editing ? 'Save' : 'Add registry' }}
            </button>
            <button
              v-if="editing"
              class="btn btn-danger"
              type="button"
              :disabled="removing"
              @click="remove"
            >
              {{ removing ? 'Deleting…' : 'Delete' }}
            </button>
          </div>
        </div>
      </form>
    </template>
  </div>
</template>

<style scoped>
.card {
  max-width: 720px;
}

.hint {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--text-muted);
}

.checkbox {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
  font-size: 13px;
  color: var(--text);
}

/* Inputs are as wide as their field everywhere else in this interface, which is right in a
   form and wrong for a checkbox: stretched across the card, the box stops being a box and
   the label reads as if it were the control. */
.checkbox input {
  width: auto;
  flex: 0 0 auto;
}

.actions {
  display: flex;
  gap: 8px;
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--border);
}

.btn-danger {
  color: #ffb4ae;
}
</style>
