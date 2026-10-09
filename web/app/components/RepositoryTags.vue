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
 * Deleting is behind the row being pointed at rather than on every row: a list
 * of releases is read, not pruned, and a page where every line ends in Delete
 * is one misdirected click away from doing something nobody meant to do.
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

    <div class="card">
      <div class="toolbar">
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
      <ul v-else class="tree-list tag-list">
        <!-- The header is what the columns are for: without it the date and the
             commit are two grey numbers with nothing to say which is which. -->
        <li class="head">
          <span />
          <span>Name</span>
          <span class="sha">Commit</span>
          <span>Created</span>
          <span />
        </li>
        <li v-for="tag in tags" :key="tag.name">
          <span class="icon">◈</span>
          <span class="cell-name">
            <NuxtLink
              class="name"
              :to="repoViewUrl(projectPath, 'tree', tag.name)"
            >
              {{ tag.name }}
            </NuxtLink>
            <!-- Only an annotated tag has a release note, so only an annotated tag
                 shows one, and it sits next to the name rather than in a column of
                 its own. -->
            <span v-if="saidAbout(tag)" class="note" :title="saidAbout(tag)">
              {{ saidAbout(tag) }}
            </span>
          </span>
          <span class="meta sha">{{ tag.target.slice(0, 8) }}</span>
          <span v-if="tag.created_at" class="meta">{{ timeAgo(tag.created_at) }}</span>
          <button
            class="row-action"
            type="button"
            :disabled="pending === tag.name"
            @click="remove(tag.name)"
          >
            {{ pending === tag.name ? 'Deleting…' : 'Delete' }}
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>