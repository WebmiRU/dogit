<script setup lang="ts">
/**
 * Tag list.
 *
 * Tags are on their own page rather than under the branch list: a repository with
 * hundreds of branches would push every tag out of sight, and tags are looked up
 * by name far more often than they are scanned.
 *
 * Laid out as columns with a header, which the branch list is not: a
 * repository's tags outnumber its branches by an order of magnitude — a release
 * a week is dozens by the end of a year — and an eye picking "which of these two
 * hundred rows is from last week" is helped by a column it can come back to.
 * Ten branches fit on a screen without any help, so that list stays a flex line.
 *
 * The release note is deliberately not a column of its own: most tags are
 * lightweight and carry none, and a column that is empty on most rows is a
 * column that only moves the date away from the name. When there is something
 * to say it rides along with the name and truncates, so a tag with a note is
 * still one row.
 *
 * Deleting is not on the row: it lives in a menu behind the ⋯ at the end of the
 * line, next to the two other things one does with a release after reading it —
 * look at it, see what it changed. A list of releases is read, not pruned, and a
 * page where every line ends in Delete is one misdirected click away from doing
 * something nobody meant to do.
 */
import type { RefsResponse } from '~/types/repository'
import { timeAgo } from '~/utils/format'

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
}>()

const emit = defineEmits<{ (event: 'refs-changed'): void }>()

const error = ref('')
const pending = ref<string | null>(null)
const filter = ref('')

/**
 * The one row whose menu is open, and where its panel goes.
 *
 * The panel is teleported to the body and placed in fixed coordinates rather
 * than drawn inside the row, so it can layer above the surrounding page content.
 * It does not follow the row when the page scrolls, so a scroll closes it: a
 * menu hanging next to the wrong row is worse than no menu at all.
 */
const open = ref<string | null>(null)
const panel = ref({ top: 0, left: 0, up: false })

const openedTag = computed(() =>
  open.value ? tags.value.find((tag) => tag.name === open.value) ?? null : null,
)

const defaultBranch = computed(() => props.refs?.default_branch ?? '')

function showMenu(name: string, event: MouseEvent) {
  // The same ⋯ twice is a toggle: the second press is a decision to look at the
  // row again, not to open the menu a second time.
  if (open.value === name) {
    closeMenu()
    return
  }
  const box = (event.currentTarget as HTMLElement).getBoundingClientRect()
  panel.value = {
    top: box.bottom + 6,
    left: box.right - 190,
    up: window.innerHeight - box.bottom < 150,
  }
  open.value = name
}

function closeMenu() {
  open.value = null
}

// Three ways the open menu stops being the thing at hand: a pointer down anywhere
// else, Escape, and the page moving under it. All three are ignored while no
// menu is open, so they cost three idle listeners for the life of the page.
function onPointerAway(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as HTMLElement | null
  if (target?.closest('.row-menu-panel') || target?.closest('.row-menu')) return
  closeMenu()
}

function onKeyAway(event: KeyboardEvent) {
  if (event.key === 'Escape') closeMenu()
}

onMounted(() => {
  window.addEventListener('pointerdown', onPointerAway, true)
  window.addEventListener('keydown', onKeyAway)
  window.addEventListener('scroll', closeMenu, true)
})

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onPointerAway, true)
  window.removeEventListener('keydown', onKeyAway)
  window.removeEventListener('scroll', closeMenu, true)
})

const allTags = computed(() => props.refs?.tags ?? [])

// Filtered on the name and on what the tag said, because a release is as often looked up by the
// line in its message as by its number.
const tags = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return allTags.value
  return allTags.value.filter((tag) =>
    tag.name.toLowerCase().includes(needle) ||
    (tag.message ?? '').toLowerCase().includes(needle) ||
    (tag.created_by ?? '').toLowerCase().includes(needle)
  )
})

/** What one row says about its tag, and nothing when it said nothing. */
function saidAbout(tag: { message?: string; created_by?: string }) {
  if (!tag.message) return ''
  return tag.created_by ? `${tag.created_by}: ${tag.message}` : tag.message
}

/**
 * A comparison of this release against the branch it is read against: the
 * default branch, because "what did this release change" is a question asked of
 * a branch, not of another release.
 *
 * Built here rather than with repoViewUrl because the compare view reads two
 * refs — from and to — and that helper carries one. The one view whose address
 * is not its helper's shape is worth a comment rather than a second parameter
 * nobody else passes.
 */
function compareUrl(tagName: string): string {
  const query = new URLSearchParams({ from: defaultBranch.value, to: tagName })
  return `/p/${props.projectPath}/-/compare?${query.toString()}`
}

async function remove(name: string) {
  if (!confirm(`Delete tag ${name}?`)) return
  pending.value = name
  error.value = ''
  try {
    await api.delete(`/projects/${props.projectId}/repository/tags/${encodeURIComponent(name)}`)
    emit('refs-changed')
  } catch (caught) {
    error.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    pending.value = null
  }
}
</script>

<template>
  <div>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <div>
      <div class="toolbar repository-toolbar repository-page-toolbar">
        <strong>Tags ({{ allTags.length }})</strong>
        <div class="spacer" />
        <div class="field" style="max-width: 260px; margin: 0">
          <input v-model="filter" type="search" placeholder="Filter tags" />
        </div>
      </div>

      <div v-if="allTags.length === 0" class="empty">
        No tags yet. A tag is created from the API; nothing in this interface makes one yet.
      </div>
      <div v-else-if="tags.length === 0" class="empty">No tag matches “{{ filter }}”.</div>
      <div v-else class="repository-table-wrap">
        <table class="admin-table repository-flat-table tag-list">
          <thead><tr><th>Name</th><th>Commit</th><th>Created</th><th class="numeric">Actions</th></tr></thead>
          <tbody>
            <tr v-for="tag in tags" :key="tag.name">
              <td>
                <div class="cell-name">
                  <NuxtLink class="name" :to="repoViewUrl(projectPath, 'tree', tag.name)">{{ tag.name }}</NuxtLink>
                  <span v-if="saidAbout(tag)" class="note" :title="saidAbout(tag)">{{ saidAbout(tag) }}</span>
                </div>
              </td>
              <td class="mono small">{{ tag.target.slice(0, 10) }}</td>
              <td class="muted small">{{ tag.created_at ? timeAgo(tag.created_at) : '—' }}</td>
              <td class="numeric">
                <button class="row-menu" type="button" aria-haspopup="menu" :aria-expanded="open === tag.name" :aria-label="`Actions for tag ${tag.name}`" @click="showMenu(tag.name, $event)">⋯</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- The menu is teleported out of the table so it layers above nearby rows. Fixed
           coordinates mean it cannot outlive the page moving under it — scrolling closes
           it instead of misplacing it. -->
      <Teleport to="#teleports">
        <div
          v-if="openedTag"
          class="row-menu-panel"
          :class="{ up: panel.up }"
          :style="{ top: `${panel.top}px`, left: `${panel.left}px` }"
        >
          <NuxtLink :to="repoViewUrl(projectPath, 'tree', openedTag.name)" @click="closeMenu">
            Browse files
          </NuxtLink>
          <NuxtLink :to="compareUrl(openedTag.name)" @click="closeMenu">
            {{ defaultBranch ? `Compare with ${defaultBranch}` : 'Compare' }}
          </NuxtLink>
          <button
            class="row-menu-danger"
            type="button"
            :disabled="pending === openedTag.name"
            @click="remove(openedTag.name)"
          >
            {{ pending === openedTag.name ? 'Deleting…' : 'Delete' }}
          </button>
        </div>
      </Teleport>
    </div>
  </div>
</template>