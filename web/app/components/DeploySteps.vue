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
  /**
   * Whether anything is actually happening now.
   *
   * Separate from the phase on purpose. A card showing the last operation has a
   * phase too — that operation's last one — and an arrow that moved because of it
   * would be claiming work that finished long ago. The arrow stands still when
   * there is nothing to stand for.
   */
  live?: boolean
}>()

type StepState = 'done' | 'now' | 'failed' | 'waiting'

interface Step {
  key: string
  label: string
  state: StepState
  /** What this step is counting, when it is counting anything. */
  counts: { done: number; of: number } | null
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
 * The "five of ten" in whatever a step last said, if it said one.
 *
 * One rule for every kind of step rather than a bar reserved for the rollout: five
 * of ten layers pushed is the same kind of fact as three of three pods up, and
 * somebody watching either of them wants the same thing from the same place. A rule
 * that only knew about pods would leave the push looking like a step that says
 * words and means nothing by them.
 *
 * The step's own step/of counters are read too, for the steps that count jobs
 * rather than things — "applying 2 of 4 manifests" — where the number is not in
 * the sentence.
 */
function countsIn(heard?: DeployProgress): { done: number; of: number } | null {
  if (!heard) return null

  if (heard.of && heard.step) return { done: heard.step, of: heard.of }

  const said = /(\d+)\s+of\s+(\d+)/.exec(heard.message ?? '')
  if (said) return { done: Number(said[1]), of: Number(said[2]) }

  return null
}

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

  const nowOn = props.progress?.phase ?? ''
  const nowAt = ORDER.findIndex((one) => one.key === nowOn)

  // Whether the operation is over. The last thing said about a finished deployment
  // has no phase in it, and without that a step nobody reached would sit at
  // "waiting" for ever — which says it is about to start.
  const over = props.progress?.message !== undefined && nowOn === ''

  return ORDER.map((step, position) => {
    const heard = wasSaid.get(step.key)
    let state: StepState = 'waiting'

    if (heard?.failed) state = 'failed'
    else if (step.key === nowOn) state = 'now'
    else if (nowAt > -1 && position < nowAt) state = 'done'
    else if (over && position > nowAt) state = 'skipped'

    return {
      key: step.key,
      label: step.label,
      state,
      detail: heard?.message,
      counts: countsIn(heard),
    }
  })
})

</script>

<template>
  <ol class="steps">
    <li v-for="step in steps" :key="step.key" class="step" :class="step.state">
      <!-- One mark on the left of every step, and the colour is the whole of it:
           green done, yellow now, red failed, blue not yet reached. Blue rather than
           grey because "has not come to it yet" is a known state and not an absence —
           grey reads as something this page has not been told. -->
      <span class="mark" :class="[step.state, { moving: step.state === 'now' && live }]">
        <template v-if="step.state === 'now'">➜</template>
        <template v-else-if="step.state === 'failed'">●</template>
        <template v-else>●</template>
      </span>

      <span class="step-body">
        <span class="label">{{ step.label }}</span>
        <span v-if="step.detail" class="detail">{{ step.detail }}</span>

        <!-- Under the words it belongs to, on the step they belong to.
             Not on whichever step happens to be current: the count arrives with the
             rollout and stays true while the old pods are still going, and a figure
             that vanishes the moment the next step starts is one that has to be
             caught rather than read. -->
        <!-- Gone once it is full. A bar at a hundred per cent is a bar with nothing
             left to say, and it would sit there for the rest of the operation
             telling somebody that finished something which is over. -->
        <span v-if="step.counts && step.counts.done < step.counts.of" class="pods">
          <!-- The numbers only. The sentence above says what is being counted —
               layers, pods, manifests — and guessing a noun here would put a word
               on the page that the module never said. -->
          <span class="pod-count mono">{{ step.counts.done }} of {{ step.counts.of }}</span>
          <span
            class="bar"
            role="progressbar"
            :aria-valuenow="step.counts.done"
            :aria-valuemax="step.counts.of"
          >
            <span class="fill" :style="{ width: `${(step.counts.done / step.counts.of) * 100}%` }" />
          </span>
        </span>
      </span>

      <!-- The count, on the line it belongs to.
           Above the list it was a heading for the whole operation and read as one;
           here it is part of the one step that has a number, beside the words that
           explain what the number is counting. -->
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

/* The one being worked on, in motion.
 *
 * The arrow says which way the work is going and the movement says it is still
 * going: a step that takes a minute is a minute of looking at it, and a mark that
 * does not change during that minute is a mark that has stopped meaning anything.
 *
 * A short distance and a slow cycle on purpose. It sits in a column of still dots,
 * so anything larger or quicker turns the list into something to watch instead of
 * something to read, and this is a list first. */
.mark.now {
  color: var(--yellow);
  font-size: 14px;
  display: inline-block;
}

/* Only while something is actually under way — see the `live` prop. */
.mark.now.moving {
  animation: nudge 0.8s ease-in-out infinite;
}

@keyframes nudge {
  0%, 100% {
    transform: translateX(-1.5px);
  }
  50% {
    transform: translateX(1.5px);
  }
}

/* Motion with no news is noise: somebody reading the page rather than watching it
   has asked for nothing to move. */
@media (prefers-reduced-motion: reduce) {
  .mark.now.moving {
    animation: none;
  }
}

.mark.failed {
  color: var(--red);
}

/* Dimmed, not greyed out: the step is not past, it has not been reached. */
.step.waiting .label {
  color: var(--text-muted);
}

/* Not `.body`: the application stylesheet has a `.body` of its own, a flex row for
   something else entirely, and it won. The label and the sentence under it came out
   on one line because they were being laid out as a row by a rule written for a
   layout that has nothing to do with this. */
.step-body {
  min-width: 0;
  flex: 1 1 auto;
}

.label {
  display: block;
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
  margin-top: 1px;
  font-size: 12px;
  color: var(--text-muted);
  word-break: break-word;
}

.step.failed .detail {
  color: var(--red);
}

/* The count, set out to the right of its own step and no wider than it needs to be.
   A bar that grows with the panel is a bar whose meaning changes with the window;
   this one is the same length on every screen, so the same fill always means the
   same amount. */
/* Under the sentence, in the same column, at the same left edge: the bar is part of
   what that step is saying, and putting it anywhere else makes it a separate thing
   with its own place to look. The number first and the bar under it, because the
   number is what is read and the bar is what shows how far along it is. */
.pods {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  margin-top: 6px;
}

/* Width and height, and no flex-basis: this sits in a column, where flex-basis is
   the vertical axis — a bar sized by it comes out as tall as it is long. */
.bar {
  flex: none;
  width: 160px;
  height: 6px;
  border-radius: 3px;
  background: var(--bg-inset);
  border: 1px solid var(--border);
  overflow: hidden;
}

.fill {
  display: block;
  height: 100%;
  background: var(--green);
  transition: width 0.4s ease;
}

.pod-count {
  font-size: 12px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
</style>