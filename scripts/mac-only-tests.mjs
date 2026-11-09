#!/usr/bin/env node
// WHICH TESTS macOS ALONE CAN RUN, DERIVED FROM THE SOURCE (nocx-ouru5.1.13).
//
// The owner's rule, 2026-11-09: the macOS CI job holds ONLY the tests that
// cannot run without a Mac. The repository used to decide that per PACKAGE —
// `make print-os-pkgs` moves a package into the OS job the moment one file in
// it carries a platform constraint — so internal/app, internal/coordinator,
// internal/helper/session and the rest ran every test of theirs on the Mac as
// well as on Linux, which is where most of ci-mac's 31 minutes went.
//
// THE MECHANISM, and why this one:
//
//   1. A test is macOS-only when its FILE says so — a `//go:build` line whose
//      only GOOS is darwin, or a `_darwin_test.go` name (the compiler's own
//      implicit constraint). That half is DERIVED from the source, so a new
//      darwin test needs no registration and cannot be forgotten.
//   2. Plus an explicit, checked list (LISTED below) for a test that lives in a
//      shared file but is macOS's subject anyway. Today there is exactly one,
//      and the check refuses a list entry whose body does not prove it (a
//      `runtime.GOOS != "darwin"` guard and a skip).
//   3. Everything else runs on Linux, AND STAYS on Linux — the Linux job passes
//      no selection at all. A test that reaches a platform seam exercises the
//      LINUX implementation there, and Linux ships too, so skipping it would
//      lose the linux half of the very code the seam exists for; the owner's
//      rule limits what the MAC runs, not what Linux does. What Linux cannot run
//      is a test in a darwin-only TEST file, which the compiler already excludes
//      there. `make ci-os-split` runs `check`, which proves the selection is
//      honest: every darwin-only test file in the tree belongs to a package in
//      OS_PKG_DIRS, every selected test is darwin-only, reachability-selected or
//      listed, every listed name exists and proves itself, no selected test
//      lives in a file the darwin build does not compile, and every mac-only
//      name is unique across the set (the Mac passes them as ONE anchored
//      regexp, so a name shared with another test would select the wrong one).
//
// AND THE THIRD SOURCE, WHICH THE FILE RULE CANNOT SEE (review round 2): a test
// in an UNTAGGED file whose code under test is a darwin IMPLEMENTATION —
// `mounts_darwin.go`, `pty_darwin.go`, the Keychain — reaches code that exists
// only on a Mac, so it is the Mac's too. `cmd/mactests` derives that set (SSA +
// CHA over the host build, plus the names the darwin build adds) and COMMITS it
// at scripts/mac-only-tests.txt, because the analysis needs a build that loads
// and only a Mac supplies the darwin files: ci-mac re-derives it (`make
// mac-check`) and fails when the file is stale, and this script reads the file
// so a checkout without a Mac can still see, check and select what the Mac runs. The checks here are the file's own —
// every entry exists, is not already derived, and is not a test the darwin build
// does not compile.
//
// A test-name CONVENTION was the other candidate and is worse here: it would
// rename existing tests to satisfy a selector, and it still says nothing about
// the tests the compiler already knows are darwin's. The per-file rule costs
// nothing to maintain and the list is where judgement goes, with a check that
// keeps the judgement honest.
//
//   node scripts/mac-only-tests.mjs check  <dir>...
//   node scripts/mac-only-tests.mjs pkgs   <dir>...   ./dir/... for each package
//                                                    holding a mac-only test
//   node scripts/mac-only-tests.mjs select <dir>...   ^(TestA|TestB)$ over the
//                                                    whole set, empty if none
//   node scripts/mac-only-tests.mjs darwin-pkgs       ./dir/... for every
//                                                    package with a darwin file
//                                                    (the mac lint's set)
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, posix, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { dirname } from 'node:path'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')

/** The committed reachability selection `cmd/mactests --write` produces. */
export const REACHED_FILE = join(ROOT, 'scripts/mac-only-tests.txt')

/** The reachability entries, as `pkg<TAB>name` pairs. Comments are skipped. */
export function reachedSelection(text = readFileSync(REACHED_FILE, 'utf8')) {
  const out = []
  for (const line of text.split('\n')) {
    if (line === '' || line.startsWith('#')) continue
    const at = line.indexOf('\t')
    if (at <= 0) throw new Error(`mac-only-tests.txt: a line is not pkg<TAB>test: ${JSON.stringify(line)}`)
    out.push({ pkg: line.slice(0, at), name: line.slice(at + 1) })
  }
  return out
}

/** Every GOOS token the compiler knows, as the build constraint spells them. */
export const GOOS = [
  'aix', 'android', 'darwin', 'dragonfly', 'freebsd', 'hurd', 'illumos', 'ios',
  'js', 'linux', 'netbsd', 'openbsd', 'plan9', 'solaris', 'wasip1', 'windows',
  // Not a GOOS, and the reason it is here: `unix` is satisfied by linux too, so
  // a test behind `//go:build unix` must never be called macOS-only.
  'unix',
]

/**
 * THE ONE HAND-WRITTEN ENTRY, and the rule for adding another: the test must
 * be macOS's SUBJECT while living in a file both platforms compile, and its
 * body must say so (a `runtime.GOOS != "darwin"` guard and a skip). A test that
 * merely passes on both sides belongs to Linux, where it already runs; a test
 * behind a darwin build constraint is derived and needs no entry here.
 */
export const LISTED = [
  // The Application Support layout is macOS's own answer to "where does a
  // profile live", and the assertion only means anything on a Mac.
  { pkg: 'internal/storage', name: 'TestNewOSPaths_macOS' },
]

/** The build constraint of a Go file, or null. */
export function buildConstraint(text) {
  const match = /^\/\/go:build[ \t]+(.+)$/m.exec(text)
  return match ? match[1].trim() : null
}

/** The test functions a file declares, in source order, TestMain excluded. */
export function testNames(text) {
  const out = []
  for (const match of text.matchAll(/^func (Test[A-Za-z0-9_]*|Example[A-Za-z0-9_]*|Fuzz[A-Za-z0-9_]*|Benchmark[A-Za-z0-9_]*)\(/gm)) {
    if (match[1] !== 'TestMain') out.push(match[1])
  }
  return out
}

/**
 * Is this test FILE one the darwin build does not compile? True when its
 * constraint names linux and not darwin, or excludes darwin outright, or its
 * name is the compiler's own `_linux_test.go` suffix. A test declared there
 * cannot be the Mac's, and selecting it would also take it off the Linux side —
 * which is now the only side that runs it.
 */
export function linuxOnlyFile(file, text) {
  if (/_linux_test\.go$/.test(file)) return true
  if (/_darwin(_test)?\.go$/.test(file)) return false
  const constraint = buildConstraint(text)
  if (!constraint) return false
  const tokens = new Set(constraint.split(/[^A-Za-z0-9_]+/).filter(Boolean))
  if (/!\s*darwin/.test(constraint)) return !tokens.has('linux')
  if (!tokens.has('linux')) return false
  return !tokens.has('darwin')
}

/**
 * Is this test FILE macOS-only? True when the compiler would include it on
 * darwin and on nothing else: the `_darwin_test.go` suffix, or a constraint
 * naming darwin as its only GOOS. `//go:build darwin || linux` and
 * `//go:build unix` are both false answers here, deliberately.
 */
export function darwinOnlyFile(file, text) {
  if (/_darwin_test\.go$/.test(file)) return true
  const constraint = buildConstraint(text)
  if (!constraint) return false
  const tokens = new Set(constraint.split(/[^A-Za-z0-9_]+/).filter(Boolean))
  const named = GOOS.filter((os) => tokens.has(os))
  return named.length === 1 && named[0] === 'darwin'
}

/** Does this test's own body declare it darwin-only (a guard and a skip)? */
export function selfDeclaredDarwin(body) {
  return /runtime\.GOOS\s*!=\s*"darwin"/.test(body) && /\bSkip(f)?\(/.test(body)
}

/** The func blocks of a test file: `name -> source`. */
export function testBodies(text) {
  const out = new Map()
  const parts = text.split(/^(?=func )/m)
  for (const part of parts) {
    const match = /^func (Test[A-Za-z0-9_]*|Example[A-Za-z0-9_]*|Fuzz[A-Za-z0-9_]*|Benchmark[A-Za-z0-9_]*)\(/.exec(part)
    if (match && match[1] !== 'TestMain') out.set(match[1], part)
  }
  return out
}

function goFiles(dir) {
  const full = join(ROOT, dir)
  let entries
  try {
    entries = readdirSync(full, { withFileTypes: true })
  } catch {
    return []
  }
  return entries
    .filter((entry) => entry.isFile() && entry.name.endsWith('.go'))
    .map((entry) => ({ path: posix.join(dir, entry.name), name: entry.name }))
}

/** Every Go package directory in the tree, as repository-relative paths. */
export function packageDirs() {
  const out = []
  const skip = new Set(['node_modules', 'worktrees', 'testdata', '.git', '.claude', 'build'])
  const walk = (dir) => {
    const full = join(ROOT, dir)
    let entries
    try {
      entries = readdirSync(full, { withFileTypes: true })
    } catch {
      return
    }
    // A nested go.mod is another module: not this repository's package, and
    // not `go list ./...`'s either — the lint set below is derived from the
    // same walk, so including one here would lint a tree the Linux job cannot
    // even load.
    if (dir !== '' && entries.some((entry) => entry.isFile() && entry.name === 'go.mod')) return
    if (entries.some((entry) => entry.isFile() && entry.name.endsWith('.go'))) {
      if (dir !== '') out.push(dir)
    }
    for (const entry of entries) {
      if (!entry.isDirectory()) continue
      if (skip.has(entry.name) || entry.name.startsWith('.')) continue
      walk(dir === '' ? entry.name : posix.join(dir, entry.name))
    }
  }
  walk('')
  return out.sort()
}

/**
 * The mac-only tests inside the given package dirs, and everything needed to
 * judge them: `derived` from the file rule, `listed` from LISTED.
 */
export function macOnly(dirs) {
  const wanted = new Set(dirs)
  const derived = []
  const seen = new Map()
  for (const dir of [...wanted].sort()) {
    for (const file of goFiles(dir)) {
      if (!file.name.endsWith('_test.go')) continue
      const text = readFileSync(join(ROOT, file.path), 'utf8')
      const names = testNames(text)
      for (const name of names) {
        if (!seen.has(name)) seen.set(name, [])
        seen.get(name).push(dir)
      }
      if (!darwinOnlyFile(file.name, text)) continue
      for (const name of names) derived.push({ pkg: dir, name })
    }
  }
  return {
    derived,
    listed: LISTED.filter((entry) => wanted.has(entry.pkg)),
    reached: reachedSelection().filter((entry) => wanted.has(entry.pkg)),
    seen,
  }
}

/** The test functions a package directory declares, by name, and their file. */
export function declaredTests(dir) {
  const out = new Map()
  for (const file of goFiles(dir)) {
    if (!file.name.endsWith('_test.go')) continue
    const text = readFileSync(join(ROOT, file.path), 'utf8')
    for (const name of testNames(text)) out.set(name, { file: file.name, text })
  }
  return out
}

/** Everything `check` proves, as a list of failures (empty is a pass). */
export function checkPartition(dirs) {
  const wanted = new Set(dirs)
  const failures = []
  if (dirs.length === 0) failures.push('no OS package directory was given')

  const { derived, listed, reached, seen } = macOnly(dirs)
  const mac = new Map()
  for (const entry of [...derived, ...reached, ...listed]) {
    const key = `${entry.pkg} ${entry.name}`
    if (mac.has(key)) failures.push(`${key} is selected twice`)
    mac.set(key, entry)
  }
  if (mac.size === 0) {
    failures.push('the macOS selection is empty: the partition is degenerate')
  }

  // EVERY DARWIN-ONLY TEST FILE IN THE TREE must sit in a package the mac job
  // is given, or its tests are selected by neither side — the hole this check
  // exists for.
  for (const dir of packageDirs()) {
    for (const file of goFiles(dir)) {
      if (!file.name.endsWith('_test.go')) continue
      const text = readFileSync(join(ROOT, file.path), 'utf8')
      if (!darwinOnlyFile(file.name, text)) continue
      if (testNames(text).length === 0) continue
      if (!wanted.has(dir)) failures.push(`${file.path} is darwin-only and ${dir} is not in OS_PKG_DIRS`)
    }
  }

  // A LISTED ENTRY must exist, must not be derivable, must prove itself, and
  // must not be behind the other build tag this repository partitions by.
  for (const entry of LISTED) {
    const files = goFiles(entry.pkg).filter((file) => file.name.endsWith('_test.go'))
    const hit = files.find((file) => testBodies(readFileSync(join(ROOT, file.path), 'utf8')).has(entry.name))
    if (!hit) {
      failures.push(`LISTED names ${entry.pkg} ${entry.name}, which no test file declares`)
      continue
    }
    const text = readFileSync(join(ROOT, hit.path), 'utf8')
    if (darwinOnlyFile(hit.name, text)) {
      failures.push(`LISTED names ${entry.pkg} ${entry.name}, which its file already derives; drop the entry`)
    }
    if (/nocx_local_ssh/.test(buildConstraint(text) ?? '')) {
      failures.push(`LISTED names ${entry.pkg} ${entry.name}, which lives behind nocx_local_ssh`)
    }
    if (linuxOnlyFile(hit.name, text)) {
      failures.push(`LISTED names ${entry.pkg} ${entry.name}, which the darwin build does not compile`)
    }
    if (!selfDeclaredDarwin(testBodies(text).get(entry.name))) {
      failures.push(`LISTED names ${entry.pkg} ${entry.name}, whose body does not guard on GOOS != "darwin" with a skip`)
    }
    if (!wanted.has(entry.pkg)) failures.push(`LISTED names ${entry.pkg}, which is not in OS_PKG_DIRS`)
  }

  // EVERY COMMITTED REACHABILITY ENTRY must be a test that exists, in a file the
  // darwin build compiles, in a package this job runs. The tool that writes the
  // file enforces the same three things where it can see the darwin build; this
  // is the half a Linux box can check for itself.
  for (const entry of reached) {
    const declared = declaredTests(entry.pkg)
    const hit = declared.get(entry.name)
    if (!hit) {
      failures.push(`mac-only-tests.txt names ${entry.pkg} ${entry.name}, which no test file declares`)
      continue
    }
    if (darwinOnlyFile(hit.file, hit.text)) {
      failures.push(`mac-only-tests.txt names ${entry.pkg} ${entry.name}, which its file already derives; drop the entry`)
    }
    if (linuxOnlyFile(hit.file, hit.text)) {
      failures.push(`mac-only-tests.txt names ${entry.pkg} ${entry.name}, which the darwin build does not compile`)
    }
  }

  // ONE ANCHORED REGEXP SELECTS THEM ALL, so a name that also appears in a
  // Linux test of this set would run that test on the Mac — or skip it on
  // Linux. Names are therefore unique across the set.
  for (const entry of mac.values()) {
    const where = seen.get(entry.name) ?? []
    if (where.length > 1) {
      failures.push(`${entry.name} is selected as macOS-only but is declared by ${where.join(', ')}`)
    }
  }

  return failures
}

/** `./dir/...` for the packages that hold at least one mac-only test. */
export function macPackages(dirs) {
  const { derived, listed, reached } = macOnly(dirs)
  const pkgs = [...new Set([...derived, ...reached, ...listed].map((entry) => entry.pkg))].sort()
  return pkgs.map((pkg) => `./${pkg}/...`)
}

/** The anchored selection both sides pass to `go test` (`-run` / `-skip`). */
export function macSelector(dirs) {
  const { derived, listed, reached } = macOnly(dirs)
  const names = [...new Set([...derived, ...reached, ...listed].map((entry) => entry.name))].sort()
  return names.length === 0 ? '' : `^(${names.join('|')})$`
}

/** `./dir/...` for every package carrying a darwin file (the mac lint's set). */
export function darwinPackages() {
  const out = []
  for (const dir of packageDirs()) {
    const hit = goFiles(dir).some((file) => {
      if (/_darwin(_test)?\.go$/.test(file.name)) return true
      const text = readFileSync(join(ROOT, file.path), 'utf8')
      return darwinOnlyFile(file.name, text)
    })
    if (hit) out.push(`./${dir}/...`)
  }
  return out.sort()
}

function main(argv) {
  const [command, ...rest] = argv
  const dirs = rest.map((dir) => relative(ROOT, join(ROOT, dir.replace(/^\.\//, ''))).replaceAll('\\', '/')).filter(Boolean)
  switch (command) {
    case 'check': {
      const failures = checkPartition(dirs)
      if (failures.length === 0) {
        const { derived, listed, reached } = macOnly(dirs)
        const total = derived.length + reached.length + listed.length
        console.log(
          `ok — ${total} macOS-only test(s) in ${macPackages(dirs).length} package(s) ` +
            `(${derived.length} in darwin test files, ${reached.length} reaching a darwin implementation, ${listed.length} listed); ` +
            'the Linux job runs every test in the set — only the darwin test files cannot compile there',
        )
        return 0
      }
      for (const failure of failures) console.error(`FAIL: ${failure}`)
      return 1
    }
    case 'pkgs':
      console.log(macPackages(dirs).join(' '))
      return 0
    case 'select':
      console.log(macSelector(dirs))
      return 0
    case 'darwin-pkgs':
      console.log(darwinPackages().join(' '))
      return 0
    default:
      console.error('usage: mac-only-tests.mjs check|pkgs|select <dir>... | darwin-pkgs')
      return 2
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  process.exitCode = main(process.argv.slice(2))
}
