/**
 * Reproduces the editor branch-switch failure in a real browser.
 *
 * It records every request the page makes, so the offending URL shows up with the
 * action that produced it rather than only in the server log.
 *
 *   node scripts/probe-editor.mjs [baseUrl]
 */
import puppeteer from 'puppeteer-core'

const baseUrl = process.argv[2] ?? 'http://localhost:3000'
const chromePath = process.env.CHROME_PATH ?? '/usr/bin/google-chrome'

const browser = await puppeteer.launch({
  executablePath: chromePath,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
  headless: 'new',
})

const page = await browser.newPage()

const requests = []
page.on('request', (request) => {
  const url = request.url()
  if (url.includes('/api/')) requests.push(`${request.method()} ${url}`)
})
page.on('console', (message) => {
  if (message.type() === 'error') console.log('console error:', message.text())
})

await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
await page.type('input[name="username"], #username, #login', 'alice')
await page.type('input[type="password"]', 'secret123')
await page.click('button[type="submit"]')
await new Promise((r) => setTimeout(r, 1500))

await page.goto(`${baseUrl}/p/hello/-/blob/main/README.md`, { waitUntil: 'networkidle2' })
console.log('--- opening the editor')
await page.goto(`${baseUrl}/p/hello/-/edit/main/README.md`, { waitUntil: 'networkidle2' })

console.log('--- requests on the editor page')
for (const entry of requests) console.log('   ', entry)

console.log('--- switching the branch in the selector')
requests.length = 0
const switched = await page.evaluate(() => {
  const select = document.querySelector('select[aria-label="Ref"]')
  if (!select) return 'no selector'
  select.value = 'dev'
  select.dispatchEvent(new Event('change', { bubbles: true }))
  return select.value
})
console.log('    selected:', switched)

await new Promise((r) => setTimeout(r, 2500))
console.log('    url now:', page.url())
console.log('--- requests after switching')
for (const entry of requests) console.log('   ', entry)

const body = await page.evaluate(() => document.body.innerText.slice(0, 400))
console.log('--- page text')
console.log(body)

await browser.close()