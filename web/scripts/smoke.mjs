/**
 * End-to-end smoke test against a running dogit stack.
 *
 * It drives a real browser through the flows a user performs: sign in, open a
 * project, list files, read a file with highlighting, walk the commit history and
 * open a commit diff. Run it with:
 *
 *   node scripts/smoke.mjs [baseUrl] [username] [password]
 *
 * The base URL defaults to http://localhost:3000, which is where the compose
 * stack serves the SPA.
 */
import puppeteer from 'puppeteer-core'

const baseUrl = process.argv[2] ?? 'http://localhost:3000'
const username = process.argv[3] ?? 'alice'
const password = process.argv[4] ?? 'secret123'

const chromePath =
  process.env.CHROME_PATH ??
  ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find(Boolean)

const failures = []
const steps = []
const consoleErrors = []
const failedRequests = []

function check(name, condition, detail = '') {
  if (condition) {
    steps.push(`ok   ${name}`)
  } else {
    failures.push(`FAIL ${name}${detail ? `: ${detail}` : ''}`)
    steps.push(`FAIL ${name}${detail ? ` (${detail})` : ''}`)
  }
}

async function textOf(page, selector) {
  return page.$eval(selector, (element) => element.textContent?.trim() ?? '').catch(() => '')
}

async function waitForText(page, selector, timeout = 15000) {
  try {
    await page.waitForSelector(selector, { timeout })
    return true
  } catch {
    return false
  }
}

const browser = await puppeteer.launch({
  executablePath: chromePath,
  headless: true,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
})

try {
  const page = await browser.newPage()
  await page.setViewport({ width: 1400, height: 900 })

  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(String(error)))

  // Track failing requests with their URL: a bare "404" in the console says
  // nothing about which asset is missing.
  page.on('response', (response) => {
    if (response.status() >= 400) {
      failedRequests.push(`${response.status()} ${response.url()}`)
    }
  })

  // --- sign in ---------------------------------------------------------
  await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
  check('login page renders', await waitForText(page, 'form'))

  await page.type('#login', username)
  await page.type('#password', password)
  await Promise.all([
    page.click('button[type=submit]'),
    page.waitForNavigation({ waitUntil: 'networkidle2' }).catch(() => {}),
  ])
  await new Promise((resolve) => setTimeout(resolve, 800))

  const signedIn = (await page.$('.topbar .user')) !== null
  check('signed in and redirected', signedIn, page.url())

  // --- project list ----------------------------------------------------
  await page.goto(`${baseUrl}/`, { waitUntil: 'networkidle2' })
  const cards = await page.$$('.project-card')
  check('project list shows at least one project', cards.length > 0, `${cards.length} cards`)

  if (cards.length === 0) throw new Error('no projects to open')

  // The project URL is taken from the card itself, so nested group paths and the
  // current view do not have to be guessed.
  const projectUrl = await page.$eval('.project-card h3 a', (a) => new URL(a.href).pathname)

  // --- repository tree -------------------------------------------------
  await page.click('.project-card h3 a')
  await page.waitForSelector('.repo-tabs', { timeout: 15000 })
  await new Promise((resolve) => setTimeout(resolve, 700))

  const cloneUrl = await textOf(page, '.repo-clone span')
  check('clone URL is shown', /^git@.+:.+\.git$/.test(cloneUrl), cloneUrl)

  const rows = await page.$$('.tree-list li')
  check('file tree lists entries', rows.length > 0, `${rows.length} rows`)

  // --- file view -------------------------------------------------------
  // A source file is preferred over a README: plain prose highlights to nothing,
  // which would make the highlighting check meaningless. When the root has no
  // source file, step into the first directory and look again.
  const sourceSelectors = [
    '.tree-list li:not(.is-dir) a[href$=".go"]',
    '.tree-list li:not(.is-dir) a[href$=".js"]',
    '.tree-list li:not(.is-dir) a[href$=".ts"]',
    '.tree-list li:not(.is-dir) a[href$=".py"]',
  ]

  let firstFile = null
  for (let depth = 0; depth < 2; depth += 1) {
    for (const selector of sourceSelectors) {
      firstFile = await page.$(selector)
      if (firstFile) break
    }
    if (firstFile) break

    const directory = await page.$('.tree-list li.is-dir a.name')
    if (!directory) break
    await directory.click()
    await page.waitForSelector('.tree-list', { timeout: 15000 })
    await new Promise((resolve) => setTimeout(resolve, 500))
  }

  if (!firstFile) {
    firstFile = await page.$('.tree-list li:not(.is-dir) a.name')
  }

  if (firstFile) {
    await firstFile.click()
    await page.waitForSelector('.code-view, .empty', { timeout: 15000 })
    await new Promise((resolve) => setTimeout(resolve, 500))

    const lineCount = (await page.$$('.code-line')).length
    check('file view renders with line numbers', lineCount > 0, `${lineCount} lines`)

    const highlightSpans = (await page.$$('.code-line .lc span[class^="hljs-"]')).length
    check('syntax highlighting applied', highlightSpans > 0, `${highlightSpans} highlighted tokens`)
  } else {
    steps.push('skip file view (no text file at the repository root)')
  }

  // --- commits ---------------------------------------------------------
  await page.goto(`${baseUrl}${projectUrl}/-/commits`, { waitUntil: 'networkidle2' })
  await new Promise((resolve) => setTimeout(resolve, 700))

  const commitRows = await page.$$('.commit-list li')
  check('commit list renders', commitRows.length > 0, `${commitRows.length} commits`)

  if (commitRows.length > 0) {
    await page.click('.commit-list li .msg a')
    await page.waitForSelector('.file-change, .empty', { timeout: 15000 })
    await new Promise((resolve) => setTimeout(resolve, 700))

    const files = await page.$$('.file-change')
    check('commit diff renders files', files.length > 0, `${files.length} files`)

    const additions = await page.$$('.code-line.add')
    check('diff shows added lines', additions.length > 0)
  }
} catch (error) {
  failures.push(`FAIL unexpected error: ${error.message}`)
} finally {
  await browser.close()
}

console.log(steps.join('\n'))
if (consoleErrors.length) {
  console.log('\nbrowser console errors:')
  for (const error of consoleErrors.slice(0, 10)) console.log(`  ${error}`)
}
if (failedRequests.length) {
  console.log('\nfailed requests:')
  for (const entry of failedRequests.slice(0, 10)) console.log(`  ${entry}`)
}
console.log(failures.length === 0 ? '\nall checks passed' : `\n${failures.length} check(s) failed`)

process.exit(failures.length === 0 ? 0 : 1)
