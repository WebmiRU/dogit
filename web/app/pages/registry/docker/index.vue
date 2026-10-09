<script setup lang="ts">
/**
 * The Docker registries on this instance.
 *
 * Ten to a page. The list is read rather than searched, so it is short enough to read and
 * long enough to need a pager, and the pager says which ten of how many rather than only
 * which page — a list that says "page 3" and stops tells a reader nothing about whether
 * there is a page 4.
 */
useHead({ title: 'Docker registries' })
</script>

<template>
  <RegistryDockerList />
</template>
