<script setup lang="ts">
/** Branch and tag switcher. */
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  refs: RefsResponse | null
  projectPath: string
  refName: string
}>()

const selected = computed({
  get: () => props.refName,
  set: (value: string) => {
    // Changing the ref navigates to the same view on the new branch, which keeps
    // the current path when possible.
    const current = useRoute()
    const target = `/p/${props.projectPath}/-/tree/${encodeURIComponent(value)}`
    navigateTo(target)
    void current
  },
})

const branches = computed(() => props.refs?.branches ?? [])
const tags = computed(() => props.refs?.tags ?? [])
</script>

<template>
  <select v-model="selected" style="width: auto; min-width: 160px" aria-label="Ref">
    <optgroup v-if="branches.length" label="Branches">
      <option v-for="branch in branches" :key="branch.name" :value="branch.name">
        {{ branch.name }}
      </option>
    </optgroup>
    <optgroup v-if="tags.length" label="Tags">
      <option v-for="tag in tags" :key="tag.name" :value="tag.name">
        {{ tag.name }}
      </option>
    </optgroup>
  </select>
</template>
