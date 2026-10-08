// Looks at the running site with a real browser and reports what it sees.
//
// A curl of an SPA returns the same shell whatever is wrong with the page, which is why a check
// that only asks "does it answer 200" says nothing about whether the site works. This drives a
// browser: it waits for the page to settle, reports the title and what is on it, and prints the
// console errors and failed requests the page produced along the way.
//
//   node scripts/look-at.mjs [path] [--shot out.png]
//
// Not a login: an unauthenticated page is what an unauthenticated browser sees, and if it lands on
// a sign-in form that is the honest answer rather than a failure to try harder.
import puppeteer from '/home/ewolf/prjs/dogit/web/node_modules/puppeteer-core/lib/puppeteer/puppeteer-core.js';

const path = process.argv[2] ?? '/';
const base = process.env.DOGIT_BASE ?? 'http://localhost:3000';

// DOGIT_SESSION lets this be logged in: the site is behind a session cookie, and without one
// every check reports a sign-in form and nothing about the pages behind it. The value is a row in
// the sessions table, so it is created the same way the application creates one.
const session = process.env.DOGIT_SESSION;

const browser = await puppeteer.launch({
  executablePath: '/usr/bin/google-chrome',
  headless: 'new',
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
});

try {
  const page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 1000 });

  const problems = [];
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push(`console: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('requestfailed', (r) =>
    problems.push(`request failed: ${r.url()} — ${r.failure()?.errorText}`));
  page.on('response', (r) => {
    // The API answering 401 or 500 is a fact about the page, and the page will look fine either
    // way — an empty list and a broken request draw the same thing on screen.
    if (r.status() >= 400) problems.push(`http ${r.status()}: ${r.url()}`);
  });

  if (session) {
    // The domain the site is actually on, taken from where it is being looked at. Hardcoded to
    // localhost, a check against a deployed stand set a cookie for a host that is not there, and
    // every page behind the sign-in reported itself as a sign-in form — which reads as the site
    // being broken rather than as the check having looked in the wrong place.
    const host = new URL(base).hostname;
    await browser.setCookie({
      name: 'dogit_session', value: session,
      domain: host, path: '/',
    });
  }

  // Not `networkidle2`: this page holds an event socket open, so "the network is idle" is a state
  // it never reaches and every check of it times out rather than reporting a page that is fine.
  // Waiting for the document and then giving it a moment is what actually means "drawn".
  await page.goto(base + path, { waitUntil: 'domcontentloaded', timeout: 30000 });
  // Long enough to see whether something is still arriving, because the difference between "slow"
  // and "never" is only visible in a second screenshot.
  const wait = Number(process.env.LOOK_WAIT ?? 2500);
  await new Promise((r) => setTimeout(r, wait));

  // A click before the read, for a page whose interesting part is behind a disclosure. Without it
  // this reports the collapsed page and calls it the page, which is how a working feature reads as a
  // missing one.
  // Open the first disclosure of a given class, by setting `open` rather than clicking: a
  // synthetic click on a <summary> does not toggle it in every engine, and a screenshot of a
  // collapsed panel is evidence of nothing.
  const openFirst = process.argv.includes('--open')
    ? process.argv[process.argv.indexOf('--open') + 1]
    : null;
  if (openFirst) {
    const opened = await page.evaluate((sel) => {
      const hits = [...document.querySelectorAll(`details.${sel}`)];
      if (!hits.length) return -1;
      hits[0].open = true;
      hits[0].dispatchEvent(new Event('toggle'));
      return hits.length;
    }, openFirst);
    await new Promise((r) => setTimeout(r, 2000));
    console.log(`opened the first details.${openFirst} (${opened} present)`);
  }

  // Open a disclosure whose label *contains* a given word. Scoped to <summary> and to an exact
  // containment test, because the row that holds the name also holds two switches and clicking the
  // row's box is how an inspection changes the thing it is inspecting.
  const clickIn = process.argv.includes('--click-in')
    ? process.argv[process.argv.indexOf('--click-in') + 1]
    : null;
  if (clickIn) {
    const opened = await page.evaluate((wanted) => {
      const hits = [...document.querySelectorAll('summary')]
        .filter((el) => (el.textContent ?? '').includes(wanted));
      if (!hits.length) return -1;
      hits[0].click();
      return hits.length;
    }, clickIn);
    // Long enough to see whether something is still arriving, because the difference between "slow"
  // and "never" is only visible in a second screenshot.
  const wait = Number(process.env.LOOK_WAIT ?? 2500);
  await new Promise((r) => setTimeout(r, wait));
    console.log(`opened the disclosure containing "${clickIn}" (${opened} matched)`);
  }

  const clickText = process.argv.includes('--click-text')
    ? process.argv[process.argv.indexOf('--click-text') + 1]
    : null;
  // Which of several equally-named things to click. A page with two places has two tabs of the
  // same name, and refusing to choose between them is right — but then there has to be a way to
  // say which one, or a page that repeats itself can never be clicked at all.
  const clickNth = process.argv.includes('--click-nth')
    ? Number(process.argv[process.argv.indexOf('--click-nth') + 1])
    : 0;
  if (clickText) {
    // By the element's own text, and only an element whose whole text is that — so that asking
    // to open a disclosure cannot land on a switch two lines above it. An inspection that changes
    // what it is inspecting is worse than one that reports nothing.
    //
    // Refuses when the name is not unique, unless a position was named: a page with two places
    // has two identically named tabs, and guessing between them is how an inspection reports on
    // the wrong one.
    const picked = process.argv.includes('--click-nth');
    const clicked = await page.evaluate((wanted, nth, choose) => {
      const hits = [...document.querySelectorAll('summary, button, a, [role="button"]')]
        .filter((el) => (el.textContent ?? '').trim() === wanted);
      if (!choose && hits.length !== 1) return hits.length;
      const el = hits[nth];
      if (!el) return hits.length;
      el.click();
      return 1;
    }, clickText, clickNth, picked);
    if (clicked !== 1) {
      console.log(`click-text "${clickText}" matched ${clicked} elements; nothing was clicked`);
    } else {
      await new Promise((r) => setTimeout(r, 2000));
      console.log('clicked:', clickText);
    }
  }

  console.log('URL:    ', page.url());
  console.log('title:  ', await page.title());
  console.log('--- what is on the page ---');
  console.log((await page.evaluate(() => document.body.innerText)).trim().split('\n').slice(0, 40).join('\n'));

  if (problems.length) {
    console.log('--- what went wrong ---');
    for (const p of [...new Set(problems)].slice(0, 25)) console.log('  ', p);
  } else {
    console.log('--- no console errors, no failed requests ---');
  }

  const shot = process.argv.includes('--shot')
    ? process.argv[process.argv.indexOf('--shot') + 1]
    : null;
  if (shot) {
    await page.screenshot({ path: shot, fullPage: true });
    console.log('screenshot:', shot);
  }
} finally {
  await browser.close();
}
