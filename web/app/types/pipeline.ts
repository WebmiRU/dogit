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