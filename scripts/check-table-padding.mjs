// Do the two tables keep their own padding?
//
// Under <style scoped> a bare `th` reached only its own component, so three components
// could each name a `th` and each get its own answer. Global CSS has one answer per
// selector, so this reads the padding off both tables on one page and says so.

import { readFileSync } from 'node:fs'
import puppeteer from '/home/ewolf/prjs/dogit/web/node_modules/puppeteer-core/lib/puppeteer/puppeteer-core.js'

const base = process.env.DOGIT_BASE ?? 'http://localhost:3000'
const session = readFileSync(process.env.SESSION_FILE ?? '/tmp/dogit-session.txt', 'utf8').trim()
const url = process.argv[2]
const tag = process.argv[3] ?? 'td'

const browser = await puppeteer.launch({
  executablePath: '/usr/bin/google-chrome',
  headless: 'new',
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
})

try {
  const page = await browser.newPage()
  await page.setCookie({ name: 'dogit_session', value: session, domain: 'localhost', path: '/' })
  // domcontentloaded, not networkidle: the event socket holds a connection open for as
  // long as the page is open, so the network never goes idle and the wait never returns.
  await page.goto(base + url, { waitUntil: 'domcontentloaded', timeout: 60000 })
  await new Promise((r) => setTimeout(r, 2500))

  const found = await page.evaluate((tag) => {
    const out = []
    const tables = [...document.querySelectorAll('table')]
    for (const t of tables) {
      const cell = t.querySelector(tag)
      if (!cell || !cell.getClientRects().length) continue
      const cs = getComputedStyle(cell)
      const applied = cs.paddingTop + ' ' + cs.paddingRight

      // Every rule in the document whose selector matches this cell, in source order.
      // The last one that sets padding is the one the browser used; the ones before it
      // are what it lost to. Guessing from a class name cannot say that.
      const matched = []
      for (const sheet of document.styleSheets) {
        let rules
        try { rules = sheet.cssRules } catch { continue }
        const walk = (list) => {
          for (const rule of list) {
            // An ordinary style rule carries an empty cssRules of its own in current
            // CSSOM, and an empty list is truthy, so testing the property alone skips
            // every rule there is and reports a page with almost no styles in it.
            if (rule.cssRules && rule.cssRules.length) { walk(rule.cssRules); continue }
            if (!rule.selectorText) continue
            for (const sel of rule.selectorText.split(',')) {
              try { if (cell.matches(sel.trim())) { matched.push(sel.trim()); break } } catch { }
            }
          }
        }
        walk(rules)
      }

      let scope = '(none)'
      for (let el = cell; el && el !== document.body; el = el.parentElement) {
        const known = ['deploy-admin', 'deploy-page', 'places', 'packages-page', 'pipelines-page', 'ssh-keys-page']
        const hit = known.find((c) => el.classList?.contains(c))
        if (hit) { scope = '.' + hit; break }
      }

      out.push({ scope, applied, table: t.className || '(без класса)', matched })
    }
    return out
  }, tag)

  console.log(`страница: ${url}`)
  console.log(`ячейки ${tag}:\n`)
  for (const r of found) {
    console.log(`  таблица ${r.table}  →  отступ ${r.applied}   (область ${r.scope})`)
    console.log(`    правила с padding, по порядку: ${r.matched.join('  |  ') || '(нет)'}`)
  }
  if (!found.length) console.log('  (таблиц не найдено)')
} finally {
  await browser.close()
}