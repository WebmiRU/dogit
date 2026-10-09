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
import { transformSync } from 'esbuild'
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
  const name = file.replace(root, 'app/')
  const source = readFileSync(file, 'utf8')

  const { errors, descriptor } = parse(source, { filename: file })
  for (const error of errors) {
    const line = error.loc?.start?.line
    broken.push(`${name}${line ? `:${line}` : ''} — ${error.message}`)
  }
  if (errors.length) continue

  // The script is read as TypeScript as well, because the template parsing above
  // says nothing about whether it compiles: a component whose script does not parse
  // still has a perfectly good template, and the dev server answers 404 for the whole
  // page rather than showing what is wrong with it.
  for (const block of [descriptor.script, descriptor.scriptSetup]) {
    if (!block) continue
    try {
      transformSync(block.content, { loader: block.lang === 'ts' ? 'ts' : 'jsx' })
    } catch (error) {
      const line = block.loc.start.line + (error.errors?.[0]?.location?.line ?? 1) - 1
      broken.push(`${name}:${line} — the script does not compile: ${error.errors?.[0]?.text ?? error.message}`)
    }
  }
}

if (broken.length) {
  console.error(`${broken.length} component(s) do not parse:`)
  for (const line of broken) console.error(`  ${line}`)
  process.exit(1)
}

console.log('every component parses')