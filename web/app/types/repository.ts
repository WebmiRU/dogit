/** Shared repository types, mirroring the API responses. */

export interface ProjectSummary {
  id: string
  path: string
  name: string
  description: string
  visibility: 'private' | 'internal' | 'public'
  default_branch: string
  access_level: number
  access_name: string
  ssh_url: string
  web_url: string
  created_at: string
  archived_at?: string | null
}

export interface TreeEntry {
  mode: string
  type: 'blob' | 'tree' | 'commit'
  oid: string
  size: number
  path: string
}

export interface TreeResponse {
  ref: string
  sha: string
  path: string
  entries: TreeEntry[]
  breadcrumbs: { name: string; path: string }[]
  is_dir: boolean
}

export interface Ref {
  name: string
  short: string
  target: string
  type: 'branch' | 'tag'
}

export interface RefsResponse {
  branches: Ref[]
  tags: Ref[]
  default_branch: string
}

export interface CommitInfo {
  sha: string
  short_sha: string
  parents: string[]
  author_name: string
  author_email: string
  committer_name: string
  committer_email: string
  message: string
  subject: string
  timestamp: string
  committed_at: string
}

export interface CommitsResponse {
  ref: string
  commits: CommitInfo[]
  total: number
  page: number
  limit: number
  has_more: boolean
}

export interface FileResponse {
  ref: string
  /** The blob id of this file, sent back as start_sha when saving. */
  sha: string
  path: string
  content: string
  size: number
  binary: boolean
  too_large: boolean
  too_large_bytes?: number
  language: string
  lines: number
  /** The commit that last changed this path, which is not always the branch tip. */
  last_commit_sha?: string
  blame_url?: string
  raw_url?: string
}

export interface FileChange {
  path: string
  old_path?: string
  status: string
  additions: number
  deletions: number
  binary: boolean
  patch: string
  truncated: boolean
}

export interface DiffStat {
  files_changed: number
  additions: number
  deletions: number
}

export interface CommitResponse {
  commit: CommitInfo
  branches: string[]
  diff_url: string
}

export interface CommitDiffResponse {
  sha: string
  files: FileChange[]
  stats: DiffStat
}

export interface BlameLine {
  commit_sha: string
  author_name: string
  timestamp: string
  line_no: number
  content: string
}
