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
  const block = (from) =>
    workflow.slice(workflow.indexOf(from), workflow.indexOf('\n\n', workflow.indexOf(from)))

  // The resolution: both halves come from the Makefile's targets, which ask the
  // one owner.
  const mac = block('name: Resolve the macOS-only test selection')
  assert.ok(mac.length > 0, 'ci-mac no longer resolves a macOS-only test selection')
  assert.match(mac, /id: mac/, 'the Mac selection has no step id for the test step to read')
  assert.ok(
    mac.includes('make -s print-mac-pkgs'),
    'the Mac package set is not asked of the Makefile',
  )
  assert.ok(
    mac.includes('make -s print-mac-select'),
    'the Mac test selection is not asked of the Makefile',
  )

  // THE SELECTION IS DERIVED IN THE JOB (review of PR #776, round 4). It was
  // committed and compared, and the comparison failed on the real Mac because the
  // analysis is the HOST build's: nothing off a Mac can stand in for a Mac's
  // answer. So the Mac derives what it runs, at the start of the job, into build/
  // — build output, never a committed copy.
  assert.ok(
    workflow.includes('- name: Derive the macOS-only test selection'),
    'ci-mac no longer derives its macOS-only selection at the start of the job',
  )
  assert.match(
    block('name: Derive the macOS-only test selection'),
    /run: make mac-selection/,
    'the derivation step does not run the target that writes the selection',
  )
  assert.ok(
    !existsSync(join(ROOT, 'scripts/mac-only-tests.txt')),
    'a committed reachability selection is back: the Mac derives what it runs',
  )
  assert.ok(
    !existsSync(join(ROOT, 'scripts/mac-only-tests-tagged.txt')),
    'a committed tagged reachability selection is back',
  )
  const gitignore = readFileSync(join(ROOT, '.gitignore'), 'utf8')
  assert.match(
    gitignore,
    /^\/build\/\*/m,
    'build/ is not ignored, so the derived selection could be committed',
  )

  // GENERATE BEFORE YOU CHECK (review of PR #776, round 5). Both files are
  // ignored build output, so a `make ci-mac` that checked first exited "does not
  // exist" on a fresh checkout before a single test. The job and the local target
  // must run the same order, or one of them is the untested path.
  const makefile = readFileSync(join(ROOT, 'Makefile'), 'utf8')
  const recipe = (name) => {
    const at = makefile.indexOf(`\n${name}:\n`)
    assert.ok(at > -1, `no ${name} target`)
    return makefile.slice(at, makefile.indexOf('\n\n', at))
  }
  const ciMac = recipe('ci-mac')
  const selectAt = ciMac.indexOf('$(MAC_SELECTION_CMD)')
  const checkAt = ciMac.indexOf('$(MAC_CHECK_CMD)')
  assert.ok(selectAt > -1, 'make ci-mac no longer derives the selection')
  assert.ok(checkAt > selectAt, 'make ci-mac checks the selection before deriving it')
  assert.ok(
    ciMac.indexOf('test -race') > checkAt,
    'make ci-mac runs its tests before the selection it derived is verified',
  )
  assert.ok(
    !ciMac.includes('$(MAKE)'),
    'make ci-mac recurses into make, which runs under -n and is not a dry run',
  )
  const deriveStep = workflow.indexOf('run: make mac-selection')
  const currentStep = workflow.indexOf('run: make mac-check')
  assert.ok(
    deriveStep > -1 && currentStep > deriveStep,
    'ci-mac derives and checks out of the order the local target uses',
  )
  assert.ok(
    currentStep < workflow.indexOf('name: Test (the macOS-only tests'),
    'ci-mac verifies the selection after it starts testing',
  )

  // The Mac runs it with -run, over the packages that hold at least one.
  const macTests = workflow.slice(workflow.indexOf('name: Test (the macOS-only tests'))
  const macTestStep = macTests.slice(0, macTests.indexOf('\n\n'))
  assert.ok(
    macTestStep.includes('-run "$RUN" $PKGS'),
    'the mac test step does not run the derived selection',
  )
  assert.ok(
    macTestStep.includes('steps.mac.outputs.pkgs'),
    'the mac test step does not take the derived package set',
  )
  assert.ok(
    macTestStep.includes('steps.mac.outputs.run'),
    'the mac test step does not take the derived selection',
  )

  // THE TAGGED HALF IS DERIVED TOO (review of PR #776). A nocx_local_ssh-tagged
  // test can reach a darwin implementation just as an untagged one can, and the
  // tagged pass on the Mac is where such a test runs — so the Mac runs the
  // TAGGED selection with the tag, which can only come from its own committed
  // file (the analysis differs: the tag selects which files exist).
  const taggedStep = workflow.slice(
    workflow.indexOf('name: Test (the macOS-only tests, nocx_local_ssh)'),
  )
  assert.ok(taggedStep.length > 0, 'the Mac no longer runs the tagged half of the selection')
  const taggedBlock = taggedStep.slice(0, taggedStep.indexOf('\n\n'))
  assert.ok(
    taggedBlock.includes('-tags gtk3,nocx_local_ssh,nocx_framecheck'),
    'the tagged Mac pass does not pass the tag',
  )
  assert.ok(
    taggedBlock.includes('-run "$RUN" $PKGS'),
    'the tagged Mac pass does not run only its own selection',
  )
  assert.ok(
    taggedBlock.includes('steps.mac.outputs.tagged_pkgs'),
    'the tagged Mac pass does not take its package set from the Makefile',
  )
  assert.ok(
    taggedBlock.includes('steps.mac.outputs.tagged_run'),
    'the tagged Mac pass does not take its selection from the Makefile',
  )
  const macResolve = block('name: Resolve the macOS-only test selection')
  assert.ok(
    macResolve.includes('make -s print-mac-tagged-pkgs'),
    'the mac selection step asks for no tagged package set',
  )
  assert.ok(
    macResolve.includes('make -s print-mac-tagged-select'),
    'the mac selection step asks for no tagged selection',
  )

  // LINUX RUNS EVERYTHING (owner's rule, 2026-11-09): the selection limits what
  // the MAC runs. A test that reaches a platform seam exercises the linux
  // implementation there, and Linux ships too, so the Linux job passes no
  // selection at all — a `-skip` here, or a skip list resolved for it, would be
  // coverage lost on the side that runs the other half of the same code.
  const linux = workflow.slice(workflow.indexOf('\n  ci-linux:'))
  assert.ok(
    !linux.includes('print-mac-select'),
    'ci-linux resolves a mac selection it has no business acting on',
  )
  assert.ok(!/-skip "\$MAC_SKIP"/.test(linux), 'a Linux OS run excludes the macOS-only tests')
  assert.ok(!/MAC_SKIP/.test(linux), 'ci-linux carries a mac skip list')
  assert.ok(!/--skip "\$MAC_SKIP"/.test(linux), 'the Linux repeat excludes the macOS-only tests')
  const ciLinuxScript = readFileSync(join(ROOT, 'scripts/ci-linux.sh'), 'utf8')
  assert.ok(!/MAC_SKIP|-skip/.test(ciLinuxScript), 'scripts/ci-linux.sh can exclude tests')

  // The Mac's repeat follows the Mac's rule.
  assert.ok(
    workflow.includes('--only "$RUN"'),
    'the mac repeat does not run only the macOS-only tests',
  )

  // The Mac lints what Linux cannot see, and nothing else.
  assert.ok(
    !workflow.includes('golangci-lint run ./...'),
    'a whole-tree lint is back on a job; that pass belongs to ci-linux',
  )
  assert.ok(
    workflow.includes('golangci-lint run $(make -s print-darwin-pkgs)'),
    'the mac lint no longer scopes to the darwin packages',
  )
  assert.ok(
    linux.includes('golangci-lint run --build-tags=gtk3 ./...'),
    'ci-linux does not run the whole-tree lint the Mac gave up',
  )

  // No macOS-only test NAME is USED in the workflow — no `-run` list, no
  // `--only`. Prose is exempt and has to be: ci-linux's own comment explains
  // internal/pty with one of these names, and a rule that forbade writing a test
  // name in a sentence would be a rule about comments rather than about
  // selectors.
  const code = workflow
    .split('\n')
    .filter((line) => !/^\s*#/.test(line))
    .join('\n')
  const select = execFileSync('make', ['-s', 'print-mac-select'], {
    cwd: ROOT,
    encoding: 'utf8',
  }).trim()
  for (const name of select.replace(/^\^\(|\)\$$/g, '').split('|')) {
    assert.ok(!code.includes(name), `${name} is written in ci.yml; the selection has one owner`)
  }
})
