<script setup lang="ts">
/**
 * What a module reported about itself.
 *
 * Two rules shape this component. A value the module did not send is shown as
 * "not reported" rather than as a zero: a module in a container usually cannot
 * see the node's memory, and a zero there would read as good news in exactly the
 * place where it is not. And the readings are a series, so the question this is
 * really for — is it filling up — has an answer.
 */
import type { ModuleStats } from '~/types/module'

const props = defineProps<{
  stats?: ModuleStats | null
  series?: ModuleStats[]
  /** Storage above this fraction is worth a warning rather than a number. */
  warnAt?: number
}>()

const warnThreshold = computed(() => props.warnAt ?? 0.9)

const storageFraction = computed(() => {
  const stats = props.stats
  if (!stats?.storage_total_bytes || stats.storage_used_bytes === undefined) return null
  if (stats.storage_total_bytes <= 0) return null
  return stats.storage_used_bytes / stats.storage_total_bytes
})

/** A disk that is nearly full is worth saying plainly, not colouring quietly. */
const storageState = computed(() => {
  const fraction = storageFraction.value
  if (fraction === null) return 'unknown'
  if (fraction >= warnThreshold.value) return 'warn'
  if (fraction >= warnThreshold.value * 0.75) return 'near'
  return 'ok'
})

const hostMemoryFraction = computed(() => {
  const stats = props.stats
  if (!stats?.host_memory_total_bytes || stats.host_memory_used_bytes === undefined) return null
  if (stats.host_memory_total_bytes <= 0) return null
  return stats.host_memory_used_bytes / stats.host_memory_total_bytes
})

/** The points of the graph, as percentages of the largest reading. */
const spark = computed(() => {
  const readings = (props.series ?? []).filter((point) =>
    Number.isFinite(point.storage_used_bytes as number),
  )
  if (readings.length < 2) return null

  const values = readings.map((point) => point.storage_used_bytes as number)
  const largest = Math.max(...values, 1)

  return {
    width: 100,
    height: 28,
    points: values
      .map((value, index) => {
        const x = (index / (values.length - 1)) * 100
        const y = 28 - (value / largest) * 24 - 2
        return `${x.toFixed(2)},${y.toFixed(2)}`
      })
      .join(' '),
    first: values[0] as number,
    last: values[values.length - 1] as number,
    grew: (values[values.length - 1] as number) > (values[0] as number),
  }
})

function percent(value: number) {
  return `${(value * 100).toFixed(1)}%`
}

function megabytes(value: number) {
  return formatBytes(value)
}
</script>

<template>
  <div v-if="!stats" class="card empty">
    This module has not reported anything yet. A module reports what it can see
    with its heartbeat; one that never has is either old, or not running.
  </div>

  <div v-else class="stats">
    <div class="stat">
      <div class="stat-label">Storage</div>
      <template v-if="storageFraction !== null">
        <div class="stat-value" :class="`state-${storageState}`">
          {{ percent(storageFraction) }}
        </div>
        <div class="stat-bar">
          <div class="bar-fill" :class="`state-${storageState}`" :style="{ width: percent(storageFraction) }" />
        </div>
        <div class="muted small">
          {{ megabytes(stats.storage_used_bytes!) }} of {{ megabytes(stats.storage_total_bytes!) }}
        </div>
      </template>
      <div v-else class="stat-value muted">not reported</div>
    </div>

    <div class="stat">
      <div class="stat-label">Process</div>
      <template v-if="stats.process_cpu_percent !== undefined || stats.process_memory_bytes !== undefined">
        <div class="stat-value">
          {{ stats.process_cpu_percent !== undefined ? `${stats.process_cpu_percent.toFixed(1)}%` : '—' }}
        </div>
        <div class="muted small">
          {{ stats.process_memory_bytes !== undefined ? `${megabytes(stats.process_memory_bytes)} resident` : 'memory not reported' }}
        </div>
      </template>
      <div v-else class="stat-value muted">not reported</div>
    </div>

    <div class="stat">
      <div class="stat-label">Node</div>
      <template v-if="hostMemoryFraction !== null || stats.host_load1 !== undefined">
        <div v-if="hostMemoryFraction !== null" class="stat-value">
          {{ percent(hostMemoryFraction) }}
        </div>
        <div class="muted small">
          <template v-if="hostMemoryFraction !== null">
            memory of {{ megabytes(stats.host_memory_total_bytes!) }}
          </template>
          <template v-if="stats.host_load1 !== undefined">
            · load {{ stats.host_load1.toFixed(2) }}
          </template>
        </div>
      </template>
      <div v-else class="stat-value muted">not reported</div>
    </div>

    <div class="stat">
      <div class="stat-label">Reported</div>
      <div class="stat-value">{{ timeAgo(stats.at) }}</div>
      <div class="muted small">
        <template v-if="stats.uptime_seconds !== undefined">
          up {{ Math.floor(stats.uptime_seconds / 3600) }}h
        </template>
        <template v-else>uptime not reported</template>
      </div>
    </div>

    <!-- The module's own facts: image counts, queue lengths, whatever only it
         knows. The core renders name and value without knowing what they mean. -->
    <div v-if="stats.extra && Object.keys(stats.extra).length" class="stat wide">
      <div class="stat-label">Also reported</div>
      <div class="extras">
        <span v-for="(value, key) in stats.extra" :key="key" class="badge">
          {{ key }}: {{ value }}
        </span>
      </div>
    </div>

    <div v-if="spark" class="stat wide">
      <div class="stat-label">Storage over the last day</div>
      <svg class="spark" :viewBox="`0 0 ${spark.width} ${spark.height}`" preserveAspectRatio="none">
        <polyline :points="spark.points" fill="none" stroke="currentColor" stroke-width="1.2" vector-effect="non-scaling-stroke" />
      </svg>
      <div class="muted small">
        {{ megabytes(spark.first) }} → {{ megabytes(spark.last) }}
        <span v-if="spark.grew" class="state-warn">growing</span>
      </div>
    </div>
  </div>
</template>

