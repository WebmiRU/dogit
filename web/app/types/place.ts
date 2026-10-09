/** The one list of where to work: projects and groups as rows of one table. */

/**
 * One place to work.
 *
 * A project and a group are the same kind of thing to somebody looking for somewhere
 * to do something, so they are rows of the same table and each row says which it is.
 */
export interface Place {
  id: string
  kind: 'project' | 'group'
  path: string
  name: string
  description?: string
  visibility?: string
  /** Set when the place is a project inside a group. */
  group_id?: string
  /** How many projects a group holds. Absent for a project. */
  project_count?: number
  access_level: number
  access_name?: string
  last_commit_at?: string
  last_commit_message?: string
  latest_pipeline_status?: string
  latest_pipeline_at?: string
  open_merge_requests: number
  open_issues: number
  created_at?: string
  archived_at?: string | null
}

/**
 * One page of rows, and how many there are.
 *
 * The count is the whole list and not the page: a page control that stops at twenty
 * with "about 200" beside it is worse than no control at all.
 */
export interface PagedPlaces {
  places: Place[]
  total: number
  page: number
  pages: number
  per_page: number
}

/** The filters a list of places can be narrowed by. */
export interface PlaceFilters {
  search?: string
  type?: '' | 'project' | 'group'
  visibility?: string
  scope?: '' | 'contributed' | 'personal' | 'member' | 'inactive'
  sort?: 'name' | 'created' | 'last_activity'
  direction?: 'asc' | 'desc'
  page?: number
  per_page?: number
}

/** The query string for a set of filters, without anything left at its default. */
export function placeQuery(filters: PlaceFilters): string {
  const parts = new URLSearchParams()

  if (filters.search?.trim()) parts.set('search', filters.search.trim())
  if (filters.type) parts.set('type', filters.type)
  if (filters.visibility) parts.set('visibility', filters.visibility)
  if (filters.scope) parts.set('scope', filters.scope)
  if (filters.sort) parts.set('sort', filters.sort)
  if (filters.direction) parts.set('direction', filters.direction)
  if (filters.page && filters.page > 1) parts.set('page', String(filters.page))
  if (filters.per_page) parts.set('per_page', String(filters.per_page))

  return parts.toString()
}

/**
 * Where a row leads.
 *
 * A project to its code and a group to its page: a group has no code to show, and
 * sending somebody to a repository view that does not exist would be worse than not
 * offering the link.
 */
export function placeHref(place: Place): string {
  return place.kind === 'group' ? `/groups/${place.id}` : `/p/${place.path}`
}