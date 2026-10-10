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
