// node --test .githooks/pre-commit.test.mjs
//
// THE FRONTEND LOCK. One hook's frontend section is the largest memory spike
// this machine sees from a commit: the typed eslint over frontend/ and then a
// full `tsc --noEmit`. Two worktrees committing at once used to run both at the
// same time — 2.1 GB and 2.0 GB RSS measured 2026-10-26 on this shared 12 GB
// box, 707 MB left. What that costs is already on record in the hook itself
// (nocx-y6d9j): a gate child OOM-killed and reported as `FAIL: eslint
// (frontend)`, which reads the same as a real failure, and workers then
// committed with --no-verify.
//
// So the hook takes a machine-wide lock around those steps. These tests drive
// TWO hook processes — not two calls in one process, because the defect is
// between processes and a single process can only ever prove one of them runs.
// The toolchain is stubbed: what is under test is the hook's ordering, not
// eslint, and running two real typed lints is the very load being removed.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, chmodSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { spawn, spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const HOOK = join(dirname(fileURLToPath(import.meta.url)), 'pre-commit')

// A stub for one node toolchain binary. Every step is appended to one shared
// log, so two hook processes can be ordered against each other by the log
// alone; O_APPEND appends of this size are atomic, which is what makes that
// ordering trustworthy. The frontend's typed eslint is the only step that
// sleeps or fails on request, because it is the only heavy one.
const STUB = `#!/bin/sh
set -u
who=\${NOCX_HOOK_TEST_WHO:-?}
log=\${NOCX_HOOK_TEST_LOG:?NOCX_HOOK_TEST_LOG}
prog=\${0##*/}
record() { printf '%s %s %s\\n' "$who" "$1" "\${2:-}" >>"$log"; }

run_step() {
    _step=$1
    record START "$_step"
    if [ "$_step" = frontend-eslint ]; then
        _sleep=\${NOCX_HOOK_TEST_ESLINT_SLEEP:-0}
        [ "$_sleep" = 0 ] || sleep "$_sleep"
        _fail=\${NOCX_HOOK_TEST_ESLINT_FAIL:-0}
        if [ "$_fail" != 0 ]; then
            printf 'fixture: 1 problem in the staged file\\n' >&2
            record END "$_step"
            exit "$_fail"
        fi
    fi
    record END "$_step"
}

case "$prog" in
    go)
        if [ "$*" = "env GOPATH" ]; then
            printf '%s\\n' "\${NOCX_HOOK_TEST_TOOLCHAIN:?NOCX_HOOK_TEST_TOOLCHAIN}"
        else
            run_step go
        fi ;;
    npx)
        case "\${1:-}" in
            eslint)
                record ARGV "$*"
                run_step frontend-eslint ;;
            prettier) run_step prettier ;;
            *) run_step "npx-\${1:-}" ;;
        esac ;;
    npm)
        case "\${2:-}" in
            lint)
                case "$(pwd)" in
                    */frontend) run_step frontend-eslint ;;
                    *) run_step npm-lint ;;
                esac ;;
            *) run_step "npm-\${2:-}" ;;
        esac ;;
    *) run_step "$prog" ;;
esac
exit 0
`

function git(cwd, ...args) {
  const r = spawnSync(
    'git',
    [
      '-c',
      'user.email=hook-test@example.invalid',
      '-c',
      'user.name=hook test',
      '-c',
      'commit.gpgsign=false',
      '-c',
      'core.hooksPath=/dev/null',
      ...args,
    ],
    { cwd, encoding: 'utf8' },
  )
  assert.equal(r.status, 0, `git ${args.join(' ')} failed: ${r.stderr}`)
  return r.stdout
}

function write(path, text) {
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, text)
}

// One stand: the stub toolchain, the shared log, and the lock directory the
// hooks under test use instead of the machine's own. The lock path is a seam
// (NOCX_GATE_LOCK_DIR) so a test run never queues behind, or blocks, a real
// commit happening in another worktree at the same time.
function stand(t) {
  const root = mkdtempSync(join(tmpdir(), 'nocx-pre-commit-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const bin = join(root, 'bin')
  const log = join(root, 'steps.log')
  const lock = join(root, 'frontend.lock')
  mkdirSync(bin, { recursive: true })
  for (const name of ['npx', 'npm', 'node', 'go']) {
    const path = join(bin, name)
    writeFileSync(path, STUB)
    chmodSync(path, 0o755)
  }
  writeFileSync(log, '')

  function repo(name, files) {
    const dir = join(root, name)
    mkdirSync(join(dir, 'node_modules'), { recursive: true })
    mkdirSync(join(dir, 'frontend', 'node_modules'), { recursive: true })
    git(dir, 'init', '-q')
    for (const [rel, text] of Object.entries(files)) write(join(dir, rel), text)
    git(dir, 'add', '.')
    git(dir, 'commit', '-q', '-m', 'the tree the hook runs against')
    return dir
  }

  function stage(dir, rel, text) {
    write(join(dir, rel), text)
    git(dir, 'add', rel)
  }

  function start(dir, who, extra = {}) {
    const child = spawn(HOOK, [], {
      cwd: dir,
      detached: true, // its own process group, so a test can interrupt the run
      stdio: ['ignore', 'pipe', 'pipe'],
      env: {
        ...process.env,
        PATH: `${bin}:${process.env.PATH}`,
        // The hook prepends GOPATH/bin; keep that lookup inside this stand,
        // rather than letting an empty stub answer put the host's /bin first.
        NOCX_HOOK_TEST_TOOLCHAIN: root,
        NOCX_HOOK_TEST_LOG: log,
        NOCX_HOOK_TEST_WHO: who,
        NOCX_GATE_LOCK_DIR: lock,
        ...extra,
      },
    })
    let out = ''
    let err = ''
    child.stdout.on('data', (d) => (out += d))
    child.stderr.on('data', (d) => (err += d))
    const done = new Promise((resolve) =>
      child.on('exit', (code, signal) => resolve({ code, signal, out, err })),
    )
    // A failed assertion must not leave a hook running: the runner would wait
    // on its pipes, and one red test would hide every test after it.
    t.after(() => {
      try {
        process.kill(-child.pid, 'SIGKILL')
      } catch {
        // already gone, which is the ordinary case
      }
    })
    return { child, done, output: () => out + err }
  }

  return {
    root,
    bin,
    log,
    lock,
    repo,
    stage,
    start,
    steps: () => readFileSync(log, 'utf8').trim().split('\n').filter(Boolean),
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// Wait for an observable state change — a line in the shared log — never for a
// duration. AGENTS.md: a test that needs a slow machine to pass is broken on a
// fast one too.
async function waitForLog(stand, line, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (stand.steps().includes(line)) return
    await sleep(20)
  }
  assert.fail(`never saw "${line}" in the hook's steps:\n${stand.steps().join('\n')}`)
}

const FE_SOURCE = 'export const answer = 42\n'
const FE_OTHER = 'export const other = 43\n'

test(
  'two hooks in two worktrees: the second waits, says whom for, and runs after the first',
  { timeout: 120_000 },
  async (t) => {
    const s = stand(t)
    const a = s.repo('worktree-a', { 'frontend/src/a.ts': FE_SOURCE })
    const b = s.repo('worktree-b', { 'frontend/src/a.ts': FE_SOURCE })
    s.stage(a, 'frontend/src/about.ts', 'export const about = 1\n')
    s.stage(b, 'frontend/src/about.ts', 'export const about = 2\n')

    const first = s.start(a, 'a', { NOCX_HOOK_TEST_ESLINT_SLEEP: '1.2' })
    await waitForLog(s, 'a START frontend-eslint')
    const second = s.start(b, 'b')
    const [ra, rb] = await Promise.all([first.done, second.done])

    assert.equal(ra.code, 0, `the first hook should pass:\n${first.output()}`)
    assert.equal(rb.code, 0, `the second hook should pass:\n${second.output()}`)
    assert.match(rb.err, /WAIT:/, `the waiting hook must say it is waiting:\n${rb.err}`)
    assert.ok(
      rb.err.includes(a),
      `the waiting hook must name the worktree it waits for (${a}):\n${rb.err}`,
    )

    const steps = s.steps()
    const at = (line) => {
      const i = steps.indexOf(line)
      assert.notEqual(i, -1, `missing "${line}" in:\n${steps.join('\n')}`)
      return i
    }
    assert.ok(
      at('b START frontend-eslint') > at('a END frontend-eslint'),
      `the second hook's typed eslint began before the first finished — two typed lints at once:\n${steps.join('\n')}`,
    )
  },
)

test(
  'a gate failure releases the lock: the next commit runs straight away',
  { timeout: 120_000 },
  async (t) => {
    const s = stand(t)
    const a = s.repo('worktree-a', { 'frontend/src/a.ts': FE_SOURCE })
    const b = s.repo('worktree-b', { 'frontend/src/a.ts': FE_SOURCE })
    s.stage(a, 'frontend/src/broken.ts', 'export const broken = 1\n')
    s.stage(b, 'frontend/src/about.ts', 'export const about = 2\n')

    const first = await s.start(a, 'a', { NOCX_HOOK_TEST_ESLINT_FAIL: '1' }).done
    assert.notEqual(first.code, 0, 'a lint failure must refuse the commit')
    assert.match(
      first.out,
      /FAIL: eslint \(frontend/,
      `the refusal must name the gate:\n${first.out}`,
    )

    const started = Date.now()
    const second = await s.start(b, 'b').done
    assert.equal(second.code, 0, `the next commit must not be blocked:\n${second.out}${second.err}`)
    assert.doesNotMatch(second.err, /WAIT:/, `the lock outlived the failure:\n${second.err}`)
    assert.ok(Date.now() - started < 20_000, 'the next commit waited for a lock nobody holds')
  },
)

test(
  'an interrupted hook releases the lock: the next commit is not stranded',
  { timeout: 120_000 },
  async (t) => {
    const s = stand(t)
    const a = s.repo('worktree-a', { 'frontend/src/a.ts': FE_SOURCE })
    const b = s.repo('worktree-b', { 'frontend/src/a.ts': FE_SOURCE })
    s.stage(a, 'frontend/src/slow.ts', 'export const slow = 1\n')
    s.stage(b, 'frontend/src/about.ts', 'export const about = 2\n')

    const first = s.start(a, 'a', { NOCX_HOOK_TEST_ESLINT_SLEEP: '30' })
    await waitForLog(s, 'a START frontend-eslint')
    process.kill(-first.child.pid, 'SIGINT') // the whole group: Ctrl-C at a terminal
    const interrupted = await first.done
    assert.ok(
      interrupted.signal || interrupted.code !== 0,
      'the interrupted hook should not report success',
    )

    const started = Date.now()
    const second = await s.start(b, 'b').done
    assert.equal(
      second.code,
      0,
      `the next commit must not be stranded:\n${second.out}${second.err}`,
    )
    assert.ok(
      Date.now() - started < 20_000,
      'the next commit waited for a lock the interrupt still holds',
    )
  },
)

test(
  'the typed eslint runs over the staged files, and a commit with none staged says so',
  { timeout: 120_000 },
  async (t) => {
    const s = stand(t)
    const a = s.repo('worktree-a', {
      'frontend/src/a.ts': FE_SOURCE,
      'frontend/src/b.ts': FE_OTHER,
      'frontend/src/styles/x.css': '.x { color: var(--color-background); }\n',
    })
    s.stage(a, 'frontend/src/a.ts', FE_SOURCE + '\n')
    s.stage(a, 'frontend/src/b.ts', FE_OTHER + '\n')

    const run = await s.start(a, 'a').done
    assert.equal(run.code, 0, `the hook should pass:\n${run.out}${run.err}`)
    const argv = s.steps().filter((l) => l.includes('ARGV'))
    assert.equal(argv.length, 1, `expected one eslint invocation, saw:\n${argv.join('\n')}`)
    assert.match(argv[0], /src\/a\.ts/)
    assert.match(argv[0], /src\/b\.ts/)
    assert.doesNotMatch(argv[0], /x\.css/, "a CSS file is not eslint's to lint")
    assert.doesNotMatch(argv[0], /\.\s*$/, 'the whole frontend was linted, not the staged files')

    // Nothing lintable staged: the typed eslint is not run, and the hook says so
    // rather than leaving a blank where a gate should be.
    const c = s.repo('worktree-c', {
      'frontend/src/styles/x.css': '.x { color: var(--color-text); }\n',
    })
    s.stage(
      c,
      'frontend/src/styles/x.css',
      '.x { color: var(--color-text); }\n.y { color: var(--color-background); }\n',
    )
    const cssOnly = await s.start(c, 'c').done
    assert.equal(cssOnly.code, 0, `the hook should pass:\n${cssOnly.out}${cssOnly.err}`)
    assert.doesNotMatch(s.steps().join('\n'), /c ARGV/, 'eslint was run with no file to lint')
    assert.match(cssOnly.out, /eslint \(frontend\) — nothing it owns is staged/)
  },
)

test(
  'a commit with no frontend change does not queue behind the frontend lock',
  { timeout: 120_000 },
  async (t) => {
    const s = stand(t)
    const a = s.repo('worktree-a', { 'frontend/src/a.ts': FE_SOURCE })
    const docs = s.repo('worktree-docs', { 'docs/notes.md': '# notes\n' })
    s.stage(a, 'frontend/src/about.ts', 'export const about = 1\n')
    s.stage(docs, 'docs/notes.md', '# notes\n\nmore\n')

    const heavy = s.start(a, 'a', { NOCX_HOOK_TEST_ESLINT_SLEEP: '3' })
    await waitForLog(s, 'a START frontend-eslint')
    const light = await s.start(docs, 'docs').done
    assert.equal(light.code, 0, `the light commit should pass:\n${light.out}${light.err}`)
    assert.doesNotMatch(
      light.err,
      /WAIT:/,
      `a tracker or docs commit must not wait for a lint:\n${light.err}`,
    )
    await heavy.done

    // Ordering, not elapsed time: the docs commit's root eslint ran while the
    // heavy hook's typed eslint was still going, so it did not queue.
    const steps = s.steps()
    const at = (line) => {
      const i = steps.indexOf(line)
      assert.notEqual(i, -1, `missing "${line}" in:\n${steps.join('\n')}`)
      return i
    }
    assert.ok(
      at('docs END npm-lint') < at('a END frontend-eslint'),
      `the light commit finished after the heavy one, so it waited:\n${steps.join('\n')}`,
    )
  },
)
