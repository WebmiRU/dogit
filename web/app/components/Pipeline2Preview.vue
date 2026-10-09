<script setup lang="ts">
import { ref } from 'vue'

const props = defineProps<{ projectPath: string }>()
const activeTab = ref<'pipeline' | 'jobs' | 'tests'>('pipeline')
</script>

<template>
  <div class="p2-preview" aria-label="Pipeline2 visual mockup">
    <header class="p2-topbar">
      <div class="p2-gitlab-mark" aria-hidden="true">
        <svg viewBox="0 0 36 34"><path d="M18 32 24.6 11.5H11.4L18 32Z" fill="#e24329"/><path d="m18 32-6.6-20.5H2.7L18 32Z" fill="#fc6d26"/><path d="M2.7 11.5.4 18.6a2 2 0 0 0 .7 2.2L18 32 2.7 11.5Z" fill="#fca326"/><path d="M18 32 24.6 11.5h8.7L18 32Z" fill="#fc6d26"/><path d="m33.3 11.5 2.3 7.1a2 2 0 0 1-.7 2.2L18 32l15.3-20.5Z" fill="#e24329"/></svg>
      </div>
      <div class="p2-global-search">
        <svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5"/><path d="m13 13 4 4"/></svg>
        <span>Search or go to...</span>
        <kbd>/</kbd>
      </div>
      <div class="p2-top-actions">
        <span class="p2-top-icon p2-plus">＋</span>
        <span class="p2-top-counter"><svg viewBox="0 0 20 20"><rect x="4" y="3" width="9" height="13" rx="1"/><path d="M8 6h8v11H8"/></svg><small>0</small></span>
        <span class="p2-top-counter"><svg viewBox="0 0 20 20"><path d="M3 5h4l2 2h8v9H3z"/><path d="m7 11 2 2 4-4"/></svg><small>1</small></span>
        <span class="p2-top-counter"><svg viewBox="0 0 20 20"><rect x="3" y="4" width="14" height="12" rx="1"/><path d="m6 10 2.5 2.5L14 7"/></svg><small>0</small></span>
        <span class="p2-admin-button"><svg viewBox="0 0 20 20"><path d="M8 2h4l.6 2.1 1.8 1 2-.6 2 3.5-1.5 1.6v2l1.5 1.7-2 3.4-2-.6-1.8 1L12 19H8l-.6-2.1-1.8-1-2 .6-2-3.4 1.5-1.7v-2L1.6 7.9l2-3.5 2 .6 1.8-1L8 2Z"/><circle cx="10" cy="10.5" r="2.5"/></svg>Admin</span>
        <span class="p2-avatar" aria-label="User avatar"><span>◉</span></span>
      </div>
    </header>

    <div class="p2-shell">
      <aside class="p2-sidebar">
        <div class="p2-side-caption">Project</div>
        <div class="p2-project-select">
          <span class="p2-project-logo">H</span>
          <span>Home Store API</span>
        </div>

        <nav class="p2-nav" aria-label="Project navigation">
          <div class="p2-nav-row p2-nav-heading"><span class="p2-nav-icon">⚒</span><span>Pinned</span><span class="p2-chevron">⌄</span></div>
          <div class="p2-nav-row p2-nav-child"><span>Work items</span></div>
          <div class="p2-nav-row p2-nav-child"><span>Merge requests</span><small class="p2-nav-count">1</small></div>
          <div class="p2-nav-row p2-nav-child is-active"><span>Pipelines</span></div>

          <div class="p2-nav-section"><span class="p2-nav-icon">♙</span><span>Manage</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">▣</span><span>Plan</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">‹›</span><span>Code</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">♧</span><span>Build</span><span class="p2-chevron">⌄</span></div>
          <div class="p2-nav-row p2-nav-child is-active"><span>Pipelines</span></div>
          <div class="p2-nav-row p2-nav-child"><span>Jobs</span></div>
          <div class="p2-nav-row p2-nav-child"><span>Pipeline editor</span></div>
          <div class="p2-nav-row p2-nav-child"><span>Pipeline schedules</span></div>
          <div class="p2-nav-row p2-nav-child"><span>Artifacts</span></div>

          <div class="p2-nav-section"><span class="p2-nav-icon">♧</span><span>Secure</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">♧</span><span>Deploy</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">◉</span><span>Operate</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">▤</span><span>Monitor</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">♜</span><span>Analyze</span><span class="p2-chevron">›</span></div>
          <div class="p2-nav-section"><span class="p2-nav-icon">⚙</span><span>Settings</span><span class="p2-chevron">›</span></div>
        </nav>

        <div class="p2-sidebar-bottom">
          <div><span class="p2-bottom-icon">♙</span>Upgrade subscription</div>
          <div><span class="p2-bottom-icon">ⓘ</span>Help</div>
          <div><span class="p2-bottom-icon">⇤</span>Collapse sidebar</div>
        </div>
      </aside>

      <main class="p2-main">
        <div class="p2-breadcrumbs">
          <span>Home Store</span><b>/</b><span>Home Store API</span><b>/</b><span>Pipelines</span><b>/</b><strong>#562</strong>
        </div>

        <section class="p2-run-summary">
          <button class="p2-delete" type="button">Delete</button>
          <h1>#562</h1>
          <div class="p2-summary-status">
            <span class="p2-passed">Passed</span>
            <span>Created 1 week ago by <strong>group-deploy-1</strong>, finished 1 week ago</span>
          </div>
          <div class="p2-commit-line">
            <span>For commit</span><code>43716dfd</code>
            <svg class="p2-copy" viewBox="0 0 18 18" aria-hidden="true"><rect x="2" y="4" width="9" height="11" rx="1"/><path d="M6 2h9v11"/></svg>
            <span class="p2-commit-title">Настройка «Клик по ссылке: переход или отбор»</span>
          </div>
          <div class="p2-related-line">1 related merge request: <NuxtLink class="p2-related-link" :to="`/p/${props.projectPath}/-/merge_requests/1`">!1 Права доступа (каскад склад-&gt;хранилище-&gt;предметы), аватар и logout</NuxtLink></div>
          <div class="p2-pipeline-meta">
            <span class="p2-tag p2-tag-green">latest</span>
            <span class="p2-tag p2-tag-blue">branch</span>
            <span class="p2-meta-item"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="5" cy="8" r="3"/><circle cx="11" cy="8" r="3"/></svg>3 jobs</span>
            <span class="p2-meta-item"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6"/><path d="M8 4v4l3 2"/></svg>2 minutes 40 seconds, queued for 2 seconds</span>
          </div>
        </section>

        <nav class="p2-content-tabs" aria-label="Pipeline tabs">
          <button type="button" :class="{ active: activeTab === 'pipeline' }" :aria-selected="activeTab === 'pipeline'" @click="activeTab = 'pipeline'">Pipeline</button>
          <button type="button" :class="{ active: activeTab === 'jobs' }" :aria-selected="activeTab === 'jobs'" @click="activeTab = 'jobs'">Jobs <b>3</b></button>
          <button type="button" :class="{ active: activeTab === 'tests' }" :aria-selected="activeTab === 'tests'" @click="activeTab = 'tests'">Tests <b>0</b></button>
        </nav>

        <section v-if="activeTab === 'pipeline'" class="p2-pipeline-canvas" aria-label="Pipeline jobs graph">
          <div class="p2-flow-line" aria-hidden="true"></div>
          <div class="p2-stage p2-build-stage">
            <div class="p2-stage-title">build</div>
            <div class="p2-jobs">
              <div class="p2-job">
                <svg class="p2-job-check" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="m7.5 12 3 3 6-6"/></svg>
                <span>build-php</span>
                <span class="p2-mini-retry" aria-hidden="true"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></span>
              </div>
              <div class="p2-job">
                <svg class="p2-job-check" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="m7.5 12 3 3 6-6"/></svg>
                <span>build-web</span>
                <span class="p2-mini-retry" aria-hidden="true"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></span>
              </div>
            </div>
          </div>

          <div class="p2-stage p2-deploy-stage">
            <div class="p2-stage-title">deploy</div>
            <div class="p2-jobs">
              <div class="p2-job">
                <svg class="p2-job-check" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="m7.5 12 3 3 6-6"/></svg>
                <span>deploy</span>
                <span class="p2-mini-retry" aria-hidden="true"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></span>
              </div>
            </div>
          </div>
        </section>

        <section v-else-if="activeTab === 'jobs'" class="p2-jobs-table-wrap" aria-label="Pipeline jobs">
          <div class="p2-jobs-table">
            <div class="p2-jobs-table-head">
              <span>Status</span><span>Job</span><span>Stage</span><span>Coverage</span><span aria-hidden="true"></span>
            </div>
            <div class="p2-job-row">
              <div class="p2-job-status">
                <span class="p2-passed">Passed</span>
                <span class="p2-job-detail"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7"/><path d="M10 5v5l3 2"/></svg>00:01:01</span>
                <span class="p2-job-detail"><svg viewBox="0 0 20 20" aria-hidden="true"><rect x="3" y="5" width="14" height="12" rx="1"/><path d="M6 2v5M14 2v5M3 9h14"/></svg>1 week ago</span>
              </div>
              <div class="p2-job-name">
                <a>#3044: deploy</a>
                <div class="p2-job-tags"><span><svg viewBox="0 0 18 18" aria-hidden="true"><circle cx="5" cy="4" r="2"/><circle cx="13" cy="4" r="2"/><circle cx="9" cy="14" r="2"/><path d="m6.5 5.5 2 6m3-6-2 6M7 4h4"/></svg>dev</span><span><svg viewBox="0 0 18 18" aria-hidden="true"><circle cx="5" cy="9" r="2.5"/><path d="M7.5 9H16M10 6.5v5"/></svg>43716dfd</span></div>
              </div>
              <div class="p2-job-stage">deploy</div>
              <div class="p2-job-coverage"></div>
              <button class="p2-retry" type="button" aria-label="Retry deploy job"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></button>
            </div>
            <div class="p2-job-row">
              <div class="p2-job-status">
                <span class="p2-passed">Passed</span>
                <span class="p2-job-detail"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7"/><path d="M10 5v5l3 2"/></svg>00:00:15</span>
                <span class="p2-job-detail"><svg viewBox="0 0 20 20" aria-hidden="true"><rect x="3" y="5" width="14" height="12" rx="1"/><path d="M6 2v5M14 2v5M3 9h14"/></svg>1 week ago</span>
              </div>
              <div class="p2-job-name">
                <a>#3043: build-web</a>
                <div class="p2-job-tags"><span><svg viewBox="0 0 18 18" aria-hidden="true"><circle cx="5" cy="4" r="2"/><circle cx="13" cy="4" r="2"/><circle cx="9" cy="14" r="2"/><path d="m6.5 5.5 2 6m3-6-2 6M7 4h4"/></svg>dev</span><span><svg viewBox="0 0 18 18" aria-hidden="true"><circle cx="5" cy="9" r="2.5"/><path d="M7.5 9H16M10 6.5v5"/></svg>43716dfd</span></div>
              </div>
              <div class="p2-job-stage">build</div>
              <div class="p2-job-coverage"></div>
              <button class="p2-retry" type="button" aria-label="Retry build-web job"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></button>
            </div>
            <div class="p2-job-row">
              <div class="p2-job-status">
                <span class="p2-passed">Passed</span>
                <span class="p2-job-detail"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7"/><path d="M10 5v5l3 2"/></svg>00:01:39</span>
                <span class="p2-job-detail"><svg viewBox="0 0 20 20" aria-hidden="true"><rect x="3" y="5" width="14" height="12" rx="1"/><path d="M6 2v5M14 2v5M3 9h14"/></svg>1 week ago</span>
              </div>
              <div class="p2-job-name">
                <a>#3042: build-php</a>
                <div class="p2-job-tags"><span><svg viewBox="0 0 18 18" aria-hidden="true"><circle cx="5" cy="4" r="2"/><circle cx="13" cy="4" r="2"/><circle cx="9" cy="14" r="2"/><path d="m6.5 5.5 2 6m3-6-2 6M7 4h4"/></svg>dev</span><span><svg viewBox="0 0 18 18" aria-hidden="true"><circle cx="5" cy="9" r="2.5"/><path d="M7.5 9H16M10 6.5v5"/></svg>43716dfd</span></div>
              </div>
              <div class="p2-job-stage">build</div>
              <div class="p2-job-coverage"></div>
              <button class="p2-retry" type="button" aria-label="Retry build-php job"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 2v6h6"/><path d="M21 12a9 9 0 0 0-15-6.7L3 8"/><path d="M21 22v-6h-6"/><path d="M3 12a9 9 0 0 0 15 6.7L21 16"/></svg></button>
            </div>
          </div>
        </section>
        <section v-else class="p2-tests-empty" aria-label="Pipeline tests"></section>
      </main>
    </div>

  </div>
</template>

<style scoped>
.p2-preview {
  position: fixed;
  z-index: 10000;
  inset: 0;
  overflow: hidden;
  background: #17171d;
  color: #e4e4e8;
  font: 13px/1.4 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  isolation: isolate;
}
.p2-preview * { box-sizing: border-box; }
.p2-topbar {
  position: absolute;
  inset: 0 0 auto;
  height: 42px;
  display: flex;
  align-items: center;
  padding: 0 9px;
  background: #29292e;
  border-bottom: 1px solid #36363c;
  z-index: 3;
}
.p2-gitlab-mark { width: 24px; height: 25px; display: flex; align-items: center; justify-content: center; }
.p2-gitlab-mark svg { display: block; width: 23px; height: 22px; }
.p2-global-search {
  position: absolute;
  left: 50%;
  top: 6px;
  transform: translateX(-50%);
  display: flex;
  align-items: center;
  gap: 7px;
  width: 378px;
  max-width: 36vw;
  height: 29px;
  padding: 0 8px;
  border: 1px solid #57575f;
  border-radius: 8px;
  color: #b8b8c0;
  background: #34343b;
  font-size: 12px;
}
.p2-global-search svg { width: 15px; height: 15px; fill: none; stroke: currentColor; stroke-width: 1.6; }
.p2-global-search span { flex: 1; }
.p2-global-search kbd { min-width: 17px; text-align: center; border-radius: 3px; background: #222228; color: #c5c5cc; font: inherit; font-size: 11px; line-height: 17px; }
.p2-top-actions { display: flex; align-items: center; gap: 14px; margin-left: auto; height: 100%; }
.p2-top-icon { display: inline-flex; align-items: center; justify-content: center; width: 23px; height: 23px; border-radius: 7px; background: #424249; color: #eeeef2; font-size: 18px; line-height: 1; }
.p2-top-counter { display: inline-flex; align-items: center; gap: 4px; color: #dbdbe1; font-size: 11px; }
.p2-top-counter svg { width: 16px; height: 16px; fill: none; stroke: currentColor; stroke-width: 1.4; }
.p2-top-counter small { font-size: 11px; }
.p2-admin-button { display: inline-flex; height: 28px; align-items: center; gap: 5px; padding: 0 9px; border-radius: 7px; background: #45454d; font-weight: 600; font-size: 12px; }
.p2-admin-button svg { height: 14px; width: 14px; fill: none; stroke: currentColor; stroke-width: 1.3; }
.p2-avatar { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; border-radius: 50%; background: #f04b35; color: #fff3ef; font-size: 20px; font-weight: 700; }
.p2-shell { position: absolute; inset: 42px 0 0; display: flex; min-height: 0; }
.p2-sidebar {
  position: relative;
  display: flex;
  flex-direction: column;
  flex: 0 0 206px;
  width: 206px;
  padding: 9px 7px 10px;
  background: #222227;
  border-right: 1px solid #29292f;
  color: #dfdfe5;
  overflow: hidden;
}
.p2-side-caption { padding: 0 8px 7px; font-size: 12px; font-weight: 650; color: #ececf0; }
.p2-project-select { display: flex; align-items: center; gap: 8px; height: 31px; margin-bottom: 9px; padding: 0 4px; font-size: 13px; font-weight: 600; white-space: nowrap; }
.p2-project-logo { display: flex; align-items: center; justify-content: center; width: 22px; height: 22px; flex: 0 0 22px; border-radius: 5px; color: #d7f3e2; background: #477c68; font-weight: 750; }
.p2-nav { min-height: 0; overflow: hidden; }
.p2-nav-row, .p2-nav-section { position: relative; display: flex; align-items: center; height: 26px; padding: 0 8px; gap: 9px; border-radius: 6px; font-size: 12.5px; white-space: nowrap; }
.p2-nav-heading { height: 28px; margin-bottom: 0; font-weight: 600; }
.p2-nav-icon { display: inline-flex; width: 15px; flex: 0 0 15px; align-items: center; justify-content: center; color: #e1e1e7; font-size: 14px; }
.p2-nav-row.p2-nav-child { padding-left: 30px; }
.p2-nav-row.is-active { background: #344b63; color: #e6f0ff; }
.p2-nav-count { margin-left: auto; font-size: 10px; }
.p2-chevron { margin-left: auto; font-size: 17px; line-height: 1; color: #c3c3cc; }
.p2-nav-section { margin-top: 3px; height: 27px; }
.p2-nav-section:nth-of-type(9) { margin-top: 3px; }
.p2-sidebar-bottom { display: grid; gap: 1px; margin-top: auto; padding-top: 8px; }
.p2-sidebar-bottom > div { display: flex; align-items: center; gap: 9px; min-height: 26px; padding: 0 5px; font-size: 12px; white-space: nowrap; }
.p2-bottom-icon { width: 14px; flex: 0 0 14px; text-align: center; font-size: 14px; }
.p2-main { display: flex; flex: 1 1 auto; min-width: 0; flex-direction: column; background: #17171d; }
.p2-breadcrumbs { display: flex; align-items: center; flex: 0 0 42px; gap: 9px; padding: 0 14px; border-bottom: 1px solid #303037; color: #c1c1ca; font-size: 11px; white-space: nowrap; }
.p2-breadcrumbs b { color: #8d8d98; font-weight: 400; }
.p2-breadcrumbs strong { color: #efeff2; font-weight: 650; }
.p2-run-summary { position: relative; flex: 0 0 auto; padding: 14px 14px 8px; }
.p2-run-summary h1 { margin: 0 0 4px; color: #f5f5f7; font-size: 27px; line-height: 1.3; font-weight: 650; letter-spacing: -0.5px; }
.p2-delete { position: absolute; top: 17px; right: 14px; height: 28px; padding: 0 13px; border: 0; border-radius: 7px; color: #f3f3f5; background: #414148; font: inherit; font-size: 12px; }
.p2-summary-status { display: flex; align-items: center; gap: 5px; color: #c7c7ce; font-size: 12px; line-height: 21px; }
.p2-summary-status strong { color: #e8e8ed; font-weight: 650; }
.p2-passed { display: inline-flex; align-items: center; height: 20px; padding: 2px 8px; border: 0; border-radius: 4px; background: #1d4227; color: #7ee29a; font-size: 12px; font-weight: 500; line-height: 1.4; white-space: nowrap; }
.p2-passed svg { width: 16px; height: 16px; fill: #1f7047; stroke: #1b3529; stroke-width: 1; }
.p2-passed svg path { fill: none; stroke: #153b2a; stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
.p2-commit-line, .p2-related-line { display: flex; align-items: center; gap: 7px; min-height: 20px; color: #c6c6ce; font-size: 12px; }
.p2-commit-line { margin-top: 7px; }
.p2-commit-line code { padding: 1px 4px; border-radius: 3px; color: #d0d0d7; background: #37373e; font: 11px/15px ui-monospace, SFMono-Regular, Menlo, monospace; }
.p2-copy { width: 14px; height: 14px; flex: 0 0 14px; fill: none; stroke: #dedee3; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.p2-commit-title { color: #c6c6ce; }
.p2-related-line { margin-top: 4px; }
.p2-related-line .p2-related-link { color: #78b6f3; text-decoration: none; cursor: pointer; }
.p2-related-line .p2-related-link:hover { text-decoration: underline; }
.p2-pipeline-meta { display: flex; align-items: center; gap: 5px; min-height: 25px; margin-top: 1px; color: #c7c7cf; font-size: 12px; }
.p2-tag { display: inline-flex; align-items: center; height: 20px; padding: 2px 8px; border: 0; border-radius: 4px; font-size: 12px; font-weight: 500; line-height: 1.4; white-space: nowrap; }
.p2-tag-green { color: #7ee29a; background: #1d4227; }
.p2-tag-blue { color: var(--info, #8bbdff); background: #303136; }
.p2-meta-item { display: inline-flex; align-items: center; gap: 4px; margin-left: 1px; white-space: nowrap; }
.p2-meta-item svg { width: 14px; height: 14px; fill: none; stroke: currentColor; stroke-width: 1.4; }
.p2-content-tabs { display: flex; flex: 0 0 43px; align-items: stretch; gap: 0; margin: 0 14px; border-bottom: 1px solid #303037; }
.p2-content-tabs > button { position: relative; display: inline-flex; align-items: center; gap: 5px; padding: 0 18px; margin: 0 0 -1px; border: 0 !important; border-radius: 0 !important; box-shadow: none !important; background: transparent; color: #ddddE3; font: inherit; font-weight: 600; white-space: nowrap; cursor: pointer; }
.p2-content-tabs > button.active { color: #f4f4f6; }
.p2-content-tabs > button.active::after { content: ""; position: absolute; right: 0; bottom: -1px; left: 0; height: 3px; border-radius: 0; background: #4f9de8; pointer-events: none; }
.p2-content-tabs > button:focus-visible { outline: 2px solid #4f9de8; outline-offset: -3px; }
.p2-content-tabs b { display: inline-flex; flex: 0 0 16px; align-items: center; justify-content: center; width: 16px; height: 16px; border-radius: 50%; background: #85858d; color: #1b1b20; font: inherit; font-size: 12px; font-weight: 650; line-height: 16px; }
.p2-pipeline-canvas { position: relative; flex: 1 1 auto; min-height: 300px; margin: 14px 14px 0; border-radius: 8px; background: #28282e; overflow: hidden; }
.p2-stage { position: absolute; top: 20px; width: 222px; border-radius: 10px; padding: 5px 5px 6px; background: #39393f; }
.p2-build-stage { left: 20px; }
.p2-deploy-stage { left: 269px; }
.p2-stage-title { height: 27px; padding: 2px 9px 3px; color: #efeff3; font-size: 12px; font-weight: 650; }
.p2-jobs { padding: 4px 5px; border-radius: 7px; background: #17171d; }
.p2-job { display: flex; align-items: center; gap: 7px; min-width: 0; height: 38px; padding: 0 5px; color: #e2e2e7; font-size: 12px; }
.p2-job-check { width: 22px; height: 22px; flex: 0 0 22px; fill: none; stroke: #42c77a; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; }
.p2-retry { display: inline-flex; align-items: center; justify-content: center; margin-left: auto; width: 32px; height: 32px; flex: 0 0 32px; border: 0; border-radius: 50%; padding: 0; background: #414148; color: #f0f0f4; cursor: default; }
.p2-retry svg { width: 19px; height: 19px; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
.p2-mini-retry { display: inline-flex; align-items: center; justify-content: center; margin-left: auto; width: 21px; height: 21px; flex: 0 0 21px; border-radius: 50%; background: #414148; color: #d0d0d6; }
.p2-mini-retry svg { width: 15px; height: 15px; fill: none; stroke: currentColor; stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.p2-flow-line { position: absolute; z-index: 0; top: 35px; left: 242px; width: 27px; height: 2px; background: #a5a5ad; }
@media (max-width: 900px) {
  .p2-sidebar { flex-basis: 184px; width: 184px; }
  .p2-global-search { left: 46%; max-width: 32vw; }
  .p2-top-actions { gap: 8px; }
  .p2-commit-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .p2-pipeline-meta { flex-wrap: wrap; }
  .p2-stage { width: 205px; }
  .p2-deploy-stage { left: 251px; }
  .p2-flow-line { left: 225px; width: 26px; }
}
@media (max-width: 620px) {
  .p2-sidebar { flex-basis: 48px; width: 48px; padding-right: 5px; padding-left: 5px; }
  .p2-side-caption, .p2-project-select span:not(.p2-project-logo), .p2-nav-row > span:not(.p2-nav-icon), .p2-nav-section > span:not(.p2-nav-icon), .p2-sidebar-bottom { display: none; }
  .p2-project-select { justify-content: center; padding: 0; }
  .p2-nav-row, .p2-nav-section { justify-content: center; padding: 0; }
  .p2-nav-row.p2-nav-child { display: none; }
  .p2-nav-icon { width: 24px; flex-basis: 24px; }
  .p2-global-search { left: 40%; width: 210px; max-width: 38vw; }
  .p2-top-counter:nth-child(3), .p2-top-counter:nth-child(4), .p2-admin-button { display: none; }
  .p2-breadcrumbs { gap: 5px; padding: 0 8px; font-size: 10px; }
  .p2-run-summary { padding-right: 8px; padding-left: 8px; }
  .p2-run-summary h1 { font-size: 24px; }
  .p2-delete { right: 8px; }
  .p2-summary-status { flex-wrap: wrap; }
  .p2-commit-title { max-width: 42vw; }
  .p2-related-line { align-items: flex-start; flex-wrap: wrap; }
  .p2-content-tabs { margin: 0 8px; }
  .p2-pipeline-canvas { margin: 10px 8px 0; }
  .p2-stage { width: 175px; }
  .p2-deploy-stage { left: 197px; }
  .p2-flow-line { left: 181px; width: 16px; }
}


/* Controls that inherit their font through a shorthand need an explicit size too. */
.p2-global-search kbd, .p2-top-counter small { font-size: 14px; }
.p2-content-tabs > button { font-size: 14px; }
.p2-content-tabs b { font-size: 12px; }

/* Typography follows the supplied reference: 14px across the page with the explicit badge/title exceptions. */
.p2-preview { font-size: 14px; }
.p2-preview * { font-size: 14px; }
.p2-run-summary h1 { font-size: 30px; }
.p2-passed { font-size: 12px; }
.p2-tag { font-size: 12px; }
.p2-jobs-table-wrap { flex: 1 1 auto; min-height: 0; margin: 12px 24px 0; overflow: auto; }
.p2-jobs-table { width: 100%; min-width: 820px; color: #e4e4e8; }
.p2-jobs-table-head, .p2-job-row { display: grid; grid-template-columns: 220px minmax(350px, 1.2fr) minmax(170px, .3fr) minmax(120px, 1.3fr) 70px; align-items: center; }
.p2-jobs-table-head { min-height: 42px; border-top: 1px solid #37373e; border-bottom: 1px solid #45454d; font-weight: 650; }
.p2-jobs-table-head > span { padding: 0 22px; }
.p2-jobs-table-head > span:last-child { padding: 0; }
.p2-job-row { min-height: 68px; border-bottom: 1px solid #45454d; }
.p2-job-status { display: flex; flex-direction: column; align-items: flex-start; justify-content: center; gap: 1px; padding: 5px 22px; }
.p2-job-status .p2-passed { height: 18px; padding: 0 5px 0 2px; gap: 3px; font-size: 12px; line-height: 18px; }
.p2-job-status .p2-passed svg { width: 12px; height: 12px; }
.p2-job-detail { display: inline-flex; align-items: center; gap: 5px; color: #b5b5bf; line-height: 1.3; white-space: nowrap; }
.p2-job-detail svg { width: 16px; height: 16px; flex: 0 0 16px; fill: none; stroke: currentColor; stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
.p2-job-name { display: flex; min-width: 0; flex-direction: column; align-items: flex-start; justify-content: center; gap: 3px; padding: 5px 22px; }
.p2-job-name a { color: #8fbee9; text-decoration: none; white-space: nowrap; }
.p2-job-tags { display: flex; flex-wrap: wrap; gap: 6px; }
.p2-job-tags > span { display: inline-flex; align-items: center; gap: 4px; padding: 1px 7px; border: 0; border-radius: 4px; background: #303136; color: #bfc9d4; font-size: 12px; line-height: 18px; white-space: nowrap; }
.p2-job-tags > span:first-child { background: #303136; color: var(--info, #8bbdff); }
.p2-job-tags > span:last-child { background: #262b36; color: var(--text-muted, #b6bdc8); }
.p2-job-tags svg { width: 14px; height: 14px; fill: none; stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.p2-job-stage { align-self: stretch; display: flex; align-items: flex-start; padding: 9px 22px; color: #bcbcc5; }
.p2-job-coverage { align-self: stretch; }
.p2-job-row .p2-retry { margin: 0 0 0 auto; align-self: center; }
.p2-tests-empty { flex: 1 1 auto; min-height: 300px; margin: 12px 24px 0; border-top: 1px solid #37373e; }
@media (max-width: 900px) {
  .p2-jobs-table-wrap { margin-right: 14px; margin-left: 14px; }
  .p2-jobs-table-head, .p2-job-row { grid-template-columns: 170px minmax(300px, 2.5fr) minmax(120px, 1fr) minmax(70px, 1fr) 60px; }
  .p2-job-status, .p2-job-name, .p2-job-stage { padding-right: 12px; padding-left: 12px; }
}
@media (max-width: 620px) {
  .p2-jobs-table-wrap { margin-right: 8px; margin-left: 8px; }
  .p2-jobs-table-head, .p2-job-row { grid-template-columns: 150px minmax(260px, 2.5fr) minmax(100px, 1fr) minmax(50px, 1fr) 58px; }
  .p2-job-status, .p2-job-name, .p2-job-stage { padding-right: 8px; padding-left: 8px; }
  .p2-job-row .p2-retry { width: 30px; height: 30px; flex-basis: 30px; }
}

</style>