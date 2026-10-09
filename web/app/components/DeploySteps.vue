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

/** One step, as the core described it. */
interface PlannedStep {
  key: string
  label: string
}

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
  /**
   * The steps this deployment will go through, in order.
   *
   * From the core, not from here. A client that kept its own list would be drawing a
   * set of phases that might apply rather than the ones that do, and the difference
   * is two rows that stay blue for ever on a deployment that has neither of them.
   */
  plan?: PlannedStep[]
  /**
   * Every phase happening at once, not just the latest one.
   *
   * A rolling update brings the new pods up and sends the old ones away in the same
   * breath, and a single arrow cannot say so: it sits on one step or the other, and
   * whichever it is not on is drawn as work already finished. Two arrows is the
   * honest picture, and it is not a special case — it is what "which steps are
   * happening now" means when a module is allowed to do two things at a time.
   */
  activePhases?: string[]
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

  // Pods on their way out, said as "7 pod(s) still running the previous image".
  //
  // Seven is what is left, so it is counted against how many the workload has — which
  // the module says in the same line, and which is the only number here that is not a
  // leftover from whatever this page was watching before. Guessing it instead — taking
  // the first leftover as the whole — is what made a three-pod workload say "0 of 4" and
  // then "1 of 5": the first frame of a rollout sees one or two pods still up, so the
  // bar's full length is that, and it can never be more.
  const draining = /(\d+)\s+pod\(s\)\s+still running/.exec(heard.message ?? '')
  if (draining) {
    const left = Number(draining[1])
    if (heard.desired && heard.desired > 0) {
      return { done: Math.max(0, heard.desired - left), of: heard.desired }
    }
    const key = heard.phase
    const peak = Math.max(peaks.value[key] ?? 0, left)
    peaks.value = { ...peaks.value, [key]: peak }
    return { done: Math.max(0, peak - left), of: peak }
  }

  return null
}

/** The most a draining phase has ever had left, so its bar can be counted forwards. */
const peaks = ref<Record<string, number>>({})

// Forgotten when the operation is over. Kept across one, and the next rollback inherits
// the longest drain anybody has watched: a two-pod drain of yesterday is the full length
// of today's bar, which fills to a quarter and stops.
watch(
  () => (props.seen ?? []).length,
  (lines, had) => {
    if (lines === 0 && had > 0) peaks.value = {}
  },
)

/**
 * Where each step has got to.
 *
 * Read from everything said so far rather than only from the latest thing: the last
 * message is a snapshot of one step, and a list built from snapshots loses the ticks
 * the moment a later step says anything.
 */
/** The steps to draw: anything announced that the plan did not name, then the plan. */
const order = computed<PlannedStep[]>(() => {
  const announced: PlannedStep[] = []
  const known = new Set((props.plan ?? []).map((one) => one.key))

  // Anything said that the plan did not name is still real work and still belongs on the
  // list: the core cannot know every step before anybody starts, and the runner announcing
  // its stages as it goes is exactly how this list learns what the build did. Collected in
  // arrival order, which is the order it happened.
  for (const key of lastHeard.value.keys()) {
    if (known.has(key)) continue
    known.add(key)
    announced.push({ key, label: labelOf(key) })
  }

  // Ahead of the plan rather than after it. The core does not start a deployment until the
  // stage before it has finished, so whatever a runner said was said before the module said
  // anything — and appending it below "Retire the old pods" dated the whole build as the
  // last thing that happened. It also put the build's stages at the far end of the list,
  // where reachedAt picked them up as the high-water mark during the build itself: every
  // step of the deployment plan then sat at a lower index and was painted as done, several
  // seconds before there was a deployment to do.
  return [...announced, ...(props.plan ?? [])]
})

/**
 * A name for a step the plan did not mention.
 *
 * The stage's own name, and not a piece of what was said about it. A label cut out of the
 * message is that same message again a few pixels along, in the same row, and only the part
 * of it that happens to fit the column — so a page read "pushed
 * registry.f220.ru/test/versions:80" and then, beside it, the whole sentence again. Naming
 * the stage and putting what was said underneath it is one thing instead of two.
 */
function labelOf(key: string) {
  return key.charAt(0).toUpperCase() + key.slice(1)
}

/** Everything said so far, by phase: the last word on each. */
const lastHeard = computed(() => {
  const said = new Map<string, DeployProgress>()
  for (const one of props.seen ?? []) {
    if (one?.phase) said.set(one.phase, one)
  }
  if (props.progress?.phase) said.set(props.progress.phase, props.progress)
  return said
})

const steps = computed<Step[]>(() => {
  const wasSaid = lastHeard.value

  // The step the arrow is on.
  //
  // A module does not always name a phase in every line: it says "every pod is running
  // the new image" or "finished" without one. Read literally, an unnamed line answers
  // "no step" — and the arrow then jumps off the list altogether while the operation is
  // plainly still going, leaving a step nobody reached sitting grey as though it were
  // about to begin. So an unnamed line means "still where I last was", which is what a
  // line without a phase has always meant to the person reading it.
  const said = props.progress?.phase ?? ''
  const lastNamed = said || ([...wasSaid.keys()].pop() ?? '')

  // Everything happening at once, which is more than one thing during a rollout: the
  // new pods come up while the old ones go away, and drawing one arrow for that says
  // one of them is not happening. With no list given — this page describing a finished
  // operation — the latest thing said is the one that counts.
  // An arrow means this is happening now, so there is none once the operation is over.
  //
  // It used to be decided by the list of open phases being empty, and a finished operation's
  // list is always empty — so the arrow went onto the last phase anybody mentioned and stayed
  // there. A card saying "Success" with a pointer on "Bring the new pods up" is two answers to
  // one question, and the one the eye reads second is the one that is wrong.
  const now = props.live
    ? (props.activePhases?.length ? [...props.activePhases] : (lastNamed ? [lastNamed] : []))
    : []

  const nowAt = order.value.findIndex((one) => one.key === lastNamed)

  // How far it got, which is not the same question as where it is now.
  //
  // The last thing said about a finished operation carries no phase in it, so asking
  // "which step is current" at the end answers "none" — and a list that takes that
  // for its high-water mark loses every tick it had earned and paints the whole
  // operation grey. The furthest step ever mentioned is the one that knows.
  let reachedAt = nowAt
  for (const key of wasSaid.keys()) {
    const at = order.value.findIndex((one) => one.key === key)
    if (at > reachedAt) reachedAt = at
  }

  // Whether the operation is over, and this asks the page rather than the message.
  //
  // It used to be decided by the phase being empty, which is wrong: a module says a
  // great many things without naming a phase while it is still working, and every one
  // of them ended the list early — steps painted green that were never touched, and an
  // arrow that had already left. A page that is showing a live operation knows it is
  // live; only a page describing a finished one may call it finished.
  const over = !props.live && (props.activePhases?.length ?? 0) === 0 && said === ''

  // Whether anything went wrong, which decides what an unreached step means.
  //
  // After a deployment that ended well, a step nobody reached had nothing to do — the
  // old pods were already gone, or there was nothing to retire. Drawing that grey says
  // something did not happen, and beside nine green dots it is the one that reads as a
  // problem. It is only a skipped step when the operation was cut short, and then grey
  // is exactly right.
  const anythingFailed = [...wasSaid.values()].some((one) => one.failed)

  return order.value.map((step, position) => {
    const heard = wasSaid.get(step.key)
    let state: StepState = 'waiting'

    if (heard?.failed) state = 'failed'
    else if (now.includes(step.key)) state = 'now'
    else if (reachedAt > -1 && position < reachedAt) state = 'done'
    else if (over && position > reachedAt) state = anythingFailed ? 'skipped' : 'done'

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