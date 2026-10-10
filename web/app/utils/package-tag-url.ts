/**
 * Stable anchors for individual image tags.
 *
 * The same image appears in a project's Images tab and in the instance-wide registry
 * catalogue. A code-point encoding gives both views the same URL fragment even when
 * repository or tag names contain slashes and punctuation.
 */
function anchorPart(value: string): string {
  return Array.from(value, (character) => character.codePointAt(0)!.toString(16)).join('_')
}

export function packageTagId(repository: string, tag: string): string {
  return `package-tag-${anchorPart(repository)}--${anchorPart(tag)}`
}

export function packageTagUrl(projectPath: string, repository: string, tag: string): string {
  return `/p/${projectPath}/-/packages#${packageTagId(repository, tag)}`
}
