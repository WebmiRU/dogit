/**
 * A project's pipelines page: the list, one run with its jobs, and starting a run.
 *
 * The page exists mostly for the logs, so the checks are about what somebody can
 * find rather than about layout.
 */
import { BASE, browser, check, login, shot, step, textOf, verdict } from './helpers.mjs'

const failures = []
const { instance, page } = await browser()

try {
  step('sign in as an administrator')
  await login(page)

  step('find a project')
  const projectPath = await page.evaluate(async () => {
    const answer = await (await fetch('/api/v1/projects', { credentials: 'include' })).json()
    return answer.projects?.[0]?.path ?? null
  })
  check(!!projectPath, `using project ${projectPath}`, failures)

  step('give it a pipeline configuration')
  await page.evaluate(async (project) => {
    const config = [
      'stages:',
      '  - build',
      '  - test',
      'say-hello:',
      '  stage: test',
      '  image: alpine:3.21',
      '  script:',
      '    - echo "hello from a job"',
      '',
    ].join('\n')

    await fetch(`/api/v1/projects/${encodeURIComponent(project)}/repository/files`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        path: '.dogit-ci.yml',
        content: config,
        message: 'Add a pipeline',
        branch: 'main',
      }),
    })
  }, projectPath)

  step('open the pipelines page')
  await page.goto(`${BASE}/p/${projectPath}/-/pipelines`, { waitUntil: 'networkidle0' })
  await page.waitForSelector('.pipeline-list, .card.empty', { timeout: 15000 })
  await new Promise((resolve) => setTimeout(resolve, 800))

  const tabs = await page.$$eval('.repo-tabs a', (nodes) => nodes.map((node) => node.textContent.trim()))
  check(tabs.includes('Pipelines'), `the tab is there: ${tabs.join(' / ')}`, failures)

  step('start a run from the page')
  await page.$$eval('button', (nodes) =>
    nodes.find((node) => node.textContent.includes('Run pipeline'))?.click(),
  )
  await new Promise((resolve) => setTimeout(resolve, 2500))

  const rows = await page.$$eval('.pipeline-row', (nodes) => nodes.length)
  check(rows > 0, `the run appears in the list (${rows})`, failures)

  const row = rows > 0 ? await textOf(page, '.pipeline-row') : ''
  check(/#\d+/.test(row), `it is numbered: ${row.replace(/\n/g, ' ').slice(0, 70)}`, failures)
  check(/main/.test(row), 'and says what it ran against', failures)

  await shot(page, 'pipelines')

  step('the run has jobs')
  const jobs = await page.$$eval('.job-card', (nodes) => nodes.length)
  check(jobs > 0, `the run lists its jobs (${jobs})`, failures)

  const job = await textOf(page, '.job-card')
  check(/say-hello/.test(job), 'the job from the configuration is the one there', failures)
  check(/alpine/.test(job), 'and shows the image it runs in', failures)
  check(/alpine/.test(job), 'and shows the image it runs in', failures)

  step('the script is there to read')
  const script = await textOf(page, '.job-card .script')
  check(/hello from a job/.test(script), 'the script came from the configuration file', failures)

  await shot(page, 'pipeline-jobs')

  step('a run without a configuration says so')
  const other = await page.evaluate(async (path) => {
    const answer = await (await fetch('/api/v1/projects', { credentials: 'include' })).json()
    // A project other than the one we just gave a configuration to: a project that
    // has one is not the thing being checked.
    const candidate = answer.projects.find((project) => project.path !== path)
    if (!candidate) return null

    const response = await fetch(`/api/v1/projects/${encodeURIComponent(candidate.path)}/pipelines`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ref: 'main' }),
    })
    return { path: candidate.path, status: response.status, body: (await response.text()).slice(0, 200) }
  }, projectPath)

  if (other) {
    // 400 when the file is unreadable, 404 when there is none: both are refusals,
    // and what matters is that nothing was silently run.
    check(
      other.status === 400 || other.status === 404,
      `a project with no configuration is refused (${other.status})`,
      failures,
    )
    check(
      /\.dogit-ci\.yml/.test(other.body),
      'and the refusal names the file it was looking for',
      failures,
    )
  } else {
    check(true, 'no project without a configuration was available to check', failures)
  }
} finally {
  await instance.close()
}

verdict(failures)