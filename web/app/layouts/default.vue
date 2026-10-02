<script setup lang="ts">
/**
 * Application shell: a top header with the admin entry point and the user menu,
 * and a left sidebar with the main sections.
 *
 * Sections that have no backend yet are marked instead of hidden, so the shape of
 * the product is visible while it is being built.
 */
const { user, ensureLoaded, logout } = useAuth()
const route = useRoute()

await ensureLoaded()

// Unauthenticated visitors are sent to the login page, remembering where they
// wanted to go.
watchEffect(() => {
  if (route.path === '/login' || user.value) return
  navigateTo({ path: '/login', query: { redirect: route.fullPath } })
})

const userMenuOpen = ref(false)
const adminMenuOpen = ref(false)

// Any navigation closes the popovers; leaving them open would cover the page the
// user just asked for.
watch(() => route.fullPath, () => {
  userMenuOpen.value = false
  adminMenuOpen.value = false
})

onMounted(() => {
  const close = (event: MouseEvent) => {
    const target = event.target as HTMLElement | null
    if (target?.closest('.menu-anchor')) return
    userMenuOpen.value = false
    adminMenuOpen.value = false
  }
  document.addEventListener('click', close)
  onBeforeUnmount(() => document.removeEventListener('click', close))
})

interface NavItem {
  label: string
  to: string
  icon: string
  /** Prefix used to highlight the entry, so project pages keep Projects lit. */
  match?: string
  disabled?: boolean
}

const navItems = computed<NavItem[]>(() => [
  { label: 'Dashboard', to: '/', icon: '◧', match: '/' },
  { label: 'Projects', to: '/projects', icon: '▤', match: '/projects' },
  { label: 'Groups', to: '/groups', icon: '◫', match: '/groups' },
  { label: 'Merge requests', to: '/merge-requests', icon: '⑂', disabled: true },
  { label: 'CI/CD', to: '/pipelines', icon: '▷', disabled: true },
  { label: 'Activity', to: '/activity', icon: '≡', match: '/activity' },
])

function isActive(item: NavItem): boolean {
  if (item.match === '/') return route.path === '/'
  if (item.match) return route.path === item.match || route.path.startsWith(`${item.match}/`)
  return route.path.startsWith(item.to)
}

/** Project pages keep the repository tabs; the sidebar only highlights sections. */
const onRepository = computed(() => route.path.startsWith('/p/'))
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <NuxtLink to="/" class="brand">
        <span class="brand-mark">◆</span>
        dogit
      </NuxtLink>

      <div class="spacer" />

      <div v-if="user" class="menu-anchor">
        <button
          class="btn"
          type="button"
          @click="adminMenuOpen = !adminMenuOpen; userMenuOpen = false"
        >
          Admin
        </button>
        <div v-if="adminMenuOpen" class="menu">
          <NuxtLink to="/admin" class="menu-item">Overview</NuxtLink>
          <NuxtLink to="/admin/modules" class="menu-item">Modules</NuxtLink>
          <NuxtLink to="/admin/users" class="menu-item menu-item-disabled">Users</NuxtLink>
          <NuxtLink to="/admin/runners" class="menu-item menu-item-disabled">Runners</NuxtLink>
          <NuxtLink to="/admin/settings" class="menu-item menu-item-disabled">Settings</NuxtLink>
        </div>
      </div>

      <div v-if="user" class="menu-anchor">
        <button
          class="avatar-button"
          type="button"
          :aria-expanded="userMenuOpen"
          @click="userMenuOpen = !userMenuOpen; adminMenuOpen = false"
        >
          <UserAvatar :name="user.username" :size="30" />
        </button>
        <div v-if="userMenuOpen" class="menu menu-right">
          <div class="menu-header">
            <UserAvatar :name="user.username" :size="32" />
            <div>
              <div class="menu-title">{{ user.name || user.username }}</div>
              <div class="muted">{{ user.username }}</div>
            </div>
          </div>
          <NuxtLink to="/settings/profile" class="menu-item">Profile</NuxtLink>
          <NuxtLink to="/settings/ssh-keys" class="menu-item">SSH keys</NuxtLink>
          <button class="menu-item" type="button" @click="logout">Sign out</button>
        </div>
      </div>

      <NuxtLink v-else to="/login" class="btn">Sign in</NuxtLink>
    </header>

    <div class="body">
      <aside v-if="user" class="sidebar">
        <nav>
          <template v-for="item in navItems" :key="item.label">
            <NuxtLink
              v-if="!item.disabled"
              :to="item.to"
              class="nav-item"
              :class="{ active: isActive(item) && !(item.match === '/projects' && onRepository) }"
            >
              <span class="nav-icon">{{ item.icon }}</span>
              {{ item.label }}
            </NuxtLink>
            <span v-else class="nav-item nav-item-disabled" :title="`${item.label} is not available yet`">
              <span class="nav-icon">{{ item.icon }}</span>
              {{ item.label }}
              <span class="badge badge-soon">soon</span>
            </span>
          </template>
        </nav>

        <div class="sidebar-footer">
          <div class="muted">dogit</div>
          <div class="muted" style="font-size: 11px">early development</div>
        </div>
      </aside>

      <main class="page" :class="{ 'page-wide': onRepository }">
        <slot />
      </main>
    </div>
  </div>
</template>
