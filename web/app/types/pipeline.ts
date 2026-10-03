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
  duration_ms?: number
  created_at: string
  started_at?: string
  finished_at?: string
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

/** The colour a status is drawn in. */
export const statusClass: Record<string, string> = {
  success: 'badge-green',
  failed: 'badge-danger',
  running: 'badge-warning',
  canceled: 'badge-neutral',
  pending: 'badge-private',
  interrupted: 'badge-warning',
  skipped: 'badge-neutral',
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
  const total = Math.floor(ms / 1000)
  const seconds = total % 60
  const minutes = Math.floor(total / 60) % 60
  const hours = Math.floor(total / 3600)
  const pad = (value: number) => String(value).padStart(2, '0')
  return hours > 0
    ? `${hours}:${pad(minutes)}:${pad(seconds)}`
    : `${pad(minutes)}:${pad(seconds)}`
}