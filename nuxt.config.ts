// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: '2025-07-15',

  // The API holds the session in an httpOnly cookie and all state is private, so
  // server-side rendering would only duplicate requests and add a Node process
  // to run in production. The app is a plain SPA served as static files.
  ssr: false,

  devtools: { enabled: false },

  devServer: {
    // The dev server takes the port the production container uses, so the URL in
    // the browser does not change between development and production. Run
    // `docker compose up -d postgres app runner` while developing: the compose
    // "web" service is only needed for the built frontend.
    port: 3000,
  },

  // Hot reload needs the API to be reachable from the dev server: without this
  // proxy the SPA would request /api from itself and get the HTML shell back.
  vite: {
    server: {
      proxy: {
        '/api': {
          target: 'http://localhost:8080',
          changeOrigin: false,
          // WebSocket upgrades as well as ordinary requests. Without this the proxy
          // passes HTTP through and refuses the upgrade, so every socket closes at once
          // with 1006 — which looks exactly like the server refusing us and is nothing
          // of the kind.
          ws: true,
        },
      },
    },
  },

  runtimeConfig: {
    // Overridable at runtime so one image works in every environment.
    public: {
      apiBase: process.env.NUXT_PUBLIC_API_BASE || '/api/v1',
      // Empty means "same origin as this page". Set in development, where the dev
      // server's proxy carries ordinary requests but not protocol upgrades.
      eventSocketURL: process.env.NUXT_PUBLIC_EVENT_SOCKET_URL || '',
    },
  },

  css: ['~/assets/sass/app.sass'],

  // The group list and the group page share a path prefix, which by default makes
  // the list their parent and the detail a child rendered through a <NuxtPage>
  // the list does not have. The two pages are siblings in every sense that
  // matters, so the detail route is lifted to the top level.
  hooks: {
    'pages:extend'(pages) {
      for (const page of pages) {
        const detail = page.children?.find((child) => child.path === ':id()')
        if (!detail) continue

        page.children = page.children?.filter((child) => child !== detail)
        if (page.children?.length === 0) delete page.children

        // The parent link is internal to the router builder and is not part of the
        // published type, so it is cleared through a narrow cast.
        ;(detail as { parent?: unknown }).parent = undefined
        detail.path = `${page.path}/:id()`
        pages.push(detail)
      }
    },
  },

  app: {
    head: {
      title: 'dogit',
      meta: [
        { charset: 'utf-8' },
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      ],
    },
  },

  nitro: {
    // Static hosting: `nuxt generate` writes the SPA shell plus assets here, and
    // nginx serves the result. Node is never part of the runtime.
    output: { publicDir: 'dist' },
    prerender: {
      crawlLinks: false,
      routes: ['/'],
    },
  },
})
