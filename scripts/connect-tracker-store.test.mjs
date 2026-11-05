// node --test scripts/connect-tracker-store.test.mjs
import test from 'node:test'
import assert from 'node:assert/strict'
import {
  chmodSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'

// A clone whose remote refuses its pushes is an ordinary clone: `make connect`
// has to leave it with working hooks and a readable tracker, and say plainly
// that it cannot publish (nocx-3n1ly). And a tracker ref the remote carries but
// this clone never received must stop connect, never become an empty store
// presented as the real one — the mechanism creates that empty store when its
// own fetch misses, and it is a set-owned file, so the guard lives here.
//
// The mechanism and br are stood in for, because what is under test is this
// script's own ordering and its ref guard. The ref is real git in a real bare
// remote, the state is a real commit, and the case that matters is real too: the
// ref is advertised while its objects cannot be transferred.

const REPO = new URL('..', import.meta.url).pathname
const ISSUES = '{"id":"nocx-aaa"}\n{"id":"nocx-bbb"}\n{"id":"nocx-ccc"}\n'

const git = (cwd, ...args) =>
  execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })

function shim(bin, name, lines) {
  writeFileSync(join(bin, name), `#!/bin/sh\n${lines.join('\n')}\n`)
  chmodSync(join(bin, name), 0o755)
}

/**
 * A bare remote and a clone of it, the tracker ref carrying ISSUES.
 * `breakObjects` deletes the blob that state names, so the ref is advertised
 * and the transfer fails — the shape a fetch that "did not land" has.
 */
function fixture(t, { breakObjects = false, trackerRef = true } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'nocx-connect-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const origin = join(dir, 'origin.git')
  git(dir, 'init', '--bare', '-q', '--initial-branch=main', origin)
  const source = join(dir, 'source')
  mkdirSync(source)
  git(source, 'init', '-q', '--initial-branch=main')
  git(source, 'config', 'user.email', 'fixture@example.invalid')
  git(source, 'config', 'user.name', 'fixture')
  mkdirSync(join(source, '.beads'))
  writeFileSync(join(source, '.beads/issues.jsonl'), ISSUES)
  git(source, 'add', '.')
  git(source, 'commit', '-q', '-m', 'tracker store: the fixture state')
  git(source, 'remote', 'add', 'origin', origin)
  git(source, 'push', '-q', 'origin', 'HEAD:refs/heads/main')
  if (trackerRef) git(source, 'push', '-q', 'origin', 'HEAD:refs/tracker/state')
  if (breakObjects) {
    // A store commit the clone does not have, whose blob the remote cannot
    // transfer: the ref is advertised, the fetch fails.
    writeFileSync(join(source, '.beads/issues.jsonl'), `${ISSUES}{"id":"nocx-ddd"}\n`)
    git(source, 'commit', '-q', '-am', 'tracker store: a state the clone has not seen')
    git(source, 'push', '-q', 'origin', 'HEAD:refs/tracker/state')
  }

  const checkout = join(dir, 'checkout')
  // --no-local: a network clone transfers only what the refs reach, so a store
  // commit the branch does not name is genuinely absent here (nocx-3n1ly).
  git(dir, 'clone', '--no-local', '-q', origin, checkout)
  if (breakObjects) {
    const blob = git(source, 'rev-parse', 'HEAD:.beads/issues.jsonl').trim()
    rmSync(join(origin, 'objects', blob.slice(0, 2), blob.slice(2)), { force: true })
  }
  git(checkout, 'config', 'user.email', 'fixture@example.invalid')
  git(checkout, 'config', 'user.name', 'fixture')
  // The script installs the mechanism and links the project's hooks out of the
  // checkout, so the checkout carries this tree's hooks and the script itself.
  cpSync(join(REPO, '.githooks'), join(checkout, '.githooks'), { recursive: true })
  mkdirSync(join(checkout, 'scripts'))
  cpSync(
    join(REPO, 'scripts/connect-tracker-store.sh'),
    join(checkout, 'scripts/connect-tracker-store.sh'),
  )

  const state = join(dir, 'state')
  const bin = join(dir, 'bin')
  mkdirSync(state)
  mkdirSync(bin)
  const marker = join(dir, 'mechanism-calls')
  const store = join(state, 'nocx', 'tracker')
  shim(bin, 'node', [
    'case "$1" in',
    '  -e) exec "$REAL_NODE" "$@" ;;',
    'esac',
    'printf "%s\\n" "$2" >> "$MARKER"',
    'case "$2" in',
    '  connect)',
    '    [ "${FAKE_EMPTY_STORE:-0}" = 1 ] || { mkdir -p "$STORE_DIR/.beads"; printf "{\\"id\\":\\"nocx-aaa\\"}\\n{\\"id\\":\\"nocx-bbb\\"}\\n{\\"id\\":\\"nocx-ccc\\"}\\n" > "$STORE_DIR/.beads/issues.jsonl"; }',
    '    [ -z "$FAKE_CONNECT_TEXT" ] || printf "%s\\n" "$FAKE_CONNECT_TEXT"',
    '    exit "${FAKE_CONNECT_EXIT:-0}" ;;',
    '  pointer-install) printf "pointer-install %s\\n" "$PWD" >> "$MARKER" ;;',
    `  status) printf '%s' '{"store":"${store}"}' ;;`,
    '  publish)',
    '    [ -z "$FAKE_PUBLISH_TEXT" ] || printf "%s\\n" "$FAKE_PUBLISH_TEXT" >&2',
    '    exit "${FAKE_PUBLISH_EXIT:-0}" ;;',
    '  *) exit 0 ;;',
    'esac',
  ])
  // The tracker's own command: this script asks where the checkout's export is
  // and whether it is in step, and nothing else.
  shim(bin, 'br', [
    'case "$1" in',
    '  where) printf \'%s\' "{\\"jsonl_path\\":\\"$EXPORT\\",\\"path\\":\\"$STORE_SPEC\\"}" ;;',
    '  count) printf \'{"count":%s}\' "${FAKE_COUNT:-3}" ;;',
    '  sync) echo "tracker: in step" ;;',
    'esac',
  ])

  const run = (env = {}) => {
    rmSync(marker, { force: true })
    const result = spawnSync('bash', [join(checkout, 'scripts/connect-tracker-store.sh')], {
      cwd: checkout,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${bin}:${process.env.PATH}`,
        XDG_STATE_HOME: state,
        HOME: dir,
        REAL_NODE: process.execPath,
        MARKER: marker,
        STORE_DIR: store,
        EXPORT: join(store, '.beads', 'issues.jsonl'),
        STORE_SPEC: join(store, '.beads'),
        NOCX_TRACKER_MACHINE: 'fixture',
        ...env,
      },
    })
    return { ...result, said: `${result.stdout}${result.stderr}`, marker }
  }
  // The script keys the hooks folder by the checkout path, with git's own hash.
  const checkoutKey = execFileSync('git', ['hash-object', '--stdin'], {
    cwd: checkout,
    input: checkout,
    encoding: 'utf8',
  }).trim()
  const hooks = join(state, basename(checkout), 'hooks', checkoutKey)
  return { dir, origin, checkout, state, store, hooks, run }
}

const HOOKS = ['pre-commit', 'commit-msg', 'pre-merge-commit', 'pre-push']

test('a refused push leaves the hooks linked and says the clone cannot publish', (t) => {
  const f = fixture(t)
  const result = f.run({
    FAKE_PUBLISH_EXIT: '1',
    FAKE_PUBLISH_TEXT: 'remote: No anonymous write access.\nfatal: Authentication failed',
  })
  assert.equal(result.status, 0, result.said)
  assert.match(result.said, /cannot publish tracker state/)
  assert.match(result.said, /No anonymous write access/)
  for (const hook of HOOKS) assert.ok(existsSync(join(f.hooks, hook)), `${hook} is not linked`)
})

test('a tracker ref that did not arrive stops connect before any store exists', (t) => {
  const f = fixture(t, { breakObjects: true })
  const result = f.run()
  assert.notEqual(result.status, 0, result.said)
  assert.match(result.said, /refs\/tracker\/state/)
  // The mechanism would answer a fetch that missed by creating an empty store,
  // so it must not be asked at all.
  assert.equal(existsSync(result.marker), false, 'the mechanism ran and could make an empty store')
  assert.equal(existsSync(f.store), false, 'an empty store was created')
})

test('a remote with no tracker ref is a first connect, not a failure', (t) => {
  const f = fixture(t, { trackerRef: false })
  const result = f.run()
  assert.equal(result.status, 0, result.said)
  assert.match(result.said, /no refs\/tracker\/state yet/)
  for (const hook of HOOKS) assert.ok(existsSync(join(f.hooks, hook)), `${hook} is not linked`)
})

test('a store that holds no items although the ref carries them is refused by name', (t) => {
  const f = fixture(t)
  const result = f.run({ FAKE_EMPTY_STORE: '1', FAKE_COUNT: '0' })
  assert.notEqual(result.status, 0, result.said)
  assert.match(result.said, /3 issues/)
  assert.match(result.said, /refs\/tracker\/state/)
})

// The measured shape on a clone with no write access: the mechanism's own
// connect stops at the push it cannot make, before it points the checkout at
// the store. The project script has to finish that, not inherit the failure.
test('a connect that stopped at its push still leaves a connected clone', (t) => {
  const f = fixture(t)
  const result = f.run({
    FAKE_CONNECT_EXIT: '1',
    FAKE_CONNECT_TEXT: 'tracker-sync: the push failed: remote: No anonymous write access.',
    FAKE_PUBLISH_EXIT: '1',
    FAKE_PUBLISH_TEXT: 'remote: No anonymous write access.\nfatal: Authentication failed',
  })
  assert.equal(result.status, 0, result.said)
  assert.match(result.said, /connect stopped at its push/)
  assert.match(result.said, /cannot publish tracker state/)
  // The pointer was installed from the store, which is where the recipe keeps it.
  assert.match(readFileSync(result.marker, 'utf8'), new RegExp(`pointer-install ${f.store}`))
  // The record hooks are the mechanism's and follow its push, so the clone is
  // told that its revisions are not recorded yet rather than finding out at a
  // push that is refused for a missing recording.
  assert.match(result.said, /could not install its record hooks/)
  for (const hook of HOOKS) assert.ok(existsSync(join(f.hooks, hook)), `${hook} is not linked`)
})

test('a publish that failed for any other reason is still a failed connect', (t) => {
  const f = fixture(t)
  const result = f.run({
    FAKE_PUBLISH_EXIT: '4',
    FAKE_PUBLISH_TEXT: 'tracker-sync: the remote is unreachable',
  })
  assert.notEqual(result.status, 0, result.said)
  assert.match(result.said, /unreachable/)
})
