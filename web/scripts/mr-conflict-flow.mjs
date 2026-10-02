/**
 * Conflict resolution, driven through the browser: open a request that conflicts,
 * read the three versions of the file, choose one, merge, and check that the
 * chosen content is what ended up on the target branch.
 *
 *   node scripts/mr-conflict-flow.mjs [baseUrl]
 */
import puppeteer from 'puppeteer-core'

const baseUrl = process.argv[2] ?? 'http://localhost:3000'
const chromePath = process.env.CHROME_PATH ?? '/usr/bin/google-chrome'

const failures = []
const steps = []
function check(name, ok, detail = '') {
  steps.push(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`)
  if (!ok) failures.push(name)
}

const browser = await puppeteer.launch({
  executablePath: chromePath,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
  headless: 'new',
})
const page = await browser.newPage()
await page.setViewport({ width: 1500, height: 1200 })
page.on('dialog', (dialog) => dialog.accept())

const text = () => page.evaluate(() => document.body.innerText)

await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
const inputs = await page.$$('input')
await inputs[0].type('alice')
await inputs[1].type('secret123')
await page.click('button[type="submit"]')
await page.waitForFunction(() => !location.pathname.includes('login'), { timeout: 10000 })

const call = (method, path, body) =>
  page.evaluate(
    async ([m, p, b]) => {
      const response = await fetch(p, {
        method: m,
        headers: b ? { 'Content-Type': 'application/json' } : undefined,
        credentials: 'include',
        body: b ? JSON.stringify(b) : undefined,
      })
      return { status: response.status, text: await response.text() }
    },
    [method, path, body ?? null],
  )

// A project whose branches both change the same file.
const suffix = Date.now().toString().slice(-6)
const project = await page.evaluate(
  async ([tag]) => {
    const created = await fetch('/api/v1/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({
        path: `conflictdemo-${tag}`,
        name: 'Conflict demo',
        visibility: 'private',
        initialize_with_readme: true,
      }),
    })
    return (await created.json()).project
  },
  [suffix],
)

await call('POST', `/api/v1/projects/${project.id}/repository/branches`, {
  name: 'feature/edits-readme',
  start_point: 'main',
})
await call('POST', `/api/v1/projects/${project.id}/repository/files`, {
  branch: 'feature/edits-readme',
  path: 'README.md',
  content: '# Conflict demo\n\nwritten on the feature branch\n',
  message: 'Write on the feature branch',
})
await call('POST', `/api/v1/projects/${project.id}/repository/files`, {
  branch: 'main',
  path: 'README.md',
  content: '# Conflict demo\n\nwritten on the target branch\n',
  message: 'Write on the target branch',
})
const opened = await call('POST', `/api/v1/projects/${project.id}/merge_requests`, {
  source_branch: 'feature/edits-readme',
  target_branch: 'main',
  title: 'Conflicting change',
})
check('the conflicting request opened', opened.status === 201, opened.text.slice(0, 160))

await page.goto(`${baseUrl}/p/${project.path}/-/merge_requests/1`, { waitUntil: 'networkidle2' })
await page
  .waitForFunction(() => document.body.innerText.includes('Resolve conflicts'), { timeout: 10000 })
  .catch(() => {})

const shown = await text()
check('the conflict is reported', shown.includes('conflict'), shown.slice(0, 160).replace(/\n/g, ' '))
check('both sides are offered', shown.includes('written on the target branch') && shown.includes('written on the feature branch'), '')
check(
  'the merge button explains itself instead of vanishing',
  await page.evaluate(() => {
    const button = [...document.querySelectorAll('button')].find((b) =>
      b.textContent.trim().startsWith('Merge'),
    )
    return !!button && button.disabled
  }),
)

// Take the source branch's side by clicking its card, the way a person would.
const picked = await page.evaluate(() => {
  const card = [...document.querySelectorAll('.version')].find((v) =>
    v.innerText.includes('The source branch'),
  )
  if (!card) return false
  card.click()
  return true
})
check('a version can be chosen by clicking it', picked)
await new Promise((r) => setTimeout(r, 400))

const clicked = await page.evaluate(() => {
  const button = [...document.querySelectorAll('button')].find((b) =>
    b.textContent.trim().startsWith('Merge with these resolutions'),
  )
  if (!button) return false
  button.click()
  return true
})
check('the resolution was submitted', clicked)

await page
  .waitForFunction(() => !!document.querySelector('.badge.state-merged'), { timeout: 10000 })
  .catch(() => {})

check(
  'the request is merged after resolving',
  await page.evaluate(() => !!document.querySelector('.badge.state-merged')),
  (await text()).slice(0, 160).replace(/\n/g, ' '),
)

const file = await call(
  'GET',
  `/api/v1/projects/${project.id}/repository/file?ref=main&path=README.md`,
)
check(
  'the chosen side is what landed on the target branch',
  file.text.includes('written on the feature branch'),
  file.text.slice(0, 140),
)

await page.screenshot({ path: '/tmp/opencode/mr-resolve.png', fullPage: true })
console.log(steps.join('\n'))
console.log(failures.length ? `\n${failures.length} failing: ${failures.join(', ')}` : '\nall good')

await browser.close()
process.exit(failures.length ? 1 : 0)