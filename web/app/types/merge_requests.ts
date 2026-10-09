/** Merge request types, mirroring the API responses. */
import type { ProjectSummary } from '~/types/repository'

export type MergeRequestState = 'opened' | 'merged' | 'closed'

export interface MergeRequestNote {
  id: number
  merge_request_id: number
  author_id: string
  author_name: string
  author_username: string
  body: string
  created_at: string
  updated_at: string
}

export interface MergeRequestDiffStats {
  files_changed: number
  additions: number
  deletions: number
}

export interface MergeRequest {
  id: number
  iid: number
  project_id: string
  project?: ProjectSummary
  url: string

  author_id: string
  author_name: string
  author_username: string
  merged_by_name?: string

  source_branch: string
  target_branch: string
  title: string
  description: string
  state: MergeRequestState
  merge_commit_sha?: string
  sha: string
  squash: boolean
  is_draft: boolean
  assignee_id?: string | null
  reviewer_id?: string | null
  milestone?: string
  labels?: string[]
  remove_source_branch?: boolean
  pipeline_required?: boolean

  created_at: string
  updated_at: string
  merged_at?: string | null
  closed_at?: string | null

  /** Only filled in for a request that is still open. */
  has_conflicts?: boolean
  merge_status?: string
  diff_stats?: MergeRequestDiffStats
}

export interface MergeRequestResponse {
  merge_request: MergeRequest
  notes?: MergeRequestNote[]
  diff_url?: string
}