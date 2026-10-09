/**
 * The deploy page: that a deployment in progress is shown while it happens.
 *
 * This exists because of a bug that looked like anything but what it was. The socket
 * was connected and healthy, events were arriving in the browser, and the page still
 * showed nothing happening — because `onEvent` had lost its `listeners.add` and so
 * never registered anybody to deliver to. Every live event was received by the socket
 * and dropped on the floor.
 *
 * A test that only looked at the connection would have passed throughout. So this one
 * watches what the page *draws* while a run is going, which is the thing that was
 * broken, and fails on the symptom rather than on the cause.
 *
 * It needs a project that actually deploys: a run only produces the later phases when
 * a place's rules match the ref being run, so the run is started on the branch the
 * repository's configuration names.
 */
import { browser, check, login, step, verdict } from './helpers.mjs'

const failures = []
const { instance, page } = await browser()

/**
 * How long to watch a run for.
 *
 * Long enough for a real rollout, not a quick one. A rolling update of ten pods one at
 * a time takes over a minute, and the deployment then waits for the pods it replaced to
 * finish going away — a test that stopped looking half way through would call the page
 * stuck when it was simply still working.
 */
const WATCH_MS = 150_000

try {
  step('sign in')
  await login(page)

  step('find a project that deploys something')
  const target = await page.evaluate(async (wanted) => {
    const answer = await (await fetch('/api/v1/projects?per_page=100', {
      credentials: 'include',
    })).json()

    const projects = wanted
      ? (answer.projects ?? []).filter((one) => one.path === wanted)
      : (answer.projects ?? [])

    for (const project of projects) {
      const places = await (await fetch(
        `/api/v1/projects/${project.id}/deploy-places`,
        { credentials: 'include' },
      )).json()
      if ((places.places ?? []).length > 0) {
        return {
          id: project.id,
          path: project.path,
          // A run only reaches a place whose rules match the ref it runs on, so the ref
          // is part of what makes a run a deployment. Overridable, because which branch
          // a repository deploys from is that repository's own business.
          ref: process.env.DOGIT_REF ?? '',
        }
      }
    }
    return null
  }, process.env.DOGIT_PROJECT ?? null)

  if (!target) {
    step('no project deploys anywhere, so there is nothing to watch')
    console.log('  (the deploy page has no run to show here)')
    await instance.close()
    process.exit(0)
  }
  check(!!target, `using project ${target.path}`, failures)

  step('open the deploy page')
  await page.goto(`http://localhost:3000/p/${target.path}/-/deploy`, {
    waitUntil: 'networkidle2',
  })
  await new Promise((resolve) => setTimeout(resolve, 3000))

  // Recorded as it happens rather than looked at afterwards: the run takes seconds, and
  // a check made afterwards would see the finished page and call it working.
  await page.evaluate(() => {
    window.__seen = []
    window.__watch = setInterval(() => {
      const row = {
        head: document.querySelector('.active-card .block-title')?.textContent?.trim() ?? '',
        lines: document.querySelectorAll('.deploy-log-line').length,
      }
      const last = window.__seen[window.__seen.length - 1]
      const same = last && last.head === row.head && last.lines === row.lines
      if (!same) window.__seen.push(row)
    }, 50)
  })

  step('start a run')
  const started = await page.evaluate(async ({ id, ref }) => {
    const response = await fetch(`/api/v1/projects/${id}/pipelines`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(ref ? { ref } : {}),
    })
    return response.status
  }, target)
  check(started < 400, `the run was accepted (status ${started})`, failures)

  step(`watch the page for ${WATCH_MS / 1000}s`)
  await new Promise((resolve) => setTimeout(resolve, WATCH_MS))
  const seen = await page.evaluate(() => {
    clearInterval(window.__watch)
    return window.__seen
  })

  for (const row of seen) {
    console.log(`    стрелка=${row.arrow ?? ''} строк=${row.lines} "${row.head}"`)
  }

  // The claim that is being protected: a run in progress is drawn while it happens.
  const drewProgress = seen.some((row) => /deploying/i.test(row.head))
  check(drewProgress, 'the page showed the run while it was happening', failures)

  const drewLines = seen.some((row) => row.lines > 0)
  check(drewLines, 'the log was written as the run reported', failures)

  // And that it does not get stuck claiming something is moving once nothing is.
  const finished = seen[seen.length - 1]
  const stuck = drewProgress && /deploying/i.test(finished.head)
  check(!stuck, 'the card stopped claiming progress once the run was over', failures)
} finally {
  await instance.close()
}

verdict(failures)