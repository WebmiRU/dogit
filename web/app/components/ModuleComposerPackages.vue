<script setup lang="ts">
interface ComposerVersion {
  version: string
  archive_available: boolean
  archive_cached: boolean
  archive_size_bytes: number
  archive_cached_at?: string
}

interface ComposerPackage {
  name: string
  version_count: number
  last_fetched_at: string
  versions: ComposerVersion[]
}

interface ComposerCatalog {
  packages: ComposerPackage[]
  total_packages: number
  total_versions: number
  cached_archives: number
  cached_archive_bytes: number
}

const props = defineProps<{ address: string }>()
const packages = ref<ComposerPackage[]>([])
const summary = ref({ packages: 0, versions: 0, archives: 0, bytes: 0 })
const loading = ref(true)
const error = ref('')

function endpoint(): string {
  return props.address.trim().replace(/\/+$/, '')
}

async function load() {
  if (!endpoint()) {
    error.value = 'The module has no published address. Set Public address in Settings.'
    loading.value = false
    return
  }

  loading.value = true
  error.value = ''
  try {
    const response = await fetch(endpoint() + '/catalog', { headers: { Accept: 'application/json' } })
    if (!response.ok) {
      throw new Error('The module answered HTTP ' + response.status)
    }
    const data = await response.json() as ComposerCatalog
    packages.value = data.packages ?? []
    summary.value = {
      packages: data.total_packages ?? 0,
      versions: data.total_versions ?? 0,
      archives: data.cached_archives ?? 0,
      bytes: data.cached_archive_bytes ?? 0,
    }
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'Could not read the Composer catalog.'
  } finally {
    loading.value = false
  }
}

function formatBytes(value: number): string {
  if (!value) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let amount = value
  let unit = 0
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024
    unit++
  }
  return (unit === 0 ? String(amount) : amount.toFixed(1)) + ' ' + units[unit]
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

onMounted(() => void load())
watch(() => props.address, () => void load())
</script>

<template>
  <section>
    <div class="repo-head">
      <div class="title">
        <h2 class="page-title">Composer packages</h2>
        <p class="page-subtitle">Cached metadata and ZIP archives served by this module.</p>
      </div>
      <button class="btn" type="button" :disabled="loading" @click="load">Refresh</button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading && packages.length === 0" class="spinner">Reading Composer catalog…</div>

    <template v-else>
      <div class="images-summary">
        <span>{{ summary.packages }} packages</span>
        <span>{{ summary.versions }} versions</span>
        <span>{{ summary.archives }} cached ZIPs</span>
        <strong>{{ formatBytes(summary.bytes) }}</strong>
      </div>

      <div v-if="packages.length === 0 && !error" class="card empty">
        No package metadata has been requested yet. The first Composer install will populate this catalog.
      </div>

      <table v-else-if="packages.length" class="admin-table">
        <thead>
          <tr>
            <th>Package</th>
            <th>Version</th>
            <th>Archive</th>
            <th class="numeric">Size</th>
            <th>Metadata fetched</th>
            <th>ZIP cached</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="pkg in packages" :key="pkg.name">
            <tr class="composer-package-heading">
              <td class="mono" colspan="6">
                <strong>{{ pkg.name }}</strong>
                <span class="muted small">· {{ pkg.version_count }} versions</span>
              </td>
            </tr>
            <tr v-for="version in pkg.versions" :key="pkg.name + ':' + version.version">
              <td class="muted small">↳</td>
              <td class="mono">{{ version.version }}</td>
              <td>
                <span v-if="!version.archive_available" class="badge">source only</span>
                <span v-else-if="version.archive_cached" class="badge badge-green">cached</span>
                <span v-else class="badge">not cached</span>
              </td>
              <td class="numeric mono small">{{ version.archive_cached ? formatBytes(version.archive_size_bytes) : '—' }}</td>
              <td class="muted small">{{ formatDate(pkg.last_fetched_at) }}</td>
              <td class="muted small">{{ version.archive_cached_at ? formatDate(version.archive_cached_at) : '—' }}</td>
            </tr>
          </template>
        </tbody>
      </table>
    </template>
  </section>
</template>
