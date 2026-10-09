<script setup lang="ts">
/**
 * The Docker registries on this instance.
 *
 * A table of addresses somebody wrote down: where each one is, what it is called it, and
 * the three decisions that make it usable rather than merely written — whether its
 * certificate is one to believe, whether images may be pushed to it, and whether it is the
 * one pushes go to.
 *
 * Nothing here polls and nothing here watches the event feed, which is the difference
 * between this page and every other list in this interface. A registry does not roll out,
 * does not report progress and does not go quiet without somebody having closed something.
 * A list that refetched itself every second would be borrowing the urgency of a deployment
 * page to describe a table.
 *
 * The password is written and never displayed. It is not in the answer from the core, so
 * this page shows whether one is set and never what it is, and the edit form starts empty
 * and says what an empty field there means.
 */
import type { DockerRegistry, DockerRegistryPage } from '~/types/registry'

const { user } = useAuth()

const route = useRoute()

const registries = ref<DockerRegistry[]>([])
const total = ref(0)
const pages = ref(1)
/** Which page, read out of the address bar — so it can be linked to and survives a reload. */
const page = computed(() => Math.max(1, Number(route.query.page ?? 1) || 1))
/** How many to a page, as the core says — not a number written down here beside it. */
const perPage = ref(10)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const answer = await api.get<DockerRegistryPage>('/registry/docker', { page: page.value })
    // A page past the end is a page with nothing on it, which is a true sentence about
    // the address bar and a useless one about the list: two registries were deleted and
    // this address still says page nine. The last page that has records is what the
    // reader meant, so that is where they are sent.
    if (answer.past_the_end) {
      await navigateTo({ path: route.path, query: answer.pages > 1 ? { page: answer.pages } : {} })
      return
    }
    registries.value = answer.registries
    total.value = answer.total
    pages.value = answer.pages
    perPage.value = answer.per_page
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  if (!user.value?.is_admin) {
    loading.value = false
    return
  }
  void load()
})

// The page in the address changes what is asked for, so a change of it is a change of
// what to ask for — including when the address arrived from a link rather than from the
// pager. Watching the query is what makes "page 2" in a pasted address work.
watch(page, () => void load())

/** Which ten of how many, for the left of the pager. */
const range = computed(() => {
  if (total.value === 0) return 'none'
  const first = (page.value - 1) * perPage.value + 1
  const last = Math.min(first + registries.value.length - 1, total.value)
  return `${first}–${last} of ${total.value}`
})

/**
 * Another page of the list.
 *
 * Written into the address rather than into a variable: a list of twelve registries has
 * two pages, and a reader on the second one who copies the address to send it to a
 * colleague should send the second page and not the first.
 */
function go(to: number) {
  const next = Math.min(Math.max(to, 1), pages.value)
  if (next === page.value) return
  void navigateTo({ path: route.path, query: next === 1 ? {} : { page: next } })
}

/**
 * Where this row came from, which is the one thing about it that decides what a reader may
 * do with it.
 *
 * A column rather than a decoration on the name, because the name is the thing being read:
 * a badge beside the address says "this one is special", and a column says which kind it
 * is, which is the question a reader asks about every row they look at.
 */
function sourceOf(reg: DockerRegistry): { label: string; title: string } {
  if (reg.source === 'module') {
    return {
      label: 'module',
      title: 'A registry this instance runs. Its address and its settings live on the module’s own page, which is where they are changed.',
    }
  }
  return {
    label: 'written',
    title: 'An address an administrator wrote down. Edited here.',
  }
}

/** A module's registry has no Edit button, and says where it is changed instead. */
function isModule(reg: DockerRegistry): boolean {
  return reg.source === 'module'
}

/**
 * The switches, as words rather than as columns.
 *
 * A column per switch would make a table eight columns wide for a fact that is one of three
 * states, and all of them are what a reader wants to see at a glance. Inline, a registry that
 * is off says so where it is.
 *
 * A module's row says its own state instead, because that is what is worth knowing about
 * it: a registry module that is switched off is not a fact about how images are pushed, it
 * is a fact about why they stopped being.
 */
function switches(reg: DockerRegistry): string[] {
  const said: string[] = []
  if (isModule(reg)) {
    // A module's row says what the module last reported about itself, because that is what
    // is worth knowing about it here: a registry module that is not answering is not a fact
    // about how images are pushed, it is a fact about why they stopped being.
    if (reg.status) said.push(reg.status)
    if (!reg.enabled) said.push('switched off')
    if (reg.published === false) said.push('publishes no address')
    return said
  }
  if (reg.enabled === false) said.push('off')
  if (reg.read_only) said.push('pull only')
  if (reg.insecure_tls) said.push('no TLS check')
  if (reg.login && !reg.has_password) said.push('login, no password')
  return said
}

/** What a row is called: the name if there is one, and the address if there is not. */
function labelOf(reg: DockerRegistry): string {
  return reg.name || reg.url || '—'
}
</script>

<template>
  <div>
    <div class="repo-head">
      <div class="title">
        <h1 class="page-title" style="margin: 0">Docker registries</h1>
        <p class="page-subtitle">
          Addresses, credentials and how each one is reached. The ones this instance runs as
          a module come first and are changed on the module’s own page; the rest are written
          down here.
        </p>
      </div>
      <button class="btn btn-primary registry-new" type="button" @click="navigateTo('/registry/docker/new')">
        <span class="registry-new-plus" aria-hidden="true">＋</span>
        New registry
      </button>
    </div>

    <div v-if="!user?.is_admin" class="alert alert-info">
      This page requires administrator rights.
    </div>

    <template v-else>
      <div v-if="error" class="alert alert-error">{{ error }}</div>

      <div v-if="loading && registries.length === 0" class="spinner">Loading registries…</div>

      <div v-else-if="!loading && registries.length === 0" class="card empty">
        No registries are written down. A registry is an address — a host, a port if it
        has one — plus whatever it needs to be reached: a login for a private one, nothing
        for a public one.
      </div>

      <table v-else class="admin-table images-table">
        <thead>
          <tr>
            <th>Registry</th>
            <th>Source</th>
            <th>Address</th>
            <th>Login</th>
            <th>How it is reached</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="reg in registries" :key="reg.integration_id ?? reg.id">
            <td>
              <!-- A module's registry leads to the module, because that is where it is
                   changed; a written-down one leads to its own form. -->
              <NuxtLink
                v-if="isModule(reg)"
                class="name"
                :to="`/admin/modules/${reg.integration_id}`"
                :title="sourceOf(reg).title"
              >
                {{ labelOf(reg) }}
              </NuxtLink>
              <NuxtLink v-else class="name" :to="`/registry/docker/${reg.id}`">
                {{ labelOf(reg) }}
              </NuxtLink>
              <span v-if="reg.note" class="images-note" :title="reg.note">{{ reg.note }}</span>
            </td>
            <td>
              <span
                class="source"
                :class="isModule(reg) ? 'source-module' : 'source-written'"
                :title="sourceOf(reg).title"
              >
                {{ sourceOf(reg).label }}
              </span>
            </td>
            <td>
              <code v-if="reg.url">{{ reg.url }}</code>
              <span v-else class="muted">publishes no address</span>
            </td>
            <td>
              <span v-if="reg.login">{{ reg.login }}</span>
              <span v-else class="muted">—</span>
            </td>
            <td>
              <span v-if="switches(reg).length === 0" class="muted">—</span>
              <span v-for="said in switches(reg)" :key="said" class="chip">{{ said }}</span>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="images-pager">
        <span class="muted small">{{ range }}</span>
        <div class="spacer" />
        <button class="btn btn-small" type="button" :disabled="page <= 1" @click="go(page - 1)">
          Previous
        </button>
        <span class="muted small">page {{ page }} of {{ pages }}</span>
        <button class="btn btn-small" type="button" :disabled="page >= pages" @click="go(page + 1)">
          Next
        </button>
      </div>
    </template>
  </div>
</template>

