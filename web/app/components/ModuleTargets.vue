<script setup lang="ts">
/**
 * The rows a module may act on, at one level.
 *
 * A row here is one place: a module, an address, and whether this place may use it.
 * Not a module — a module can be pointed at two chats, and a chat can be inherited by
 * twenty repositories, so the row is where the two meet.
 *
 * This list is the same list for every kind of module, because it is the same question:
 * a notification module writes to a chat, a deployment module writes to a cluster, and
 * in both cases the questions asked of a row are the same three. Which is who has it,
 * what it is called, and is it switched on here. What the rows are called is the
 * module's own word for them, which it declares in its manifest; what this page calls
 * them is the only thing that changes, and it is `words` below.
 *
 * The same component serves the instance, a group and a project, because it is the
 * same question at each: what does this place get, and what did it decide itself.
 *
 * Two rules shape what is shown:
 *
 * - A row somebody has changed says so, and says which level changed it. A list that
 *   cannot answer "whose setting is this" is a list nobody will trust enough to
 *   switch anything off in.
 * - Nothing here decides what anything may do. That belongs to the configuration, which
 *   says so in its own file; this list only says where a message would go, or where a
 *   release may be put.
 */
import type { SettingSpec } from '~/types/module'
import type {
  ModuleRow, ModuleRowDraft, ModuleRowFlag, ModuleRowsAnswer,
} from '~/types/notification'

const props = withDefaults(
  defineProps<{
    /**
     * Which modules' rows these are, as a kind prefix.
     *
     * Which is not cosmetic. Every kind of module has rows in the same table, and a
     * list that did not say which kind it wanted would show a project the chat
     * channels it can be told on next to the clusters it can be deployed to — two
     * lists of "places this project may act in", indistinguishable, and one of them
     * carrying credentials nobody asked to see.
     */
    kind?: string
    /** Whose rows these are: instance, group or project. */
    scope?: 'instance' | 'group' | 'project'
    scopeId?: string
    canManage?: boolean
    /**
     * One module, when the page is that module's own.
     *
     * The module page shows its own rows and nothing else: somebody reading
     * "Telegram writes to these two chats" is not helped by a list of where email
     * goes as well.
     */
    onlyModule?: string
    /** Which module a newly added row belongs to. */
    defaultModule?: string
    /**
     * How the rows are drawn: as one table, or as blocks with room under each row.
     *
     * A table is right when a row is a line and the page is about the list. It is
     * wrong the moment a row has something under it — and for a deployment module every
     * row does: the status, the log, the operations and the images of one place. Stacked
     * under a table with nothing to attach them to, three places give three histories
     * and no way to tell which is which, and no way to open the second one.
     */
    layout?: 'table' | 'blocks'
    /**
     * The project this page is about, where there is one.
     *
     * Needed for the things that are about a project rather than about a row: asking
     * a cluster whether it answers is a question about this project's route to it,
     * and the core has to know which project is asking.
     */
    projectId?: string
  }>(),
  { scope: 'instance', kind: 'notify:', layout: 'table', canManage: true },
)

const emit = defineEmits<{
  /**
   * The rows as they were loaded.
   *
   * Said rather than fetched twice by the page: the page has to draw a block per
   * place, and it cannot do that from a list of names it does not have. Two reads of
   * one list would also be two moments at which the list was true.
   */
  (event: 'rows', rows: ModuleRow[]): void
}>()

const { add: notify } = useNotifyPool()

/**
 * What this list calls a row, in the singular and the plural.
 *
 * Taken from the kind rather than passed in separately, so that a page cannot say
 * "recipient" above a list of clusters: the noun and the kind are the same decision,
 * and giving them to a caller separately is how they come to disagree. Only two kinds
 * exist today, and an unrecognised one is called a row rather than guessed at.
 */
const words = computed(() => {
  const kind = props.kind
  if (kind.startsWith('deploy:')) {
    return {
      one: 'cluster', many: 'clusters', address: 'Where it goes',
      add: 'Add a cluster', adding: 'Add a cluster', changing: 'Change this cluster',
      named: 'the cluster', loading: 'Loading clusters…', nothing: 'cluster',
      empty: 'No deploy modules are installed',
      emptyBody:
        'Deployments are carried out by modules, and there are none here yet. Install '
        + 'one, and the places it can put a release will appear in this list.',
    }
  }
  return {
    one: 'recipient', many: 'recipients', address: 'Writes to',
    add: 'Add a recipient', adding: 'Add a recipient', changing: 'Change this recipient',
    named: 'the recipient', loading: 'Loading recipients…', nothing: 'recipient',
    empty: 'No notification modules are installed',
    emptyBody:
      'Notifications are delivered by modules, and there are none here yet. Install '
      + 'one — Telegram, email, a webhook — and the places it can write to will appear '
      + 'in this list.',
  }
})

const rows = ref<ModuleRow[]>([])
const available = ref<ModuleRowsAnswer['modules']>([])
/** What stopped applying and was removed, said once and then left alone. */
const stale = ref('')
const loading = ref(true)
const busy = ref('')

/** The row being added or edited, or null when the form is closed. */
const editing = ref<ModuleRowDraft | null>(null)
const formError = ref('')
const formBusy = ref(false)

/**
 * Whether this list can hold rows of more than one module.
 *
 * Decided by the rows rather than by the kind: "deploy:" is one kind and one module
 * today, and a second one installed tomorrow would make the column true by itself
 * without anybody deciding it should be.
 */
/**
 * Which rows have what is under them open.
 *
 * A row in blocks layout is a header for the rest of a place, and a header with nothing
 * under it is a question the page should answer by itself: the first row open, because
 * somebody opening this page has usually come about one place, and it is the one they
 * will read. Everything else waits until asked.
 */
const opened = ref<Record<string, boolean>>({})
const firstRowID = ref('')

function toggleRow(row: ModuleRow) {
  if (props.layout !== 'blocks') return
  opened.value = { ...opened.value, [row.id]: !opened.value[row.id] }
}

const isOpen = (row: ModuleRow) => {
  if (props.layout !== 'blocks') return true
  const asked = opened.value[row.id]
  if (asked !== undefined) return asked
  // The first row is open because somebody opening this page has usually come about one
  // place, and it is the one they will read. A row that is there and closed is a page
  // that says it has nothing to show until you click it.
  return row.id === firstRowID.value
}

const severalModules = computed(() => {
  const kinds = new Set(rows.value.map((row) => row.module_id))
  return kinds.size > 1
})

const scopeQuery = computed(() => {
  if (props.scope === 'instance') return 'scope=instance'
  const name = props.scope === 'group' ? 'groupID' : 'projectID'
  return `scope=${props.scope}&${name}=${encodeURIComponent(props.scopeId ?? '')}`
})

/** What this level is called, in the words used in the "from" column. */
const scopeWord = computed(() => {
  if (props.scope === 'group') return 'the group'
  if (props.scope === 'project') return 'the repository'
  return 'the instance'
})

function where(row: ModuleRow): string {
  if (row.scope_type === props.scope) return row.overridden ? 'overridden here' : 'this ' + scopeWord.value.replace('the ', '')
  const from = row.inherited_from === 'group' ? 'the group' : 'the instance'
  return row.overridden ? 'overridden here, ' + from : 'from ' + from
}

/**
 * Reads the list.
 *
 * Hiding what is already on screen while it does so is a choice this component does
 * not make: a person who flipped a switch is looking at that switch, and a blank
 * table in its place reads as a page that has forgotten what it just did.
 */
async function load() {
  if (rows.value.length === 0) loading.value = true
  try {
    const answer = await api.get<ModuleRowsAnswer>(`/module-targets?kind=${encodeURIComponent(props.kind)}&${scopeQuery.value}`)
    const modules = answer.modules ?? []
    rows.value = props.onlyModule
      ? (answer.targets ?? []).filter((row) => row.module_id === props.onlyModule)
      : (answer.targets ?? [])
    // The choice of module is this module's on its own page, so the list has one
    // entry and the form does not ask a question with a single answer.
    available.value = props.onlyModule ? modules.filter((one) => one.id === props.onlyModule) : modules
    if (!firstRowID.value && rows.value[0]) firstRowID.value = rows.value[0].id
    emit('rows', rows.value)

    // Settings that had stopped applying are gone by the time this list arrives, and
    // the count says so. Left out, a person who set something and cannot find it has
    // no way to tell a bug from something they never saved.
    stale.value = staleNotice(answer.stale_removed ?? 0)
  } catch (caught) {
    // Said in the corner rather than in the page: the list is either there or it is
    // not, and a strip of error above an empty table explains nothing.
    failed(caught, `the ${words.value.many} could not be read`)
  } finally {
    loading.value = false
  }
}

/**
 * What to say about settings that were removed.
 *
 * Only the singular and plural differ, and saying "1 settings" is the kind of thing
 * that makes people stop reading notices.
 */
function staleNotice(dropped: number): string {
  if (dropped <= 0) return ''
  if (dropped === 1) {
    return 'One setting of this project\u2019s no longer applied after a move and was removed.'
  }
  return `${dropped} settings of this project\u2019s no longer applied after a move and were removed.`
}

/** How to call a recipient in a sentence. */
/**
 * What this level changed about the row, in the module's own words for the fields.
 *
 * The page's two ways of changing a place look the same and are not: the module's
 * settings are what the module is (how long to wait, whether to tidy up), and a row is
 * one destination. So a row says which of its own values this project decided, which is
 * the question "why does this project behave differently from the others" — and it is
 * the only place the answer can be, since the values on screen are the effective ones
 * and do not say which of them came from here.
 */
function changedHere(row: ModuleRow): string[] {
  const specs = settingsOf(row.module_id)
  const said: string[] = []

  for (const key of Object.keys(row.set_here ?? {})) {
    if (!row.set_here?.[key]) continue
    const label = specs.find((one) => one.key === key)?.label ?? key
    said.push(`${label}: ${asText(row.own_values?.[key] ?? row.values?.[key])}`)
  }

  // The switches this level decided, said in the module's own words for them.
  //
  // Counted here because a level that decided only a switch has decided something, and
  // "changed here" that lists nothing reads as "nothing was changed here" — which is
  // exactly wrong when the row beneath it carries this level's decisions.
  for (const flag of flagsOf(row)) {
    if (row.flags_here?.[flag.key] === undefined) continue
    said.push(`${flag.label}: ${row.flags_here[flag.key] ? 'on' : 'off'}`)
  }
  return said
}

/** A value as a short piece of text, for a row of overrides. */
function asText(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  const text = String(value)
  // A credential pasted into a row is not worth printing in full on a list, and the
  // form shows it in full to whoever changes it.
  return text.length > 40 ? `${text.slice(0, 40)}…` : text
}

function describe(row: ModuleRow): string {
  const address = addressOf(row)
  return address === '—' ? moduleName(row) : `${moduleName(row)} ${address}`
}

/**
 * Says what happened, or why it did not.
 *
 * Through the pool rather than a banner in the page: the answer to "did that work"
 * belongs with every other answer, in the corner, and not in the place where the
 * thing that was just saved used to be.
 */
function failed(caught: unknown, what: string) {
  notify(`${what}: ${caught instanceof ApiError ? caught.message : 'the request failed'}`, {
    type: 'error',
  })
}

/**
 * The settings that make a recipient of this module, in the module's own words.
 *
 * Not all of them: a module says which of its settings are a destination and which
 * are its own, and only the first kind is asked for here. That is what keeps a bot
 * token — the module's business — off the page of somebody's repository.
 */
function settingsOf(moduleId: string): SettingSpec[] {
  const module = available.value.find((one) => one.id === moduleId)
  // The fields the module described for a row. Read from the target rather than from the
  // module's settings, because a row's values are not the module's settings: a deploy
  // module has no "kubeconfig" setting, it has a kubeconfig on a cluster, and reading
  // the form out of the settings list is what left it with one text box.
  const described = module?.target?.fields
  if (described?.length) return described
  const all = module?.settings ?? []
  const ofTarget = module?.target?.settings
  if (!ofTarget?.length) return all
  return ofTarget.map((key) => all.find((spec) => spec.key === key)).filter((spec): spec is SettingSpec => !!spec)
}

/** What a recipient of this module is called in a list. */
function addressOf(row: ModuleRow): string {
  const identify = available.value.find((one) => one.id === row.module_id)?.target?.identify ?? []
  for (const key of identify) {
    const value = row.values?.[key]
    if (value !== undefined && value !== null && String(value) !== '') return String(value)
  }
  return '—'
}

/**
 * Whether a row's name is worth printing.
 *
 * A module that names a recipient after its address — "chat -100…" — has not said
 * anything the address does not already say, and printing both makes a list twice as
 * long as it needs to be.
 */
function showsLabel(row: ModuleRow): boolean {
  const address = addressOf(row)
  return row.label !== '' && !row.label.includes(address) && address !== '—'
}

function moduleName(row: ModuleRow): string {
  return row.module_name || row.module_kind.replace(props.kind, '')
}

function openAdd() {
  const first =
    available.value.find((one) => one.id === props.defaultModule) ?? available.value[0]
  editing.value = {
    id: '',
    moduleId: first?.id ?? '',
    label: '',
    values: {},
    overrideId: '',
  }
  formError.value = ''
}

/** Change an inherited row here: what differs is written down, the rest is inherited. */
function openOverride(row: ModuleRow) {
  editing.value = {
    id: row.id,
    moduleId: row.module_id,
    label: row.label,
    values: Object.fromEntries(
      settingsOf(row.module_id).map((spec) => [spec.key, String(row.own_values?.[spec.key] ?? '')]),
    ),
    overrideId: row.id,
  }
  formError.value = ''
}

function closeForm() {
  editing.value = null
  formError.value = ''
}

async function save() {
  if (!editing.value) return
  formBusy.value = true
  formError.value = ''

  const body = {
    module_id: editing.value.moduleId,
    label: editing.value.label,
    enabled: true,
    values: editing.value.values,
    overrides: editing.value.overrideId,
  }

  try {
    // The scope travels with the write. Left off, the core is told the instance is
    // meant — which an administrator's session makes possible, and which would
    // change every repository on the instance.
    if (editing.value.id) {
      await api.patch(`/module-targets/${editing.value.id}?${scopeQuery.value}`, body)
    } else {
      await api.post(`/module-targets?${scopeQuery.value}`, body)
    }
    // A form is the one place a read afterwards is worth it: the row that was just
    // written has an id and a place in the order that only the core knows, and the
    // list is what shows it. A switch and a deletion are the other two cases, and
    // neither needs to read anything back.
    const added = !editing.value.id
    const module = available.value.find((one) => one.id === editing.value?.moduleId)
    const named = editing.value?.label || module?.name || words.value.named
    closeForm()
    await load()
    if (added) notify(`Added ${named}`)
  } catch (caught) {
    formError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    formBusy.value = false
  }
}

/**
 * Switches a recipient on or off.
 *
 * The switch moves first and the request follows. A switch that waits for a round trip
 * to move is a switch that lies to whoever pressed it, and the answer arrives either
 * way — as a notice when it worked is not needed, as one when it did not.
 */
async function toggle(row: ModuleRow) {
  const wanted = !row.enabled
  row.enabled = wanted
  row.enabled_here = true

  try {
    await api.patch(`/module-targets/${row.id}?${scopeQuery.value}`, {
      module_id: row.module_id,
      label: row.label,
      enabled: wanted,
      values: {},
    })
  } catch (caught) {
    row.enabled = !wanted
    row.enabled_here = false
    failed(caught, `could not switch ${describe(row)}`)
  }
}

async function remove(row: ModuleRow) {
  busy.value = row.id
  try {
    await api.del(`/module-targets/${row.id}?${scopeQuery.value}`)
    rows.value = rows.value.filter((one) => one.id !== row.id)
    emit('rows', rows.value)
    notify(`Removed ${describe(row)}`)
  } catch (caught) {
    failed(caught, `could not remove ${describe(row)}`)
  } finally {
    busy.value = ''
  }
}

/**
 * The test button sends one message to this recipient and nowhere else.
 *
 * It is always there. A button that can be switched off is a button somebody
 * switches off once and then cannot find, and a check that cannot be run is not a
 * check.
 */
/**
 * Asks whether this place can be reached, before anything is deployed to it.
 *
 * Only offered where it means something. Whether a chat is deliverable is answered by
 * sending to it, which the button beside does; whether a cluster answers is not
 * something anybody can see without trying, and finding out at deploy time is the
 * moment least wanted to be told a cluster is unreachable.
 *
 * The module answers rather than the core working it out, because getting in is the
 * module's business and the only definition of reachable that matches what a
 * deployment will actually meet.
 */
async function probe(row: ModuleRow) {
  busy.value = row.id
  try {
    const answer = await api.post<{ ok?: boolean; reason?: string }>(
      `/projects/${props.projectId}/deploy-clusters/test`, { cluster: addressOf(row) })
    notify(answer.ok
      ? `${addressOf(row)} answered`
      : (answer.reason ?? `${addressOf(row)} did not answer`),
      { type: answer.ok ? 'success' : 'error' })
  } catch (caught) {
    failed(caught, `could not reach ${describe(row)}`)
  } finally {
    busy.value = ''
  }
}

/**
 * The switches this module says a row has, in the order it declared them.
 *
 * Declared rather than known, so that a module gains a switch by naming it and the core
 * has to learn nothing about what it means.
 */
function flagsOf(row: ModuleRow): ModuleRowFlag[] {
  const module = available.value.find((one) => one.id === row.module_id)
  return module?.target?.flags ?? []
}

/**
 * Where a switch stands right now.
 *
 * The row's own decision where it made one, and the module's default where nobody did —
 * which is why an absent decision is not the same as off. A project that has never been
 * near this switch must not find a release waiting for a button because the switch it
 * never touched came out false.
 */
function flagOn(row: ModuleRow, flag: ModuleRowFlag): boolean {
  const own = row.flags_here?.[flag.key]
  if (own !== undefined) return own
  const inherited = row.flags?.[flag.key]
  if (inherited !== undefined) return inherited
  return flag.default
}

async function toggleFlag(row: ModuleRow, flag: ModuleRowFlag) {
  const wanted = !flagOn(row, flag)
  row.flags = { ...(row.flags ?? {}), [flag.key]: wanted }
  row.flags_here = { ...(row.flags_here ?? {}), [flag.key]: true }

  try {
    await api.patch(`/module-targets/${row.id}?${scopeQuery.value}`, {
      module_id: row.module_id,
      flags: { [flag.key]: wanted },
    })
    notify(`${flag.label} for ${addressOf(row)} is now ${wanted ? 'on' : 'off'}`, {
      type: 'success',
    })
  } catch (caught) {
    row.flags = { ...(row.flags ?? {}), [flag.key]: !wanted }
    failed(caught, `could not switch ${flag.label.toLowerCase()} for ${describe(row)}`)
  }
}

async function test(row: ModuleRow) {
  busy.value = row.id
  try {
    await api.post(`/module-targets/${row.id}/test?${scopeQuery.value}`, {})
    // A message that arrives is its own answer. One that does not needs saying, and
    // it says where the reason will be.
    notify(`Test message queued for ${describe(row)}. If nothing arrives, the module's log says why.`)
  } catch (caught) {
    failed(caught, 'the test could not be sent')
  } finally {
    busy.value = ''
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div v-if="stale" class="alert alert-warning">
      {{ stale }}
      <button class="btn btn-small" type="button" @click="stale = ''">Close</button>
    </div>

    <div v-if="!canManage" class="alert alert-info">
      Changing this needs rights to manage {{ scopeWord.replace('the ', '') }}.
    </div>

    <div v-else-if="loading" class="spinner">{{ words.loading }}</div>


    <div v-else-if="available.length === 0" class="card empty">
      <h3>{{ words.empty }}</h3>
      <p class="muted">{{ words.emptyBody }}</p>
      <NuxtLink class="btn" to="/admin/modules">Modules</NuxtLink>
    </div>

    <!-- One block per row, when a row has something of its own under it.
         The row is the block's heading: it is what you click to open the rest, and it
         is what the rest belongs to. With three places there are three headings and
         three sets of contents under them, and opening the second one is opening the
         second one. -->
    <div v-if="rows.length && layout === 'blocks'" class="blocks">
      <section v-for="row in rows" :key="row.id" class="block">
        <div class="block-head-row" :class="{ open: isOpen(row) }">
          <button
            class="caret"
            type="button"
            :aria-expanded="isOpen(row)"
            :title="isOpen(row) ? 'Hide this place' : 'Show this place'"
            @click="toggleRow(row)"
          >
            {{ isOpen(row) ? '▾' : '▸' }}
          </button>
          <span class="place mono">{{ addressOf(row) }}</span>
          <span v-if="showsLabel(row)" class="muted small">{{ row.label }}</span>
          <span v-if="row.scope_type !== 'instance'" class="muted small">{{ where(row) }}</span>
          <!-- What this level decided about this place, so that "inherited" and "the
               same as everybody" are not the same thing on screen. -->
          <span v-if="changedHere(row).length" class="changed-here">
            <span class="muted small">changed here:</span>
            <span class="mono small">{{ changedHere(row).join(', ') }}</span>
          </span>
          <!-- Two switches, each with its name beside it.
               Unlabelled switches were the wrong answer and an obvious one to be wrong
               about: two identical pills on one row, and nothing to tell which question
               either of them is asking. One is "may this project use this place at
               all", which is the coarse one every kind of module has; the other is
               whatever else the module declared, named in the module's own words. -->
          <span class="switches">
            <label class="targets-switch-pair" :title="row.enabled
              ? `This project may use ${addressOf(row)}`
              : `This project may not use ${addressOf(row)}`">
              <button
                class="switch"
                :class="{ on: row.enabled }"
                type="button"
                :disabled="!canManage || busy === row.id"
                :aria-pressed="row.enabled"
                @click="toggle(row)"
              >
                <span class="knob" />
              </button>
              <span class="switch-label">in use</span>
            </label>
            <label
              v-for="flag of flagsOf(row)"
              :key="flag.key"
              class="targets-switch-pair"
              :title="flag.description || flag.label"
            >
              <button
                class="switch"
                :class="{ on: flagOn(row, flag) }"
                type="button"
                :disabled="!canManage || busy === row.id"
                :aria-pressed="flagOn(row, flag)"
                @click="toggleFlag(row, flag)"
              >
                <span class="knob" />
              </button>
              <span class="switch-label">{{ flag.label }}</span>
            </label>
          </span>
          <button
            class="btn btn-small"
            type="button"
            :disabled="busy === row.id"
            :title="`Ask whether ${addressOf(row)} answers`"
            @click="probe(row)"
          >
            Check
          </button>
          <button
            v-if="canManage && row.scope_type === props.scope"
            class="btn btn-small"
            type="button"
            @click="openOverride(row)"
          >
            Edit
          </button>
          <button
            v-else-if="canManage"
            class="btn btn-small"
            type="button"
            title="Write down what is different here"
            @click="openOverride(row)"
          >
            Change here
          </button>
        </div>
        <div v-if="isOpen(row)" class="block-body">
          <slot name="row" :row="row" />
        </div>
      </section>
    </div>

    <table v-else-if="rows.length" class="admin-table targets-table">
        <thead>
          <tr>
            <!-- Which module a row belongs to, only where the list can hold more than
                 one. A page about a single deploy module already has a table naming
                 it directly above this one, and saying it again in a second table of
                 its own reads as a second module rather than as a repetition. -->
            <th v-if="severalModules">Module</th>
            <th>{{ words.address }}</th>
            <!-- Where a row came from is worth saying on a project or a group, where
                 it may have been decided above. On the instance there is nothing
                 above, so the column would only ever say the same thing. -->
            <th v-if="props.scope !== 'instance'">From</th>
            <th>On</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.id">
            <td v-if="severalModules">
              <strong>{{ moduleName(row) }}</strong>
              <div class="muted small mono">{{ row.module_kind }}</div>
            </td>
            <td>
              <span class="mono">{{ addressOf(row) }}</span>
              <div v-if="showsLabel(row)" class="muted small">{{ row.label }}</div>
            </td>
            <td v-if="props.scope !== 'instance'" class="muted small">{{ where(row) }}</td>
            <td>
              <!-- A two-item list is a question of "is it on", so it is a switch and
                   not a choice between Enabled and Disabled. -->
              <button
                class="switch"
                :class="{ on: row.enabled }"
                type="button"
                :disabled="!canManage || busy === row.id"
                :aria-pressed="row.enabled"
                :title="row.enabled ? 'On' : 'Off'"
                @click="toggle(row)"
              >
                <span class="knob" />
              </button>
            </td>
            <td>
              <!-- The buttons live in a div inside the cell rather than being the
                   cell: a table cell styled as a flex row stops being a table cell,
                   and the row's edges stop lining up with the row above it. -->
              <div class="targets-actions">
              <!-- Sending a message is what a notification module does. A cluster is
                   a place to put a release and there is no message to send it, so the
                   button that appears on every row of the other list is not offered
                   here rather than being offered and failing. -->
              <button
                v-if="!props.kind.startsWith('deploy:')"
                class="btn btn-small"
                type="button"
                :disabled="busy === row.id"
                @click="test(row)"
              >
                Test
              </button>
              <!-- A place rather than a destination: whether it can be reached is
                   asked of it and of nothing else. -->
              <button
                v-else
                class="btn btn-small"
                type="button"
                :disabled="busy === row.id"
                :title="`Ask whether ${addressOf(row)} answers`"
                @click="probe(row)"
              >
                Check
              </button>
              <button
                v-if="canManage && row.scope_type === props.scope"
                class="btn btn-small"
                type="button"
                @click="openOverride(row)"
              >
                Edit
              </button>
              <button
                v-else-if="canManage"
                class="btn btn-small"
                type="button"
                :title="'Write down what is different here'"
                @click="openOverride(row)"
              >
                Change here
              </button>
              <button
                v-if="canManage && row.scope_type === props.scope"
                class="btn btn-small btn-danger"
                type="button"
                :disabled="busy === row.id"
                @click="remove(row)"
              >
                Delete
              </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-else class="card empty">
        Nothing is configured, so nothing will be announced here. What a pipeline
        says about notifications decides whether it speaks; this list says where a
        message would go.
      </div>

      <form v-if="editing" class="card row-form" @submit.prevent="save">
        <div class="card-body">
          <h4>{{ editing.id ? words.changing : words.adding }}</h4>

          <!-- Asked only when there is a choice to make. On the module's own page
               there is one module, and offering a list of one is noise. -->
          <div v-if="!editing.id && available.length > 1" class="field">
            <label for="recipient-module">Module</label>
            <select id="recipient-module" v-model="editing.moduleId">
              <option v-for="one in available" :key="one.id" :value="one.id">
                {{ one.name }} ({{ one.kind }})
              </option>
            </select>
          </div>

          <!-- Said before the fields, because it changes how they are read: a box
               left empty here keeps whatever is above, and a box filled in does not
               touch what the row inherits. -->
          <p v-if="editing.overrideId" class="muted form-note">
            Only what you fill in is changed here. Everything left alone keeps coming
            from above.
          </p>

          <div class="field">
            <label for="recipient-name">Name</label>
            <input
              id="recipient-name"
              v-model="editing.label"
              :placeholder="`What this ${words.one} is for — the shared chat, production`"
            >
          </div>

          <!-- What a recipient is made of is the module's own question, asked in the
               module's own words. The core has no fields of its own to offer. -->
          <div v-for="spec in settingsOf(editing.moduleId)" :key="spec.key" class="field">
            <label :for="`recipient-${spec.key}`">{{ spec.label }}</label>
            <input
              :id="`recipient-${spec.key}`"
              v-model="editing.values[spec.key]"
              :placeholder="spec.default !== undefined ? String(spec.default) : ''"
            >
            <div v-if="spec.description" class="muted field-note">{{ spec.description }}</div>
            <div v-if="spec.secret && editing.overrideId" class="muted field-note">
              Left empty, the value from above is kept. What is stored is never shown.
            </div>
          </div>

          <div v-if="formError" class="alert alert-error">{{ formError }}</div>

          <div class="form-actions">
            <button class="btn btn-primary" type="submit" :disabled="formBusy">Save</button>
            <button class="btn" type="button" @click="closeForm">Cancel</button>
          </div>
        </div>
      </form>

      <button v-else class="btn" type="button" @click="openAdd">{{ words.add }}</button>
  </div>
</template>
