<script setup lang="ts">
/**
 * One Docker registry, edited.
 *
 * The record is named in the page's own title rather than in the address bar: a list of ten
 * registries is ten tabs called "dogit", which is a browser history nobody can read.
 */
const route = useRoute()

useHead({ title: 'Registry' })
</script>

<template>
  <RegistryDockerForm :registry-id="String(route.params.id)" />
</template>
