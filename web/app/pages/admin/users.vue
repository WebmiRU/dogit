<script setup lang="ts">
/**
 * The people on this instance.
 *
 * Administrators only, and the page says so rather than hiding: a list of accounts
 * is a list of who to ask about what, which is not every signed-in user's business.
 *
 * What is deliberately absent is anything derived from a password. This is a page
 * people copy from when they need to reach somebody, and it should carry nothing
 * that could be used against them.
 */

interface AdminUser {
  id: string
  username: string
  name: string
  email: string
  is_admin: boolean
  created_at: string
  last_sign_in?: string
  /** How many projects this account can reach. */
  projects?: number
  ssh_keys?: number
}

const { user } = useAuth()

const users = ref<AdminUser[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const filter = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const query = filter.value.trim()
    const answer = await api.get<{ users: AdminUser[]; total: number }>(
      `/admin/users${query ? `?q=${encodeURIComponent(query)}` : ''}`,
    )
    users.value = answer.users
    total.value = answer.total
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  if (user.value?.is_admin) void load()
})

const admins = computed(() => users.value.filter((one) => one.is_admin).length)
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title" style="margin: 0">Users</h1>
        <p class="page-subtitle">
          Everyone with an account on this instance, and what they can reach.
        </p>
      </div>
    </div>

    <div v-if="!user?.is_admin" class="alert alert-info">
      This page requires administrator rights.
    </div>

    <template v-else>
      <div v-if="error" class="alert alert-error">{{ error }}</div>

      <div class="toolbar">
        <input
          v-model="filter"
          class="filter"
          type="search"
          placeholder="Filter by name or username"
          aria-label="Filter users"
          @keyup.enter="load"
        />
        <button class="btn" type="button" @click="load">Search</button>
      </div>

      <div class="summary">
        <span>{{ total }} {{ total === 1 ? 'account' : 'accounts' }}</span>
        <span>{{ admins }} {{ admins === 1 ? 'administrator' : 'administrators' }}</span>
      </div>

      <div v-if="loading" class="spinner">Loading users…</div>

      <div v-else-if="users.length === 0" class="card empty">Nobody matches that.</div>

      <table v-else class="table">
        <thead>
          <tr>
            <th>User</th>
            <th>Email</th>
            <th class="numeric">Projects</th>
            <th class="numeric">Keys</th>
            <th>Joined</th>
            <th>Last seen</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="one in users" :key="one.id">
            <td>
              <div class="who">
                <UserAvatar :name="one.name || one.username" :size="28" />
                <div>
                  <div class="who-name">
                    {{ one.name || one.username }}
                    <span v-if="one.is_admin" class="badge badge-private">admin</span>
                  </div>
                  <div class="muted small mono">{{ one.username }}</div>
                </div>
              </div>
            </td>
            <td class="muted">{{ one.email }}</td>
            <td class="numeric mono">{{ one.projects ?? '—' }}</td>
            <td class="numeric mono">{{ one.ssh_keys ?? '—' }}</td>
            <td class="muted small">{{ timeAgo(one.created_at) }}</td>
            <td class="muted small">
              {{ one.last_sign_in ? timeAgo(one.last_sign_in) : 'never' }}
            </td>
          </tr>
        </tbody>
      </table>
    </template>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}

.filter {
  flex: 1;
  max-width: 420px;
  padding: 7px 10px;
  font: inherit;
  font-size: 13px;
  color: inherit;
  background: var(--bg, transparent);
  border: 1px solid var(--border);
  border-radius: 6px;
}

.summary {
  display: flex;
  gap: 16px;
  padding: 10px 12px;
  margin-bottom: 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 13px;
  color: var(--text-muted);
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.table th {
  text-align: left;
  font-weight: 600;
  font-size: 12px;
  color: var(--text-muted);
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
}

.table td {
  padding: 9px 12px;
  border-bottom: 1px solid var(--border);
}

.table tbody tr:hover td {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.03));
}

.who {
  display: flex;
  align-items: center;
  gap: 10px;
}

.who-name {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 500;
}

.numeric {
  text-align: right;
}

.small {
  font-size: 12px;
}
</style>