<script setup lang="ts">
/**
 * One file's diff, coloured and numbered.
 *
 * The same rendering is used by a commit, a merge request and a branch
 * comparison, so there is one place that knows what a unified diff means. A line
 * is classified by its first character, which is what git guarantees: "+", "-",
 * " " or "@@".
 */
import type { FileChange } from '~/types/repository'

const props = withDefaults(defineProps<{
  change: FileChange
  /** Shows a button to fold the file away. Files start expanded by default. */
  collapsible?: boolean
  /** Shown above the patch, usually a link to the file. */
  title?: string
  /** Choose side-by-side or unified diff rendering. */
  viewMode?: 'unified' | 'split'
  /** Wrap long lines rather than requiring horizontal scrolling. */
  wrapLines?: boolean
}>(), {
  viewMode: 'unified',
  wrapLines: false,
})

type DiffLine = {
  kind: 'add' | 'del' | 'hunk' | 'context'
  text: string
  oldNo: number
  newNo: number
}

/**
 * The line numbers are derived from the hunk headers rather than counted, so a
 * patch with elided context still shows the numbers the file actually has.
 */
const lines = computed<DiffLine[]>(() => {
  const out: DiffLine[] = []
  let oldNo = 0
  let newNo = 0

  for (const raw of props.change.patch.split('\n')) {
    if (raw.startsWith('@@')) {
      const match = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw)
      if (match) {
        oldNo = Number(match[1])
        newNo = Number(match[2])
      }
      out.push({ kind: 'hunk', text: raw, oldNo: 0, newNo: 0 })
      continue
    }

    // The file header is not part of the change; the file is named above.
    if (
      raw.startsWith('diff ') ||
      raw.startsWith('index ') ||
      raw.startsWith('--- ') ||
      raw.startsWith('+++ ') ||
      raw.startsWith('new file') ||
      raw.startsWith('deleted file') ||
      raw.startsWith('similarity index') ||
      raw.startsWith('rename ') ||
      raw.startsWith('old mode') ||
      raw.startsWith('new mode')
    ) {
      continue
    }

    if (raw.startsWith('+')) {
      out.push({ kind: 'add', text: raw.slice(1), oldNo: 0, newNo: newNo++ })
    } else if (raw.startsWith('-')) {
      out.push({ kind: 'del', text: raw.slice(1), oldNo: oldNo++, newNo: 0 })
    } else if (raw.startsWith(' ') || raw === '') {
      out.push({ kind: 'context', text: raw.slice(1), oldNo: oldNo++, newNo: newNo++ })
    } else {
      // Anything else is a note git adds around a rename or a mode change; it is
      // shown as context rather than dropped.
      out.push({ kind: 'context', text: raw, oldNo: 0, newNo: 0 })
    }
  }

  return out
})

const open = ref(true)
function toggle() {
  if (props.collapsible) open.value = !open.value
}

const visibleLines = computed(() => {
  if (props.collapsible && !open.value) return []
  // A long unchanged stretch is elided: reading it costs more than it explains,
  // and the line numbers make it obvious that something was skipped.
  return lines.value
})

/** Collapse runs of unchanged lines longer than this many lines. */
const CONTEXT_RUN = 8

const rendered = computed(() => {
  const out: (DiffLine | { kind: 'skip'; count: number })[] = []
  let run: DiffLine[] = []

  const flush = () => {
    if (run.length > CONTEXT_RUN) {
      const head = run.slice(0, 3)
      const tail = run.slice(-3)
      out.push(...head)
      out.push({ kind: 'skip', count: run.length - head.length - tail.length })
      out.push(...tail)
    } else {
      out.push(...run)
    }
    run = []
  }

  for (const line of visibleLines.value) {
    if (line.kind === 'hunk') {
      flush()
      out.push(line)
      continue
    }
    if (line.kind === 'context') {
      run.push(line)
      continue
    }
    flush()
    out.push(line)
  }
  flush()

  return out
})
</script>

<template>
  <div class="diff-file">
    <div class="toolbar">
      <button v-if="collapsible" class="btn btn-ghost" type="button" @click="toggle">
        {{ open ? '▾' : '▸' }}
      </button>
      <span class="name">
        <slot name="title">{{ title || change.path }}</slot>
      </span>

      <span class="badge">{{ change.status }}</span>
      <div class="spacer" />
      <span class="add">+{{ change.additions }}</span>
      <span class="del">−{{ change.deletions }}</span>
    </div>

    <div v-if="change.binary" class="empty">Binary file, no textual changes.</div>
    <div v-else-if="open && props.viewMode === 'split'" class="diff-body split-body" :class="{ 'wrap-lines': props.wrapLines }">
      <template v-for="(line, index) in rendered" :key="index">
        <div v-if="line.kind === 'hunk'" class="split-hunk">{{ line.text }}</div>
        <div v-else-if="line.kind === 'skip'" class="split-skip">⋯ {{ line.count }} unchanged lines</div>
        <div v-else class="diff-split-row">
          <div class="diff-side diff-left" :class="line.kind === 'del' ? 'k-del' : line.kind === 'context' ? 'k-context' : ''">
            <span class="ln">{{ line.kind === 'add' ? '' : line.oldNo || '' }}</span>
            <span class="lc">{{ line.kind === 'add' ? '' : (line.kind === 'del' ? '−' : ' ') + line.text }}</span>
          </div>
          <div class="diff-side diff-right" :class="line.kind === 'add' ? 'k-add' : line.kind === 'context' ? 'k-context' : ''">
            <span class="ln">{{ line.kind === 'del' ? '' : line.newNo || '' }}</span>
            <span class="lc">{{ line.kind === 'del' ? '' : (line.kind === 'add' ? '+' : ' ') + line.text }}</span>
          </div>
        </div>
      </template>
    </div>
    <div v-else-if="open" class="diff-body" :class="{ 'wrap-lines': props.wrapLines }">
      <div v-for="(line, index) in rendered" :key="index" class="diff-line" :class="`k-${line.kind}`">
        <template v-if="line.kind === 'skip'">
          <span class="ln" /><span class="ln" />
          <span class="lc skip">⋯ {{ line.count }} unchanged lines</span>
        </template>
        <template v-else>
          <span class="ln">{{ line.oldNo || '' }}</span>
          <span class="ln">{{ line.newNo || '' }}</span>
          <span class="lc"
            >{{ line.kind === 'add' ? '+' : line.kind === 'del' ? '−' : ' ' }}{{ line.text }}</span
          >
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diff-file + .diff-file {
  margin-top: 16px;
  border-top: 1px solid var(--border);
  padding-top: 12px;
}

.diff-body {
  margin-top: 8px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-code, #0d1117);
  overflow-x: auto;
  font: 12px/1.6 var(--mono);
}

.diff-line {
  display: flex;
  white-space: pre;
  min-width: max-content;
}

.ln {
  flex: 0 0 48px;
  text-align: right;
  padding-right: 10px;
  color: #6e7681;
  user-select: none;
}

.lc {
  flex: 1;
  padding-right: 12px;
}

/* Two shades for each side, as the usual diff viewers use: a line that was
   added or removed, and the sign that marks it. */
.k-add {
  background: rgba(63, 185, 80, 0.14);
}

.k-add .lc {
  color: #aff5b4;
}

.k-del {
  background: rgba(248, 81, 73, 0.12);
}

.k-del .lc {
  color: #ffdcd7;
}

.k-hunk {
  background: rgba(110, 118, 129, 0.15);
}

.k-hunk .lc {
  color: #8b949e;
}

.k-context .lc {
  color: #c9d1d9;
}

.skip {
  color: #6e7681;
  font-style: italic;
}

.btn-ghost {
  border: 0;
  background: transparent;
  padding: 0 4px;
}

.add {
  color: #3fb950;
}

.del {
  color: #f85149;
}
</style>
<style scoped>
/* Options used by the MR changes preview. */
.wrap-lines .diff-line { white-space: pre-wrap; min-width:0; }
.wrap-lines .lc { white-space:pre-wrap; overflow-wrap:anywhere; word-break:normal; }
.split-body { overflow-x:auto; }
.diff-split-row { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr); min-width:720px; border-bottom:1px solid rgba(110,118,129,.08); }
.diff-side { display:flex; min-width:0; white-space:pre; }
.diff-side + .diff-side { border-left:1px solid #30343b; }
.diff-side .ln { flex:0 0 42px; padding-right:8px; }
.diff-side .lc { flex:1; min-width:0; padding:0 8px; }
.diff-side .lc:empty { min-height:1.6em; }
.split-hunk { padding:3px 10px; background:rgba(110,118,129,.15); color:#8b949e; }
.split-skip { padding:3px 10px; text-align:center; background:rgba(110,118,129,.07); color:#6e7681; font-style:italic; }
.wrap-lines .diff-split-row { min-width:0; }
.wrap-lines .diff-side { white-space:pre-wrap; overflow-wrap:anywhere; }
</style>
