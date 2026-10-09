// Prints what the deploy page has actually drawn, rather than what it looks like.
//
// A screenshot says how many cards there are and not which operation each is, and every theory
// about why a page drew four cards for one deployment was wrong until the ids were in front of
// me. So this prints them.
import puppeteer from '/home/ewolf/prjs/dogit/web/node_modules/puppeteer-core/lib/puppeteer/puppeteer-core.js'

const base = process.env.DOGIT_BASE ?? 'http://localhost:3000'
const path = process.argv[2]
const wait = Number(process.env.LOOK_WAIT ?? 30000)
const place = process.argv[3] ?? ''

const browser = await puppeteer.launch({ executablePath: '/usr/bin/google-chrome', args: ['--no-sandbox'] })
const page = await browser.newPage()
if (process.env.DOGIT_SESSION) {
  const url = new URL(base)
  await browser.setCookie({
    name: 'dogit_session',
    value: process.env.DOGIT_SESSION,
    domain: url.hostname,
    path: '/',
  })
}

await page.goto(base + path, { waitUntil: 'domcontentloaded', timeout: 30000 })

if (place) {
  await page.waitForFunction(
    (wanted) =>
      [...document.querySelectorAll('summary')].some((el) =>
        (el.textContent ?? '').includes(wanted),
      ),
    { timeout: 20000 },
    place,
  )
  await page.evaluate((wanted) => {
    const hit = [...document.querySelectorAll('summary')].find((el) =>
      (el.textContent ?? '').includes(wanted))
    hit?.click()
  }, place)
}

// Watched over time, rather than looked at once: what goes wrong here is a page that accumulates
// cards while a deployment runs, and a single look at the end either misses it or catches it after
// whatever cleaned up. Only what changed is printed, so the shape of the thing is readable.
const read = () =>
  page.evaluate(() => {
    return [...document.querySelectorAll('section.operation')].map((el) => {
      const head = el.querySelector('.block-head')
      // Every step's own state, in its own words.
      //
      // A screenshot says how many steps are green and leaves counting them to whoever is
      // looking, and the question that matters here is exactly a count: is the step a finished
      // deployment finished on drawn as a step that never happened.
      const steps = [...el.querySelectorAll('.step')].map((s) => {
        const state = [...s.classList].find((c) => c !== 'step') ?? '?'
        const label = s.querySelector('.label')?.textContent?.trim() ?? '?'
        return `${state}:${label}`
      })
      return {
        badge: head?.querySelector('.badge')?.textContent?.trim() ?? '',
        chips: [...(head?.querySelectorAll('.mono') ?? [])]
          .map((c) => c.textContent.trim())
          .filter(Boolean)
          .join(' '),
        cross: Boolean(head?.querySelector('.put-away')),
        steps: steps.length,
        where: steps.join(' | '),
      }
    })
  })

let previous = ''
const until = Date.now() + wait
while (Date.now() < until) {
  const cards = await read()
  const shape = cards.map((one) => `${one.badge}|${one.chips}|${one.cross}|${one.steps}`).join('~')
  if (shape !== previous) {
    console.log(`\n--- ${new Date().toISOString().slice(11, 19)}  cards: ${cards.length}`)
    cards.forEach((one, index) => {
      console.log(`  [${index}] ${one.badge}  ${one.chips}  cross=${one.cross}  steps=${one.steps}`)
      if (one.where) console.log(`      ${one.where}`)
    })
    previous = shape
  }
  await new Promise((r) => setTimeout(r, 1500))
}

await browser.close()
