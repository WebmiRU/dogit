/**
 * Removes a module the way an administrator does: options from the manifest, a
 * second confirmation for what cannot be undone, and then a log that arrives
 * while the page is open.
 *
 * The point is that the administrator may leave mid-removal. This checks it by
 * reloading the page and finding the removal still there with its history.
 */
import { BASE, browser, check, login, shot, step, textOf, verdict } from './helpers.mjs'

const failures = []
const { instance, page } = await browser()

/** Forgets the module and waits for it to come back on its own. */
async function reset(page) {
  await page.goto(`${BASE}/admin/modules`, { waitUntil: 'networkidle0' })

  const id = await page.$eval('table tbody tr a', (node) => {
    const href = node.getAttribute('href') ?? ''
    return href.split('/').pop()
  }).catch(() => null)

  if (id) {
    await page.evaluate(async (moduleId) => {
      await fetch(`/api/v1/modules/${moduleId}`, { method: 'DELETE', credentials: 'include' })
    }, id)
    console.log('  forgot the module')
  }

  const deadline = Date.now() + 60000
  while (Date.now() < deadline) {
    await page.reload({ waitUntil: 'networkidle0' })
    const present = await page.$$eval('table tbody tr a', (nodes) => nodes.length)
    if (present > 0) {
      console.log('  the module registered itself again')
      return
    }
    // Its heartbeat interval is thirty seconds, so this wait is not a retry
    // storm: the module is asked to come back, not polled into existence.
    await new Promise((resolve) => setTimeout(resolve, 2000))
  }
  throw new Error('the module never came back')
}

/** Ticks a checkbox the way a person does, so Vue notices. */
async function tick(page, selector) {
  await page.click(selector)
}

/**
 * Waits for a line the *module* wrote.
 *
 * The first line of every removal is the core's own — "asking X to remove itself"
 * — so waiting for "anything at all" would pass before the module had said
 * anything, which is the whole thing under test.
 */
async function waitForLog(wanted, timeout = 40000) {
  const started = Date.now()
  const deadline = started + timeout
  while (Date.now() < deadline) {
    const log = await page.$eval('.log', (node) => node.textContent).catch(() => '')
    if (new RegExp(wanted).test(log)) {
      console.log(`  it arrived after ${((Date.now() - started) / 1000).toFixed(1)}s`)
      return log
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  const seen = await page.$eval('.log', (node) => node.textContent).catch(() => '(no log)')
  console.log(`  timed out after ${timeout}ms; the log read: ${JSON.stringify(seen)}`)
  return ''
}

try {
  step('sign in as an administrator')
  await login(page)

  step('start from a clean module')
  // A previous run leaves a finished removal behind, and its log would be
  // mistaken for this one's. Forgetting the module is also the honest way to
  // begin: the demo re-registers itself on its next heartbeat, which is the same
  // thing that happens after a real reinstall.
  await reset(page)

  step('open the removal tab of the demo module')
  await page.goto(`${BASE}/admin/modules`, { waitUntil: 'networkidle0' })
  await page.click('table tbody tr a')
  await page.waitForSelector('nav.tabs .tab')
  await page.$$eval('nav.tabs .tab', (nodes) => {
    nodes.find((node) => node.textContent.includes('Removal'))?.click()
  })
  await new Promise((resolve) => setTimeout(resolve, 800))

  // The wording is the module's own, because the core does not know what a cache
  // entry is.
  const dialog = await textOf(page, '.removal')
  check(/Delete the cache/.test(dialog), 'the option comes from the module’s own manifest', failures)
  check(/cannot be undone/.test(dialog), 'and is marked as something that cannot be undone', failures)

  const removeButton = await page.$$eval('button', (nodes) =>
    nodes.findIndex((node) => node.textContent.trim() === 'Remove module'),
  )
  check(removeButton >= 0, 'the remove button is there', failures)

  // Dangerous options are not ticked by default: deleting is a decision, not a
  // setting that came along for the ride.
  const checkedBefore = await page.$eval('.option input[type="checkbox"]', (node) => node.checked)
  check(!checkedBefore, 'a dangerous option starts unticked', failures)

  // Ticking a dangerous option is allowed; it is the *pressing of the button*
  // that then needs a second word. Refusing to let it be chosen would be paternal
  // nonsense — an administrator may well want the data gone.
  await tick(page, '.option input[type="checkbox"]')
  await new Promise((resolve) => setTimeout(resolve, 300))

  const confirmVisible = await page.$$eval('.confirm .option', (nodes) => nodes.length)
  check(confirmVisible > 0, 'choosing it asks for a second confirmation', failures)

  const stillDisabled = await page.$$eval('button', (nodes) => {
    const button = nodes.find((node) => node.textContent.trim() === 'Remove module')
    return button?.disabled ?? false
  })
  check(stillDisabled, 'and the button stays unusable until it is given', failures)

  await shot(page, 'removal-before')

  step('confirm, then remove')
  await tick(page, '.confirm .option input[type="checkbox"]')
  await new Promise((resolve) => setTimeout(resolve, 300))

  const disabled = await page.$$eval('button', (nodes) => {
    const button = nodes.find((node) => node.textContent.trim() === 'Remove module')
    return button?.disabled ?? true
  })
  check(!disabled, 'with the confirmation given, the button becomes usable', failures)

  await page.$$eval('button', (nodes) => {
    nodes.find((node) => node.textContent.trim() === 'Remove module')?.click()
  })

  step('watch the log arrive')
  const first = await waitForLog('removing cache entry 1')
  check(/removing cache entry 1/.test(first), "the module's first line arrives", failures)

  const second = await waitForLog('removing cache entry 2')
  check(/removing cache entry 2/.test(second), 'and the next one arrives while the page is open', failures)
  await shot(page, 'removal-running')

  step('leave the page and come back')
  await page.goto(`${BASE}/admin/modules`, { waitUntil: 'networkidle0' })
  await page.click('table tbody tr a')
  await page.waitForSelector('nav.tabs .tab')
  await page.$$eval('nav.tabs .tab', (nodes) => {
    nodes.find((node) => node.textContent.includes('Removal'))?.click()
  })
  await new Promise((resolve) => setTimeout(resolve, 1500))

  // The log is in the database, not in the tab. An administrator who closed their
  // laptop and came back an hour later has to find it there.
  const after = await page.$eval('.log', (node) => node.textContent).catch(() => '')
  check(/removing cache entry 1/.test(after), 'the log survived leaving the page', failures)

  step('wait for the summary')
  const deadline = Date.now() + 40000
  let status = ''
  while (Date.now() < deadline) {
    status = await textOf(page, '.card-header .badge')
    if (/finished/i.test(status)) break
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  check(/finished/i.test(status), `the removal finished (status: ${status})`, failures)

  const finalLog = await page.$eval('.log', (node) => node.textContent).catch(() => '')
  check(/done/.test(finalLog), 'the module’s summary is in the log', failures)

  // A warning the module raised stays a warning rather than being flattened into
  // the same text as ordinary progress.
  check(/not ours to delete/.test(finalLog), 'the module’s own remarks are kept', failures)

  await shot(page, 'removal-done')

  step('the removal is over and cannot be repeated while it runs')
  const dialogAfter = await textOf(page, '.removal').catch(() => '')
  check(!/Remove module/.test(dialogAfter), 'the dialog is replaced by the log of what happened', failures)
} finally {
  await instance.close()
}

verdict(failures)