/**
 * Building the addresses of repository views.
 *
 * A branch name may contain a slash, and a slash cannot be carried in the path:
 * the router decodes %2F into a real separator, so "/-/tree/feature%2Fx" arrives
 * as the ref "feature" and the path "x". A ref with a slash therefore travels in
 * the query string, while a ref without one stays in the path where it reads
 * better. Both forms have to be produced in one place, or a page links somewhere
 * its own parser cannot read.
 */

/** A ref is safe to put in the path only when it has no slash in it. */
export function refFitsInPath(ref: string): boolean {
  return !!ref && !ref.includes('/')
}

/**
 * Views that address a file or a directory, where the ref is the first path
 * segment and everything after it is the path inside the repository.
 *
 * Every other view lists something, and its only path segment is something else
 * entirely: "/-/merge_requests/7" is request seven, not the ref "7". Those views
 * carry the ref in the query so the two can never be confused.
 */
const pathViews = new Set(['tree', 'blob', 'edit'])

/**
 * The address of a repository view.
 *
 * The result carries the ref in the path when the view allows it and the ref has
 * no slash, and in the query otherwise, so the same link works for "main" and for
 * "feature/deep/name".
 */
export function repoViewUrl(
  projectPath: string,
  view: string,
  ref: string,
  path = '',
): string {
  const base = `/p/${projectPath}/-/${view}`
  const encodedPath = path.split('/').filter(Boolean).map(encodeURIComponent).join('/')

  if (pathViews.has(view) && refFitsInPath(ref)) {
    return encodedPath
      ? `${base}/${encodeURIComponent(ref)}/${encodedPath}`
      : `${base}/${encodeURIComponent(ref)}`
  }

  const query = new URLSearchParams({ ref })
  if (encodedPath) query.set('path', encodedPath)
  return `${base}?${query.toString()}`
}