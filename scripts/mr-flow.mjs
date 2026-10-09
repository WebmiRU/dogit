/**
 * Merge requests, driven the way a person drives them: open one from the
 * branches page, read it, comment, merge it, and check the work landed on the
 * target branch. Then it opens one that conflicts and checks that the conflict is
 * reported rather than silently applied.
 *
 *   node scripts/mr-flow.mjs [baseUrl]
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
await page.setViewport({ width: 1280, height: 965 })
page.on('dialog', (dialog) => dialog.accept())

const text = () => page.evaluate(() => document.body.innerText)
const setValue = (selector, value) =>
  page.evaluate(
    ([sel, val]) => {
      const el = document.querySelector(sel)
      if (!el) return false
      el.value = val
      el.dispatchEvent(new Event('input', { bubbles: true }))
      el.dispatchEvent(new Event('change', { bubbles: true }))
      return true
    },
    [selector, value],
  )
const clickButton = (label) =>
  page.evaluate((wanted) => {
    const button = [...document.querySelectorAll('button')].find(
      (b) => b.textContent.trim().startsWith(wanted),
    )
    if (!button) return false
    button.click()
    return true
  }, label)

await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
const inputs = await page.$$('input')
await inputs[0].type('alice')
await inputs[1].type('secret123')
await page.click('button[type="submit"]')
await page.waitForFunction(() => !location.pathname.includes('login'), { timeout: 10000 })

// A project of its own, so the merge never touches the test repository.
const suffix = Date.now().toString().slice(-6)
const project = await page.evaluate(
  async ([tag]) => {
    const created = await fetch('/api/v1/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({
        path: `mrdemo-${tag}`,
        name: 'Merge request demo',
        description: 'for the browser flow',
        visibility: 'private',
        initialize_with_readme: true,
      }),
    })
    return (await created.json()).project
  },
  [suffix],
)
check('the demo project was created', !!project?.id, String(project?.id))

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

const source = `feature/merge-demo`
await call('POST', `/api/v1/projects/${project.id}/repository/branches`, {
  name: source,
  start_point: 'main',
})
const written = await call('POST', `/api/v1/projects/${project.id}/repository/files`, {
  branch: source,
  path: 'feature.txt',
  content: 'the feature, written on its branch\n',
  message: 'Add the feature file',
})
check('the feature branch has work in it', written.status === 201, `status ${written.status}`)

// --- open the request from the branches page
await page.goto(`${baseUrl}/p/${project.path}/-/merge_requests`, { waitUntil: 'networkidle2' })
check('the merge request page exists', (await text()).includes('New merge request'))

await clickButton('New merge request')
await setValue('#mr-source', source)
await setValue('#mr-title', 'Add the feature file')
await setValue('#mr-description', 'This adds feature.txt so there is something to merge.')
await clickButton('Create merge request')
await page.waitForFunction(() => location.pathname.includes('/-/merge_requests/'), { timeout: 10000 }).catch(() => {})

check('the merge request opened', page.url().includes('/-/merge_requests/1'), page.url())
// The page fetches the request, its notes and its diff: waiting for the title to
// appear is waiting for the page, not for the router.
await page
  .waitForFunction(() => document.body.innerText.includes('Add the feature file'), { timeout: 10000 })
  .catch(() => {})
const opened = await text()
check('its title is shown', opened.includes('Add the feature file'))
check('its branches are shown', opened.includes(source) && opened.includes('main'))
check('the diff is shown', opened.includes('feature.txt'), opened.slice(0, 160).replace(/\n/g, ' '))
check(
  'the merge button offers a fast-forward',
  opened.includes('fast-forward'),
  opened.slice(0, 200).replace(/\n/g, ' '),
)

// --- a comment
await page
  .waitForFunction(() => !!document.querySelector('.comment-form textarea'), { timeout: 10000 })
  .catch(() => {})
await setValue('.comment-form textarea', 'Looks good to me')
await clickButton('Comment')
await page
  .waitForFunction(() => document.body.innerText.includes('Looks good to me'), { timeout: 10000 })
  .catch(() => {})
check('the comment appears', (await text()).includes('Looks good to me'))

// --- the global list finds it
await page.goto(`${baseUrl}/merge-requests?state=opened`, { waitUntil: 'networkidle2' })
check(
  'the global list shows the request',
  (await text()).includes('Add the feature file'),
  (await text()).slice(0, 200).replace(/\n/g, ' '),
)

// --- merge it
await page.goto(`${baseUrl}/p/${project.path}/-/merge_requests/1`, { waitUntil: 'networkidle2' })
check('the merge button is clickable', await clickButton('Merge'))
await new Promise((r) => setTimeout(r, 2000))
const merged = await text()
check(
  'the request is marked merged',
  await page.evaluate(() => !!document.querySelector('.badge.state-merged')),
  merged.slice(0, 160).replace(/\n/g, ' '),
)

const onMain = await call(
  'GET',
  `/api/v1/projects/${project.id}/repository/file?ref=main&path=feature.txt`,
)
check('the work is on the target branch', onMain.status === 200, onMain.text.slice(0, 120))
await page.screenshot({ path: '/tmp/opencode/mr.png', fullPage: true })

// --- a conflicting request must say so
const conflictSource = 'feature/conflicting'
await call('POST', `/api/v1/projects/${project.id}/repository/branches`, {
  name: conflictSource,
  start_point: 'main',
})
// Both branches change the same file.
const heads = await call('GET', `/api/v1/projects/${project.id}/repository/branches`)
void heads
await call('POST', `/api/v1/projects/${project.id}/repository/files`, {
  branch: conflictSource,
  path: 'feature.txt',
  content: 'a different version\n',
  message: 'Disagree with main',
})
await call('POST', `/api/v1/projects/${project.id}/repository/files`, {
  branch: 'main',
  path: 'feature.txt',
  content: 'the merged version\n',
  message: 'Change on main',
})

const conflicted = await call('POST', `/api/v1/projects/${project.id}/merge_requests`, {
  source_branch: conflictSource,
  target_branch: 'main',
  title: 'This one conflicts',
})
check('a conflicting request can be opened', conflicted.status === 201, conflicted.text.slice(0, 160))

await page.goto(`${baseUrl}/p/${project.path}/-/merge_requests/2`, { waitUntil: 'networkidle2' })
await page
  .waitForFunction(() => document.body.innerText.includes('This one conflicts'), { timeout: 10000 })
  .catch(() => {})
const conflictText = await text()
check('the conflict is reported', conflictText.includes('conflict'), conflictText.slice(0, 220).replace(/\n/g, ' '))
check(
  'the merge button is present but disabled for a conflict',
  await page.evaluate(() => {
    const button = [...document.querySelectorAll('button')].find((b) =>
      b.textContent.trim().startsWith('Merge'),
    )
    return !!button && button.disabled
  }),
  '',
)
check('the conflict can be resolved from the page', (await text()).includes('Resolve conflicts'))
await page.screenshot({ path: '/tmp/opencode/mr-conflict.png', fullPage: true })

console.log(steps.join('\n'))
console.log(failures.length ? `\n${failures.length} failing: ${failures.join(', ')}` : '\nall good')

await browser.close()
process.exit(failures.length ? 1 : 0)