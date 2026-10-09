<script setup lang="ts">
/**
 * Where the notifications stack.
 *
 * Bottom right, growing upwards and scrolling once it would otherwise run off
 * the top: a batch of results has to stay reachable without pushing everything
 * else off the screen.
 */
const { items, remove } = useNotifyPool()
</script>

<template>
  <div v-if="items.length" class="notify-pool">
    <Notify
      v-for="item in items"
      :id="item.id"
      :key="item.id"
      :message="item.message"
      :type="item.type"
      :timer="item.timer"
      :links="item.links"
      :more="item.more"
      @close="remove"
    />
  </div>
</template>

<style scoped>
.notify-pool {
  position: fixed;
  right: 20px;
  bottom: 20px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
  max-width: 400px;
  max-height: 80vh;
  overflow-y: auto;
  z-index: 999;
}
</style>