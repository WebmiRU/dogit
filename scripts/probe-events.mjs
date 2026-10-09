// What one deployment costs on the event socket: how many events, of what kind, and how many bytes.
//
// A number nobody can break down is a number nobody can argue with, and this one was worth having
// because two thousand eight hundred events in fifteen seconds had been reported with no idea what
// they were. Counted by kind, so a cheap stream and an expensive one can be told apart.
//
// It reconnects, because the socket on a stand drops every few tens of seconds and a measurement
// that stops at the first drop describes half a deployment. Counts and bytes are kept across the
// drops, and the drops are counted themselves — how many there were is part of what a page has to
// live with.
//
// The socket is instance-wide, not per project: everything the account may see arrives here. On a
// stand with one active project that is the whole of it.
import { readFileSync } from 'node:fs'

// `ws` rather than the built-in WebSocket, and the reason is worth writing down because it cost
// an afternoon: Node's takes its second argument as protocols, so a cookie passed as `headers`
// is silently dropped. The probe connected, was refused, and reported a socket that opens and
// dies in six seconds — which is a real and alarming symptom, produced entirely by the instrument.
// An event stream authenticated by a cookie needs a library that can send one.
const { default: WebSocket } = await import(
  '/home/ewolf/prjs/dogit/web/node_modules/ws/index.js')

const base = process.env.DOGIT_BASE ?? 'https://f220.ru'
const seconds = Number(process.env.PROBE_SECONDS ?? 90)
const only = process.env.PROBE_PROJECT ?? ''
const session = readFileSync(process.env.SESSION_FILE ?? '/tmp/stand-session.txt', 'utf8').trim()

// Which socket to talk to. Default: the same host as the base URL, which is the path
// through the dev server. Set PROBE_WS to point somewhere else — the dev server answers
// an ordinary 200 where a 101 belongs, and a probe that takes its address from the base
// URL cannot tell "the socket is broken" from "I asked the wrong door".
const url = process.env.PROBE_WS || base.replace(/^http/, 'ws') + '/api/v1/events/socket'

const byKind = new Map()
// A project filter that matches nothing is indistinguishable from a dead socket unless the
// dropped ones are counted and shown. This probe reported "0 events" for a deployment that the
// server had in fact sent 29 events for, because the filter was given `versions` while the
// events carried `test/versions`. Silence is not a measurement.
const projectsSeen = new Map()
let total = 0
let filtered = 0
let bytes = 0
let opens = 0
let closes = 0
let started = 0

function connect() {
  const socket = new WebSocket(url, { headers: { Cookie: `dogit_session=${session}` } })
  socket.on('error', (err) => console.log(`[${elapsed()}] socket error: ${err.message}`))

  socket.on('open', () => {
    opens += 1
    if (started === 0) started = Date.now()
    // The server waits five seconds for this and then lets the connection go, so a client
    // that opens and says nothing is a client that measures nothing. Not a hypothesis: this
    // probe spent an afternoon reporting a socket that dies every five seconds, and the five
    // seconds were this.
    socket.send(JSON.stringify({ since: Number(process.env.PROBE_SINCE ?? 0) }))
    console.log(`[${elapsed()}] connected, asked from #${process.env.PROBE_SINCE ?? 0}`)
  })

  socket.addEventListener('message', (event) => {
    bytes += typeof event.data === 'string' ? event.data.length : event.data?.byteLength ?? 0
    let kind = 'unreadable'
    let project = ''
    try {
      const parsed = JSON.parse(event.data)
      kind = parsed.kind ?? 'no kind'
      project = parsed.project_path ?? ''
    } catch {
      /* counted as unreadable */
    }
    if (project) projectsSeen.set(project, (projectsSeen.get(project) ?? 0) + 1)
    if (only && project && project !== only) {
      filtered += 1
      return
    }
    byKind.set(kind, (byKind.get(kind) ?? 0) + 1)
    total += 1
  })

  socket.addEventListener('close', (event) => {
    closes += 1
    console.log(`[${elapsed()}] closed: code ${event.code}, reconnecting`)
    if (Date.now() - started < seconds * 1000) setTimeout(connect, 1000)
  })
}

function elapsed() {
  return started === 0 ? '??' : ((Date.now() - started) / 1000).toFixed(1) + 's'
}

connect()

setTimeout(() => {
  const secs = (Date.now() - started) / 1000
  console.log(`\n=== ${total} событий за ${secs.toFixed(1)}s — ${(total / secs).toFixed(0)} в секунду ===`)
  console.log(`=== ${(bytes / 1024).toFixed(0)} КиБ, среднее ${total ? Math.round(bytes / total) : 0} байт на событие ===`)
  console.log(`=== соединений: ${opens}, обрывов: ${closes}`)
  if (filtered) {
    console.log(`=== отброшено фильтром «${only}»: ${filtered}`)
    for (const [path, count] of [...projectsSeen].sort((a, b) => b[1] - a[1])) {
      console.log(`      ${String(count).padStart(6)}  ${path}`)
    }
  }
  const rows = [...byKind.entries()].sort((a, b) => b[1] - a[1])
  for (const [kind, count] of rows) {
    console.log(`  ${String(count).padStart(6)}  ${((count / total) * 100).toFixed(1).padStart(5)}%  ${kind}`)
  }
  process.exit(0)
}, seconds * 1000)