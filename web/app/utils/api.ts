/**
 * Thin client for the dogit REST API.
 *
 * The session lives in an httpOnly cookie, so every request must send
 * credentials. Requests are same-origin in production (nginx proxies /api to
 * Go) and cross-origin in development, which is why the CSRF-relevant
 * Sec-Fetch-Site header is not set here: browsers set it themselves, and forging
 * it from JavaScript is impossible.
 */

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }

  get isUnauthorized() {
    return this.status === 401
  }
}

function apiBase(): string {
  const config = useRuntimeConfig()
  return (config.public.apiBase as string) || '/api/v1'
}

/** Builds a URL with query parameters, skipping empty values. */
export function apiUrl(path: string, query: Record<string, string | number | undefined | null> = {}): string {
  const base = apiBase().replace(/\/$/, '')
  const url = `${base}${path.startsWith('/') ? path : `/${path}`}`

  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue
    params.set(key, String(value))
  }
  const qs = params.toString()
  return qs ? `${url}?${qs}` : url
}

interface RequestOptions {
  method?: string
  body?: unknown
  query?: Record<string, string | number | undefined | null>
  signal?: AbortSignal
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, query, signal } = options

  const response = await fetch(apiUrl(path, query), {
    method,
    credentials: 'include',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })

  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  let payload: any = undefined
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      throw new ApiError(response.status, 'invalid_response', 'the server returned malformed JSON')
    }
  }

  if (!response.ok) {
    const error = payload?.error
    throw new ApiError(
      response.status,
      error?.code ?? 'error',
      error?.message ?? `request failed with status ${response.status}`,
    )
  }

  return payload as T
}

export const api = {
  get: <T>(path: string, query?: RequestOptions['query'], signal?: AbortSignal) =>
    request<T>(path, { query, signal }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body }),
  patch: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PATCH', body }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
}

/** Absolute URL for endpoints consumed outside the JSON client, such as raw files. */
export function rawApiUrl(path: string, query?: RequestOptions['query']): string {
  return apiUrl(path, query)
}
