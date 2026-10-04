<script setup lang="ts">
/**
 * What happened, in the order it happened, said in words.
 *
 * The steps above say where an operation is; this says what it did. They answer
 * different questions, and the second one is the one that matters when something
 * breaks: a row of dots tells you the operation stopped at the fourth step, and only a
 * log tells you that the fourth step was refused because the cluster would not give the
 * new pod a name.
 *
 * Nothing here is written by this component. The headings are the plan the core sent,
 * and every line is a message the core or the runner published, in the order they were
 * published. A deployment log is small; there is no reason to be precious about it, and
 * the reason to keep it is exactly the moment nobody wants to be reconstructing from
 * memory.
 */
import { computed } from 'vue'

interface Line {
  phase: string
  message: string
  step: number
  of: number
}

interface Step {
  key: string
  label: string
}

const props = defineProps<{
  lines: Line[]
  plan: Step[]
}>()

interface Group {
  label: string
  lines: Line[]
}

/**
 * Lines under the step they belong to, steps in the order the plan gave them.
 *
 * A phase the plan does not mention is not dropped: it goes at the end under its own
 * name. Losing a line because it arrived from somewhere we did not expect is how a log
 * stops being evidence.
 */
const groups = computed<Group[]>(() => {
  const placed = new Map<string, Line[]>()
  const loose: Line[] = []

  for (const line of props.lines) {
    if (props.plan.some((step) => step.key === line.phase)) {
      const bucket = placed.get(line.phase) ?? []
      bucket.push(line)
      placed.set(line.phase, bucket)
      continue
    }
    loose.push(line)
  }

  const ordered = props.plan
    .filter((step) => placed.has(step.key))
    .map((step) => ({ label: step.label, lines: placed.get(step.key) ?? [] }))

  // One heading per unexpected phase rather than one for all of them: a run of unrelated
  // messages under a shared heading would claim they belong together.
  const looseByPhase = new Map<string, Line[]>()
  for (const line of loose) {
    const bucket = looseByPhase.get(line.phase) ?? []
    bucket.push(line)
    looseByPhase.set(line.phase, bucket)
  }
  for (const [phase, lines] of looseByPhase) {
    ordered.push({ label: phase, lines })
  }

  return ordered
})

/** The step's own position, when the line carries one. */
function position(line: Line): string {
  if (!line.of) return ''
  return `${line.step} of ${line.of}`
}
</script>

<template>
  <div v-if="lines.length" class="deploy-log">
    <div v-for="group in groups" :key="group.label" class="deploy-log-group">
      <h4 class="deploy-log-heading">{{ group.label }}</h4>
      <ul class="deploy-log-lines">
        <li v-for="(line, index) in group.lines" :key="index" class="deploy-log-line">
          <span class="deploy-log-text">{{ line.message }}</span>
          <span v-if="position(line)" class="deploy-log-position">{{ position(line) }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.deploy-log {
  margin-top: 1rem;
  border-top: 1px solid var(--border, #e2e2e6);
  padding-top: 0.75rem;
}

.deploy-log-heading {
  margin: 0 0 0.25rem;
  font-size: 0.85rem;
  font-weight: 600;
}

.deploy-log-lines {
  margin: 0 0 0.75rem;
  padding-left: 1.1rem;
  list-style: none;
}

.deploy-log-line {
  font-size: 0.85rem;
  line-height: 1.5;
  color: var(--muted, #6b6b70);
  display: flex;
  gap: 0.5rem;
  align-items: baseline;
}

.deploy-log-line::before {
  content: '-';
  color: var(--border, #b9b9bf);
}

.deploy-log-position {
  font-variant-numeric: tabular-nums;
  opacity: 0.7;
}
</style>