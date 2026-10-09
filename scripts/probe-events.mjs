// What the event socket actually carries, counted by kind.
//
// A stand reported two thousand eight hundred events in fifteen seconds, and nobody knew what
// they were. Two numbers a reader can check — how many, and of what — turn "that seems a lot"
// into either a real defect or nothing at all.
import { readFileSync } from 'node:fs'

const base = process.env.DOGIT_BASE ?? 'https://f220.ru'
const seconds = Number(process.env.PROBE_SECONDS ?? 10)
const session = readFileSync(process.env.SESSION_FILE ?? '/tmp/stand-session.txt', 'utf8').trim()

const url = base.replace(/^http/, 'ws') + '/api/v1/events/socket'
const socket = new WebSocket(url, { headers: { Cookie: `dogit_session=${session}` } })

const byKind = new Map()
let total = 0
let opened = 0
let closes = 0

const started = Date.now()
socket.addEventListener('open', () => {
  opened += 1
  console.log(`connected at ${new Date().toLocaleTimeString()}`)
})
socket.addEventListener('message', (event) => {
  let kind = 'unreadable'
  try {
    kind = JSON.parse(event.data).kind ?? 'no kind'
  } catch {
    /* counted as unreadable */
  }
  byKind.set(kind, (byKind.get(kind) ?? 0) + 1)
  total += 1
})
socket.addEventListener('close', (event) => {
  closes += 1
  console.log(`closed after ${(Date.now() - started) / 1000}s: code ${event.code}`)
})

setTimeout(() => {
  const elapsed = (Date.now() - started) / 1000
  console.log(`\n${total} events in ${elapsed.toFixed(1)}s — ${(total / elapsed).toFixed(0)} a second`)
  console.log(`opened ${opened} time(s), closed ${closes} time(s)`)
  const rows = [...byKind.entries()].sort((a, b) => b[1] - a[1])
  for (const [kind, count] of rows) {
    console.log(`  ${String(count).padStart(6)}  ${(count / total * 100).toFixed(1).padStart(5)}%  ${kind}`)
  }
  socket.close()
  process.exit(0)
}, seconds * 1000)