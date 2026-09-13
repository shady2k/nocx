#!/usr/bin/env node
/**
 * A MODEL of `deadcode`, for the ratchet's fixture test — a stub of an external
 * tool, not a second analysis on any gate's path.
 *
 * The ratchet's cgo rule hands deadcode the `//export`ed callbacks through an
 * overlay and lets the analyser do the rest, so what the fixture test has to
 * watch is the ratchet's half: which callbacks it renders a root for, what it
 * writes into that root, and whether the analyser ever sees it. The real
 * analyser cannot run there — ci-frontend has no Go toolchain (deadcode is
 * installed in the ci-mac and ci-linux jobs, which is where the ratchet itself
 * runs) — so it is modelled here.
 *
 * The model implements the two rules the fixture depends on and nothing else:
 *
 *   - a function named in an overlay root file is live, and
 *   - a function called by a live function of the same module is live.
 *
 * Everything else is dead. It is deliberately tiny: it resolves no types, reads
 * no build constraints, and would be a poor analyser for anything but the
 * fixture module beside it, where every call is a package-level function of a
 * unique name. Output is deadcode's own format, because the ratchet's parser is
 * part of what this drives.
 */
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = process.cwd()
const sources = new Map()
for (const file of goFiles(root)) sources.set(file, readFileSync(file, 'utf8'))

/** name → where it is declared. Methods are keyed by their own name too; the fixture has none. */
const declarations = new Map()
for (const [file, source] of sources) {
  source.split('\n').forEach((line, i) => {
    const decl = /^func[ \t]+(?:\([^)]*\)[ \t]+)?([A-Za-z_]\w*)[ \t]*\(/.exec(line)
    if (decl) declarations.set(decl[1], { file, line: i + 1 })
  })
}

/** The callbacks the ratchet rendered as roots: the call lines of its overlay files. */
function rootNames() {
  const overlay = /(?:^|\s)-overlay=(\S+)/.exec(process.env.GOFLAGS ?? '')?.[1]
  if (!overlay) return []
  const names = []
  for (const solid of Object.values(JSON.parse(readFileSync(overlay, 'utf8')).Replace ?? {})) {
    for (const line of readFileSync(solid, 'utf8').split('\n')) {
      const call = /^\t([A-Za-z_]\w*)\(/.exec(line)
      if (call) names.push(call[1])
    }
  }
  return names
}

/** Every function name called in the body of the declaration at `line` (1-based). */
function callsFrom(file, line) {
  const lines = sources.get(file).split('\n')
  const body = []
  for (let i = line; i < lines.length && lines[i] !== '}'; i++) body.push(lines[i])
  return [...body.join('\n').matchAll(/\b([A-Za-z_]\w*)[ \t]*\(/g)].map((m) => m[1])
}

const live = new Set()
const worklist = rootNames()
while (worklist.length > 0) {
  const name = worklist.pop()
  if (live.has(name) || !declarations.has(name)) continue
  live.add(name)
  const decl = declarations.get(name)
  for (const call of callsFrom(decl.file, decl.line)) {
    if (declarations.has(call)) worklist.push(call)
  }
}

const dead = [...declarations]
  .filter(([name]) => !live.has(name))
  .sort((a, b) => a[1].file.localeCompare(b[1].file) || a[1].line - b[1].line)
  .map(([name, decl]) => `${relative(root, decl.file)}:${decl.line}:6: unreachable func: ${name}`)

if (dead.length > 0) process.stdout.write(`${dead.join('\n')}\n`)

function goFiles(dir) {
  const files = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (/^(?:[._]|node_modules$)/.test(entry.name)) continue
      files.push(...goFiles(path))
    } else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
      files.push(path)
    }
  }
  return files.sort()
}
