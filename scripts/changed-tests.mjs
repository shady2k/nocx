#!/usr/bin/env node
// Which tests this pull request adds or changes, and the repeat that runs them.
//
// A new flake can land on one lucky green run: the suite walks each test once,
// so a test that fails one time in ten passes nine pull requests out of ten and
// the tenth is somebody else's red. nocx-1ebrs.66.1.37 answers that by running
// only the tests the PR itself adds or changes, many times, in the jobs that
// already gate them:
//
//   node scripts/changed-tests.mjs --base <sha> --head <sha>          # JSON
//   node scripts/changed-tests.mjs --base <sha> --head <sha> --format github
//   node scripts/changed-tests.mjs run --plan '<json>' \
//        --packages "$PKGS" --tags gtk3,nocx_framecheck --count 20 --race
//
// The first form is the answer the `changes` job publishes to the test jobs;
// the second writes it as two GitHub outputs; `run` is what a Go job executes
// for the subset of those packages it already owns. Repeating the e2e specs is
// Playwright's own `--repeat-each`, so this script only names them.
//
// THE LIST IS A DIFF, NOT A SECOND CLASSIFIER. Paths and statuses come from
// `git diff --name-status`; the product/supporting decision stays with
// scripts/product-change.mjs, the spec set stays with Playwright (the files
// must be `e2e/*.spec.ts`, and the container's own run decides what this
// platform collects), and a failed repeat is read by the person in the same
// words `go test` and Playwright already print. Nothing here re-decides what a
// spec or a test is beyond the two shapes the bead names.
//
// A DELETED TEST IS NOT LISTED. It cannot fail; what a deletion owes is the
// coverage receipt, which e2e/check-coverage.mjs and the package's build
// already own.
import { execFileSync, spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join, posix } from 'node:path'
import { pathToFileURL } from 'node:url'

/** The module path from go.mod — the prefix `make print-portable-pkgs` uses. */
export function goModulePath(root = process.cwd()) {
  const text = readFileSync(join(root, 'go.mod'), 'utf8')
  const match = /^module[ \t]+(\S+)$/m.exec(text)
  if (!match) throw new Error('go.mod names no module')
  return match[1]
}

/** `e2e/foo.spec.ts` — the spec files Playwright collects by path. */
export function isE2eSpecPath(file) {
  return /^e2e\/[^/]+\.spec\.ts$/.test(file)
}

/** The `./`-relative Go package a file belongs to. */
export function packageDir(file) {
  const dir = posix.dirname(file)
  return dir === '.' ? './' : `./${dir}`
}

// A git `--name-status -M -z` record: `M\0path\0`, `R100\0from\0to\0`.
// `status` is the letter; a rename or copy also carries `from`.
export function parseNameStatus(raw) {
  const parts = raw.split('\0')
  const changes = []
  for (let i = 0; i < parts.length;) {
    const status = parts[i++]
    if (!status) continue
    const code = status[0]
    if (code === 'R' || code === 'C') {
      const from = parts[i++]
      const to = parts[i++]
      if (!from || !to) throw new Error(`incomplete ${status} record`)
      changes.push({ status: code, path: to, from })
    } else {
      const path = parts[i++]
      if (!path) throw new Error(`incomplete ${status} record`)
      changes.push({ status: code, path })
    }
  }
  return changes
}

function scanGo(source) {
  const tokens = []
  let i = 0
  while (i < source.length) {
    const c = source[i]
    if (/\s/.test(c)) {
      i++
      continue
    }
    if (c === '/' && source[i + 1] === '/') {
      const end = source.indexOf('\n', i)
      i = end < 0 ? source.length : end
      continue
    }
    if (c === '/' && source[i + 1] === '*') {
      const end = source.indexOf('*/', i + 2)
      i = end < 0 ? source.length : end + 2
      continue
    }
    if (c === '"' || c === "'") {
      let j = i + 1
      while (j < source.length) {
        if (source[j] === '\\') {
          j += 2
          continue
        }
        if (source[j] === c) {
          j++
          break
        }
        j++
      }
      tokens.push({ kind: 'literal', text: source.slice(i, j), start: i, end: j })
      i = j
      continue
    }
    if (c === '`') {
      const end = source.indexOf('`', i + 1)
      const j = end < 0 ? source.length : end + 1
      tokens.push({ kind: 'literal', text: source.slice(i, j), start: i, end: j })
      i = j
      continue
    }
    if (/[A-Za-z_]/.test(c)) {
      let j = i + 1
      while (j < source.length && /[A-Za-z0-9_]/.test(source[j])) j++
      tokens.push({ kind: 'ident', text: source.slice(i, j), start: i, end: j })
      i = j
      continue
    }
    tokens.push({ kind: 'punct', text: c, start: i, end: i + 1 })
    i++
  }
  return tokens
}

function skipGroup(tokens, from, open, close) {
  let depth = 0
  for (let i = from; i < tokens.length; i++) {
    const token = tokens[i]
    if (token.kind !== 'punct') continue
    if (token.text === open) depth++
    else if (token.text === close) {
      depth--
      if (depth === 0) return i
    }
  }
  return -1
}

function findBody(tokens, from) {
  for (let i = from; i < tokens.length; i++) {
    const token = tokens[i]
    if (token.kind === 'punct' && token.text === '(') {
      const end = skipGroup(tokens, i, '(', ')')
      if (end < 0) return -1
      i = end
      continue
    }
    if (token.kind === 'ident' && (token.text === 'struct' || token.text === 'interface')) {
      let brace = i + 1
      while (
        brace < tokens.length &&
        !(tokens[brace].kind === 'punct' && tokens[brace].text === '{')
      )
        brace++
      const end = skipGroup(tokens, brace, '{', '}')
      if (end < 0) return -1
      i = end
      continue
    }
    if (token.kind === 'punct' && token.text === '{') return i
  }
  return -1
}

/**
 * The body of every top-level `func TestX`, keyed by name.
 *
 * A method has a receiver between `func` and its name and is skipped: it is
 * not a test `go test -run` can select. The body is the text inside its braces,
 * so a comment or a string moved anywhere inside it counts as a change while a
 * change beside the function does not.
 */
export function goTestFunctions(source) {
  const tokens = scanGo(source)
  const funcs = new Map()
  let depth = 0
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i]
    if (token.kind === 'punct' && token.text === '{') {
      depth++
      continue
    }
    if (token.kind === 'punct' && token.text === '}') {
      if (depth > 0) depth--
      continue
    }
    if (depth !== 0 || token.kind !== 'ident' || token.text !== 'func') continue
    const name = tokens[i + 1]
    // Go's own rule: the prefix Test and then no lowercase letter. A helper
    // called Testhelper is not a test, and -run could never select it.
    if (!name || name.kind !== 'ident' || !/^Test(?:$|[^a-z])/.test(name.text)) continue
    let j = i + 2
    while (j < tokens.length && !(tokens[j].kind === 'punct' && tokens[j].text === '(')) {
      if (tokens[j].kind === 'punct' && tokens[j].text === '{') break
      j++
    }
    if (j >= tokens.length || tokens[j].text !== '(') continue
    const paramsEnd = skipGroup(tokens, j, '(', ')')
    if (paramsEnd < 0) continue
    const bodyStart = findBody(tokens, paramsEnd + 1)
    if (bodyStart < 0) continue
    const bodyEnd = skipGroup(tokens, bodyStart, '{', '}')
    if (bodyEnd < 0) continue
    funcs.set(name.text, source.slice(tokens[bodyStart].end, tokens[bodyEnd].start))
    i = bodyEnd
  }
  return funcs
}

/** Names present in head whose body differs from base, or that are new. */
export function changedTestNames(baseSource, headSource) {
  const head = goTestFunctions(headSource)
  const base = baseSource == null ? new Map() : goTestFunctions(baseSource)
  return [...head.keys()]
    .filter((name) => !base.has(name) || base.get(name) !== head.get(name))
    .sort()
}

/**
 * The plan: changed spec files, and changed test functions by package.
 *
 * `git` is the runner, so a test can answer it without a repository; every
 * revision is read through it, so the answer describes the PR's commits rather
 * than whatever the checkout happens to hold.
 */
export function listChangedTests({ base, head, root = process.cwd(), git = runGit }) {
  if (!base || !head) throw new Error('both the base and the head revision are required')
  const changes = parseNameStatus(
    git(['diff', '--name-status', '-M', '-z', `${base}...${head}`], root),
  )
  const specs = new Set()
  const byPackage = new Map()
  for (const change of changes) {
    if (change.status === 'D') continue
    if (isE2eSpecPath(change.path)) specs.add(change.path)
    if (!change.path.endsWith('_test.go')) continue
    const added = change.status === 'A' || change.status === 'C'
    const moved = change.status === 'R' && packageDir(change.from) !== packageDir(change.path)
    const baseSource =
      added || moved ? null : git(['show', `${base}:${change.from ?? change.path}`], root)
    const headSource = git(['show', `${head}:${change.path}`], root)
    const names = changedTestNames(baseSource, headSource)
    if (names.length === 0) continue
    const pkg = packageDir(change.path)
    const tests = byPackage.get(pkg) ?? new Set()
    for (const name of names) tests.add(name)
    byPackage.set(pkg, tests)
  }
  return {
    specs: [...specs].sort(),
    go: [...byPackage.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([pkg, tests]) => ({ package: pkg, tests: [...tests].sort() })),
  }
}

/** The subset of a plan's Go entries whose package is in one job's own set. */
export function filterPlan(entries, patterns, module) {
  return entries.filter((entry) => packageInSet(entry.package, patterns, module))
}

/**
 * Does `pkg` (`./internal/foo`) belong to a make package list?
 *
 * The two owners spell their lists differently: `print-portable-pkgs` echoes
 * `go list` import paths while `print-os-pkgs` echoes `./dir/...` patterns.
 * Both are accepted, and a `/...` means the subtree Go says it means.
 */
export function packageInSet(pkg, patterns, module) {
  const relative = pkg.replace(/^\.\//, '').replace(/\/$/, '')
  for (const raw of String(patterns ?? '').split(/\s+/)) {
    if (!raw) continue
    let pattern = raw
    if (pattern === module) pattern = ''
    else if (pattern.startsWith(`${module}/`)) pattern = pattern.slice(module.length + 1)
    let subtree = false
    if (pattern.endsWith('/...')) {
      subtree = true
      pattern = pattern.slice(0, -4)
    }
    pattern = pattern.replace(/^\.\//, '').replace(/\/$/, '')
    if (subtree ? relative === pattern || relative.startsWith(`${pattern}/`) : relative === pattern)
      return true
  }
  return false
}

/** One `go test` invocation per package, selecting exactly the named tests. */
export function repeatInvocations(
  plan,
  { packages, module, tags = '', count = 20, race = false, go = 'go' },
) {
  if (!Number.isSafeInteger(count) || count < 1) throw new Error('count must be a positive integer')
  return filterPlan(plan, packages, module).map((entry) => {
    const args = ['test']
    if (race) args.push('-race')
    if (tags) args.push('-tags', tags)
    args.push('-count', String(count))
    args.push('-run', `^(${entry.tests.join('|')})$`)
    args.push(entry.package)
    return { command: go, args }
  })
}

export function runGit(args, cwd) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
}

function quote(arg) {
  return /^[A-Za-z0-9_./:@%+=,-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", `'\\''`)}'`
}

function usage() {
  return [
    'Usage:',
    '  node scripts/changed-tests.mjs --base <sha> --head <sha> [--format json|e2e|github]',
    '  node scripts/changed-tests.mjs run --plan <json> --packages <list>',
    '       [--tags <go,tags>] [--count 20] [--race] [--go go]',
  ].join('\n')
}

function option(args, name, fallback) {
  const index = args.indexOf(name)
  if (index < 0) return fallback
  const value = args[index + 1]
  if (value === undefined) throw new Error(`${name} needs a value`)
  return value
}

function main(argv) {
  if (argv.includes('--help') || argv.includes('-h')) {
    console.log(usage())
    return 0
  }
  if (argv[0] === 'run') {
    const planText = option(argv, '--plan', '')
    if (!planText) {
      console.log('changed-tests: no plan')
      return 0
    }
    const plan = JSON.parse(planText)
    const packages = option(argv, '--packages', '')
    const invocations = repeatInvocations(plan, {
      packages,
      module: goModulePath(),
      tags: option(argv, '--tags', ''),
      count: Number(option(argv, '--count', '20')),
      race: argv.includes('--race'),
      go: option(argv, '--go', 'go'),
    })
    if (invocations.length === 0) {
      console.log('changed-tests: no changed Go test in this package set')
      return 0
    }
    let failed = false
    for (const invocation of invocations) {
      const line = [invocation.command, ...invocation.args].map(quote).join(' ')
      console.error(`$ ${line}`)
      const result = spawnSync(invocation.command, invocation.args, { stdio: 'inherit' })
      if (result.error) throw result.error
      if (result.status !== 0) failed = true
    }
    return failed ? 1 : 0
  }
  const plan = listChangedTests({
    base: option(argv, '--base', ''),
    head: option(argv, '--head', ''),
  })
  const format = option(argv, '--format', 'json')
  if (format === 'json') console.log(JSON.stringify(plan))
  else if (format === 'e2e') console.log(plan.specs.join(' '))
  else if (format === 'github') {
    console.log(`e2e_specs=${plan.specs.join(' ')}`)
    console.log(`go_tests=${plan.go.length ? JSON.stringify(plan.go) : ''}`)
  } else throw new Error(`unknown format: ${format}`)
  return 0
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
  try {
    process.exitCode = main(process.argv.slice(2))
  } catch (error) {
    console.error(`changed-tests: ${error instanceof Error ? error.message : error}`)
    process.exitCode = 1
  }
}
