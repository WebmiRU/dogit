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
        },
      },
    },
  },

  runtimeConfig: {
    // Overridable at runtime so one image works in every environment.
    public: {
      apiBase: process.env.NUXT_PUBLIC_API_BASE || '/api/v1',
    },
  },

  css: ['~/assets/css/main.css'],

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
