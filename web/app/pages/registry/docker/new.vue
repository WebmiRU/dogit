<script setup lang="ts">
/**
 * A new Docker registry, written down.
 *
 * The form does the work and the page says where it is; there is nothing else here, and
 * that is the point of a section with a list and an edit form — a new page is a page whose
 * behaviour can differ from the edit one, and here it differs in exactly one way: nothing
 * is behind it yet.
 */
useHead({ title: 'New registry' })
</script>

<template>
  <RegistryDockerForm />
</template>
