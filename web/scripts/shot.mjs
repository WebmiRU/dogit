/**
 * Opens one page, waits for it to settle, and writes a screenshot.
 *
 *   node scripts/shot.mjs <path> <output.png> [username]
 */
import puppeteer from 'puppeteer-core'

const path = process.argv[2] ?? '/'
const output = process.argv[3] ?? '/tmp/opencode/shot.png'
const baseUrl = process.env.DOGIT_URL ?? 'http://localhost:3000'
const chromePath = process.env.CHROME_PATH ?? '/usr/bin/google-chrome'

const browser = await puppeteer.launch({
  executablePath: chromePath,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
  headless: 'new',
})
const page = await browser.newPage()
await page.setViewport({ width: 1280, height: 965 })

const errors = []
page.on('console', (m) => {
  if (m.type() === 'error') errors.push(m.text())
})
page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))

await page.goto(`${baseUrl}/login`, { waitUntil: 'networkidle2' })
await page.type('input[name="username"], #username, #login', 'alice')
await page.type('input[type="password"]', 'secret123')
await page.click('button[type="submit"]')
await page.waitForFunction(() => !location.pathname.includes('login'), { timeout: 10000 })

await page.goto(`${baseUrl}${path}`, { waitUntil: 'networkidle2' })
await new Promise((r) => setTimeout(r, 800))

console.log('url:', page.url())
console.log('--- text')
console.log(await page.evaluate(() => document.body.innerText))
if (errors.length) console.log('--- errors\n' + errors.join('\n'))

await page.screenshot({ path: output, fullPage: true })
console.log('--- screenshot:', output)

await browser.close()