<script setup lang="ts">
/** Application shell: header, navigation and the auth guard. */
const { user, ensureLoaded, logout } = useAuth()
const route = useRoute()

// The session is resolved once on first navigation; every page then reads it
// from the shared composable state.
await ensureLoaded()

// Unauthenticated visitors are sent to the login page, remembering where they
// wanted to go.
watchEffect(() => {
  if (route.path === '/login' || user.value) return
  navigateTo({ path: '/login', query: { redirect: route.fullPath } })
})
</script>

<template>
  <div v-if="route.path === '/login'" class="app-shell">
    <slot />
  </div>
  <div v-else class="app-shell">
    <header class="topbar">
      <NuxtLink to="/" class="brand">dogit</NuxtLink>
      <nav>
        <NuxtLink to="/">Projects</NuxtLink>
      </nav>
      <div class="spacer" />
      <div v-if="user" class="user">
        <span>{{ user.username }}</span>
        <button class="btn" type="button" @click="logout">Sign out</button>
      </div>
      <NuxtLink v-else to="/login" class="btn">Sign in</NuxtLink>
    </header>
    <main class="page">
      <slot />
    </main>
  </div>
</template>
