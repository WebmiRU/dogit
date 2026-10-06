/** Shapes of the pipeline API, as the interface sees them. */

export type PipelineStatus =
  | 'pending'
  | 'running'
  | 'success'
  | 'failed'
  | 'canceled'
  | 'interrupted'

export type JobStatus =
  | 'pending'
  | 'running'
  | 'success'
  | 'failed'
  | 'canceled'
  | 'skipped'

/** One person, as the interface names them. */
export interface PipelinePerson {
  username: string
  name: string
  avatar_url?: string
}

/** A job as it appears inside a stage, on a pipeline row. */
export interface PipelineStageJob {
  iid: number
  name: string
  status: JobStatus
  duration_ms?: number
}

/**
 * A stage: a group of jobs that run one after another.
 *
 * The stages are the pipeline's outline. Everything a person wants to know at a
 * glance — did it pass, where did it get to — is visible without opening a job.
 */
export interface PipelineStage {
  name: string
  status: JobStatus
  job_count: number
  jobs: PipelineStageJob[]
}

export interface ModulePipeline {
  id: number
  iid: number
  project_id: string
  ref: string
  sha: string
  source: string
  status: PipelineStatus
  jobs?: number
  created_at: string
  started_at?: string
  finished_at?: string
  /** How long the pipeline ran, once it has started. */
  duration_ms?: number
  /** The commit's message, as it was when this run was made. */
  title?: string
  /** Who wrote the commit this run was made against. */
  author_name?: string
  avatar_url?: string
  /** Who asked for the run: a person, or nothing for a push. */
  triggered_by?: PipelinePerson
  stages?: PipelineStage[]
}

export interface PipelineJob {
  id: number
  pipeline_id: number
  iid: number
  name: string
  stage: string
  status: JobStatus
  image: string
  script: string[]
  allow_failure: boolean
  /** Present when the job produces an image; the registry module's own shape. */
  build?: Record<string, unknown>
  /** Present on a job that deploys rather than runs a script. */
  deploy?: Record<string, unknown>
  duration_ms?: number
  created_at: string
  started_at?: string
  finished_at?: string
  /**
   * Why it failed, in the words of whatever ran it.
   *
   * Kept beside the log rather than inside it because a job that died before printing
   * anything — a checkout that could not authenticate, a machine that would not start —
   * has a log with nothing in it, and the two things a reader needs are the verdict
   * and the reason.
   */
  error?: string
}

/** What a status means, in the terms somebody watching a build cares about. */
export const statusText: Record<PipelineStatus | JobStatus, string> = {
  pending: 'Waiting',
  running: 'Running',
  success: 'Passed',
  failed: 'Failed',
  canceled: 'Canceled',
  interrupted: 'Interrupted',
  skipped: 'Skipped',
}

/**
 * The colour a status is drawn in.
 *
 * One set for every state, and the same one the notification text uses: green for
 * success, yellow for running, red for failure, grey for canceled and skipped,
 * blue for waiting. Blue rather than grey for waiting, because grey already means
 * something did not happen and a queued build has not gone wrong — it simply has
 * not started.
 */
export const statusClass: Record<string, string> = {
  success: 'badge-green',
  failed: 'badge-danger',
  running: 'badge-warning',
  canceled: 'badge-neutral',
  pending: 'badge-blue',
  interrupted: 'badge-neutral',
  skipped: 'badge-neutral',
}

/**
 * The sign for a state in a message.
 *
 * A message cannot be coloured, so it carries a character instead — the same one
 * the badge wears, so that a green tick on the page and a green tick in the chat
 * mean one thing.
 */
export const statusMark: Record<string, string> = {
  success: '✅',
  failed: '❌',
  running: '🟡',
  pending: '🔵',
  canceled: '⚪',
  interrupted: '⚪',
  skipped: '⚪',
}

/**
 * A duration as 01:23 or 1:02:03.
 *
 * Two digits for minutes as well as seconds: a build that has been going four
 * minutes and one that has been going one minute look identical otherwise, and
 * the difference is usually the whole reason somebody is looking.
 */
export function formatDuration(ms?: number): string {
  if (ms === undefined || ms < 0) return '—'
  // A job that died in a quarter of a second is not a job that took no time: it is a
  // job whose time rounds to nothing, and "00:00" beside it reads as a run that never
  // happened. The tenth of a second says what was measured, and the next number up is
  // still a second.
  if (ms < 1000) return `${(ms / 100).toFixed(1)}s`
  const total = Math.floor(ms / 1000)
  const seconds = total % 60
  const minutes = Math.floor(total / 60) % 60
  const hours = Math.floor(total / 3600)
  const pad = (value: number) => String(value).padStart(2, '0')
  return hours > 0
    ? `${hours}:${pad(minutes)}:${pad(seconds)}`
    : `${pad(minutes)}:${pad(seconds)}`
}

/**
 * Where a deployment has got to, while it is getting there.
 *
 * Sent as an event while the deployment runs rather than read afterwards: a deploy is
 * minutes of work and the interesting part of it — which pods are up, what failed —
 * exists only while it happens. Everything else on this page is either a state or a
 * number that changes when the job does.
 */
export interface DeployProgress {
  /** Which part of the deployment this is. */
  phase: 'prepare' | 'pre' | 'pull' | 'apply' | 'rollout' | 'post' | string
  /** The sentence to show, in the module's words. */
  message: string
  /** Pods ready and wanted, while a rollout is happening. */
  ready?: number
  desired?: number
  /** Which of the things this phase had to do, and which it is on. */
  step?: number
  of?: number
  /**
   * Whether this is the last the module will say about this phase.
   *
   * Without it a phase is over the moment it stops speaking, and nothing on the page
   * can tell that from a phase that is merely quiet — which is how an arrow ends up
   * parked on a step whose work finished, and why the line saying the old pods are
   * gone could never be written: the count made it impossible to send.
   */
  finished?: boolean
  /** Set once something has gone wrong, and the message says what. */
  failed?: boolean
  /**
   * The record this deployment is writing, once it has started.
   *
   * Sent with the first line rather than only at the end, because everything worth
   * knowing about the run — which image, under which names, touching which workload,
   * going to which place — is known at that moment and there is no reason to make a
   * page wait two minutes for it. A rollout whose own row is blank for its whole
   * length says nothing at the moment somebody is looking hardest at it.
   */
  deployment?: {
    project?: string
    cluster?: string
    namespace?: string
    image?: string
    workload?: string
    place?: string
    tags?: string[]
    commit?: string
    state?: string
  }
}
