/**
 * Authenticated user state.
 *
 * The user is fetched once per page load; the session itself lives in an httpOnly
 * cookie the JavaScript cannot read, which is why there is no token in this
 * state and a 401 simply means "not signed in".
 */

export interface CurrentUser {
  id: string
  username: string
  email: string
  name: string
  is_admin: boolean
  created_at: string
}

interface CurrentUserResponse {
  user: CurrentUser
  project_count?: number
}

const user = ref<CurrentUser | null>(null)
const loaded = ref(false)
const loading = ref(false)

export function useAuth() {
  /** Fetches the current user once, caching the result. */
  async function ensureLoaded(force = false): Promise<CurrentUser | null> {
    if (loaded.value && !force) return user.value
    if (loading.value) return user.value

    loading.value = true
    try {
      const response = await api.get<CurrentUserResponse>('/user')
      user.value = response.user
    } catch (error) {
      if (!(error instanceof ApiError) || !error.isUnauthorized) throw error
      user.value = null
    } finally {
      loaded.value = false
      loaded.value = true
      loading.value = false
    }
    return user.value
  }

  async function login(login: string, password: string) {
    const response = await api.post<CurrentUserResponse>('/auth/login', { login, password })
    user.value = response.user
    loaded.value = true
    return response.user
  }

  async function register(payload: {
    username: string
    email: string
    name?: string
    password: string
    password_confirmation: string
  }) {
    const response = await api.post<CurrentUserResponse>('/auth/register', payload)
    user.value = response.user
    loaded.value = true
    return response.user
  }

  async function logout() {
    await api.post('/auth/logout')
    user.value = null
    loaded.value = true
    await navigateTo('/login')
  }

  const isAuthenticated = computed(() => user.value !== null)

  return { user, isAuthenticated, loading, ensureLoaded, login, register, logout }
}
