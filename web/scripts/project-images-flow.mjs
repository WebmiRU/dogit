/**
 * A project's images: push something, then read it back through the page.
 *
 * The page asks the core whether it may look and the module what is there, so both
 * halves are exercised — a working listing with nothing pushed would prove only
 * the first, and a push with no page would prove only the second.
 */
import { BASE, browser, check, login, shot, step, textOf, verdict } from './helpers.mjs'

const failures = []
const { instance, page } = await browser()

const REGISTRY = process.env.DOGIT_REGISTRY ?? '127.0.0.1:8091'
const IMAGE_FILE = 'hello.txt'

try {
  step('sign in as an administrator')
  await login(page)

  step('find a project to push to')
  const projectPath = await page.evaluate(async () => {
    const answer = await (await fetch('/api/v1/projects', { credentials: 'include' })).json()
    return answer.projects?.[0]?.path ?? null
  })
  check(!!projectPath, `using project ${projectPath}`, failures)

  step('start from a project with no images')
  // A previous run may have left a tag behind, and a page that lists one cannot
  // also be checked for saying it has none.
  await page.goto(`${BASE}/p/${projectPath}/-/packages`, { waitUntil: 'networkidle0' })
  await new Promise((resolve) => setTimeout(resolve, 1500))

  await page.evaluate(async (path) => {
    const answer = await (await fetch(`/api/v1/projects/${encodeURIComponent(path)}/packages`, {
      credentials: 'include',
    })).json()
    if (!answer.registry || !answer.token) return

    const listing = await (await fetch(`${answer.registry.url}/packages`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${answer.token}` },
      body: JSON.stringify({ project: path }),
    })).json()

    for (const repository of listing.repos ?? []) {
      for (const tag of repository.tags ?? []) {
        await fetch(`${answer.registry.url}/packages/delete`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${answer.token}` },
          body: JSON.stringify({ project: path, repository: repository.name, tag: tag.name }),
        })
      }
    }
  }, projectPath)

  await page.reload({ waitUntil: 'networkidle0' })
  await new Promise((resolve) => setTimeout(resolve, 1500))

  const empty = await textOf(page, '.card.empty')
  check(/no images yet|no registry/i.test(empty), 'it says plainly that there is nothing yet', failures)

  step('push an image into that project')
  const tag = `page-${Date.now()}`
  const image = `${REGISTRY}/${projectPath}:${tag}`

  const { execFile } = await import('node:child_process')
  const run = (command, args, options = {}) =>
    new Promise((resolve) => {
      execFile(command, args, { timeout: 300000, ...options }, (error, stdout, stderr) => {
        resolve({ error, stdout, stderr })
      })
    })

  const temp = `/tmp/opencode/registry-page-${Date.now()}`
  await run('mkdir', ['-p', temp])
  await run('sh', ['-c', `printf 'FROM scratch\\nCOPY ${IMAGE_FILE} /${IMAGE_FILE}\\n' > ${temp}/Dockerfile`])
  await run('sh', ['-c', `echo "pushed from the project page test" > ${temp}/${IMAGE_FILE}`])

  const build = await run('docker', ['build', '-q', '-t', image, temp])
  check(!build.error, `built ${image}`, failures)

  const loginResult = await run('sh', ['-c', `printf 'secret123' | docker login ${REGISTRY} -u alice --password-stdin`])
  check(!loginResult.error, 'signed in to the registry', failures)

  const push = await run('docker', ['push', image])
  check(!push.error, `pushed ${tag}: ${push.stderr.trim().split('\n').slice(-1)[0]}`, failures)

  step('the page shows it')
  await page.reload({ waitUntil: 'networkidle0' })
  await new Promise((resolve) => setTimeout(resolve, 2000))

  const body = await textOf(page, '.card')
  check(body.includes(tag), `the tag ${tag} is listed`, failures)
  check(/sha256|[0-9a-f]{12}/.test(body), 'with the digest it is stored under', failures)

  const hint = await textOf(page, '.push-hint')
  check(hint.includes(REGISTRY), 'and a push instruction naming the registry', failures)

  await shot(page, 'project-images')

  step('and can remove it again')
  page.on('dialog', async (dialog) => dialog.accept())
  await page.$$eval('.btn', (nodes) => {
    const button = [...nodes].reverse().find((node) => node.textContent.trim() === 'Delete')
    button?.click()
  })
  await new Promise((resolve) => setTimeout(resolve, 2500))

  const after = await textOf(page, '.card')
  check(!after.includes(tag), 'the tag is gone from the page', failures)

  await run('docker', ['logout', REGISTRY])
  await run('sh', ['-c', `rm -rf ${temp}`])
} finally {
  await instance.close()
}

verdict(failures)