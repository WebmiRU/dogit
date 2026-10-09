<script setup lang="ts">
/** Administrator overview: instance counters and the state of unfinished features. */
import type { AdminOverview } from '~/types/dashboard'

const { user } = useAuth()

const overview = ref<AdminOverview | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    overview.value = await api.get<AdminOverview>('/admin/overview')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const counts = computed(() => {
  if (!overview.value) return []
  return Object.entries(overview.value.counts).map(([label, value]) => ({
    label: (label[0] ?? '').toUpperCase() + label.slice(1),
    value,
  }))
})

const features = computed(() => Object.entries(overview.value?.features ?? {}) as [string, boolean][])

</script>

<template>
  <div>
    <h1 class="page-title">Admin area</h1>
    <p class="page-subtitle">
      Instance-wide counters. Sections that have no backend yet are listed as
      unavailable instead of offering buttons that would fail.
    </p>

    <div v-if="loading" class="spinner">Loading…</div>
    <div v-else-if="error" class="alert alert-error">{{ error }}</div>
    <div v-else-if="!user?.is_admin" class="alert alert-info">
      This area requires administrator rights.
    </div>

    <template v-else>
      <div class="stat-grid">
        <div v-for="tile in counts" :key="tile.label" class="stat-tile">
          <div class="value">{{ tile.value }}</div>
          <div class="label">{{ tile.label }}</div>
        </div>
      </div>

      <section class="card">
        <div class="card-header">
          <strong>Feature status</strong>
          <NuxtLink to="/admin/modules" class="muted" style="margin-left: auto; font-size: 12px">
            manage modules
          </NuxtLink>
        </div>
        <ul class="tree-list">
          <li v-for="[name, available] in features" :key="name">
            <span class="icon">{{ available ? '✓' : '·' }}</span>
            <span class="name">{{ name }}</span>
            <span class="badge" :class="available ? 'badge-green' : ''">
              {{ available ? 'available' : 'not implemented' }}
            </span>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.admin-nav {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
}

.admin-nav a {
  text-decoration: none;
}
</style>
