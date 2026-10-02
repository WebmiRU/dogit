/**
 * Walks the module screens: the list, one module's overview with what it reported,
 * its settings, and the removal dialog built from the manifest.
 */
import { BASE, browser, check, login, shot, step, textOf, verdict } from './helpers.mjs'

const failures = []
const { instance, page } = await browser()

try {
  step('sign in as an administrator')
  await login(page)

  step('open the module list')
  await page.goto(`${BASE}/admin/modules`, { waitUntil: 'networkidle0' })

  const rows = await page.$$eval('table tbody tr', (nodes) => nodes.length)
  check(rows > 0, `the list shows ${rows} module(s) in a table`, failures)

  const headers = await page.$$eval('table thead th', (nodes) => nodes.map((n) => n.textContent.trim()))
  check(
    ['Module', 'Status', 'Address', 'Storage'].every((wanted) => headers.includes(wanted)),
    `the table has the columns that matter: ${headers.join(', ')}`,
    failures,
  )

  // Storage is shown as reported: a module that has said nothing says so, rather
  // than showing a zero that would read like good news.
  const storage = (await page.$$eval('table tbody tr td', (nodes) =>
    nodes.map((node) => node.textContent.trim()),
  ))[3]
  check(
    /not reported|\d+% used/.test(storage ?? ''),
    `the storage column says "${storage}"`,
    failures,
  )

  const moduleName = await page.$eval('table tbody tr a', (node) => node.textContent.trim())
  check(!!moduleName, `the first module is ${moduleName}`, failures)
  await shot(page, 'modules-list')

  step(`open ${moduleName}`)
  // Client-side navigation: there is no page load to wait for, so the wait is for
  // the thing that proves the new page is there.
  await page.click('table tbody tr a')
  await page.waitForSelector('nav.tabs .tab', { timeout: 15000 })
  await new Promise((resolve) => setTimeout(resolve, 800))

  const title = await page.$eval('h1.page-title', (node) => node.textContent.trim())
  check(title === moduleName, `the page is titled "${title}"`, failures)

  const tabs = await textOf(page, 'nav.tabs .tab')
  check(
    /Overview/.test(tabs) && /Settings/.test(tabs) && /Removal/.test(tabs),
    `the tabs are ${tabs.replace(/\n/g, ' / ')}`,
    failures,
  )

  // The address the module is reachable at is on the page, so an operator does
  // not have to go looking for it.
  const facts = await textOf(page, 'dl.facts')
  check(/Address/.test(facts), 'the overview shows the address', failures)
  check(/Scopes/.test(facts), 'the overview shows the scopes', failures)
  check(/Endpoint/.test(facts), 'the overview shows where the module lives', failures)

  const reported = await page.$eval('section', (node) => node.textContent)
  check(
    /has not reported anything yet|Storage/.test(reported),
    'the overview shows what the module reported, or says it reported nothing',
    failures,
  )
  await shot(page, 'module-overview')

  step('settings tab')
  await page.$$eval('nav.tabs .tab', (nodes) => {
    nodes.find((node) => node.textContent.includes('Settings'))?.click()
  })
  await new Promise((resolve) => setTimeout(resolve, 600))
  const settingsText = await textOf(page, 'section')
  check(settingsText.length > 0, 'the settings tab renders the module’s own settings', failures)
  await shot(page, 'module-settings')

  step('removal tab')
  await page.$$eval('nav.tabs .tab', (nodes) => {
    nodes.find((node) => node.textContent.includes('Removal'))?.click()
  })
  await new Promise((resolve) => setTimeout(resolve, 600))

  const removal = await textOf(page, '.removal')
  check(
    /declared nothing to ask about|involves the following/.test(removal),
    'the removal dialog is built from what the module declared',
    failures,
  )
  check(
    /cannot be stopped once started/.test(removal),
    'the dialog says removal cannot be stopped',
    failures,
  )
  check(
    (await page.$$eval('button', (nodes) =>
      nodes.some((node) => node.textContent.trim() === 'Remove module'),
    )),
    'there is a button to remove the module',
    failures,
  )
  check(
    !(await page.$$eval('button', (nodes) =>
      nodes.some((node) => /cancel/i.test(node.textContent)),
    )),
    'and no button to cancel it',
    failures,
  )

  await shot(page, 'module-removal')
} finally {
  await instance.close()
}

verdict(failures)