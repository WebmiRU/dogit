<script setup lang="ts">
/** Source file view with line numbers and blame. */
import type { BlameLine, FileResponse, RefsResponse } from '~/types/repository'
import hljs from 'highlight.js/lib/common'

const emit = defineEmits<{ (event: 'change-ref', ref: string): void }>()

const props = defineProps<{
  projectId: string
  projectPath: string
  refs: RefsResponse | null
  refName: string
  path: string
  canPush?: boolean
}>()

const file = ref<FileResponse | null>(null)
const blame = ref<BlameLine[] | null>(null)
const showBlame = ref(false)
const loading = ref(true)
const loadError = ref('')

async function loadFile() {
  loading.value = true
  loadError.value = ''
  blame.value = null
  try {
    file.value = await api.get<FileResponse>(`/projects/${props.projectId}/repository/file`, {
      ref: props.refName,
      path: props.path,
    })
  } catch (caught) {
    loadError.value = caught instanceof ApiError ? caught.message : 'the request failed'
  } finally {
    loading.value = false
  }
}

async function toggleBlame() {
  showBlame.value = !showBlame.value
  if (!showBlame.value || blame.value || !file.value) return

  const response = await api.get<{ lines: BlameLine[] }>(
    `/projects/${props.projectId}/repository/blame`,
    { ref: props.refName, path: props.path },
  )
  blame.value = response.lines
}

onMounted(loadFile)
watch(() => [props.refName, props.path], loadFile)

/** Highlighted HTML per line, so line numbers stay aligned with the code. */
const lines = computed<string[]>(() => {
  if (!file.value || file.value.binary || file.value.too_large) return []

  const raw = file.value.content.split('\n')
  if (raw.length > 0 && raw[raw.length - 1] === '') raw.pop()

  // Highlighting the whole file at once is both faster and more accurate than
  // per line, because the grammar can span line boundaries.
  let highlighted: string[]
  try {
    const language = file.value.language && hljs.getLanguage(file.value.language)
      ? file.value.language
      : 'plaintext'
    highlighted = hljs.highlight(file.value.content, { language, ignoreIllegals: true }).value.split('\n')
    if (highlighted.length > raw.length) highlighted.length = raw.length
  } catch {
    highlighted = raw.map(escapeHtml)
  }
  return highlighted.map((html, index) => html || raw[index] || '')
})

const fileName = computed(() => props.path.split('/').pop() ?? props.path)
const directory = computed(() => props.path.split('/').slice(0, -1).join('/'))

const rawUrl = computed(() =>
  file.value?.raw_url
    ? `${apiUrl(file.value.raw_url.replace(/^\/api\/v1/, ''))}`
    : '',
)
</script>

<template>
  <div class="card">
    <div class="toolbar">
      <BranchSelector :refs="refs" :ref-name="refName" @change="emit('change-ref', $event)" />

      <div class="breadcrumbs">
        <NuxtLink :to="`/p/${projectPath}/-/tree/${encodeURIComponent(refName)}`">{{ projectPath }}</NuxtLink>
        <span class="sep">/</span>
        <NuxtLink
          v-if="directory"
          :to="`/p/${projectPath}/-/tree/${encodeURIComponent(refName)}/${directory}`"
        >
          {{ directory }}
        </NuxtLink>
        <span v-if="directory" class="sep">/</span>
        <span>{{ fileName }}</span>
      </div>

      <div class="spacer" />
      <NuxtLink
        v-if="canPush"
        class="btn btn-primary"
        :to="`/p/${projectPath}/-/edit/${encodeURIComponent(refName)}/${path}`"
      >
        Edit
      </NuxtLink>
      <button class="btn" type="button" @click="toggleBlame">
        {{ showBlame ? 'Hide blame' : 'Blame' }}
      </button>
      <a v-if="rawUrl" class="btn" :href="rawUrl" target="_blank" rel="noopener">Raw</a>
      <NuxtLink
        v-if="file?.last_commit_sha"
        class="btn"
        :to="`/p/${projectPath}/-/commit/${file.last_commit_sha}`"
      >
        History
      </NuxtLink>
    </div>

    <div v-if="loading" class="spinner">Loading file…</div>
    <div v-else-if="loadError" class="alert alert-error" style="margin: 16px">{{ loadError }}</div>

    <template v-else-if="file">
      <div v-if="file.binary" class="empty">
        This is a binary file
        (<a :href="rawUrl" target="_blank" rel="noopener">download</a>).
      </div>
      <div v-else-if="file.too_large" class="empty">
        This file is {{ formatBytes(file.size) }}, too large to display.
        <a :href="rawUrl" target="_blank" rel="noopener">Download it instead</a>.
      </div>
      <div v-else-if="lines.length === 0" class="empty">This file is empty.</div>
      <div v-else class="code-view">
        <div
          v-for="(html, index) in lines"
          :key="index"
          class="code-line"
        >
          <span v-if="showBlame && blame?.[index]" class="ln" :title="`${blame[index].author_name} · ${blame[index].timestamp}`">
            {{ blame[index].commit_sha.slice(0, 8) }}
          </span>
          <span v-else class="ln">{{ index + 1 }}</span>
          <span class="lc" v-html="html" />
        </div>
      </div>
    </template>
  </div>
</template>

<style>
/* Token colours come from highlight.js' GitHub-dark palette, inlined so the app
   ships without loading an external stylesheet. */
.hljs-comment,
.hljs-quote {
  color: #8b949e;
}
.hljs-keyword,
.hljs-selector-tag,
.hljs-literal,
.hljs-section,
.hljs-doctag,
.hljs-type,
.hljs-name,
.hljs-strong {
  color: #ff7b72;
}
.hljs-string,
.hljs-title,
.hljs-selector-id,
.hljs-selector-class,
.hljs-attribute,
.hljs-symbol,
.hljs-bullet,
.hljs-addition {
  color: #a5d6ff;
}
.hljs-number,
.hljs-variable,
.hljs-template-variable,
.hljs-tag,
.hljs-regexp,
.hljs-link {
  color: #79c0ff;
}
.hljs-meta,
.hljs-built_in,
.hljs-builtin-name,
.hljs-params {
  color: #d2a8ff;
}
.hljs-deletion {
  color: #ffa198;
}
.hljs-emphasis {
  font-style: italic;
}
</style>
