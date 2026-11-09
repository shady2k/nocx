// node --test scripts/workflow-wiring.test.mjs
//
// A workflow names files that must exist in the tree it runs from, and GitHub
// resolves none of them: a step that imports a moved module fails at run time,
// on its schedule, long after the rename was reviewed. Measured 2026-11-01
// (nocx-fg79x.6): the tracker mechanism's recipe moved from
// .githooks/backlog-gate/br-tracker.mjs to .githooks/backlog-gate/trackers/br.mjs
// and every reference moved with it except the one in
// .github/workflows/tracker-gate.yml. The scheduled gate then stopped at its
// first step with ERR_MODULE_NOT_FOUND, and the two steps after it -- the gate
// itself and the push that records the checked head -- had never run in CI
// (nocx-pknm1).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const WORKFLOWS = join(ROOT, '.github/workflows')

// A path-shaped module reference: it carries a directory component, so a bare
// `check.mjs` named beside a `$gate/` variable is not one, and neither is a
// `$gate/commit-links.mjs` -- that is a shell expansion and the variable is
// resolved at run time. The leading lookbehind drops it and any word-adjacent
// suffix of it.
const MODULE = /(?<![$\w])[\w.-]+(?:\/[\w.-]+)+\.mjs\b/g

const referenced = (text) => [...text.matchAll(MODULE)].map((match) => match[0])

test('every module a workflow names exists in the tree', () => {
  const missing = []
  for (const name of readdirSync(WORKFLOWS).filter((file) => file.endsWith('.yml'))) {
    const text = readFileSync(join(WORKFLOWS, name), 'utf8')
    for (const reference of referenced(text)) {
      const path = reference.replace(/^\.\//, '')
      if (!existsSync(join(ROOT, path))) missing.push(`${name}: ${reference}`)
    }
  }
  assert.deepEqual(missing, [], `workflows name modules that do not exist:\n${missing.join('\n')}`)
})

// THE REF NAME HAS ONE OWNER, and it is the recipe the mechanism reads
// (tracker-sync.mjs refuses a recipe whose ref is a branch or a tag). The
// workflow derives the ref by importing that recipe instead of carrying a
// second copy of the name, so this test asks the module the workflow names.
test('the tracker gate takes the store ref from the recipe, not a copy of the name', async () => {
  const workflow = readFileSync(join(WORKFLOWS, 'tracker-gate.yml'), 'utf8')
  const imported = workflow.match(/import\(\s*['"]([^'"]+)['"]\s*\)/)
  assert.ok(imported, 'the workflow imports no module; the ref has no owner left')
  const recipe = await import(pathToFileURL(join(ROOT, imported[1].replace(/^\.\//, ''))).href)
  assert.match(recipe.default?.ref ?? '', /^refs\//)
  assert.doesNotMatch(recipe.default.ref, /^refs\/(heads|tags)\//)
  // A literal here would be a second owner, and the two would agree until one
  // of them moved.
  assert.doesNotMatch(workflow, /refs\/tracker\/state/)
})

// THE NATIVE FISH ARCHIVE'S CACHE IDENTITY HAS ONE OWNER (nocx-ouru5.1.12).
// scripts/install-fish-ci.sh prints it (--identity) and reads it back when it
// installs; the workflow may only pass it through. The three jobs that install
// native Fish each failed at that step whenever the release host answered 504,
// so a warm cache is what keeps the Linux jobs landing — and a hardcoded key or
// path here would be a second owner that agrees only until one of them moves.
test('the native Fish archive is cached under the identity its installer owns', () => {
  const workflow = readFileSync(join(WORKFLOWS, 'ci.yml'), 'utf8')

  const printed = execFileSync('sh', [join(ROOT, 'scripts/install-fish-ci.sh'), '--identity'], {
    cwd: ROOT,
    encoding: 'utf8',
  })
  const fields = Object.fromEntries(
    printed
      .trim()
      .split('\n')
      .map((line) => {
        const at = line.indexOf('=')
        assert.ok(at > 0, `--identity printed a line that is not name=value: ${line}`)
        return [line.slice(0, at), line.slice(at + 1)]
      }),
  )
  assert.deepEqual(
    Object.keys(fields).sort(),
    ['key', 'path'],
    '--identity must print $GITHUB_OUTPUT fields',
  )
  assert.match(
    fields.key,
    /^nocx-fish-\d+\.\d+\.\d+-[a-z0-9_-]+$/,
    `key names no pinned version and platform: ${fields.key}`,
  )
  assert.match(
    fields.path,
    /\/nocx-fish-archive$/,
    `path is not the archive's own directory: ${fields.path}`,
  )

  const installs = [...workflow.matchAll(/^(\s*)- name: Install the pinned native Fish$/gm)]
  assert.equal(installs.length, 3, 'the three jobs this repository installs native Fish on')

  for (const install of installs) {
    const before = workflow.slice(0, install.index)
    const restore = before.lastIndexOf('- name: Cache the pinned native Fish archive')
    const identity = before.lastIndexOf("- name: The pinned native Fish archive's cache identity")
    assert.ok(identity > -1, 'an install step is not preceded by the identity step')
    assert.ok(restore > identity, 'the cache restore does not follow the identity step')
    const steps = workflow.slice(before.lastIndexOf('\n', identity), install.index)
    assert.match(steps, /id: fish-cache/, 'the identity step has no id for the restore to read')
    assert.match(
      steps,
      /run: scripts\/install-fish-ci\.sh --identity/,
      'the identity step is not the installer itself',
    )
    assert.match(
      steps,
      /uses: actions\/cache@v\d+/,
      'no cache restore stands between the identity and the install',
    )
    assert.match(
      steps,
      /key: \$\{\{ steps\.fish-cache\.outputs\.key \}\}/,
      'the restore keys on something other than the installer key',
    )
    assert.match(
      steps,
      /path: \$\{\{ steps\.fish-cache\.outputs\.path \}\}/,
      'the restore caches a directory other than the installer one',
    )
    const installStep = workflow.slice(install.index, workflow.indexOf('\n\n', install.index))
    assert.match(
      installStep,
      /scripts\/install-fish-ci\.sh "\$RUNNER_TEMP\/fish-ci"/,
      'the install step no longer runs the installer as it did',
    )
  }

  // NO SECOND COPY of either half of the identity.
  assert.doesNotMatch(
    workflow,
    /nocx-fish-\d/,
    'a cache key is spelled in the workflow; the installer owns it',
  )
  assert.doesNotMatch(
    workflow,
    /nocx-fish-archive/,
    'a cache path is spelled in the workflow; the installer owns it',
  )
})

// THE macOS JOB HOLDS ONLY WHAT NEEDS A MAC (nocx-ouru5.1.13). The selection is
// derived from the build constraints by scripts/mac-only-tests.mjs and asked for
// over `make -s`, so the workflow may not carry a second copy of it: a test name
// written here, or a package set spelled beside the derivation, would drift the
// day a darwin file is added — and the drift would show up as a test that ran on
// neither side.
test('ci-mac runs the derived macOS-only selection, and ci-linux excludes it', () => {
  const workflow = readFileSync(join(WORKFLOWS, 'ci.yml'), 'utf8')
  const block = (from) => workflow.slice(workflow.indexOf(from), workflow.indexOf('\n\n', workflow.indexOf(from)))

  // The resolution: both halves come from the Makefile's targets, which ask the
  // one owner.
  const mac = block('name: Resolve the macOS-only test selection')
  assert.ok(mac.length > 0, 'ci-mac no longer resolves a macOS-only test selection')
  assert.match(mac, /id: mac/, 'the Mac selection has no step id for the test step to read')
  assert.ok(mac.includes('make -s print-mac-pkgs'), 'the Mac package set is not asked of the Makefile')
  assert.ok(mac.includes('make -s print-mac-select'), 'the Mac test selection is not asked of the Makefile')

  // The Mac runs it with -run, over the packages that hold at least one.
  const macTests = workflow.slice(workflow.indexOf('name: Test (the macOS-only tests'))
  const macTestStep = macTests.slice(0, macTests.indexOf('\n\n'))
  assert.ok(macTestStep.includes('-run "$RUN" $PKGS'), 'the mac test step does not run the derived selection')
  assert.ok(macTestStep.includes('steps.mac.outputs.pkgs'), 'the mac test step does not take the derived package set')
  assert.ok(macTestStep.includes('steps.mac.outputs.run'), 'the mac test step does not take the derived selection')

  // Linux excludes exactly the same selection, at the point of use.
  const linux = workflow.slice(workflow.indexOf('\n  ci-linux:'))
  const linuxResolve = linux.slice(
    linux.indexOf('name: Resolve the macOS-only test selection'),
    linux.indexOf('\n\n', linux.indexOf('name: Resolve the macOS-only test selection')),
  )
  assert.ok(linuxResolve.includes('make -s print-mac-select'), 'ci-linux does not ask the Makefile for the mac selection')
  assert.ok(linuxResolve.includes('skip='), 'ci-linux resolves no skip list')
  const linuxSkips = [...linux.matchAll(/-skip "\$MAC_SKIP"/g)].length
  assert.ok(linuxSkips >= 2, `the Linux OS runs do not exclude the mac-only tests (${linuxSkips} of them do)`)

  // The repeat follows the same rule on each side.
  assert.ok(workflow.includes('--only "$RUN"'), 'the mac repeat does not run only the macOS-only tests')
  assert.ok(linux.includes('--skip "$MAC_SKIP"'), 'the Linux repeat does not exclude the macOS-only tests')

  // The Mac lints what Linux cannot see, and nothing else.
  assert.ok(!workflow.includes('golangci-lint run ./...'), 'a whole-tree lint is back on a job; that pass belongs to ci-linux')
  assert.ok(workflow.includes('golangci-lint run $(make -s print-darwin-pkgs)'), 'the mac lint no longer scopes to the darwin packages')
  assert.ok(linux.includes('golangci-lint run --build-tags=gtk3 ./...'), 'ci-linux does not run the whole-tree lint the Mac gave up')

  // No macOS-only test NAME is spelled in the workflow.
  const select = execFileSync('make', ['-s', 'print-mac-select'], { cwd: ROOT, encoding: 'utf8' }).trim()
  for (const name of select.replace(/^\^\(|\)\$$/g, '').split('|')) {
    assert.ok(!workflow.includes(name), `${name} is written in ci.yml; the selection has one owner`)
  }
})
