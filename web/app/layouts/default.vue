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
  /**
   * A section made of parts rather than one page: the label becomes a heading and the parts
   * are always shown under it. Not a link and not a disclosure — a section's parts are
   * short enough to read at a glance, and hiding them behind a click hides the shape of the
   * product from the one place that shows it.
   */
  parts?: NavChild[]
}

const navItems = computed<NavItem[]>(() => [
  { label: 'Dashboard', to: '/', icon: '◧', match: '/' },
  { label: 'Projects', to: '/projects', icon: '▤', match: '/projects' },
  { label: 'Groups', to: '/groups', icon: '◫', match: '/groups' },
  { label: 'Merge requests', to: '/merge-requests', icon: '⑂', match: '/merge-requests' },
  { label: 'Activity', to: '/activity', icon: '≡', match: '/activity' },
  {
    label: 'Registries',
    to: '/registry',
    icon: '⬓',
    match: '/registry',
    parts: [
      { label: 'Docker', to: '/registry/docker' },
      { label: 'PHP (Composer)', note: 'not implemented yet' },
      { label: 'NPM (Node.js)', note: 'not implemented yet' },
    ],
  },
  {
    // Administration was a dropdown in the top bar, behind a button that said "Admin" and
    // hid the fact that it held five pages. In the sidebar it is a section like any other:
    // its parts are listed, they are the same parts, and a reader can see what there is
    // before deciding to click anything.
    label: 'Administration',
    to: '/admin',
    icon: '⚙',
    match: '/admin',
    // Shown to everybody in the sidebar and answered by the pages themselves for those who
    // are not administrators: the section says what the instance has, and a reader who
    // clicks a part is told it needs administrator rights rather than finding a page that
    // quietly renders nothing.
    parts: [
      { label: 'Overview', to: '/admin' },
      { label: 'Modules', to: '/admin/modules' },
      { label: 'Users', to: '/admin/users' },
      { label: 'Runners', to: '/admin/runners' },
      { label: 'Settings', note: 'not implemented yet' },
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

/** Project pages keep the repository tabs; the sidebar only highlights sections. */
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
      <aside v-if="user" class="sidebar">
        <!-- Every item here leads somewhere. A link that does not work yet was
             promising something the instance cannot do, and the promise was the only
             thing about it: pipelines are reached from the project they belong to,
             which is also where a person looking for one actually is. -->
        <nav>
          <template v-for="item in navItems" :key="item.label">
            <!-- A section made of parts is a heading with the parts under it. The heading
                 is not a link: the section has no page of its own — each part is its own
                 list — so a link here would be a second way to the first part, and two
                 entries for one place leave a reader unable to tell which one they are on. -->
            <div v-if="item.parts" class="nav-group">
              <div class="nav-heading" :class="{ active: isActive(item) }">
                <span class="nav-icon">{{ item.icon }}</span>
                {{ item.label }}
              </div>
              <div class="nav-children">
                <template v-for="part in item.parts" :key="part.label">
                  <NuxtLink
                    v-if="part.to"
                    :to="part.to"
                    class="nav-child"
                    :class="{ active: partIsActive(item, part) }"
                  >
                    {{ part.label }}
                  </NuxtLink>
                  <!-- Named rather than hidden, like every other part of this product
                       without a backend yet: a part that is merely absent is a part nobody
                       knows is coming. -->
                  <span v-else class="nav-child nav-child-planned" :title="part.note">
                    {{ part.label }}
                    <em>{{ part.note }}</em>
                  </span>
                </template>
              </div>
            </div>

            <NuxtLink
              v-else
              :to="item.to"
              class="nav-item"
              :class="{ active: isActive(item) && !(item.match === '/projects' && onRepository) }"
            >
              <span class="nav-icon">{{ item.icon }}</span>
              {{ item.label }}
            </NuxtLink>
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
