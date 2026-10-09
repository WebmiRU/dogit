/**
 * Signs in and walks pages, so a flow can be checked rather than described.
 *
 * Puppeteer, not Playwright: it is what the other scripts in this directory use,
 * and a second browser driver in one project is one more thing to keep working.
 */
import puppeteer from 'puppeteer-core'
import { mkdirSync } from 'node:fs'

export const BASE = process.env.DOGIT_URL ?? 'http://localhost:3000'
export const SHOTS = '/tmp/opencode/shots'

/** The size the interface is judged at. */
export const VIEWPORT = { width: 1280, height: 965 }

export async function browser() {
  mkdirSync(SHOTS, { recursive: true })

  const instance = await puppeteer.launch({
    executablePath: process.env.CHROME_PATH ?? '/usr/bin/google-chrome',
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
    headless: 'new',
  })

  const page = await instance.newPage()
  await page.setViewport(VIEWPORT)

  page.on('console', (message) => {
    if (message.type() === 'error') console.log(`  browser: ${message.text()}`)
  })
  page.on('pageerror', (error) => console.log(`  pageerror: ${error.message}`))

  return { instance, page }
}

export async function login(page, username = 'alice', password = 'secret123') {
  await page.goto(`${BASE}/login`, { waitUntil: 'networkidle0' })
  await page.waitForSelector('form')
  await page.type('#login', username)
  await page.type('#password', password)
  await Promise.all([
    page.waitForNavigation({ waitUntil: 'networkidle0' }),
    page.click('button[type="submit"]'),
  ])
}

/** Text of everything matching a selector, joined. */
export async function textOf(page, selector) {
  return page.$$eval(selector, (nodes) => nodes.map((node) => node.textContent?.trim() ?? '').join('\n'))
}

export async function shot(page, name) {
  const path = `${SHOTS}/${name}.png`
  await page.screenshot({ path, fullPage: true })
  console.log(`  shot: ${path}`)
  return path
}

export function step(text) {
  console.log(`\n· ${text}`)
}

export function ok(text) {
  console.log(`  ✓ ${text}`)
}

export function fail(text) {
  console.log(`  ✗ ${text}`)
}

/** Runs checks and reports a single verdict at the end. */
export function verdict(failures) {
  console.log('')
  if (failures.length) {
    console.log(`${failures.length} check(s) failed`)
    process.exitCode = 1
    return
  }
  console.log('the flow behaves')
}

export function check(condition, text, failures) {
  if (condition) ok(text)
  else {
    fail(text)
    failures.push(text)
  }
}