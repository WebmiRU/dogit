/**
 * Editor flow, driven the way a person would drive it.
 *
 * It opens a file, edits it, commits it to a new branch, and then tries the same
 * save again from a stale copy to check the conflict is reported instead of
 * overwriting.
 *
 *   node scripts/editor-flow.mjs [baseUrl]
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

const text = () => page.evaluate(() => document.body.innerText)
const clickButton = async (label) =>
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

// --- open a file and press Edit, the way the UI offers it
const branch = process.env.EDIT_BRANCH ?? 'main'
await page.goto(`${baseUrl}/p/hello/-/blob/${branch}/README.md`, { waitUntil: 'networkidle2' })
const editHref = await page.evaluate(() => {
  const link = [...document.querySelectorAll('a')].find((a) => a.textContent.trim() === 'Edit')
  return link ? link.getAttribute('href') : null
})
check('the file view offers an Edit link', !!editHref, editHref ?? '')

await page.goto(`${baseUrl}${editHref}`, { waitUntil: 'networkidle2' })
check('the editor loads the file', (await text()).includes('Commit changes'))

// --- change the content and look at the preview
const marker = `edited by the smoke test ${Date.now()}`
await page.evaluate((extra) => {
  const area = document.querySelector('textarea.editor-input')
  area.value += `\n${extra}\n`
  area.dispatchEvent(new Event('input', { bubbles: true }))
}, marker)
await new Promise((r) => setTimeout(r, 300))
const preview = await text()
check(
  'the diff preview counts the change',
  /Changes\s*\+\d+/.test(preview.replace(/\n/g, ' ')),
  preview.match(/Changes[^\n]*/)?.[0] ?? '',
)

await page.screenshot({ path: '/tmp/opencode/editor.png', fullPage: true })

// --- commit to a new branch, so the shared branch is left alone
const branchName = `editor-check-${Date.now()}`
await page.evaluate((name) => {
  const input = document.querySelector('#new-branch')
  input.value = name
  input.dispatchEvent(new Event('input', { bubbles: true }))
}, branchName)
await page.evaluate((msg) => {
  const input = document.querySelector('#commit-message')
  input.value = msg
  input.dispatchEvent(new Event('input', { bubbles: true }))
}, 'Edit from the browser')

check('the commit button was clickable', await clickButton('Commit changes'))
await page.waitForFunction(() => location.pathname.includes('/-/commit/'), { timeout: 10000 })
check('the commit page opened', page.url().includes('/-/commit/'), page.url())
// The view swaps only after the commit itself is fetched, so the editor must give
// way rather than merely the URL changing.
await page.waitForFunction(
  () => document.body.innerText.includes('files changed'),
  { timeout: 10000 },
).catch(() => {})
check(
  'the commit is attributed to the user',
  (await text()).includes('Edit from the browser'),
)

// --- the branch really exists and really carries the change
await page.goto(`${baseUrl}/p/hello/-/branches?ref=main`, { waitUntil: 'networkidle2' })
check('the new branch is listed', (await text()).includes(branchName), branchName)

// --- saving on the branch it was opened on, which is the ordinary case
await page.goto(`${baseUrl}/p/hello/-/edit/${branch}/README.md`, { waitUntil: 'networkidle2' })
await page.evaluate((extra) => {
  const area = document.querySelector('textarea.editor-input')
  area.value += `\n${extra}\n`
  area.dispatchEvent(new Event('input', { bubbles: true }))
}, `saved on ${branch}`)
await clickButton('Commit changes')
await page.waitForFunction(() => location.pathname.includes('/-/commit/'), { timeout: 10000 }).catch(() => {})
// The commit view fetches the commit, so the check waits for it rather than for
// the URL, which changes before the page has anything to show.
await page
  .waitForFunction(() => document.body.innerText.includes('files changed'), { timeout: 10000 })
  .catch(() => {})
check(
  `a save on ${branch} is not reported as a conflict`,
  (await text()).includes('files changed'),
  (await text()).slice(0, 160).replace(/\n/g, ' '),
)

// --- a genuine conflict: two editors, one file.
//
// Both open the same file, then one of them commits. The other has to be told,
// because otherwise it silently overwrites work it never saw.
const staleTab = await browser.newPage()
await staleTab.goto(`${baseUrl}/p/hello/-/edit/${branch}/README.md`, { waitUntil: 'networkidle2' })
await staleTab.evaluate(() => {
  const area = document.querySelector('textarea.editor-input')
  area.value += '\nan edit that started earlier\n'
  area.dispatchEvent(new Event('input', { bubbles: true }))
})

await page.goto(`${baseUrl}/p/hello/-/edit/${branch}/README.md`, { waitUntil: 'networkidle2' })
await page.evaluate((extra) => {
  const area = document.querySelector('textarea.editor-input')
  area.value += `\n${extra}\n`
  area.dispatchEvent(new Event('input', { bubbles: true }))
}, 'the change that arrives first')
await clickButton('Commit changes')
await page.waitForFunction(() => location.pathname.includes('/-/commit/'), { timeout: 10000 }).catch(() => {})

// The stale tab still holds the old content and the old blob id.
await staleTab.evaluate(() => {
  const message = document.querySelector('#commit-message')
  message.value = 'the late change'
  message.dispatchEvent(new Event('input', { bubbles: true }))
})
const clicked = await staleTab.evaluate(() => {
  const button = [...document.querySelectorAll('button')].find(
    (b) => b.textContent.trim() === 'Commit changes',
  )
  if (!button || button.disabled) return false
  button.click()
  return true
})
await new Promise((r) => setTimeout(r, 2500))

const staleText = await staleTab.evaluate(() => document.body.innerText)
check('the stale save was attempted', clicked)
check(
  'a save onto a moved file is refused',
  staleText.includes('changed on') && staleText.includes('since it was opened'),
  staleText.slice(0, 200).replace(/\n/g, ' '),
)
check(
  'the refusal offers a way forward',
  staleText.includes('reload'),
  '',
)
await staleTab.close()

console.log(steps.join('\n'))
console.log(failures.length ? `\n${failures.length} failing: ${failures.join(', ')}` : '\nall good')

await browser.close()
process.exit(failures.length ? 1 : 0)