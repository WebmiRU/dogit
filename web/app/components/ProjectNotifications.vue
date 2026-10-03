<script setup lang="ts">
/**
 * Where this project's notifications go.
 *
 * Its own tab rather than a block in settings, for the same reason it is a tab on a
 * group: this is a list of people who are going to be interrupted, and it is worth
 * being able to look at all of them at once without scrolling past something else
 * first.
 *
 * The tab exists even when no notification module is installed. The page then says
 * so and says what to do about it, which is the only way somebody who has never seen
 * a notification module finds out that there are any.
 */
const props = defineProps<{
  projectId: string
  projectPath: string
  canManage: boolean
}>()
</script>

<template>
  <div>
    <h2 class="page-title">Notifications</h2>
    <p class="page-subtitle">
      Who is told when something in {{ props.projectPath }} happens. Rows inherited
      from the group or the instance are shown here too — switch one off, or change
      it for this project only.
    </p>

    <NotificationRecipients scope="project" :scope-id="props.projectId" :can-manage="props.canManage" />
  </div>
</template>