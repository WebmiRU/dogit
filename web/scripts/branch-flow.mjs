/**
 * Branch and tag management, driven the way a person drives it.
 *
 * It creates a branch, deletes it, and checks that the list, the branch selector
 * and the page all agree afterwards; then it deletes the branch the page is
 * looking at and checks that the page moves rather than breaking; then it checks
 * that tags live on their own page.
 *
 *   node scripts/branch-flow.mjs [baseUrl]
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
await page.setViewport({ width: 1400, height: 1000 })
page.on('dialog', (dialog) => dialog.accept())

const text = () => page.evaluate(() => document.body.innerText)

await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
const inputs = await page.$$('input')
await inputs[0].type('alice')
await inputs[1].type('secret123')
await page.click('button[type="submit"]')
await page.waitForFunction(() => !location.pathname.includes('login'), { timeout: 10000 })

// The API is used to set up and to verify, so a passing test cannot be satisfied
// by the page hiding something that is still on the server.
async function call(method, path, body) {
  return page.evaluate(
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
}

async function projectId() {
  const response = await call('GET', '/api/v1/projects')
  return JSON.parse(response.text).projects.find((p) => p.path === 'hello').id
}

const id = await projectId()
const branch = `deletable-${Date.now()}`

const created = await call('POST', `/api/v1/projects/${id}/repository/branches`, {
  name: branch,
  start_point: 'main',
})
check('the test branch was created', created.status === 201, `status ${created.status}`)

await page.goto(`${baseUrl}/p/hello/-/branches?ref=main`, { waitUntil: 'networkidle2' })
check('the branch is listed', (await text()).includes(branch), branch)

// --- delete it from the list
const clicked = await page.evaluate((name) => {
  const row = [...document.querySelectorAll('li')].find((li) => li.innerText.includes(name))
  const button = row?.querySelector('button')
  if (!button) return false
  button.click()
  return true
}, branch)
check('the delete button was pressed', clicked)

await page
  .waitForFunction((name) => !document.body.innerText.includes(name), { timeout: 10000 }, branch)
  .catch(() => {})
check('the branch disappears from the list', !(await text()).includes(branch))

const gone = await call('GET', `/api/v1/projects/${id}/repository/refs`)
check(
  'the branch is gone on the server too',
  !JSON.parse(gone.text).branches.some((b) => b.name === branch),
)

// --- the default branch is marked as such, and is not offered for deletion
const defaultRow = await page.evaluate(() => {
  const row = [...document.querySelectorAll('li')].find((li) => li.querySelector('.badge'))
  if (!row) return null
  // The row starts with an icon, so the branch name is the link's text.
  return {
    name: row.querySelector('.name')?.textContent?.trim(),
    hasDelete: !!row.querySelector('button'),
  }
})
check('the default branch is the one marked default', defaultRow?.name === 'main', JSON.stringify(defaultRow))
check('the default branch cannot be deleted from here', defaultRow?.hasDelete === false)

// --- deleting the branch the page is showing moves the page instead of breaking it
// --- deleting the branch the page is showing must move the page, not break it
const live = `viewing-${Date.now()}`
await call('POST', `/api/v1/projects/${id}/repository/branches`, { name: live, start_point: 'main' })
await page.goto(`${baseUrl}/p/hello/-/tree/${live}`, { waitUntil: 'networkidle2' })

const selected = await page.evaluate(
  () => document.querySelector('select[aria-label="Ref"]')?.value ?? null,
)
check('the page is looking at that branch', selected === live, String(selected))

// Deleted from elsewhere, the way a push or another person would do it.
await call('DELETE', `/api/v1/projects/${id}/repository/branches/${live}`)

await page.evaluate(() => window.dispatchEvent(new Event('focus')))
await new Promise((r) => setTimeout(r, 1500))
const still = await text()
check(
  'a branch deleted under the page does not leave it broken',
  !still.includes('does not exist') && !still.includes('Unexpected'),
  still.slice(0, 160).replace(/\n/g, ' '),
)

// --- a branch whose name contains a slash has to be reachable.
//
// A slash cannot live in the path: the router decodes %2F into a real separator,
// so the link has to carry the ref in the query instead. Getting this wrong sends
// the browser to the tree of "feature" filtered by the directory "x".
await page.goto(`${baseUrl}/p/hello/-/branches`, { waitUntil: 'networkidle2' })

const slashed = await page.evaluate(() => {
  const row = [...document.querySelectorAll('li')].find((li) =>
    li.innerText.includes('feature/export-csv'),
  )
  return row?.querySelector('a')?.getAttribute('href') ?? null
})
check('a slashed branch is linked', !!slashed && slashed.includes('ref='), slashed ?? '')

if (slashed) {
  await page.goto(`${baseUrl}${slashed}`, { waitUntil: 'networkidle2' })
  await new Promise((r) => setTimeout(r, 800))
  const treeText = await text()
  check(
    'following the link shows that branch',
    treeText.includes('README.md') && !treeText.includes('unexpected error'),
    treeText.slice(0, 160).replace(/\n/g, ' '),
  )

  const selected = await page.evaluate(
    () => document.querySelector('select[aria-label="Ref"]')?.value ?? null,
  )
  check('the selector agrees about the branch', selected === 'feature/export-csv', String(selected))
}

// --- tags have their own page
await page.goto(`${baseUrl}/p/hello/-/tags`, { waitUntil: 'networkidle2' })
const tagsText = await text()
check('the tags page exists', tagsText.includes('Tags ('), tagsText.slice(0, 140).replace(/\n/g, ' '))
check(
  'the tags page does not list branches',
  !tagsText.includes('Branches ('),
  tagsText.slice(0, 140).replace(/\n/g, ' '),
)

await page.goto(`${baseUrl}/p/hello/-/branches`, { waitUntil: 'networkidle2' })
check(
  'the branches page does not list tags',
  !(await text()).includes('Tags ('),
  (await text()).slice(0, 140).replace(/\n/g, ' '),
)

// --- a tag can be created and deleted through its own page
const tag = `v0.0.${Date.now().toString().slice(-4)}`
const tagMade = await call('POST', `/api/v1/projects/${id}/repository/tags`, {
  name: tag,
  message: 'made by the branch flow',
})
check('a tag can be created', tagMade.status === 201, `status ${tagMade.status}`)

await page.goto(`${baseUrl}/p/hello/-/tags`, { waitUntil: 'networkidle2' })
check('the tag is listed', (await text()).includes(tag), tag)

const tagClicked = await page.evaluate((name) => {
  const row = [...document.querySelectorAll('li')].find((li) => li.innerText.includes(name))
  const button = row?.querySelector('button')
  if (!button) return false
  button.click()
  return true
}, tag)
check('the tag delete button was pressed', tagClicked)

await page
  .waitForFunction((name) => !document.body.innerText.includes(name), { timeout: 10000 }, tag)
  .catch(() => {})
check('the tag disappears from the list', !(await text()).includes(tag))

const tagsLeft = await call('GET', `/api/v1/projects/${id}/repository/refs`)
check(
  'the tag is gone on the server too',
  !JSON.parse(tagsLeft.text).tags.some((t) => t.name === tag),
)

await page.screenshot({ path: '/tmp/opencode/tags.png', fullPage: true })

console.log(steps.join('\n'))
console.log(failures.length ? `\n${failures.length} failing: ${failures.join(', ')}` : '\nall good')

await browser.close()
process.exit(failures.length ? 1 : 0)