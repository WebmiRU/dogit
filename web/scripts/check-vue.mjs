/**
 * Parses every single-file component and reports the ones that do not.
 *
 * A missing end tag in one file takes the whole dev server's overlay down with
 * it, and the message points at a line in a file nobody has open. This is a
 * second of work and it names the file.
 *
 *   node scripts/check-vue.mjs
 */
import { parse } from '@vue/compiler-sfc'
import { readdirSync, readFileSync } from 'node:fs'
import { join, extname } from 'node:path'

const root = new URL('../app/', import.meta.url).pathname

function componentsIn(dir) {
  const found = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) found.push(...componentsIn(path))
    else if (extname(entry.name) === '.vue') found.push(path)
  }
  return found
}

const broken = []
for (const file of componentsIn(root)) {
  const { errors } = parse(readFileSync(file, 'utf8'), { filename: file })
  for (const error of errors) {
    const line = error.loc?.start?.line
    broken.push(`${file.replace(root, 'app/')}${line ? `:${line}` : ''} — ${error.message}`)
  }
}

if (broken.length) {
  console.error(`${broken.length} component(s) do not parse:`)
  for (const line of broken) console.error(`  ${line}`)
  process.exit(1)
}

console.log('every component parses')