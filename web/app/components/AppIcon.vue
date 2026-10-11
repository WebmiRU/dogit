<script setup lang="ts">
/**
 * One drawn mark, named rather than drawn at every place it is used.
 *
 * Every icon is a 16×16 outline stroked at the same weight, because a menu whose marks
 * were cut at different weights reads as a menu assembled out of whatever was to hand.
 * Naming them here rather than inlining each one at each use is also what lets a mark be
 * changed once: there were two copies of the same list of icons in this layout, and they
 * had already started to differ.
 *
 * The unicode characters this replaced — ◧ ▤ ◫ ⑂ ⬓ — were not drawn at all. They were
 * whatever glyph the machine's font happened to carry, so the same row read as "dashboard"
 * on one computer and as a box or a digit on the next.
 */
const props = defineProps<{ name: string }>()

const PATHS: Record<string, string> = {
  // Groups
  overview: 'M8 1.8a6.2 6.2 0 1 0 0 12.4A6.2 6.2 0 0 0 8 1.8zM10.6 5.4 7.1 8l-.9 2.9 3.5-2.5z',
  workspace: 'M8 1.6 14.6 5 8 8.4 1.4 5zM1.4 8 8 11.4l6.6-3.4M1.4 11 8 14.4 14.6 11',
  store: 'M2 6.2 8 2.6l6 3.6v7.6L8 14.6l-6-3.6zM2 6.2l6 2.6 6-2.6M8 8.8v5.8',
  admin: 'M6 1.6h4l.6 1.9 1.6.9 1.9-.5 1.1 1.9-1.4 1.5v1.7l1.4 1.5-1.1 1.9-1.9-.5-1.6.9-.6 1.9H6l-.6-1.9-1.6-.9-1.9.5L.8 10.5l1.4-1.5V7.3L.8 5.8l1.1-1.9 1.9.5L5.4 3.5z',

  // Entries. Each of these is a different mark: they shared one for a while, and a menu
  // in which three rows carry the same picture is a menu whose picture says nothing.
  dashboard: 'M2 11.6a6 6 0 1 1 12 0zM8 11.6l3.2-4',
  'admin-overview': 'M8 1.8a6.2 6.2 0 1 0 0 12.4A6.2 6.2 0 0 0 8 1.8zM8 7v4.2M8 4.6v.6',
  activity: 'M8 2.4a5.6 5.6 0 1 0 0 11.2A5.6 5.6 0 0 0 8 2.4zM8 4.4v3.8l2.6 1.5M2.4 4.4v-2M2.4 4.4h2',
  projects: 'M2 4.5h12v9H2zM2 4.5 8 1.5l6 3M8 1.5v12',
  groups: 'M5.6 2.9a2.3 2.3 0 1 0 0 4.6 2.3 2.3 0 0 0 0-4.6zM11.6 4.1a1.7 1.7 0 1 0 0 3.4 1.7 1.7 0 0 0 0-3.4zM1.6 13.4c0-2.4 1.8-3.6 4-3.6s4 1.2 4 3.6M10.4 9.9c2 .1 4 .8 4 3.5',
  merge: 'M4 1.9a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM4 11.1a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM12 6.5a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM4 4.9v6.2M4 5.2c0 1.9 1.8 2.9 4 2.9h2.5',
  registry: 'M4.3 3.3h2.4v2.4H4.3zM7 3.3h2.4v2.4H7zM9.7 3.3h2.4v2.4H9.7zM1.6 6h2.4v2.4H1.6zM4.3 6h2.4v2.4H4.3zM7 6h2.4v2.4H7zM9.7 6h2.4v2.4H9.7zM1.3 9.1h10.4c.8 0 1.4.5 1.4 1.2 0 2-2 3.4-4.6 3.4H5.3c-2.4 0-4-1.4-4-3.4 0-.7.4-1.2 1-1.2zM13.4 9.6c.8-1.1 1.2-2.5 1-3.9l1.8.8c.3 1.7-.2 3.3-1.2 4.5',
  modules: 'M2.2 2.4h4.8v4.8H2.2zM9 2.4h4.8v4.8H9zM2.2 8.8h4.8v4.8H2.2zM9 8.8h4.8v4.8H9z',
  users: 'M8 2.4a2.6 2.6 0 1 0 0 5.2 2.6 2.6 0 0 0 0-5.2zM2.8 14c0-2.9 2.3-4.4 5.2-4.4s5.2 1.5 5.2 4.4',
  runners: 'M4.4 5.8a2.6 2.6 0 1 0 0 5.2 2.6 2.6 0 0 0 0-5.2zM7 8.4h6.4M11.4 6.2l2.2 2.2-2.2 2.2',
}
</script>

<template>
  <svg viewBox="0 0 16 16" aria-hidden="true">
    <path :d="PATHS[props.name] ?? PATHS.dashboard" />
  </svg>
</template>
