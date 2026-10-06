<script setup lang="ts">
/**
 * This project's places, one row each, and under each row the card about that place.
 *
 * A row is a place and the card under it is that place and nothing else: which pods came
 * up, what the module said, what has been deployed there and which images have ever been
 * there are four questions with four answers for a project that deploys to two
 * clusters. Drawn for the project as a whole they are merged into one, and a rollout on
 * one of them is shown next to a version of the other that has been running for a month
 * — which reads as one deployment that has been going for a month.
 *
 * What a row may change is what is this project's own answer about that place, and two
 * things: whether a push may deploy there by itself, and whether this project may deploy
 * there at all. Everything else about a cluster — where it is, what it is called, which
 * namespace, how long to wait — belongs to whoever installed the module and is on its
 * Options tab. A kubeconfig is a credential and this page is not the place for it: the
 * page is open to everybody who may read the project, and a cluster's address is not
 * theirs to read.
 *
 * Each row saves itself, because a row is what somebody decided: two clusters are two
 * credentials, and a Save that took both would make switching one switch a chance to
 * rewrite the other's answer.
 */
import type { ModuleRow } from '~/types/module'

const props = defineProps<{
  projectId: string
  projectPath: string
  module: ModuleRow
  canManage: boolean
}>()

/** One row of the clusters list, as this project sees it. */
interface Place {
  name: string
  namespace: string
  /**
   * Whether this project has said anything of its own about this place.
   *
   * The core is what knows: a place it has decided nothing about is somebody else's, and
   * the only thing this project may say about it is whether it is in use here.
   */
  ours: boolean
  /**
   * What this project has written down about this place, as the core sent it: the name,
   * the row's identity, and every field this project has decided. Kept whole because a
   * save replaces the level's whole list: a row sent with one switch in it takes every
   * other decision of this project with it.
   */
  own: Record<string, unknown>
  /** The core's own identity of this row, carried on save so that a row keeps the row it is. */
  id?: string
  /** Whether this project may deploy here at all. Nobody having said means yes. */
  inUse: boolean
  /** Whether a push may do it by itself. Nobody having said means yes. */
  autodeploy: boolean
}

const places = ref<Place[]>([])
/** The same rows as they are stored at the levels above, keyed by name. */
const inherited = ref<Record<string, Record<string, unknown>>>({})
const { add: notify } = useNotifyPool()

const loading = ref(true)
const saving = ref('')
const error = ref('')

/** The name of the place being added, and whether the form for it is open. */
const adding = ref(false)
const newName = ref('')

/** What each row's two switches said when it was loaded, so a row knows whether it has changed. */
const loaded = ref<Record<string, { inUse: boolean; autodeploy: boolean }>>({})

async function load() {
  // Only the first load blanks the page. A save is followed by a re-read, and a page that
  // goes back to "Loading…" in between takes the place somebody is standing in — the
  // fold closes, the tab returns to Now, and the fields they had just filled in are not
  // there any more. The list is the answer; it is re-read under the eyes of whoever is
  // reading it rather than in place of it.
  if (places.value.length === 0) loading.value = true
  error.value = ''
  try {
    // "own" and not "effective": the values this instance wrote for the cluster are not
    // sent to a page, so this project sees the name of its place and what it has decided
    // about it, and nothing else.
    const answer = await api.get<{ own?: Record<string, unknown> }>(
      `/modules/${props.module.id}/settings?scope=project&projectID=${encodeURIComponent(props.projectId)}`,
    )
    const rows = Array.isArray(answer.own?.clusters)
      ? (answer.own!.clusters as Record<string, unknown>[])
      : []

    // A row with no name is a row somebody started filling in, and it is not a place.
    // The name a row is called comes from the core when this project has not named it
    // itself: a place exists here whether this project has an opinion about it or not,
    // and a project that has no name for a cluster still has to be able to say which
    // cluster it is not deploying to.
    const seen: Place[] = []
    for (const row of rows) {
      const name = typeof row?.name === 'string' && row.name.trim()
        ? row.name.trim()
        : (typeof row?.dogit_row_name === 'string' ? row.dogit_row_name.trim() : '')
      if (!name) continue
      seen.push({
        name,
        ours: row.dogit_row_own === true,
        own: { ...row },
        id: typeof row.dogit_row_id === 'string' ? row.dogit_row_id : undefined,
        namespace: typeof row.default_namespace === 'string' ? row.default_namespace : '',
        inUse: row.enabled !== false,
        autodeploy: row.auto_deploy !== false,
      })
    }
    places.value = seen

    const baseline: Record<string, { inUse: boolean; autodeploy: boolean }> = {}
    for (const one of seen) baseline[one.name] = { inUse: one.inUse, autodeploy: one.autodeploy }
    loaded.value = baseline
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

/**
 * Flips one switch and saves it there and then.
 *
 * No Save button and no pending state: a switch is a thing you do, not a thing you fill
 * in and confirm, and a switch that has to be saved is a switch somebody leaves in the
 * wrong position because they assumed it had taken. The row's text fields, which do have
 * to be typed and checked, are saved from the row's own Save on the Settings tab.
 *
 * The word beside the switch is the state, so the answer to "did that work" is on the
 * page and not only in a notification that has already gone.
 */
async function flip(place: Place, key: 'inUse' | 'autodeploy') {
  const was = loaded.value[place.name]
  if (!was) return
  const before = { inUse: place.inUse, autodeploy: place.autodeploy }
  place[key] = !place[key]
  saving.value = place.name
  error.value = ''
  try {
    await save(places.value)
    await load()
    const now = place[key]
    const what = key === 'inUse'
      ? `${place.name} is now ${now ? 'in use' : 'not in use'}`
      : `${place.name} is now deployed ${now ? 'by a push' : 'only by hand'}`
    // Seconds, not milliseconds: a number five thousand here is a notice that
    // stays for an hour and a half, and everybody's screen fills with them.
    notify(what, { type: 'success', timer: 5 })
  } catch (caught) {
    place.inUse = before.inUse
    place.autodeploy = before.autodeploy
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
    notify(error.value, { type: 'error', timer: 0 })
  } finally {
    saving.value = ''
  }
}

/**
 * Writes this project's list of places, as it now stands.
 *
 * The whole list, every time. A save replaces what this level holds for the module, so a
 * save of one row is a save of one row: every other place's answers would be gone, and
 * gone without a word, from a page where the only thing that was touched was one switch.
 * Two rows are two answers, and writing one of them must not decide the other.
 */
async function save(rows: Place[]) {
  return api.put(
    `/modules/${props.module.id}/settings/bulk?scope=project&projectID=${encodeURIComponent(props.projectId)}`,
    { values: { clusters: rows.map((one) => own(one)) } },
  )
}

/**
 * What to send about one place.
 *
 * The whole of what this project has written down about it, with its two switches as they
 * now stand — and not just the switch. A row sent with nothing but `enabled` says that
 * this project has decided nothing else about this place either: turn In use off and the
 * project's own Autodeploy off with it, which is two answers changed by one click, one
 * of them not even on screen.
 */
function own(place: Place): Record<string, unknown> {
  // A name is sent only when this project has one of its own: the name on the page is
  // usually the module's, and writing it here would say this project named this place.
  const row: Record<string, unknown> = {}
  if (typeof place.own.name === 'string' && place.own.name.trim()) row.name = place.own.name
  // The identity goes back with the row: without it the core gives a new one on every
  // save, and a row whose identity changes each time is a row nothing can be recognised as.
  if (place.id) row.dogit_row_id = place.id
  row.enabled = place.inUse
  row.auto_deploy = place.autodeploy
  return row
}

/**
 * Adds a place of this project's own.
 *
 * Below the list rather than inside one place's settings: a place is a row of the list,
 * and a button that adds a row belongs where the rows are, not inside one of them. What it
 * writes is a name and nothing else — where a cluster is, and what it is called at the top
 * of the chain, is the module's to say, and this project's row is only that it uses it
 * and under what name.
 */
async function addPlace() {
  const name = newName.value.trim()
  if (!name || saving.value) return
  saving.value = 'adding'
  error.value = ''
  try {
    await save([...places.value, { name, namespace: '', ours: true, own: { name }, inUse: true, autodeploy: true }])
    adding.value = false
    newName.value = ''
    await load()
    notify(`${name} is now one of this project's places`, { type: 'success', timer: 5 })
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
    notify(error.value, { type: 'error', timer: 0 })
  } finally {
    saving.value = ''
  }
}

onMounted(load)
watch(() => props.module.id, load)
</script>

<template>
  <section class="places">
    <h2 class="places-title">{{ module.name }} places</h2>
    <p class="muted small">
      One row per place {{ props.projectPath }} may deploy to, and under it everything
      about that place: what is happening there, what has been deployed there, which
      images have been there, and its own settings.
    </p>

    <p v-if="error" class="alert alert-error">{{ error }}</p>
    <p v-if="loading" class="spinner">Loading…</p>

    <p v-else-if="places.length === 0" class="muted small">
      This module has no place this project may name. Its Options tab is where they are
      written down.
    </p>

    <!-- One place, folded away. The name is the whole of what is shown until somebody
         asks for it, the same way the repository's own configuration folds: a page of
         places that is a page of open cards is a page where nothing stands out. -->
    <!-- Keyed by the core's identity of the row rather than by its name: a place that is
         renamed is the same place, and a page that tore it down and built it again over a
         name would close the fold and lose the tab somebody was on. -->
    <details v-for="(place, index) in places" :key="place.id ?? place.name" class="place">
      <summary class="place-head">
        <span class="place-name mono">{{ place.name }}</span>
        <span v-if="place.namespace" class="muted small">{{ place.namespace }}</span>

        <!-- Whose place this is, said once, where the place is named. A page that cannot
             answer "whose setting is this" is a page nobody will trust enough to switch
             anything off in — and the answer belongs beside the two switches that are
             this project's own answers about it. -->
        <span class="badge place-origin" :class="{ ours: place.ours }">
          {{ place.ours ? 'changed here' : 'inherited' }}
        </span>

        <!-- The two answers this project has about this place, in the row that names it,
             so that a list of places can be read without opening any of them. Clicking
             one saves it at once; clicking the row itself only opens or closes. -->
        <label
          class="switch-pair"
          @click.stop
          title="Autodeploy: whether a push or a tag may deploy to this place by itself. Off means the run still happens and the image is still built — only the deployment here waits for somebody to start it by hand."
        >
          <button
            class="switch"
            :class="{ on: place.autodeploy }"
            type="button"
            role="switch"
            aria-label="Autodeploy here"
            :aria-checked="place.autodeploy"
            :disabled="saving === place.name || !props.canManage || !place.inUse"
            @click.stop="flip(place, 'autodeploy')"
          >
            <span class="knob" />
          </button>
          <span class="switch-name">Autodeploy</span>
        </label>

        <!-- This one's label is its state, ON or OFF, and it sits where the other
             labels sit: a switch with a name on its left and a switch with a name on its
             right is two rows that read as one, and every switch on the page should be
             found by looking in the same place for its name. What the switch answers —
             may this project deploy here at all — is in its tooltip. -->
        <label
          class="switch-pair"
          @click.stop
          title="In use: whether this project may deploy here at all. Off leaves the place configured on the module's own page and untouched here — switched off for this project, not deleted."
        >
          <button
            class="switch"
            :class="{ on: place.inUse }"
            type="button"
            role="switch"
            aria-label="In use here"
            :aria-checked="place.inUse"
            :disabled="saving === place.name || !props.canManage"
            @click.stop="flip(place, 'inUse')"
          >
            <span class="knob" />
          </button>
          <span class="switch-state">{{ place.inUse ? 'ON' : 'OFF' }}</span>
        </label>
      </summary>

      <div class="place-body">
        <!-- The card about this place and no other. -->
        <ModuleDeployments
          :project-id="props.projectId"
          :project-path="props.projectPath"
          :module="module"
          :can-manage="props.canManage"
          :place="{ cluster: place.name, namespace: place.namespace }"
          :show-repository="index === 0"
          @settings-changed="load"
        />
      </div>
    </details>

    <!-- Adding a place: below the rows, because that is what it does to the list. -->
    <div v-if="props.canManage" class="places-add">
      <form v-if="adding" class="places-add-form" @submit.prevent="addPlace">
        <input
          v-model="newName"
          class="add-name"
          type="text"
          placeholder="a place, such as production-eu"
          aria-label="Name of the place to add"
          spellcheck="false"
        >
        <button class="btn btn-small btn-primary" type="submit" :disabled="saving === 'adding' || !newName.trim()">
          {{ saving === 'adding' ? 'Adding…' : 'Add' }}
        </button>
        <button class="btn btn-small" type="button" @click="adding = false; newName = ''">
          Cancel
        </button>
      </form>
      <button v-else class="btn btn-small" type="button" @click="adding = true">
        Add a place
      </button>
    </div>
  </section>
</template>

<style scoped>
.places {
  margin-top: 28px;
}

/* Where a place is added: under the rows, with room to type a name in. */
.places-add {
  margin-top: 14px;
}

.places-add-form {
  display: flex;
  align-items: center;
  gap: 8px;
}

.add-name {
  flex: 1 1 320px;
  max-width: 420px;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
}

.places-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 4px;
}

.place {
  margin-top: 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

/* The row that is always visible: the name, and one word about what happens here by
   itself. Everything else is inside, and comes out when the name is clicked. */
/* Aligned along the bottom rather than through the middle: the row is a line of things
   of different heights — a name, a badge, a switch and two labels — and centring them
   puts four different lines through the middle of one row. Their feet on one line is what
   makes a row of mixed things read as one row. */
.place-head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px 14px;
  padding: 12px 14px;
  cursor: pointer;
  list-style: none;
}

.place-head::-webkit-details-marker {
  display: none;
}

.place-name {
  font-weight: 600;
}

.place-note {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

/*
 * Whose place this is, said as a badge beside its name: a place written at the module or
 * at the group is somebody else's decision, and a place this project wrote is its own.
 * Two words, and the difference between them is the whole point of inheritance — a list
 * that cannot answer "whose setting is this" is a list nobody will trust enough to switch
 * anything off in.
 *
 * Nothing of its own about how it looks: the badge class says the chip, the padding and
 * the capital, and a badge that pads itself differently is a badge to be read twice — once
 * to see what it says and once to work out what kind of thing it is.
 *
 * What is its own is where it stands: it takes the room the name leaves, so that the two
 * words of differing length push the gap beside the name rather than the switches along
 * the row. A row whose controls move when a word changes is a row where a switch is
 * somewhere else every time somebody reads the badge.
 */
.place-origin {
  margin-right: auto;
}

.place-origin.ours {
  background: var(--accent-soft);
  color: var(--accent);
}

.place-body {
  padding: 0 14px 14px;
}

.place-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
  padding-bottom: 10px;
  margin-bottom: 10px;
  border-bottom: 1px solid var(--border);
}

/* The switch and the word beside it stand on the same line as everything else in the
   head, so their own row is aligned by its foot as well — a label that hangs below its
   switch is one pixel of nothing that two places in a row will not both have. */
.switch-pair {
  display: inline-flex;
  align-items: flex-end;
  gap: 6px;
  cursor: pointer;
  /* A label is given a bottom margin by the page's own form styles, and six pixels of it
     is six pixels between this row's switches and the row they belong to. */
  margin-bottom: 0;
}

.switch-name {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
  /* The width of the longest of these words, and the width of the ON/OFF word beside the
     other switch, so that switching one does not narrow the row and push everything
     after it sideways. */
  min-width: 68px;
}

/* The state, spelled out. Fixed width, so that ON and OFF take the same room. */
.switch-state {
  min-width: 26px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

.not-saved {
  margin-right: auto;
}
</style>