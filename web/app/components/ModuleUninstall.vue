<script setup lang="ts">
/**
 * Removing a module.
 *
 * The module declares what removing it means; this renders those declarations and
 * passes the chosen keys back untouched. The core checks the answer against the
 * manifest, so a mismatch between core and module stops the deletion instead of
 * guessing which of them is right.
 *
 * There is no cancel button, on purpose. Removal is not something to half regret:
 * an administrator who pressed it has decided what should not exist. What they get
 * instead is a log they may leave and come back to, and the truth if it goes
 * wrong.
 */
import type { ModuleRow, UninstallJob, UninstallLogLine } from '~/types/module'
import { uninstallStatusText } from '~/types/module'

const props = defineProps<{ module: ModuleRow }>()

const { add: notify } = useNotifyPool()

const options = computed(() => props.module.manifest?.uninstall?.options ?? [])
const hasOptions = computed(() => options.value.length > 0)

const chosen = ref<string[]>([])
const confirmed = ref(false)
const starting = ref(false)
const job = ref<UninstallJob | null>(null)
const lines = ref<UninstallLogLine[]>([])
const logError = ref('')
const followed = ref(false)

let stream: EventSource | null = null

/** Anything this module offers is offered unchecked-by-default when dangerous. */
watchEffect(() => {
  chosen.value = options.value
    .filter((option) => option.default && !option.dangerous)
    .map((option) => option.key)
})

const dangerous = computed(() => options.value.filter((option) => chosen.value.includes(option.key) && option.dangerous))
const missing = computed(() => options.value.filter((option) => option.required && !chosen.value.includes(option.key)))
const running = computed(() => !!job.value && !['done', 'failed', 'interrupted'].includes(job.value.status))

const percent = computed(() => {
  const current = job.value
  if (!current?.progress_total) return null
  if (current.progress_done === undefined) return null
  if (current.progress_total <= 0) return null
  return Math.min(100, Math.round((current.progress_done * 100) / current.progress_total))
})

onMounted(loadExisting)
onBeforeUnmount(closeStream)

/** A removal started earlier is still the one to watch, an hour later or not. */
async function loadExisting() {
  try {
    const answer = await api.get<{ job: UninstallJob | null }>(`/modules/${props.module.id}/uninstall`)
    if (answer.job) {
      job.value = answer.job
      follow(answer.job.id)
    }
  } catch (caught) {
    logError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  }
}

async function start() {
  starting.value = true
  logError.value = ''
  try {
    const answer = await api.post<{ job: UninstallJob }>(`/modules/${props.module.id}/uninstall`, {
      options: chosen.value,
    })
    job.value = answer.job
    lines.value = []
    follow(answer.job.id)
    notify('Removal started. The log below keeps updating; you may leave this page.', {
      type: 'info',
      timer: 8,
    })
  } catch (caught) {
    logError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    starting.value = false
  }
}

/**
 * Watches the log.
 *
 * EventSource rather than a hand-rolled stream reader, because it reconnects on
 * its own and because the core serves the log from the database: reconnection
 * resumes from the last id seen, so a dropped line is not a lost line.
 */
function follow(jobId: string) {
  closeStream()

  const url = rawApiUrl(`/modules/${props.module.id}/uninstall/log`)
  stream = new EventSource(url, { withCredentials: true })

  stream.onopen = () => {
    followed.value = true
  }

  stream.addEventListener('message', (event) => {
    const line = JSON.parse((event as MessageEvent).data) as UninstallLogLine
    lines.value.push(line)

    // The window is not the archive: the log can be thousands of lines and the
    // browser only needs the tail to be readable.
    if (lines.value.length > 500) {
      lines.value.splice(0, lines.value.length - 500)
    }
  })

  stream.addEventListener('job', (event) => {
    const payload = JSON.parse((event as MessageEvent).data) as { job: UninstallJob }
    job.value = payload.job

    if (payload.job.status === 'done') {
      notify(`${props.module.kind} removed.${summaryLine(payload.job)}`, { type: 'success', timer: 0 })
      closeStream()
    }
    if (payload.job.status === 'failed') {
      notify(`Removing ${props.module.kind} failed: ${payload.job.error ?? 'see the log'}`, {
        type: 'error',
        timer: 0,
      })
      closeStream()
    }
    if (payload.job.status === 'stalled') {
      notify(`${props.module.kind} stopped talking. The log shows how far it got.`, {
        type: 'warning',
        timer: 0,
      })
    }
  })

  stream.onerror = () => {
    // EventSource retries on its own and comes back where it left off; saying so
    // is better than showing an error that is about to resolve itself.
    followed.value = false
  }
}

function closeStream() {
  stream?.close()
  stream = null
}

/** The module's own totals, in a sentence, because they are the point of it. */
function summaryLine(current: UninstallJob): string {
  const summary = current.summary ?? {}
  const parts: string[] = []

  if (summary.removed_repositories !== undefined) parts.push(`${summary.removed_repositories} repositories`)
  if (summary.removed_images !== undefined) parts.push(`${summary.removed_images} images`)
  if (summary.removed_tags !== undefined) parts.push(`${summary.removed_tags} tags`)
  if (summary.freed_bytes !== undefined) parts.push(`${formatBytes(Number(summary.freed_bytes))} freed`)
  if (summary.dropped_database) parts.push(`database ${summary.dropped_database} dropped`)

  return parts.length ? ` ${parts.join(', ')}.` : ''
}

const statusClass: Record<string, string> = {
  running: 'badge-green',
  queued: 'badge-private',
  stalled: 'badge-warning',
  failed: 'badge-danger',
  interrupted: 'badge-warning',
  done: 'badge-green',
}
</script>

<template>
  <div class="removal">
    <div v-if="logError" class="alert alert-error">{{ logError }}</div>

    <!-- Nothing is running: what pressing the button will do. -->
    <div v-if="!job" class="card">
      <div class="card-body">
        <template v-if="hasOptions">
          <p class="muted" style="margin-top: 0">
            This module says removing it involves the following. It declares them, so
            the wording is its own and describes what it will actually delete.
          </p>

          <label v-for="option in options" :key="option.key" class="option">
            <input
              type="checkbox"
              :value="option.key"
              v-model="chosen"
              :disabled="option.required"
            />
            <span>
              <span class="option-label">
                {{ option.label }}
                <span v-if="option.dangerous" class="badge badge-danger">Cannot be undone</span>
                <span v-if="option.required" class="badge">Required</span>
              </span>
              <span v-if="option.description" class="muted small">{{ option.description }}</span>
            </span>
          </label>

          <div v-if="dangerous.length" class="confirm">
            <label class="option">
              <input type="checkbox" v-model="confirmed" />
              <span>
                <span class="option-label">
                  I understand {{ dangerous.map((option) => option.label.toLowerCase()).join(', ') }}
                  cannot be undone.
                </span>
              </span>
            </label>
          </div>

          <div v-if="missing.length" class="alert alert-error">
            This module cannot be removed without
            {{ missing.map((option) => `“${option.label}”`).join(', ') }}.
          </div>

          <button
            class="btn btn-danger"
            type="button"
            :disabled="starting || missing.length > 0 || (dangerous.length > 0 && !confirmed)"
            @click="start"
          >
            {{ starting ? 'Starting…' : 'Remove module' }}
          </button>
        </template>

        <!-- Nothing to ask: the module holds nothing that needs asking about. -->
        <template v-else>
          <p class="muted" style="margin-top: 0">
            This module declared nothing to ask about, so removing it simply forgets it.
          </p>
          <button class="btn btn-danger" type="button" @click="start">Remove module</button>
        </template>

        <!-- Said in both cases, because it is true in both: there is no way back
             from a removal that has started, and an administrator deserves to know
             that before pressing the button rather than after. -->
        <p class="muted small">
          Removal cannot be stopped once started. The module is asked to do the work
          and everything it says is kept, so you may close this page and come back.
        </p>
      </div>
    </div>

    <!-- Something is running, or ran: the log is the whole answer. -->
    <div v-else class="card">
      <div class="card-header">
        <span class="badge" :class="statusClass[job.status]">
          {{ uninstallStatusText[job.status] }}
        </span>
        <span v-if="job.status === 'running'" class="muted small">
          {{ followed ? 'following' : 'reconnecting…' }}
        </span>
        <div class="uninstall-spacer" />
        <span class="muted small">started {{ timeAgo(job.started_at ?? job.created_at) }}</span>
      </div>

      <div class="card-body">
        <div v-if="job.status === 'interrupted'" class="alert alert-warning">
          {{ job.error || 'The core restarted while this removal was running.' }}
          The module may well have finished; nobody heard. Run it again — a module has
          to survive being asked twice.
        </div>
        <div v-else-if="job.status === 'stalled'" class="alert alert-warning">
          The module stopped talking. The last thing it managed to write is in the log.
          If it was merely slow, it will carry on by itself; the status returns to
          running the moment it says anything.
        </div>
        <div v-else-if="job.status === 'failed'" class="alert alert-error">
          {{ job.error }}
        </div>

        <div v-if="percent !== null" class="progress">
          <div class="progress-fill" :style="{ width: `${percent}%` }" />
        </div>
        <p v-if="percent !== null" class="muted small">
          {{ job.progress_done }} of {{ job.progress_total }}
        </p>

        <pre class="uninstall-log">{{ lines.map((line) => line.message).join('\n') || 'waiting for the module…' }}</pre>
      </div>
    </div>
  </div>
</template>

