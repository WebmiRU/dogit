<script setup lang="ts">
/**
 * What a deployment is doing, while it is doing it.
 *
 * The page's answer to "what is happening at the moment", which is the question a
 * deployment page always raises and which a row saying "running" does not answer. A
 * deploy is minutes of work: manifests applied one at a time, an image pulling, pods
 * coming up two at a time. All of that exists only while it happens, so it arrives as
 * it happens and is drawn from what has arrived so far.
 *
 * The phases are a fixed row of steps rather than a bar of percentages: nobody can
 * say how far through pulling an image anybody is, and a bar that guesses is worse
 * than a list that does not.
 */
import type { DeployProgress } from '~/types/pipeline'

const props = defineProps<{
  progress: DeployProgress | null
}>()

/** The phases, in the order a deployment goes through them. */
const PHASES = [
  { key: 'prepare', label: 'Prepare' },
  { key: 'pre', label: 'Pre-steps' },
  { key: 'pull', label: 'Image' },
  { key: 'apply', label: 'Apply' },
  { key: 'rollout', label: 'Rollout' },
  { key: 'post', label: 'Post-steps' },
] as const

const current = computed(() => props.progress?.phase ?? '')
const index = computed(() => PHASES.findIndex((one) => one.key === current.value))

/** Pods, when this step is about pods. */
const pods = computed(() => {
  const progress = props.progress
  if (!progress?.desired) return null
  return { ready: progress.ready ?? 0, desired: progress.desired }
})

const podFraction = computed(() => {
  const counts = pods.value
  if (!counts || counts.desired <= 0) return 0
  return Math.min(1, Math.max(0, counts.ready / counts.desired))
})
</script>

<template>
  <div v-if="props.progress" class="deploy-progress">
    <ol class="phases">
      <li
        v-for="(phase, position) in PHASES"
        :key="phase.key"
        class="phase"
        :class="{
          done: index > position,
          now: index === position,
        }"
      >
        <span class="dot" />
        <span class="phase-label">{{ phase.label }}</span>
      </li>
    </ol>

    <div class="now">
      <!-- The pods, which is the one number somebody watching actually wants. -->
      <div v-if="pods" class="pods">
        <div class="bar" role="progressbar" :aria-valuenow="pods.ready" :aria-valuemax="pods.desired">
          <div class="fill" :style="{ width: `${podFraction * 100}%` }" />
        </div>
        <span class="pod-count">{{ pods.ready }} of {{ pods.desired }} ready</span>
      </div>

      <p class="message">
        <span v-if="props.progress.of" class="count">
          {{ props.progress.step }} of {{ props.progress.of }}
        </span>
        {{ props.progress.message }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.deploy-progress {
  margin-top: 6px;
}

.phases {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 0;
  list-style: none;
  margin: 0 0 8px;
  padding: 0;
}

/* Each phase carries a dot, and the phases are joined by a line drawn between them:
   a row of six separate things reads as six steps somebody has to count, and a row
   with a line through it reads as one thing moving along. */
.phase {
  display: flex;
  align-items: center;
  gap: 6px;
  padding-right: 12px;
  font-size: 12px;
  color: var(--text-muted);
  position: relative;
}

.phase + .phase::before {
  content: '';
  position: absolute;
  left: -6px;
  width: 8px;
  height: 1px;
  background: var(--border);
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  border: 1px solid var(--border-strong);
  flex: 0 0 auto;
}

.phase.done .dot {
  background: var(--green);
  border-color: var(--green);
}

.phase.now {
  color: var(--text);
}

.phase.now .dot {
  background: var(--accent);
  border-color: var(--accent);
}

.pods {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}

.bar {
  flex: 1 1 200px;
  max-width: 320px;
  height: 6px;
  border-radius: 3px;
  background: var(--bg-inset);
  border: 1px solid var(--border);
  overflow: hidden;
}

.fill {
  height: 100%;
  background: var(--accent);
  transition: width 0.4s ease;
}

.pod-count {
  font-size: 12px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.message {
  margin: 0;
  font-size: 13px;
}

.count {
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
  margin-right: 6px;
}
</style>