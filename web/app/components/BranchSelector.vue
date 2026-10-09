<script setup lang="ts">
/** Branch and tag switcher. */
import type { RefsResponse } from '~/types/repository'

const props = defineProps<{
  refs: RefsResponse | null
  refName: string
}>()

// The component only reports the choice; the page decides where to go. Navigating
// from here would have to guess which view the user was on, and switching a branch
// on the commits page used to drop the user back onto the code page.
const emit = defineEmits<{ (event: 'change', ref: string): void }>()

const branches = computed(() => props.refs?.branches ?? [])
const tags = computed(() => props.refs?.tags ?? [])

/** The current ref may be a tag, a branch or a commit that is in neither list. */
const known = computed(() => [...branches.value, ...tags.value].some((ref) => ref.name === props.refName))
</script>

<template>
  <div style="display: flex; align-items: center; gap: 6px">
    <select
      :value="refName"
      aria-label="Ref"
      style="width: auto; min-width: 170px"
      @change="emit('change', ($event.target as HTMLSelectElement).value)"
    >
      <!-- A ref that is not in the list is still selected, so the control never
           silently disagrees with the page it is on. -->
      <option v-if="!known && refName" :value="refName">{{ refName }}</option>
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
  </div>
</template>
