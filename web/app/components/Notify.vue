<script setup lang="ts">
/**
 * One notification.
 *
 * The state lives in useNotifyPool; this draws it. The progress bar is the
 * timer: it fills up as the notice runs out, so how long is left is something
 * that can be seen rather than guessed at.
 */
const props = withDefaults(
  defineProps<{
    id: number
    message: string
    type?: 'success' | 'error' | 'warning' | 'info'
    timer?: number
    links?: { label: string; to: string }[]
    more?: string
  }>(),
  { type: 'success', timer: 5, links: () => [], more: '' },
)

const emit = defineEmits<{ (event: 'close', id: number): void }>()

const visible = ref(true)

// A notice without text is dropped rather than drawn: an empty panel is worse
// than none, and something left in the pool by an earlier shape must not take
// the page down with it.
const messageLines = computed(() =>
  (props.message ?? '').split('\n').filter((line) => line !== ''),
)

function close() {
  visible.value = false
  // The delay matches the leave transition, so the notice is removed once it has
  // finished sliding out rather than mid-animation.
  setTimeout(() => emit('close', props.id), 300)
}

onMounted(() => {
  if (props.timer > 0) setTimeout(close, props.timer * 1000)
})
</script>

<template>
  <Transition name="notify">
    <div
      v-if="visible && messageLines.length"
      class="notification"
      :class="`notification--${type}`"
    >
      <button class="delete" type="button" aria-label="Close" @click="close" />

      <span v-for="(line, index) in messageLines" :key="index">{{ line }}</span>

      <!-- Links are how a notice stays usable: "what now" is a link, not a
           sentence to be remembered. -->
      <NuxtLink
        v-for="(link, index) in links"
        :key="`l${index}`"
        :to="link.to"
        class="notification__link"
        @click="close"
      >
        {{ link.label }}
      </NuxtLink>

      <span v-if="more" class="notification__more">{{ more }}</span>

      <div v-if="timer" class="progress" :style="{ animationDuration: timer + 's' }" />
    </div>
  </Transition>
</template>

<style scoped>
.notify-enter-active,
.notify-leave-active {
  transition: all 0.3s ease;
}

.notify-enter-from {
  opacity: 0;
  transform: translateY(-10px);
}

.notify-leave-to {
  opacity: 0;
  transform: translateY(-10px);
}
</style>