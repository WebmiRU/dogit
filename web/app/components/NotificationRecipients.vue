<script setup lang="ts">
/**
 * Who gets told when something happens, at one level.
 *
 * A row here is one recipient: a module, an address, and whether this place wants
 * messages. Not a module — a module can be pointed at two chats, and a chat can be
 * inherited by twenty repositories, so the row is where the two meet.
 *
 * The same component serves the instance, a group and a project, because it is the
 * same question at each: what does this place get, and what did it decide itself.
 * What differs is the scope query and the wording of the "from" column, and both come
 * from the scope prop.
 *
 * Two rules shape what is shown:
 *
 * - A row somebody has changed says so, and says which level changed it. A list that
 *   cannot answer "whose setting is this" is a list nobody will trust enough to
 *   switch anything off in.
 * - Nothing here decides what is worth saying. That belongs to the pipeline, which
 *   says so in its own configuration; this list only says where a message would go.
 */
import type { SettingSpec } from '~/types/module'
import type { Recipient, RecipientDraft, RecipientsModule } from '~/types/notification'

const props = withDefaults(
  defineProps<{
    /** Whose recipients these are: instance, group or project. */
    scope?: 'instance' | 'group' | 'project'
    scopeId?: string
    canManage?: boolean
    /**
     * One module, when the page is that module's own.
     *
     * The module page shows its own recipients and nothing else: somebody reading
     * "Telegram writes to these two chats" is not helped by a list of where email
     * goes as well.
     */
    onlyModule?: string
    /** Which module a newly added recipient belongs to. */
    defaultModule?: string
  }>(),
  { scope: 'instance', canManage: true },
)

const { add: notify } = useNotifyPool()

const recipients = ref<Recipient[]>([])
const available = ref<RecipientsModule[]>([])
const loading = ref(true)
const busy = ref('')

/** The row being added or edited, or null when the form is closed. */
const editing = ref<RecipientDraft | null>(null)
const formError = ref('')
const formBusy = ref(false)

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

function where(row: Recipient): string {
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
  if (recipients.value.length === 0) loading.value = true
  try {
    const answer = await api.get<{ targets: Recipient[]; modules: RecipientsModule[] }>(
      `/notification-targets?${scopeQuery.value}`,
    )
    const modules = answer.modules ?? []
    recipients.value = props.onlyModule
      ? (answer.targets ?? []).filter((row) => row.module_id === props.onlyModule)
      : (answer.targets ?? [])
    // The choice of module is this module's on its own page, so the list has one
    // entry and the form does not ask a question with a single answer.
    available.value = props.onlyModule ? modules.filter((one) => one.id === props.onlyModule) : modules
  } catch (caught) {
    // Said in the corner rather than in the page: the list is either there or it is
    // not, and a strip of error above an empty table explains nothing.
    failed(caught, 'the recipients could not be read')
  } finally {
    loading.value = false
  }
}

/** How to call a recipient in a sentence. */
function describe(row: Recipient): string {
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

/** The settings that make a recipient of this module, in the module's own words. */
function settingsOf(moduleId: string): SettingSpec[] {
  return available.value.find((one) => one.id === moduleId)?.settings ?? []
}

/** What a recipient of this module is called in a list. */
function addressOf(row: Recipient): string {
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
function showsLabel(row: Recipient): boolean {
  const address = addressOf(row)
  return row.label !== '' && !row.label.includes(address) && address !== '—'
}

function moduleName(row: Recipient): string {
  return row.module_name || row.module_kind.replace('notify:', '')
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
function openOverride(row: Recipient) {
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
      await api.patch(`/notification-targets/${editing.value.id}?${scopeQuery.value}`, body)
    } else {
      await api.post(`/notification-targets?${scopeQuery.value}`, body)
    }
    // A form is the one place a read afterwards is worth it: the row that was just
    // written has an id and a place in the order that only the core knows, and the
    // list is what shows it. A switch and a deletion are the other two cases, and
    // neither needs to read anything back.
    const added = !editing.value.id
    const module = available.value.find((one) => one.id === editing.value?.moduleId)
    const named = editing.value?.label || module?.name || 'the recipient'
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
async function toggle(row: Recipient) {
  const wanted = !row.enabled
  row.enabled = wanted
  row.enabled_here = true

  try {
    await api.patch(`/notification-targets/${row.id}?${scopeQuery.value}`, {
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

async function remove(row: Recipient) {
  busy.value = row.id
  try {
    await api.del(`/notification-targets/${row.id}?${scopeQuery.value}`)
    recipients.value = recipients.value.filter((one) => one.id !== row.id)
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
async function test(row: Recipient) {
  busy.value = row.id
  try {
    await api.post(`/notification-targets/${row.id}/test?${scopeQuery.value}`, {})
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
    <div v-if="!canManage" class="alert alert-info">
      Changing this needs rights to manage {{ scopeWord.replace('the ', '') }}.
    </div>

    <div v-else-if="loading" class="spinner">Loading recipients…</div>


    <div v-else-if="available.length === 0" class="card empty">
      <h3>No notification modules are installed</h3>
      <p class="muted">
        Notifications are delivered by modules, and there are none here yet. Install
        one — Telegram, email, a webhook — and the places it can write to will appear
        in this list.
      </p>
      <NuxtLink class="btn" to="/admin/modules">Modules</NuxtLink>
    </div>

    <template v-else>
      <table v-if="recipients.length" class="table">
        <thead>
          <tr>
            <th>Module</th>
            <th>Writes to</th>
            <!-- Where a row came from is worth saying on a project or a group, where
                 it may have been decided above. On the instance there is nothing
                 above, so the column would only ever say the same thing. -->
            <th v-if="props.scope !== 'instance'">From</th>
            <th>On</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in recipients" :key="row.id">
            <td>
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
            <td class="actions">
              <button class="btn btn-small" type="button" :disabled="busy === row.id" @click="test(row)">
                Test
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
            </td>
          </tr>
        </tbody>
      </table>

      <div v-else class="card empty">
        Nothing is configured, so nothing will be announced here. What a pipeline
        says about notifications decides whether it speaks; this list says where a
        message would go.
      </div>

      <form v-if="editing" class="card recipient-form" @submit.prevent="save">
        <div class="card-body">
          <h4>{{ editing.id ? 'Change this recipient' : 'Add a recipient' }}</h4>

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
              placeholder="What this recipient is for — the shared chat, deploys"
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

      <button v-else class="btn" type="button" @click="openAdd">Add a recipient</button>
    </template>
  </div>
</template>
<style scoped>
.table {
  width: 100%;
  border-collapse: collapse;
  margin-bottom: 16px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
}

.table th {
  text-align: left;
  padding: 8px 12px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border);
}

.table td {
  padding: 10px 12px;
  vertical-align: middle;
  border-bottom: 1px solid var(--border);
}

.table tbody tr:last-child td {
  border-bottom: none;
}

.actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
  white-space: nowrap;
}

.small {
  font-size: 12px;
}

.recipient-form {
  max-width: 560px;
  margin-bottom: 16px;
}

.recipient-form h4 {
  margin: 0 0 12px;
  font-size: 14px;
}

.field {
  margin-bottom: 14px;
}

/* The module's own words about a setting, kept off the box it describes. */
.field-note {
  margin-top: 4px;
  font-size: 12px;
}

.form-note {
  margin: 0 0 14px;
  padding: 8px 10px;
  border-left: 2px solid var(--accent);
  background: var(--bg-inset);
  font-size: 12px;
}

.form-actions {
  display: flex;
  gap: 8px;
}
</style>
