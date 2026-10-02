export interface DashboardStats {
  projects: number
  groups: number
  owned_projects: number
  maintainer_projects: number
  ssh_keys: number
}

export interface ActivityEntry {
  id: number
  kind: 'push' | 'pipeline.created' | 'pipeline.updated' | 'job.updated' | 'merge_request.changed'
  created_at: string
  project_id?: string
  project_path: string
  project_name: string
  actor_id?: string
  actor_name: string
  summary: string
  detail?: string
  ref?: string
}

export interface CommitActivityEntry {
  project_id: string
  project_path: string
  project_name: string
  sha: string
  branch: string
  author_name: string
  message: string
  timestamp: string
}

export interface DashboardResponse {
  stats: DashboardStats
  activity: ActivityEntry[]
  commits: CommitActivityEntry[]
  projects: import('./repository').ProjectSummary[]
}

export interface GroupSummary {
  id: string
  slug: string
  name: string
  full_path: string
  access_level: number
  access_name: string
}

export interface SSHKeySummary {
  id: string
  title: string
  fingerprint: string
  public_key: string
  created_at: string
  last_used_at?: string | null
}

export interface AdminOverview {
  counts: Record<string, number>
  features: Record<string, boolean>
}
