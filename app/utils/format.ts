/** Formatting helpers shared across views. */

/**
 * An image as a person reads it: the repository, and the digest in brackets.
 *
 * The digest without its `sha256:` and in brackets, because the algorithm is a
 * convention everybody already knows and brackets are not:
 * `reg/app[bc9b83f2da0e]` reads as one thing made of a name and a digest, where
 * `reg/app@sha256:bc9b83f2da0e` reads as a path, a scheme and a value — and spends
 * seven characters of a row that has other things to say.
 *
 * The full address, algorithm and all, is what the title attribute carries: this is for
 * reading, that one is for copying.
 */
export function shortImage(image: string): string {
  if (!image) return '—'
  const at = image.indexOf('@')
  if (at < 0) return image
  const repository = image.slice(0, at)
  const digest = image.slice(at + 1).replace(/^[a-z0-9]+:/, '')
  if (!digest) return repository
  return `${repository}[${digest.slice(0, 12)}]`
}

/** Just the digest, short and without its algorithm — for a line naming it beside a tag. */
export function shortDigest(image: string): string {
  const at = image.indexOf('@')
  const digest = (at < 0 ? image : image.slice(at + 1)).replace(/^[a-z0-9]+:/, '')
  return digest ? `[${digest.slice(0, 12)}]` : ''
}

/**
 * The digest on its own: short, no algorithm, and no brackets.
 *
 * A third form because both of the others are tied to where they are read, and neither
 * survives being moved. shortImage squashes the digest into the name as
 * "repository[digest]", which reads as one identifier written oddly rather than as a name
 * and a digest. shortDigest's brackets are for a digest sitting inside a sentence, where
 * something has to set it off from the words around it — in a cell of its own, under a
 * column already headed Digest, they are only noise.
 */
export function bareDigest(image: string): string {
  const at = image.indexOf('@')
  const digest = (at < 0 ? image : image.slice(at + 1)).replace(/^[a-z0-9]+:/, '')
  return digest ? digest.slice(0, 12) : ''
}

/** Relative time such as "3 minutes ago", falling back to an absolute date. */
export function timeAgo(value: string | Date | undefined | null): string {
  if (!value) return ''
  const date = typeof value === 'string' ? new Date(value) : value
  if (Number.isNaN(date.getTime())) return ''

  const seconds = Math.round((Date.now() - date.getTime()) / 1000)
  if (seconds < 45) return 'just now'

  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ['minute', 60],
    ['hour', 3600],
    ['day', 86400],
    ['month', 2592000],
    ['year', 31536000],
  ]

  let unit: Intl.RelativeTimeFormatUnit = 'minute'
  let size = 60
  for (const [candidate, divisor] of units) {
    if (seconds >= divisor) {
      unit = candidate
      size = divisor
    }
  }

  const formatter = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })
  return formatter.format(-Math.round(seconds / size), unit)
}

export function formatDate(value: string | Date | undefined | null): string {
  if (!value) return ''
  const date = typeof value === 'string' ? new Date(value) : value
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString()
}

export function formatBytes(size: number | undefined | null): string {
  if (!size) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = size
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

/** Shortens a commit message to its first line. */
/**
 * A duration in the words somebody would say out loud.
 *
 * A job that took ninety seconds is "1m 30s", not "90000 ms": a number in
 * milliseconds has to be divided in the head, and the whole point of showing a
 * duration is to save that.
 */
export function formatDuration(ms: number | undefined | null): string {
  if (!ms || ms < 0) return ''

  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}s`

  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  if (minutes < 60) return rest ? `${minutes}m ${rest}s` : `${minutes}m`

  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  return restMinutes ? `${hours}h ${restMinutes}m` : `${hours}h`
}

export function firstLine(message: string | undefined | null): string {
  if (!message) return ''
  const index = message.indexOf('\n')
  return index === -1 ? message : message.slice(0, index)
}

/** Escapes text for safe insertion into HTML, used by the highlighter. */
export function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}
