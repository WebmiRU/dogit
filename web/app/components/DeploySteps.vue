<script setup lang="ts">
/**
 * The steps of a deployment, as a list that fills in.
 *
 * A fixed set of steps, because they are the same every time and a list that appears
 * one line at a time gives no sense of how much is left. Each one is:
 *
 *   ✓  done       — with a tick, because a tick means it and a word means it
 *   →  now        — the one being worked on, which is the only one that is moving
 *   ✗  failed     — in red, with the reason underneath, because "failed" on its own
 *                    tells somebody nothing they can act on
 *   ·  waiting    — grey, and still unsaid: the cluster has not reported on it yet
 *
 * The steps are the core's and the module's, in one list. The image was built and
 * pushed before the deploy module was called at all, and a list that starts at
 * "applying manifests" reads as though the image arrived from nowhere.
 */
import type { DeployProgress } from '~/types/pipeline'

const props = defineProps<{
  /** The most recent thing said about this deployment. */
  progress: DeployProgress | null
  /** Everything said so far, so a step that finished earlier keeps its tick. */
  seen?: DeployProgress[]
}>()

type StepState = 'done' | 'now' | 'failed' | 'waiting'

interface Step {
  key: string
  label: string
  state: StepState
  detail?: string
}

/** The steps, in the order a deployment goes through them. */
const ORDER: { key: string; label: string }[] = [
  { key: 'build', label: 'Build the image' },
  { key: 'push', label: 'Push the image' },
  { key: 'prepare', label: 'Read the manifests' },
  { key: 'pre', label: 'Run the pre-step jobs' },
  { key: 'pull', label: 'Give the cluster a way to pull' },
  { key: 'apply', label: 'Apply to the cluster' },
  { key: 'rollout', label: 'Bring the new pods up' },
  { key: 'retire', label: 'Retire the old pods' },
  { key: 'post', label: 'Run the post-step jobs' },
]

/**
 * Where each step has got to.
 *
 * Read from everything said so far rather than only from the latest thing: the last
 * message is a snapshot of one step, and a list built from snapshots loses the ticks
 * the moment a later step says anything.
 */
const steps = computed<Step[]>(() => {
  const wasSaid = new Map<string, DeployProgress>()
  for (const one of props.seen ?? []) {
    if (one?.phase) wasSaid.set(one.phase, one)
  }
  if (props.progress?.phase) wasSaid.set(props.progress.phase, props.progress)

  const failedOn = [...wasSaid.values()].find((one) => one.failed)?.phase ?? ''
  const nowOn = props.progress?.phase ?? ''
  const nowAt = ORDER.findIndex((one) => one.key === nowOn)

  return ORDER.map((step, position) => {
    const heard = wasSaid.get(step.key)
    let state: StepState = 'waiting'

    if (heard?.failed) state = 'failed'
    else if (step.key === nowOn) state = 'now'
    else if (nowAt > -1 && position < nowAt) state = 'done'

    return {
      key: step.key,
      label: step.label,
      state,
      detail: heard?.message,
    }
  }).filter((step) => step.state !== 'waiting' || step.key === nowOn || failedOn !== '')
})

/** The pods, while they are the thing being counted. */
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
  <ol class="steps">
    <li v-for="step in steps" :key="step.key" class="step" :class="step.state">
      <!-- One mark on the left of every step, and the colour is the whole of it:
           green done, yellow now, red failed, blue not yet reached. Blue rather than
           grey because "has not come to it yet" is a known state and not an absence —
           grey reads as something this page has not been told. -->
      <span class="mark" :class="step.state">
        <template v-if="step.state === 'done'">●</template>
        <template v-else-if="step.state === 'now'">➜</template>
        <template v-else-if="step.state === 'failed'">●</template>
        <template v-else>●</template>
      </span>

      <span class="body">
        <span class="label">{{ step.label }}</span>
        <span v-if="step.detail" class="detail">{{ step.detail }}</span>

        <div v-if="step.state === 'now' && pods" class="pods">
          <div
            class="bar"
            role="progressbar"
            :aria-valuenow="pods.ready"
            :aria-valuemax="pods.desired"
          >
            <div class="fill" :style="{ width: `${podFraction * 100}%` }" />
          </div>
          <span class="pod-count">{{ pods.ready }} of {{ pods.desired }}</span>
        </div>
      </span>
    </li>
  </ol>
</template>

<style scoped>
.steps {
  list-style: none;
  margin: 0;
  padding: 0;
}

.step {
  display: flex;
  gap: 10px;
  padding: 5px 0;
  align-items: flex-start;
}

/* One mark in one place, so the eye goes down a single column rather than hunting
   for a tick that is somewhere to the right of a word that is somewhere below it. */
.mark {
  flex: 0 0 16px;
  width: 16px;
  height: 20px;
  text-align: center;
  font-size: 12px;
  line-height: 20px;
}

.mark.waiting {
  color: var(--accent);
  opacity: 0.5;
}

.mark.done {
  color: var(--green);
}

.mark.now {
  color: var(--yellow);
  font-size: 14px;
}

.mark.failed {
  color: var(--red);
}

/* Dimmed, not greyed out: the step is not past, it has not been reached. */
.step.waiting .label {
  color: var(--text-muted);
}

.body {
  min-width: 0;
}

.label {
  font-size: 13px;
}

.step.done .label {
  color: var(--text-muted);
}

.step.failed .label {
  color: var(--red);
}

.step.now .label {
  font-weight: 600;
}

/* The detail is what the module said, kept under its step: it is often a cluster's
   own wording about a failure, and it belongs next to the thing that went wrong
   rather than in a separate error box. */
.detail {
  display: block;
  font-size: 12px;
  color: var(--text-muted);
  word-break: break-word;
}

.step.failed .detail {
  color: var(--red);
}

.pods {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 5px;
}

.bar {
  flex: 1 1 160px;
  max-width: 260px;
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
</style>