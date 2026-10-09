<script setup lang="ts">
/**
 * Registries.
 *
 * The section itself has no page: each kind is its own list, and there is nothing a reader
 * would want here that is not on one of them. Docker is the only kind with a backend, so
 * this is where an address lands.
 *
 * The kinds are sub-items of the sidebar rather than tabs at the top of a page, because a
 * tab that is not there yet has nowhere to be greyed: a kind without a backend should be
 * named where the other kinds are named, so its absence is a fact about the product and
 * not about this page.
 */
await navigateTo('/registry/docker', { replace: true })
</script>

<template>
  <div />
</template>
