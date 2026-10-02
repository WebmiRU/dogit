<script setup lang="ts">
/** User avatar: initials on a colour derived from the username. */
const props = withDefaults(
  defineProps<{
    name: string
    size?: number
  }>(),
  { size: 28 },
)

const initials = computed(() => {
  const parts = props.name.trim().split(/[\s._-]+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return (parts[0] ?? '').slice(0, 2).toUpperCase()

  const [first, second] = parts
  return `${first?.[0] ?? ''}${second?.[0] ?? ''}`.toUpperCase()
})

// A stable per-user colour: the same person always gets the same avatar, and
// two different users rarely collide, which makes the sidebar readable.
const hue = computed(() => {
  let hash = 0
  for (const char of props.name) {
    hash = (hash * 31 + char.charCodeAt(0)) % 360
  }
  return hash
})

const style = computed(() => ({
  width: `${props.size}px`,
  height: `${props.size}px`,
  fontSize: `${Math.round(props.size * 0.42)}px`,
  background: `hsl(${hue.value} 55% 32%)`,
  color: `hsl(${hue.value} 80% 88%)`,
}))
</script>

<template>
  <span class="avatar" :style="style" :title="name" aria-hidden="true">{{ initials }}</span>
</template>

<style scoped>
.avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  font-weight: 600;
  letter-spacing: 0.3px;
  flex: 0 0 auto;
  user-select: none;
}
</style>
