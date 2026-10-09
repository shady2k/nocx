// node --test .githooks/backlog-gate/commit-links.test.mjs
//
// What a push introduces, as CI sees it after the push: a scratch remote, a
// clone that pushed to it and fetched back, and commit-links.mjs run there.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
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

test('an unfetched remote branch is included without moving local refs', () => {
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
  const refs = git('for-each-ref', '--format=%(refname):%(objectname)')
  const result = links('--introduced', git('rev-parse', 'HEAD'), '--by', 'refs/heads/new')
  assert.equal(result.status, 1, result.stdout + result.stderr)
  assert.match(result.stdout, /1 commit\(s\) checked, 1 link problem/)
  assert.equal(spawnSync('git', ['cat-file', '-e', unseen], { cwd: work }).status, 0)
  assert.equal(git('for-each-ref', '--format=%(refname):%(objectname)'), refs)
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

test('an unfetched advertised old tip can bound a force-update', () => {
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
  assert.equal(spawnSync('git', ['cat-file', '-e', before], { cwd: work }).status, 0)
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
