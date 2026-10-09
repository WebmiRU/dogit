<script setup lang="ts">
/** SSH key management. */
import type { SSHKeySummary } from '~/types/dashboard'

const keys = ref<SSHKeySummary[]>([])
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const response = await api.get<{ keys: SSHKeySummary[] }>('/user/keys')
    keys.value = response.keys
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const form = reactive({ title: '', key: '' })
const adding = ref(false)
const addError = ref('')

async function addKey() {
  addError.value = ''
  adding.value = true
  try {
    await api.post('/user/keys', { title: form.title, key: form.key })
    form.title = ''
    form.key = ''
    await load()
  } catch (caught) {
    addError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    adding.value = false
  }
}

async function removeKey(key: SSHKeySummary) {
  if (!confirm(`Remove ${key.title || key.fingerprint}?`)) return
  try {
    await api.delete(`/user/keys/${key.id}`)
    keys.value = keys.value.filter((candidate) => candidate.id !== key.id)
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  }
}

/** Copies the public key command for platforms without a terminal. */
const sshDirHint = '~/.ssh/id_ed25519.pub'
</script>

<template>
  <div class="ssh-keys-page">
    <h1 class="page-title">SSH keys</h1>
    <p class="page-subtitle">
      These keys authenticate you for git over SSH. Add the public half only — the
      private key never leaves your machine.
    </p>

    <div class="card" style="margin-bottom: 20px">
      <div class="card-header"><strong>Add a key</strong></div>
      <div class="card-body">
        <div v-if="addError" class="alert alert-error">{{ addError }}</div>

        <form @submit.prevent="addKey">
          <div class="field">
            <label for="keyfile">Public key</label>
            <textarea
              id="keyfile"
              v-model="form.key"
              rows="4"
              :placeholder="`ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... you@laptop`"
              required
            />
            <p class="muted" style="margin: 6px 0 0; font-size: 12px">
              Generate one with <code>ssh-keygen -t ed25519 -C "you@laptop"</code> and paste
              the contents of <code>{{ sshDirHint }}</code>.
            </p>
          </div>
          <div class="field">
            <label for="keytitle">Title</label>
            <input id="keytitle" v-model="form.title" placeholder="laptop" />
          </div>
          <button class="btn btn-primary" type="submit" :disabled="adding">
            {{ adding ? 'Adding…' : 'Add key' }}
          </button>
        </form>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><strong>Registered keys</strong></div>

      <div v-if="loading" class="spinner">Loading keys…</div>
      <div v-else-if="loadError" class="alert alert-error" style="margin: 16px">{{ loadError }}</div>
      <div v-else-if="keys.length === 0" class="feed-empty">
        No keys yet. Without one you can still browse, but not push or clone.
      </div>
      <ul v-else class="tree-list">
        <li v-for="key in keys" :key="key.id">
          <span class="icon">🔑</span>
          <div class="name">
            <div>{{ key.title || 'untitled' }}</div>
            <div class="meta mono">{{ key.fingerprint }}</div>
          </div>
          <span class="meta">
            added {{ timeAgo(key.created_at) }}
            <template v-if="key.last_used_at"> · used {{ timeAgo(key.last_used_at) }}</template>
          </span>
          <button class="btn" type="button" @click="removeKey(key)">Remove</button>
        </li>
      </ul>
    </div>

    <section class="card" style="margin-top: 20px">
      <div class="card-header"><strong>Cloning with a key</strong></div>
      <div class="card-body">
        <p class="muted" style="margin-top: 0">
          After adding a key, point your SSH client at dogit and clone. In
          development the SSH port is not the standard one, so add a host entry
          first:
        </p>
        <pre class="code-sample">Host dogit
    HostName localhost
    Port 2222
    User git</pre>
        <pre class="code-sample">git clone git@dogit:group/project.git</pre>
      </div>
    </section>
  </div>
</template>

