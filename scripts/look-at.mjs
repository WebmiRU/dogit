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
    await browser.setCookie({
      name: 'dogit_session', value: session,
      domain: 'localhost', path: '/',
    });
  }

  await page.goto(base + path, { waitUntil: 'networkidle2', timeout: 30000 });
  // Given a moment to render after the network settles: a Vue page finishes drawing a tick or two
  // after its last request answers.
  await new Promise((r) => setTimeout(r, 1200));

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
