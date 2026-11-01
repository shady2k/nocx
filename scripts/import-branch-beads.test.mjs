/**
 * Tests for scripts/import-branch-beads.mjs (nocx-fg79x.1).
 *
 * A scratch branch export and a scratch store export, built here: comment-id
 * collisions, a shared issue whose branch row edits fields, a store-only issue
 * (a branch deletion is indistinguishable from the store being newer), a
 * branch-only issue with dependencies and labels, tombstones, and duplicate
 * issue ids in either file. The import into the store is br's own and its
 * semantics are not re-implemented here; what is pinned is the plan's
 * classification, remapping and order.
 */
import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { planImport } from './import-branch-beads.mjs'

const SCRIPT = new URL('./import-branch-beads.mjs', import.meta.url).pathname

function issue(id, fields = {}) {
  return {
    id,
    title: `title ${id}`,
    status: 'open',
    priority: 2,
    issue_type: 'task',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    labels: [],
    dependencies: [],
    ...fields,
  }
}

function comment(id, issueId, text, author = 'alice', created = '2026-09-02T00:00:00Z') {
  return { id, issue_id: issueId, author, text, created_at: created }
}

const STORE = [
  issue('nocx-a', {
    title: 'A store',
    status: 'open',
    labels: ['infra'],
    updated_at: '2026-09-10T00:00:00Z',
    comments: [comment(1, 'nocx-a', 'a old 1'), comment(2, 'nocx-a', 'a old 2')],
  }),
  issue('nocx-b', { title: 'B store only' }),
  issue('nocx-c', { comments: [comment(3, 'nocx-c', 'c old')] }),
  issue('nocx-t', { title: 'T tombstoned', status: 'tombstone', close_reason: 'duplicate' }),
]

const BRANCH = [
  issue('nocx-a', {
    title: 'A branch edit',
    status: 'closed',
    labels: ['ui'],
    dependencies: [{ issue_id: 'nocx-a', depends_on_id: 'nocx-b', type: 'blocks' }],
    updated_at: '2026-09-20T00:00:00Z',
    comments: [
      // The same comment as the store's, spelled +00:00 rather than Z: one
      // instant, so it is not a new comment.
      comment(1, 'nocx-a', 'a old 1', 'alice', '2026-09-02T00:00:00+00:00'),
      // Same issue and id, different text: a branch edit of a store comment,
      // never imported (the store's text wins).
      comment(2, 'nocx-a', 'a EDITED old 2', 'alice', '2026-09-02T00:00:00Z'),
      comment(4, 'nocx-a', 'a new 1', 'bob', '2026-10-05T10:00:00Z'),
      comment(3, 'nocx-a', 'a new 2', 'carol', '2026-10-05T11:00:00Z'), // id collides with nocx-c
    ],
  }),
  issue('nocx-c', { comments: [comment(3, 'nocx-c', 'c old')] }),
  issue('nocx-d', {
    title: 'D branch only',
    status: 'deferred',
    priority: 1,
    labels: ['remote-host'],
    dependencies: [{ issue_id: 'nocx-d', depends_on_id: 'nocx-b', type: 'parent-child' }],
    updated_at: '2026-09-25T00:00:00Z',
    comments: [
      comment(1, 'nocx-d', 'd first', 'dave', '2026-09-26T00:00:00Z'), // collides with nocx-a comment 1
      comment(5, 'nocx-d', 'd second', 'erin', '2026-09-26T01:00:00Z'),
    ],
  }),
  issue('nocx-e', { title: 'E a branch tombstone', status: 'tombstone', close_reason: 'gone' }),
  issue('nocx-t', { title: 'T branch copy', status: 'tombstone' }),
]

function plan() {
  return planImport({
    branchRecords: structuredClone(BRANCH),
    storeRecords: structuredClone(STORE),
    branchLabel: 'branch fixture',
    storeLabel: 'store fixture',
  })
}

function tempDir() {
  return mkdtempSync(join(tmpdir(), 'nocx-import-branch-beads-test-'))
}

function writeJsonl(path, records) {
  writeFileSync(path, records.map((record) => JSON.stringify(record) + '\n').join(''))
}

function runCli(args) {
  return spawnSync(process.execPath, [SCRIPT, ...args], { encoding: 'utf8' })
}

test('branch-only issues are imported whole, with their dependencies, labels and order', () => {
  const { rows } = plan()
  const d = rows.find((row) => row.id === 'nocx-d')
  assert.ok(d, 'the branch-only issue is in the plan')
  const source = BRANCH.find((row) => row.id === 'nocx-d')
  assert.deepEqual(d, {
    ...source,
    comments: [
      comment(7, 'nocx-d', 'd first', 'dave', '2026-09-26T00:00:00Z'),
      comment(5, 'nocx-d', 'd second', 'erin', '2026-09-26T01:00:00Z'),
    ],
  })
})

test('a shared issue keeps every store field; branch edits are reported, not imported', () => {
  const { rows, summary } = plan()
  const a = rows.find((row) => row.id === 'nocx-a')
  const store = STORE.find((row) => row.id === 'nocx-a')
  for (const field of ['title', 'status', 'priority', 'labels', 'dependencies', 'created_at']) {
    assert.deepEqual(a[field], store[field], `${field} is the store's`)
  }
  const edit = summary.plan.ignoredBranchEdits.find((entry) => entry.id === 'nocx-a')
  assert.deepEqual(edit.fields, ['dependencies', 'labels', 'status', 'title'])
})

test('only branch-only comments are appended, in branch order after the store comments', () => {
  const { rows } = plan()
  const a = rows.find((row) => row.id === 'nocx-a')
  assert.deepEqual(
    a.comments.map((c) => c.id),
    [1, 2, 4, 6],
  )
  assert.deepEqual(
    a.comments.slice(2).map((c) => [c.text, c.author, c.created_at]),
    [
      ['a new 1', 'bob', '2026-10-05T10:00:00Z'],
      ['a new 2', 'carol', '2026-10-05T11:00:00Z'],
    ],
  )
  assert.equal(a.comments[2].issue_id, 'nocx-a')
  assert.ok(
    a.updated_at > '2026-09-10T00:00:00Z',
    'the row is newer than the store row so br applies it',
  )
  assert.match(a.updated_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/)
})

test('a colliding comment id is remapped on the imported comment only, deterministically', () => {
  const { rows, summary } = plan()
  const a = rows.find((row) => row.id === 'nocx-a')
  assert.deepEqual(
    a.comments.map((c) => c.id),
    [1, 2, 4, 6],
  )
  assert.deepEqual(summary.plan.remappedCommentIds, [
    { issueId: 'nocx-a', from: 3, to: 6 },
    { issueId: 'nocx-d', from: 1, to: 7 },
  ])
  const all = rows.flatMap((row) => row.comments ?? []).map((c) => c.id)
  assert.equal(new Set(all).size, all.length, 'no duplicate comment id in the plan')
  assert.equal(all.includes(1), true, 'the existing store comment keeps its id')
})

test('the store is authoritative for deletions and tombstones', () => {
  const { rows, summary } = plan()
  assert.deepEqual(
    rows.map((row) => row.id),
    ['nocx-a', 'nocx-d'],
  )
  assert.equal(summary.plan.storeOnlyIssues, 1, 'nocx-b is store-only, never a branch deletion')
  assert.deepEqual(summary.plan.storeTombstonesPresent, ['nocx-t'])
  assert.deepEqual(summary.plan.branchOnlyTombstonesSkipped, ['nocx-e'])
  assert.equal(summary.plan.sharedIssues, 3, 'nocx-a, nocx-c and nocx-t')
})

test('no branch-only rows means an empty plan, not a rewrite', () => {
  const { rows, summary } = planImport({
    branchRecords: structuredClone(STORE),
    storeRecords: structuredClone(STORE),
    branchLabel: 'same',
    storeLabel: 'same',
  })
  assert.deepEqual(rows, [])
  assert.equal(summary.plan.comments, 0)
})

test('duplicate issue ids in either input are refused', () => {
  const dir = tempDir()
  const store = join(dir, 'store.jsonl')
  const branch = join(dir, 'branch.jsonl')
  const out = join(dir, 'plan.jsonl')
  writeJsonl(store, STORE)
  writeJsonl(branch, [...BRANCH, issue('nocx-d')])
  let result = runCli(['--file', branch, '--store', store, '--out', out, '--json'])
  assert.equal(result.status, 2)
  assert.match(result.stderr, /duplicate issue id nocx-d/)

  writeJsonl(branch, BRANCH)
  writeJsonl(store, [...STORE, issue('nocx-b')])
  result = runCli(['--file', branch, '--store', store, '--out', out, '--json'])
  assert.equal(result.status, 2)
  assert.match(result.stderr, /duplicate issue id nocx-b/)
})

test('a comment that names another issue is refused', () => {
  const dir = tempDir()
  const store = join(dir, 'store.jsonl')
  const branch = join(dir, 'branch.jsonl')
  const out = join(dir, 'plan.jsonl')
  writeJsonl(store, STORE)
  const bad = structuredClone(BRANCH)
  bad.find((row) => row.id === 'nocx-d').comments[0].issue_id = 'nocx-a'
  writeJsonl(branch, bad)
  const result = runCli(['--file', branch, '--store', store, '--out', out, '--json'])
  assert.equal(result.status, 2)
  assert.match(result.stderr, /names issue nocx-a/)
})

test('the CLI writes exactly the plan records and a JSON summary', () => {
  const dir = tempDir()
  const store = join(dir, 'store.jsonl')
  const branch = join(dir, 'branch.jsonl')
  const out = join(dir, 'plan.jsonl')
  writeJsonl(store, STORE)
  writeJsonl(branch, BRANCH)
  const result = runCli(['--file', branch, '--store', store, '--out', out, '--json'])
  assert.equal(result.status, 0, result.stderr)
  const summary = JSON.parse(result.stdout)
  const rows = readFileSync(out, 'utf8')
    .trim()
    .split('\n')
    .map((line) => JSON.parse(line))
  assert.deepEqual(
    rows.map((row) => row.id),
    ['nocx-a', 'nocx-d'],
  )
  assert.equal(summary.plan.issues, 2)
  assert.equal(summary.plan.comments, 4)
  assert.equal(summary.branch.issues, BRANCH.length)
})

test('a branch is read at its head with git show, so it is never checked out', () => {
  const dir = tempDir()
  const repo = join(dir, 'repo')
  const store = join(dir, 'store.jsonl')
  const out = join(dir, 'plan.jsonl')
  writeJsonl(store, STORE)
  execFileSync('git', ['init', '-q', repo])
  execFileSync('git', ['-C', repo, 'config', 'user.email', 'test@example.com'])
  execFileSync('git', ['-C', repo, 'config', 'user.name', 'test'])
  mkdirSync(join(repo, '.beads'))
  writeJsonl(join(repo, '.beads', 'issues.jsonl'), BRANCH)
  execFileSync('git', ['-C', repo, 'add', '.beads/issues.jsonl'])
  execFileSync('git', ['-C', repo, 'commit', '-q', '-m', 'branch export'])
  const result = spawnSync(
    process.execPath,
    [SCRIPT, '--branch', 'HEAD', '--store', store, '--out', out, '--json'],
    {
      cwd: repo,
      encoding: 'utf8',
    },
  )
  assert.equal(result.status, 0, result.stderr)
  const rows = readFileSync(out, 'utf8')
    .trim()
    .split('\n')
    .map((line) => JSON.parse(line))
  assert.deepEqual(
    rows.map((row) => row.id),
    ['nocx-a', 'nocx-d'],
  )
})

test('usage errors and unreadable inputs are exit 2, never a partial plan', () => {
  const dir = tempDir()
  const out = join(dir, 'plan.jsonl')
  assert.equal(runCli([]).status, 2)
  assert.equal(runCli(['--out', out]).status, 2)
  assert.equal(runCli(['--file', 'a', '--branch', 'b', '--out', out]).status, 2)
  assert.equal(runCli(['--file', 'a', '--out', out, '--wat']).status, 2)
  const bad = join(dir, 'bad.jsonl')
  writeFileSync(bad, '{"id":"nocx-a",\n')
  assert.equal(runCli(['--file', bad, '--store', bad, '--out', out]).status, 2)
  assert.equal(runCli(['--branch', 'no-such-ref', '--store', bad, '--out', out]).status, 2)
})
