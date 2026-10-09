/**
 * The group story, driven through the browser: create a group, create a project
 * inside it from the form, open the group page, open the project, and edit a file
 * through the nested URL.
 *
 *   node scripts/group-flow.mjs [baseUrl]
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
      (b) => b.textContent.trim() === wanted,
    )
    if (!button) return false
    button.click()
    return true
  }, label)

await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
await page.type('input[name="username"], #username, #login', 'alice')
await page.type('input[type="password"]', 'secret123')
await page.click('button[type="submit"]')
await page.waitForFunction(() => !location.pathname.includes('login'), { timeout: 10000 })

const suffix = Date.now().toString().slice(-6)

// --- a group, created from the form
await page.goto(`${baseUrl}/groups`, { waitUntil: 'networkidle2' })
await clickButton('New group')
await setValue('#slug', `team-${suffix}`)
await setValue('#gname', 'Browser team')
await clickButton('Create group')
await new Promise((r) => setTimeout(r, 1500))
check('the group was created', (await text()).includes(`team-${suffix}`), `team-${suffix}`)

// --- a project inside it, chosen from the namespace dropdown
await page.goto(`${baseUrl}/projects`, { waitUntil: 'networkidle2' })
await clickButton('New project')
await setValue('#group', `team-${suffix}`)
await setValue('#path', `service-${suffix}`)
await setValue('#name', 'Service')
await setValue('#description', 'Created from the browser inside a group')
await clickButton('Create project')
await page.waitForFunction(() => location.pathname.startsWith('/p/'), { timeout: 10000 }).catch(() => {})

const projectPath = page.url().replace(`${baseUrl}`, '')
// The tree is fetched after the project itself, so the checks below wait for it
// rather than reading a page that has only finished half loading.
await page
  .waitForFunction(() => document.body.innerText.includes('README.md'), { timeout: 10000 })
  .catch(() => {})

check('the project opened', projectPath.startsWith('/p/'), projectPath)
check('it lives in the group namespace', projectPath.includes('/team-'), projectPath)
check(
  'the description is shown on the project page',
  (await text()).includes('Created from the browser inside a group'),
)
check(
  'the seeded repository has its first commit',
  (await text()).includes('README.md'),
  (await text()).slice(0, 120).replace(/\n/g, ' '),
)

// --- the group page lists the project
await page.goto(`${baseUrl}/groups`, { waitUntil: 'networkidle2' })
const groupLink = await page.evaluate((slug) => {
  const article = [...document.querySelectorAll('article')].find((a) =>
    a.innerText.includes(slug),
  )
  const link = article?.querySelector('a')
  return link ? link.getAttribute('href') : null
}, `team-${suffix}`)
check('the group links to its page', !!groupLink, groupLink ?? '')

await page.goto(`${baseUrl}${groupLink}`, { waitUntil: 'networkidle2' })
check(
  'the group page lists the project',
  (await text()).includes(`team-${suffix}/service-${suffix}`),
  (await text()).slice(0, 200).replace(/\n/g, ' '),
)
await page.screenshot({ path: '/tmp/opencode/group.png', fullPage: true })

// --- editing a file inside a group project, which is the nested URL
await page.goto(`${baseUrl}/p/team-${suffix}/service-${suffix}/-/edit/main/README.md`, {
  waitUntil: 'networkidle2',
})
check(
  'the editor opens a file in a grouped project',
  (await text()).includes('Commit changes'),
  (await text()).slice(0, 160).replace(/\n/g, ' '),
)

const edited = await page.evaluate(() => {
  const area = document.querySelector('textarea.editor-input')
  if (!area) return false
  area.value += '\nedited inside a group\n'
  area.dispatchEvent(new Event('input', { bubbles: true }))
  return true
})
if (!edited) {
  console.log('--- the editor did not render; page text:')
  console.log(await text())
  console.log('--- url:', page.url())
}
await setValue('#commit-message', 'Edit from a grouped project')
await clickButton('Commit changes')
await page
  .waitForFunction(() => document.body.innerText.includes('files changed'), { timeout: 10000 })
  .catch(() => {})
check(
  'the edit in a grouped project commits',
  (await text()).includes('Edit from a grouped project'),
  (await text()).slice(0, 200).replace(/\n/g, ' '),
)

// --- the description is editable now
await page.goto(`${baseUrl}/p/team-${suffix}/service-${suffix}/-/settings`, {
  waitUntil: 'networkidle2',
})
await setValue('#project-description', 'A description edited in the browser')
await clickButton('Save changes')
await new Promise((r) => setTimeout(r, 1200))
check('the description saves', (await text()).includes('Saved.'), (await text()).slice(0, 200))

await page.goto(`${baseUrl}/p/team-${suffix}/service-${suffix}`, { waitUntil: 'networkidle2' })
check(
  'the new description is shown on the project page',
  (await text()).includes('A description edited in the browser'),
)

console.log(steps.join('\n'))
console.log(failures.length ? `\n${failures.length} failing: ${failures.join(', ')}` : '\nall good')

await browser.close()
process.exit(failures.length ? 1 : 0)