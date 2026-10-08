<script setup lang="ts">
/**
 * What a module's data lives in, and what is waiting for it.
 *
 * Its own tab rather than a line above the settings form, because the line could never be
 * honest. There are four situations here and they each mean something different: a resource
 * being held, a database name with nothing behind it, a module that asked for a database and
 * has none, and a module that never wanted one. Crammed into one row above a form they read
 * as four variants of a footnote.
 *
 * No secret, and none fetched. A page that shows a host by opening a password is a page that
 * eventually prints one, so what is here is what the columns hold and nothing else.
 */
interface Resource {
  id: string
  kind: string
  software?: string
  version?: string
  name?: string
  origin: 'managed' | 'manual' | string
  parts?: Record<string, string>
  created_at: string
  released_at?: string
}

const props = defineProps<{
  /** The resource this module holds, if any. */
  held?: Resource | null
  /** Resources described for this kind of module that nothing holds yet. */
  awaiting?: Resource[]
  /** Whether the module asked for a database at registration. */
  wantsOne: boolean
  /** The name recorded on the module, which a deleted resource leaves behind. */
  recordedName?: string
  /** The role, for a database this instance made. */
  role?: string
}>()

/** What a thing is, the way a requirement names it: db:postgresql:19. */
function coordinate(one: Resource): string {
  let out = one.kind
  if (one.software) out += `:${one.software}`
  if (one.version) out += `:${one.version}`
  return out
}

/**
 * Where it is, in one cell, and the parts are chosen by the kind rather than printed in
 * whatever order they came.
 *
 * A row of label-and-value pairs is a wall of six short cells for five facts, and the host is
 * the only one anybody looks for. Bucket included because for an object store it is the
 * answer, and a URL without it says where the server is rather than what is being used.
 */
function where(one: Resource): string {
  const parts = one.parts ?? {}
  if (one.kind === 'db') {
    const host = parts.host ?? ''
    const name = parts.database_name ?? ''
    const user = parts.username ? `${parts.username}@` : ''
    return `${user}${host}${name ? '/' + name : ''}`
  }
  if (one.kind === 's3') {
    const endpoint = parts.endpoint ?? ''
    const bucket = parts.bucket ? '/' + parts.bucket : ''
    return `${endpoint}${bucket}`
  }
  return parts.endpoint ?? ''
}

/** The parts that are worth a column of their own: the ones a reader compares by eye. */
function detail(one: Resource): string[] {
  const parts = one.parts ?? {}
  if (one.kind === 'db') {
    return [parts.port ? `port ${parts.port}` : '', parts.username ? `as ${parts.username}` : '']
      .filter(Boolean)
  }
  const out = []
  if (parts.region) out.push(parts.region)
  if (parts.access_key) out.push(`key ${parts.access_key}`)
  return out
}

function originWord(one: Resource): string {
  return one.origin === 'managed' ? 'Made here' : 'Described'
}

function originClass(one: Resource): string {
  return one.origin === 'managed' ? 'badge-blue' : 'badge-neutral'
}

/** Whether this instance may destroy it: only what it made. Decides what the page offers. */
function canDrop(one: Resource): boolean {
  return one.origin === 'managed'
}

const awaiting = computed(() => props.awaiting ?? [])

/**
 * The one thing worth saying when there is nothing to show.
 *
 * Wording that carries the consequence. "None" means two different things depending on whether
 * the module wanted one, and the case where it wanted one and has none is a gap somebody has
 * to go and fix — so it is not dressed in the same words as a module that never wanted one.
 */
const nothing = computed(() => {
  if (props.recordedName) {
    return {
      title: props.recordedName,
      body: 'This instance has the name of a database for this module and no database to go '
          + 'with it: the resource it pointed at was deleted and the name outlived it. The '
          + 'name is cleared at the module’s next registration, and the module is given '
          + 'whatever there is then.',
      warn: false,
    }
  }
  if (props.wantsOne) {
    return {
      title: 'None',
      body: 'This module asked for a database when it registered and has none. That is a gap '
          + 'rather than a choice, and worth telling somebody about.',
      warn: true,
    }
  }
  return {
    title: 'None',
    body: 'This module declared from the start that it keeps its data elsewhere, so there is '
        + 'nothing here and nothing missing.',
    warn: false,
  }
})

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString()
}
</script>

<template>
  <div class="resources-tab">
    <!-- What it holds. One row at most, by the one-resource-per-module rule, and the rule is
         said here rather than left for somebody to discover when the table has two rows. -->
    <h2 class="section-title">What this module holds</h2>

    <table v-if="held" class="table">
      <thead>
        <tr>
          <th>What</th>
          <th>Where it is</th>
          <th>Detail</th>
          <th>Where it came from</th>
          <th>Since</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr>
          <td class="nowrap"><span class="mono">{{ coordinate(held) }}</span></td>
          <td>
            <span class="mono">{{ where(held) }}</span>
            <span v-if="held.name" class="muted small">— {{ held.name }}</span>
          </td>
          <td>
            <span v-for="one in detail(held)" :key="one" class="muted small">{{ one }}</span>
          </td>
          <td><span class="badge" :class="originClass(held)">{{ originWord(held) }}</span></td>
          <td class="nowrap muted small">{{ formatDate(held.created_at) }}</td>
          <td class="right muted small">
            <NuxtLink to="/admin/resources" class="btn btn-small">On the Resources page</NuxtLink>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-else class="card empty">
      <p>
        <strong>{{ nothing.title }}</strong>
      </p>
      <p class="muted">{{ nothing.body }}</p>
    </div>

    <!-- What is waiting. The part a line above the settings form could not have carried: a
         resource described for this kind of module, sitting free because nothing holds it.
         This is the answer to "I wrote one down and nothing happened", and it is invisible
         on every other page. -->
    <h2 class="section-title">Waiting for a module of this kind</h2>

    <table v-if="awaiting.length" class="table">
      <thead>
        <tr>
          <th>What</th>
          <th>Where it is</th>
          <th>Detail</th>
          <th>Where it came from</th>
          <th>Described</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="one in awaiting" :key="one.id">
          <td class="nowrap"><span class="mono">{{ coordinate(one) }}</span></td>
          <td>
            <span class="mono">{{ where(one) }}</span>
            <span v-if="one.name" class="muted small">— {{ one.name }}</span>
          </td>
          <td>
            <span v-for="part in detail(one)" :key="part" class="muted small">{{ part }}</span>
          </td>
          <td><span class="badge" :class="originClass(one)">{{ originWord(one) }}</span></td>
          <td class="nowrap muted small">{{ formatDate(one.created_at) }}</td>
        </tr>
      </tbody>
    </table>

    <div v-else class="card empty">
      <p class="muted">
        Nothing is waiting. A resource described for this kind of module would be listed here
        until a module of this kind registers, and would then be given to it.
      </p>
    </div>

    <p v-if="held && canDrop(held)" class="muted small footnote">
      This instance made this database, so it may also drop it — from the Resources page, and
      only there.
    </p>
  </div>
</template>

<style scoped>
.resources-tab {
  display: flex;
  flex-direction: column;
}

.table td {
  vertical-align: top;
}

.table .detail {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.footnote {
  margin-top: 14px;
}
</style>