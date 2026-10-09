<script setup lang="ts">
/** Sign in and registration. */
const { login, register, ensureLoaded } = useAuth()
const route = useRoute()

const mode = ref<'login' | 'register'>('login')
const form = reactive({
  login: '',
  email: '',
  name: '',
  password: '',
  password_confirmation: '',
})

const error = ref('')
const submitting = ref(false)

onMounted(async () => {
  // Already signed in: nothing to do on this page.
  const current = await ensureLoaded()
  if (current) navigateTo('/')
})

async function submit() {
  error.value = ''
  submitting.value = true
  try {
    if (mode.value === 'login') {
      await login(form.login, form.password)
    } else {
      await register({
        username: form.login,
        email: form.email,
        name: form.name,
        password: form.password,
        password_confirmation: form.password_confirmation,
      })
    }
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await navigateTo(redirect)
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <div class="card auth-card">
      <div class="card-body">
        <h1>dogit</h1>
        <p class="muted">Sign in to your account</p>

        <div class="auth-tabs">
          <button
            type="button"
            :class="{ 'is-active': mode === 'login' }"
            @click="mode = 'login'"
          >
            Sign in
          </button>
          <button
            type="button"
            :class="{ 'is-active': mode === 'register' }"
            @click="mode = 'register'"
          >
            Register
          </button>
        </div>

        <div v-if="error" class="alert alert-error">{{ error }}</div>

        <form @submit.prevent="submit">
          <div class="field">
            <label for="login">{{ mode === 'login' ? 'Username or email' : 'Username' }}</label>
            <input id="login" v-model="form.login" autocomplete="username" required />
          </div>

          <template v-if="mode === 'register'">
            <div class="field">
              <label for="email">Email</label>
              <input id="email" v-model="form.email" type="email" autocomplete="email" required />
            </div>
            <div class="field">
              <label for="name">Display name</label>
              <input id="name" v-model="form.name" autocomplete="name" />
            </div>
          </template>

          <div class="field">
            <label for="password">Password</label>
            <input
              id="password"
              v-model="form.password"
              type="password"
              :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
              required
            />
          </div>

          <div v-if="mode === 'register'" class="field">
            <label for="password2">Confirm password</label>
            <input
              id="password2"
              v-model="form.password_confirmation"
              type="password"
              autocomplete="new-password"
              required
            />
          </div>

          <button class="btn btn-primary" type="submit" :disabled="submitting" style="width: 100%">
            {{ submitting ? 'Please wait…' : mode === 'login' ? 'Sign in' : 'Create account' }}
          </button>
        </form>

        <p v-if="mode === 'register'" class="muted" style="margin-top: 14px; font-size: 12px">
          The first account on a new instance becomes its administrator.
        </p>
      </div>
    </div>
  </div>
</template>
