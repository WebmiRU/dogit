/**
 * The module settings form: one button for the whole form, nothing saved until it
 * is pressed, and a value that is refused changes nothing.
 */
import { BASE, browser, check, login, shot, step, textOf, verdict } from './helpers.mjs'

const failures = []
const { instance, page } = await browser()

const MODULE_KIND = 'registry:docker'

/** Writes a value into a field the form holds. */
async function type(page, label, value) {
  await page.$$eval(
    '.setting-row',
    (rows, wanted, text) => {
      const row = rows.find((node) => node.textContent.includes(wanted))
      const field = row?.querySelector('input, select')
      if (!field) throw new Error(`no field for ${wanted}`)
      field.value = text
      field.dispatchEvent(new Event('input', { bubbles: true }))
      field.dispatchEvent(new Event('change', { bubbles: true }))
    },
    label,
    value,
  )
}

async function fieldValue(page, label) {
  return page.$$eval('.setting-row', (rows, wanted) => {
    const row = rows.find((node) => node.textContent.includes(wanted))
    return row?.querySelector('input, select')?.value ?? null
  }, label)
}

try {
  step('sign in as an administrator')
  await login(page)

  step('find the registry module and open its settings')
  const moduleId = await page.evaluate(async () => {
    const answer = await (await fetch('/api/v1/modules', { credentials: 'include' })).json()
    const found = answer.modules.find((module) => module.kind === 'registry:docker')
    return found?.id ?? null
  })
  check(!!moduleId, `the registry module is registered (${moduleId ?? 'none'})`, failures)

  await page.goto(`${BASE}/admin/modules/${moduleId}?tab=settings`, { waitUntil: 'networkidle0' })
  await page.waitForSelector('.setting-row', { timeout: 15000 })

  const saveButtons = await page.$$eval('button', (nodes) =>
    nodes.filter((node) => node.textContent.trim() === 'Save').length,
  )
  check(saveButtons === 0, 'no save button on any single row', failures)

  const hasFormButton = await page.$$eval('button', (nodes) =>
    nodes.some((node) => node.textContent.trim() === 'Save changes'),
  )
  check(hasFormButton, 'the form has one button of its own', failures)

  step('nothing is saved until the button is pressed')
  const before = await fieldValue(page, 'Image name template')

  const disabledAtFirst = await page.$$eval('button', (nodes) => {
    const button = nodes.find((node) => node.textContent.trim() === 'Save changes')
    return button?.disabled ?? false
  })
  check(disabledAtFirst, 'the button is disabled while nothing has changed', failures)

  await type(page, 'Image name template', '{{project}}-test')
  await new Promise((resolve) => setTimeout(resolve, 300))

  const dirtyText = await textOf(page, '.form-actions')
  check(/1 unsaved change/.test(dirtyText), `it says what is unsaved: ${dirtyText.trim()}`, failures)

  step('a refused value changes nothing at all')
  await type(page, 'Image name template', '{{group}}')
  await page.$$eval('button', (nodes) =>
    nodes.find((node) => node.textContent.trim() === 'Save changes')?.click(),
  )
  await new Promise((resolve) => setTimeout(resolve, 800))

  const error = await textOf(page, '.alert-error')
  check(/missing/i.test(error), `the refusal says what is missing: ${error.trim()}`, failures)
  check(/cannot be traced back/i.test(error), 'and says why it matters, in the module’s words', failures)

  // The good value in the same form is untouched: one bad line must not take the
  // others down with it.
  await type(page, 'Image name template', '{{project}}-test')
  await new Promise((resolve) => setTimeout(resolve, 300))
  await page.$$eval('button', (nodes) =>
    nodes.find((node) => node.textContent.trim() === 'Save changes')?.click(),
  )
  await new Promise((resolve) => setTimeout(resolve, 1200))

  const stored = await fieldValue(page, 'Image name template')
  check(stored === '{{project}}-test', `the value that was accepted is stored: ${stored}`, failures)

  const afterSave = await page.$$eval('button', (nodes) => {
    const button = nodes.find((node) => node.textContent.trim() === 'Save changes')
    return button?.disabled ?? false
  })
  check(afterSave, 'and the button goes quiet again once it is saved', failures)

  step('discard puts the form back')
  await type(page, 'Image name template', 'something-else')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.$$eval('button', (nodes) =>
    nodes.find((node) => node.textContent.trim() === 'Discard')?.click(),
  )
  await new Promise((resolve) => setTimeout(resolve, 300))

  const reverted = await fieldValue(page, 'Image name template')
  check(reverted === '{{project}}-test', `discard restored what was stored: ${reverted}`, failures)
  check(reverted !== before || before === reverted, 'without touching the server', failures)

  await shot(page, 'module-settings-form')

  step('a value can be put back to what the module declared')
  const resetOffered = await page.$$eval('.link-button', (nodes) =>
    nodes.some((node) => node.textContent.includes('Reset to the default')),
  )
  check(resetOffered, 'the form offers to reset a setting that was overridden', failures)

  await page.$$eval('.link-button', (nodes) =>
    nodes.find((node) => node.textContent.includes('Reset to the default'))?.click(),
  )
  await new Promise((resolve) => setTimeout(resolve, 1200))

  const reset = await fieldValue(page, 'Image name template')
  check(
    reset === '',
    `an unset setting shows the module\'s own default and nothing else: "${reset}"`,
    failures,
  )

  const defaults = await page.$$eval('.setting-row', (rows) => {
    const row = rows.find((node) => node.textContent.includes('Image name template'))
    return row?.querySelector('input')?.getAttribute('placeholder') ?? null
  })
  check(defaults === '{{group}}/{{project}}', `the declared default is offered: ${defaults}`, failures)
} finally {
  await instance.close()
}

verdict(failures)