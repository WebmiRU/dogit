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

// Any navigation closes the popovers; leaving them open would cover the page the
// user just asked for.
watch(() => route.fullPath, () => {
  userMenuOpen.value = false
})

onMounted(() => {
  const close = (event: MouseEvent) => {
    const target = event.target as HTMLElement | null
    if (target?.closest('.menu-anchor')) return
    userMenuOpen.value = false
  }
  document.addEventListener('click', close)
  onBeforeUnmount(() => document.removeEventListener('click', close))
})

interface NavChild {
  label: string
  /** Absent while this kind has no backend, which is what marks it rather than hides it. */
  to?: string
  /** Why it leads nowhere yet, when it does. */
  note?: string
}

interface NavItem {
  label: string
  to: string
  icon: string
  /** Prefix used to highlight the entry, so project pages keep Projects lit. */
  match?: string
  /** Pages this one is a doorway to, listed under it rather than beside it. */
  parts?: NavChild[]
}

interface NavGroup {
  key: string
  label: string
  icon: string
  items: NavItem[]
}

/**
 * The menu, in groups.
 *
 * Grouped because six destinations in one column is a list, and a list does not say which
 * of them are the places you work and which are the places you occasionally visit. The
 * groups are the same kind of thing the project rail draws — a name, a rule above it, and
 * the entries under it — so the two menus are visibly the same object.
 */
const navGroups = computed<NavGroup[]>(() => [
  {
    key: 'overview',
    label: 'Overview',
    icon: 'overview',
    items: [
      { label: 'Dashboard', to: '/', icon: 'dashboard', match: '/' },
      { label: 'Activity', to: '/activity', icon: 'activity', match: '/activity' },
    ],
  },
  {
    key: 'workspace',
    label: 'Workspace',
    icon: 'workspace',
    items: [
      { label: 'Projects', to: '/projects', icon: 'projects', match: '/projects' },
      { label: 'Groups', to: '/groups', icon: 'groups', match: '/groups' },
      { label: 'Merge requests', to: '/merge-requests', icon: 'merge', match: '/merge-requests' },
    ],
  },
  {
    // The group is the section and its entries are the pages. It carried an entry of its
    // own as well once, which meant the menu said "Registries" and then, underneath, said
    // "Registries" again — two rows for one place, and no way to tell which one you were
    // on. The caption already names the section; the entries are what it contains.
    key: 'store',
    label: 'Registries',
    icon: 'store',
    // Only what this instance can answer. A menu that lists a kind of registry this build
    // cannot hold says the feature is planned, which is a plan rather than a fact — and it
    // says it in the same place the reader looks to find out what is here.
    items: [
      { label: 'Docker', to: '/registry/docker', icon: 'registry', match: '/registry' },
    ],
  },
  {
    // Administration was a dropdown in the top bar, behind a button that said "Admin" and
    // hid the fact that it held four pages. In the menu it is a group like any other: its
    // entries are listed, they are the same entries, and a reader can see what there is
    // before deciding to click anything. Shown to everybody and answered by the pages
    // themselves for those who are not administrators: the section says what the instance
    // has, and a reader who clicks a part is told it needs administrator rights rather
    // than finding a page that quietly renders nothing.
    key: 'admin',
    label: 'Administration',
    icon: 'admin',
    items: [
      { label: 'Overview', to: '/admin', icon: 'admin-overview', match: '/admin' },
      { label: 'Modules', to: '/admin/modules', icon: 'modules', match: '/admin/modules' },
      { label: 'Users', to: '/admin/users', icon: 'users', match: '/admin/users' },
      { label: 'Runners', to: '/admin/runners', icon: 'runners', match: '/admin/runners' },
    ],
  },
])

function isActive(item: NavItem): boolean {
  if (item.match === '/') return route.path === '/'
  if (item.match) return route.path === item.match || route.path.startsWith(`${item.match}/`)
  return route.path.startsWith(item.to)
}

/**
 * Whether one part of a section is the page being looked at.
 *
 * Exactly, for the part that is the section's own address — that is Overview, and
 * "/admin" is a prefix of "/admin/modules", so a prefix rule would light up Overview on
 * every administration page and leave two entries lit at once. Everything below it matches
 * by prefix, because that is what a module's own page needs: "/admin/modules" has to stay
 * lit while its settings tab is open.
 */
function partIsActive(item: NavItem, part: NavChild): boolean {
  if (!part.to) return false
  if (route.path === part.to) return true
  if (part.to === item.to) return false
  return route.path.startsWith(`${part.to}/`)
}

/** Project pages use their own section navigation; the global sidebar highlights Projects. */
const onRepository = computed(() => route.path.startsWith('/p/'))
</script>

<template>
  <div>
    <!-- Notifications are mounted outside the shell so a notice survives the
         navigation that caused it. -->
    <NotifyPool />

    <div class="app-shell">
    <header class="topbar">
      <NuxtLink to="/" class="brand">
        <span class="brand-mark">◆</span>
        dogit
      </NuxtLink>

      <div class="spacer" />

      <div v-if="user" class="menu-anchor">
        <button
          class="avatar-button"
          type="button"
          :aria-expanded="userMenuOpen"
          @click="userMenuOpen = !userMenuOpen"
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
      <aside v-if="user && !onRepository" class="sidebar">
        <nav class="app-nav-rail" aria-label="Main sections">
          <section
            v-for="group in navGroups"
            :key="group.key"
            class="app-nav-group"
          >
            <h2 class="app-nav-group-title">
              <AppIcon :name="group.icon" />
              <span>{{ group.label }}</span>
            </h2>
            <div class="app-nav-links">
              <template v-for="item in group.items" :key="item.label">
                <NuxtLink
                  :to="item.to"
                  class="app-nav-item"
                  :class="{ 'is-active': isActive(item) }"
                >
                  <span class="app-nav-item-icon">
                    <AppIcon :name="item.icon" />
                  </span>
                  <span class="app-nav-item-label">{{ item.label }}</span>
                </NuxtLink>
                <template v-for="part in item.parts ?? []" :key="part.label">
                  <NuxtLink
                    v-if="part.to"
                    :to="part.to"
                    class="app-nav-child"
                    :class="{ 'is-active': partIsActive(item, part) }"
                  >
                    {{ part.label }}
                  </NuxtLink>
                  <!-- Named rather than hidden, like every other part of this product
                       without a backend yet: a part that is merely absent is a part nobody
                       knows is coming. -->
                  <span v-else class="app-nav-child is-planned" :title="part.note">
                    {{ part.label }}
                    <em>{{ part.note }}</em>
                  </span>
                </template>
              </template>
            </div>
          </section>
        </nav>

        <div class="sidebar-footer">
          <div class="muted">dogit</div>
          <div class="muted" style="font-size: 11px">early development</div>
        </div>
      </aside>

      <main class="page" :class="{ 'page-project-layout': onRepository }">
        <div class="page-content" :class="{ 'project-page-content': onRepository }">
          <slot />
        </div>
      </main>
      </div>

      <footer class="footer">
        <strong>dogit</strong>
        <span class="dot">·</span>
        <span>© 2026</span>
        <span class="dot">·</span>
        <span>self-hosted, all rights reserved</span>
      </footer>
  </div>
  </div>
</template>
