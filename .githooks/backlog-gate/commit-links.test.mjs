// node --test .githooks/backlog-gate/commit-links.test.mjs
//
// What a push introduces, as CI sees it after the push: a scratch remote, a
// clone that pushed to it and fetched back, and commit-links.mjs run there.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join } from 'node:path'
import { parsePrePush } from './commit-links.mjs'
import { fileURLToPath } from 'node:url'

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), 'commit-links.mjs')
const TASK = {
  id: 'nocx-t1',
  title: 'a leaf',
  status: 'open',
  issue_type: 'task',
  priority: 1,
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}
// The flake a quarantine entry names, and the bookkeeping task that publishes
// the entry. Both must exist at the export revision the check reads.
const FLAKE = { ...TASK, id: 'nocx-t2', title: 'a flaky test' }
const BOOK = { ...TASK, id: 'nocx-t3', title: 'publish the quarantine' }
const QUARANTINE = 'docs/testing/quarantine.json'

let root, work
const git = (...args) => execFileSync('git', args, { cwd: work, encoding: 'utf8' }).trim()
const commit = (subject) => {
  writeFileSync(join(work, 'f'), `${subject}\n`)
  git('add', '-A')
  git('commit', '-q', '-m', subject)
  return git('rev-parse', 'HEAD')
}
const links = (...args) =>
  spawnSync(process.execPath, [SCRIPT, ...args], { cwd: work, encoding: 'utf8' })

before(() => {
  root = mkdtempSync(join(tmpdir(), 'commit-links-'))
  work = join(root, 'work')
  execFileSync('git', ['init', '-q', '--bare', '-b', 'main', join(root, 'remote.git')])
  execFileSync('git', ['clone', '-q', join(root, 'remote.git'), work])
  git('config', 'user.email', 'test@example.com')
  git('config', 'user.name', 'test')
  git('config', 'commit.gpgsign', 'false')
  mkdirSync(join(work, '.beads'))
  writeFileSync(join(work, '.beads/issues.jsonl'), `${JSON.stringify(TASK)}\n`)
  mkdirSync(join(work, 'docs/testing'), { recursive: true })
  writeFileSync(
    join(work, QUARANTINE),
    JSON.stringify({ schema: 1, tests: [{ test: 'go:TestEarlier', bead: TASK.id }] }),
  )
  commit('chore: the first commit (nocx-t1)')
  git('push', '-q', 'origin', 'HEAD:main')
})

after(() => rmSync(root, { recursive: true, force: true }))

test('a tag on a commit the remote already holds introduces nothing, and passes', () => {
  git('tag', 'v1')
  git('push', '-q', 'origin', 'v1')
  const run = links('--introduced', git('rev-parse', 'v1'), '--by', 'refs/tags/v1')
  assert.equal(run.status, 0, run.stderr)
  assert.match(run.stdout, /refs\/tags\/v1 introduces no commits: nothing to check/)
})

test('a new branch on a commit the remote already holds introduces nothing, and passes', () => {
  git('push', '-q', 'origin', 'HEAD:refs/heads/release/a')
  git('fetch', '-q', 'origin')
  const run = links('--introduced', git('rev-parse', 'HEAD'), '--by', 'refs/heads/release/a')
  assert.equal(run.status, 0, run.stderr)
  assert.match(run.stdout, /introduces no commits: nothing to check/)
})

test('a new branch carrying a linked commit is checked and passes', () => {
  git('checkout', '-q', '-b', 'release/b')
  const tip = commit('fix: a linked change (nocx-t1)')
  git('push', '-q', 'origin', 'release/b')
  git('fetch', '-q', 'origin')
  const run = links('--introduced', tip, '--by', 'refs/heads/release/b')
  assert.equal(run.status, 0, run.stdout + run.stderr)
  assert.match(run.stdout, /1 commit\(s\) checked, 0 link problem/)
})

test('a new commit with no task link is refused, past the branch\'s own "before"', () => {
  const old = git('rev-parse', 'HEAD')
  const tip = commit('fix: a change that names nothing')
  git('push', '-q', 'origin', 'release/b')
  git('fetch', '-q', 'origin')
  const run = links('--introduced', tip, '--by', 'refs/heads/release/b', '--before', old)
  assert.equal(run.status, 1, run.stdout + run.stderr)
  assert.match(run.stdout, /1 commit\(s\) checked, 1 link problem/)
})

test('a tag on a new unlinked commit is refused', () => {
  git('checkout', '-q', '--detach')
  const tip = commit('chore: an unlinked release commit')
  git('tag', 'v2')
  git('push', '-q', 'origin', 'v2')
  const run = links('--introduced', tip, '--by', 'refs/tags/v2')
  assert.equal(run.status, 1, run.stdout + run.stderr)
})

test('only refs actually advertised by the target remote suppress introduced commits', () => {
  git('checkout', '-q', '--detach')
  const tip = commit('fix: local tag does not make this remote-held')
  git('tag', 'only-local')
  const run = links('--introduced', tip, '--by', 'refs/heads/new', '--remote', 'origin')
  assert.equal(run.status, 1, run.stdout + run.stderr)
  assert.match(run.stdout, /1 commit\(s\) checked, 1 link problem/)
})

test('unfetchable advertised objects do not prevent checking new local work', () => {
  const other = join(root, 'other')
  execFileSync('git', ['clone', '-q', join(root, 'remote.git'), other])
  const otherGit = (...args) => execFileSync('git', args, { cwd: other, encoding: 'utf8' }).trim()
  otherGit('config', 'user.name', 'other')
  otherGit('config', 'user.email', 'other@example.invalid')
  otherGit('config', 'commit.gpgsign', 'false')
  otherGit('checkout', '-q', '-b', 'unseen')
  writeFileSync(join(other, 'unseen'), 'a different contributor\n')
  otherGit('add', 'unseen')
  otherGit('commit', '-q', '-m', 'fix: another contributor (nocx-t1)')
  const unseen = otherGit('rev-parse', 'HEAD')
  otherGit('push', '-q', 'origin', 'unseen')
  assert.notEqual(spawnSync('git', ['cat-file', '-e', unseen], { cwd: work }).status, 0)
  const bin = join(root, 'no-fetch')
  mkdirSync(bin)
  writeFileSync(
    join(bin, 'git'),
    '#!/bin/sh\nif [ "$1" = fetch ]; then\n  printf "fixture remote refuses downloads\\n" >&2\n  exit 73\nfi\nexec "$PROOF_GIT" "$@"\n',
    { mode: 0o755 },
  )
  const result = spawnSync(
    process.execPath,
    [SCRIPT, '--introduced', git('rev-parse', 'HEAD'), '--by', 'refs/heads/new'],
    {
      cwd: work,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${bin}${delimiter}${process.env.PATH}`,
        PROOF_GIT: execFileSync('sh', ['-c', 'command -v git'], { encoding: 'utf8' }).trim(),
      },
    },
  )
  assert.equal(result.status, 1, result.stdout + result.stderr)
  assert.match(result.stdout, /1 commit\(s\) checked, 1 link problem/)
})

test('a tip git cannot read, or a push with no ref, is misuse, never a pass', () => {
  assert.equal(links('--introduced', 'deadbeef', '--by', 'refs/tags/x').status, 2)
  assert.equal(links('--introduced', git('rev-parse', 'HEAD')).status, 2)
  assert.equal(links('--before', git('rev-parse', 'HEAD'), '--range', 'a..b').status, 2)
})

test('pre-push parser rejects empty input, blank lines, malformed lines and unreadable object ids', () => {
  assert.throws(() => parsePrePush(''), /empty/)
  assert.throws(() => parsePrePush('\n'), /empty/)
  assert.throws(() => parsePrePush(`${'a'.repeat(40)} refs/heads/main\n`), /malformed/)
  assert.throws(
    () => parsePrePush(`refs/heads/main nope refs/heads/main ${'0'.repeat(40)}\n`),
    /malformed/,
  )
})

test('pre-push parser returns all refs, including deletion lines and exact before sha', () => {
  const pushed = 'a'.repeat(40)
  const before = 'b'.repeat(40)
  const rows = parsePrePush(
    `HEAD ${pushed} refs/heads/main ${before}\n(delete) ${'0'.repeat(40)} refs/tags/v1 ${'c'.repeat(40)}\n`,
  )
  assert.deepEqual(rows, [
    { localRef: 'HEAD', localSha: pushed, remoteRef: 'refs/heads/main', remoteSha: before },
    {
      localRef: '(delete)',
      localSha: '0'.repeat(40),
      remoteRef: 'refs/tags/v1',
      remoteSha: 'c'.repeat(40),
    },
  ])
})

test('a newly written merge of old unlinked history still needs its own task', () => {
  git('checkout', '-q', '-b', 'historical', 'main')
  writeFileSync(join(work, 'old-history'), 'imported historical work\n')
  git('add', 'old-history')
  execFileSync('git', ['commit', '-q', '-m', 'old work without a task'], {
    cwd: work,
    env: {
      ...process.env,
      GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z',
      GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z',
    },
  })
  git('checkout', '-q', 'main')
  const base = git('rev-parse', 'HEAD')
  git('merge', '-q', '--no-ff', 'historical', '-m', 'Merge old history')
  const missing = links('--range', `${base}..HEAD`)
  assert.equal(missing.status, 1, missing.stdout + missing.stderr)
  git('commit', '-q', '--amend', '-m', 'Merge old history (nocx-t1)')
  const linked = links('--range', `${base}..HEAD`)
  assert.equal(linked.status, 0, linked.stdout + linked.stderr)
})

test('a real Git revert retains its original task link', () => {
  const changed = commit('fix: a reversible change (nocx-t1)')
  git('revert', '--no-edit', changed)
  const result = links('--range', `${changed}..HEAD`)
  assert.equal(result.status, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /1 commit\(s\) checked, 0 link problem/)
})

test('an unavailable advertised old tip does not block a valid force-update', () => {
  const other = join(root, 'other')
  const otherGit = (...args) => execFileSync('git', args, { cwd: other, encoding: 'utf8' }).trim()
  writeFileSync(join(other, 'unseen'), 'the remote branch moved again\n')
  otherGit('add', 'unseen')
  otherGit('commit', '-q', '-m', 'fix: updated remote branch (nocx-t1)')
  const before = otherGit('rev-parse', 'HEAD')
  otherGit('push', '-q', 'origin', 'unseen')
  assert.notEqual(spawnSync('git', ['cat-file', '-e', before], { cwd: work }).status, 0)
  const result = links(
    '--introduced',
    git('rev-parse', 'HEAD'),
    '--by',
    'refs/heads/unseen',
    '--before',
    before,
  )
  assert.equal(result.status, 0, result.stdout + result.stderr)
})

test('the first push to an empty remote checks every introduced commit', () => {
  const empty = join(root, 'empty.git')
  execFileSync('git', ['init', '-q', '--bare', empty])
  const result = links(
    '--introduced',
    git('rev-parse', 'HEAD'),
    '--by',
    'refs/heads/main',
    '--remote',
    empty,
  )
  assert.equal(result.status, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /[1-9]\d* commit\(s\) checked, 0 link problem/)
})

test('records-only classification uses the staged and committed JSONL changes, not the title', () => {
  const cases = [
    {
      name: 'comments only',
      issues: [{ ...TASK, comments: [{ text: 'accepted evidence' }] }],
      status: 1,
    },
    { name: 'new task', issues: [TASK, { ...TASK, id: 'nocx-t2' }], status: 0 },
    { name: 'real field update', issues: [{ ...TASK, status: 'closed' }], status: 0 },
    {
      name: 'comments with product work',
      issues: [{ ...TASK, comments: [{ text: 'accepted evidence' }] }],
      product: true,
      status: 0,
    },
    {
      name: 'timestamp only',
      issues: [{ ...TASK, updated_at: '2026-10-01T00:00:00Z' }],
      status: 0,
    },
    { name: 'deleted task', issues: [], status: 0 },
  ]
  for (const [index, example] of cases.entries()) {
    git('checkout', '-q', '-b', `records-proof-${index}`, 'main')
    const base = git('rev-parse', 'HEAD')
    writeFileSync(
      join(work, '.beads/issues.jsonl'),
      example.issues.map((issue) => JSON.stringify(issue)).join('\n') + '\n',
    )
    if (example.product) writeFileSync(join(work, 'product'), 'actual product change\n')
    git('add', '-A')
    const message = join(root, 'pending-message')
    writeFileSync(message, 'fix: ordinary linked work (nocx-t1)\n')
    // Resolve links from the base so removal of a task tests classification,
    // rather than independently failing because its task no longer exists.
    const staged = links('--message', message, '--export-at', base)
    assert.equal(staged.status, example.status, example.name + staged.stdout + staged.stderr)
    if (example.status) assert.match(staged.stdout, /carries only records/)
    git('commit', '-q', '-m', 'fix: ordinary linked work (nocx-t1)')
    const historical = links('--range', `${base}..HEAD`, '--export-at', base)
    assert.equal(
      historical.status,
      example.status,
      example.name + historical.stdout + historical.stderr,
    )
    if (example.status) assert.match(historical.stdout, /carries only records/)
  }
})

// Both tasks a quarantine test needs exist in the branch's base export: the
// tracker file is JSONL, and adding rows is a commit of its own so the range
// under test is exactly the quarantine change.
function quarantineBase(name) {
  git('checkout', '-q', '-b', name, 'main')
  const path = join(work, '.beads/issues.jsonl')
  const issues = readFileSync(path, 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line))
  writeFileSync(
    path,
    [...issues, FLAKE, BOOK].map((issue) => JSON.stringify(issue)).join('\n') + '\n',
  )
  git('add', '-A')
  git('commit', '-q', '-m', 'chore: file the quarantine tasks (nocx-t1)')
  return git('rev-parse', 'HEAD')
}

function addQuarantineEntry(test, bead) {
  const path = join(work, QUARANTINE)
  const entries = JSON.parse(readFileSync(path, 'utf8'))
  entries.tests.push({ test, bead })
  writeFileSync(path, JSON.stringify(entries))
}

test('a commit that only adds a quarantine entry names the bookkeeping task, not the flake', () => {
  const base = quarantineBase('quarantine-proof')
  addQuarantineEntry('go:TestFlake', FLAKE.id)
  git('add', '-A')

  const message = join(root, 'quarantine-message')
  writeFileSync(message, `chore: hold TestFlake (${FLAKE.id})\n`)
  const staged = links('--message', message, '--export-at', base)
  assert.equal(staged.status, 1, staged.stdout + staged.stderr)
  assert.match(staged.stdout, /bookkeeping/)
  assert.match(staged.stdout, new RegExp(FLAKE.id))

  git('commit', '-q', '-m', `chore: hold TestFlake (${FLAKE.id})`)
  const committed = links('--range', `${base}..HEAD`, '--export-at', base)
  assert.equal(committed.status, 1, committed.stdout + committed.stderr)
  assert.match(committed.stdout, /1 commit\(s\) checked, 1 link problem/)

  git(
    'commit',
    '-q',
    '--amend',
    '-m',
    `chore: publish the TestFlake quarantine (${BOOK.id})\n\nHolds ${FLAKE.id} until it is fixed.\n`,
  )
  const named = links('--range', `${base}..HEAD`, '--export-at', base)
  assert.equal(named.status, 0, named.stdout + named.stderr)
  assert.match(named.stdout, /1 commit\(s\) checked, 0 link problem/)
  git('checkout', '-q', 'main')
})

test('a commit that changes more than the quarantine may name the flake it fixes', () => {
  const base = quarantineBase('quarantine-with-fix')
  addQuarantineEntry('go:TestFlake', FLAKE.id)
  writeFileSync(join(work, 'fixed-product'), 'the flake is gone\n')
  git('add', '-A')
  git('commit', '-q', '-m', `fix: make TestFlake reliable (${FLAKE.id})`)
  const run = links('--range', `${base}..HEAD`, '--export-at', base)
  assert.equal(run.status, 0, run.stdout + run.stderr)
  git('checkout', '-q', 'main')
})

test('a commit that removes an entry is not "only adds", so it may name that bead', () => {
  const base = quarantineBase('quarantine-retire')
  const path = join(work, QUARANTINE)
  const entries = JSON.parse(readFileSync(path, 'utf8'))
  entries.tests = entries.tests.filter((entry) => entry.test !== 'go:TestEarlier')
  writeFileSync(path, JSON.stringify(entries))
  git('add', '-A')
  git('commit', '-q', '-m', `chore: retire the earlier quarantine (${TASK.id})`)
  const run = links('--range', `${base}..HEAD`, '--export-at', base)
  assert.equal(run.status, 0, run.stdout + run.stderr)
  git('checkout', '-q', 'main')
})

// A rerun of an already-merged pull request re-reads the original event, whose
// base the base branch has since passed. The base branch then contains the PR's
// head, so a range built from a merge-base with it is head..head -- nothing is
// checked and the check exits 2 on a PR that introduced commits. The event's own
// base sha still names the commits the PR introduced (nocx-1ebrs.66.1.32).
test('a rerun of a merged pull request checks the commits it introduced', () => {
  git('checkout', '-q', '-B', 'pr/merged', 'main')
  const base = git('rev-parse', 'HEAD')
  const tip = commit("fix: the pull request's own commit (nocx-t1)")
  git('checkout', '-q', '-B', 'merged', 'main')
  git('merge', '-q', '--no-ff', '-m', 'Merge pull request #1 (nocx-t1)', 'pr/merged')
  // The premise: the merged base branch now contains the head, so the merge
  // base the old range used is the head itself.
  assert.equal(git('merge-base', 'merged', tip), tip)
  const run = links('--pull-request', base, '--head', tip)
  assert.equal(run.status, 0, run.stdout + run.stderr)
  assert.match(run.stdout, /1 commit\(s\) checked, 0 link problem/)
  git('checkout', '-q', 'main')
})

test('a pull request range needs both the event base and the head', () => {
  const run = links('--pull-request', git('rev-parse', 'HEAD'))
  assert.equal(run.status, 2)
  assert.match(run.stderr, /--pull-request <base-sha> takes --head <head-sha>/)
})
