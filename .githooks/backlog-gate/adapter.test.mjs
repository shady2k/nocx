// node --test .githooks/backlog-gate/adapter.test.mjs
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { contextualReady, normalize } from './adapter.mjs'

const row = (comments, status = 'in_progress') => ({
  id: 'nocx-a',
  title: 'A',
  issue_type: 'task',
  status,
  created_at: '2026-09-26T10:00:00Z',
  updated_at: '2026-09-26T10:00:00Z',
  comments,
})

test('a native submitted status takes its record from the latest submitted comment', () => {
  const [issue] = normalize([
    row(
      [
        { id: 1, author: 'c', text: 'submitted: old111 -- go build', created_at: 't1' },
        { id: 2, author: 'c', text: 'implemented: nope -- not this kind', created_at: 't2' },
        { id: 3, author: 'c', text: 'submitted: abc123 -- go vet', created_at: 't3' },
      ],
      'submitted',
    ),
  ])
  assert.equal(issue.status, 'submitted')
  assert.deepEqual(issue.delivery, { revision: 'abc123', evidence: 'go vet' })
  assert.equal(issue.integration, undefined)
})

test('a native implemented status takes its record from the latest implemented comment', () => {
  const [issue] = normalize([
    row(
      [{ id: 1, author: 'c', text: 'implemented: def456 -- go test ./x', created_at: 't1' }],
      'implemented',
    ),
  ])
  assert.equal(issue.status, 'implemented')
  assert.deepEqual(issue.integration, { revision: 'def456', evidence: 'go test ./x' })
})

test('a native status with no usable record still reaches the gate, with an empty record', () => {
  // The gate's *-without-evidence check reports it; dropping the status would hide it.
  const [bare] = normalize([row([], 'implemented')])
  assert.equal(bare.status, 'implemented')
  assert.deepEqual(bare.integration, { revision: '', evidence: '' })
  const [half] = normalize([
    row([{ id: 1, author: 'c', text: 'submitted: abc123', created_at: 't1' }], 'submitted'),
  ])
  assert.deepEqual(half.delivery, { revision: '', evidence: '' })
})

test('in_progress is active whatever marker comments it carries', () => {
  // A reopen is a status change now; a leftover marker is history, not status.
  const [issue] = normalize([
    row([{ id: 1, author: 'c', text: 'implemented: abc123 -- go vet', created_at: 't1' }]),
  ])
  assert.equal(issue.status, 'active')
  assert.equal(issue.integration, undefined)
  assert.equal(issue.delivery, undefined)
})

const nativeIssue = (id, type, status, parent = null, extras = {}) => ({
  id,
  title: id,
  issue_type: type,
  status,
  description: '',
  acceptance_criteria: '',
  dependencies: parent ? [{ type: 'parent-child', depends_on_id: parent }] : [],
  comments: [],
  ...extras,
})

function checkout() {
  const path = mkdtempSync(join(tmpdir(), 'adapter-checkout-'))
  execFileSync('git', ['init', '-q', path])
  execFileSync('git', ['-C', path, 'config', 'user.email', 'adapter-test@example.invalid'])
  execFileSync('git', ['-C', path, 'config', 'user.name', 'Adapter Test'])
  writeFileSync(join(path, 'proof'), 'proof\n')
  execFileSync('git', ['-C', path, 'add', 'proof'])
  execFileSync('git', ['-C', path, 'commit', '-qm', 'proof'])
  return {
    path,
    revision: execFileSync('git', ['-C', path, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
  }
}

test('the gate sees native epic criteria without accepting an empty criterion', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'adapter-criterion-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const config = join(dir, 'config.json')
  writeFileSync(config, JSON.stringify({ strength: 'report' }))
  const issues = normalize([
    {
      ...nativeIssue('valid', 'epic', 'open'),
      updated_at: '2026-09-28T00:00:00Z',
      acceptance_criteria: 'The owner can safely claim the next ready task.',
    },
    {
      ...nativeIssue('empty', 'epic', 'open'),
      updated_at: '2026-09-28T00:00:00Z',
      acceptance_criteria: '   ',
    },
  ])
  const checked = spawnSync(
    process.execPath,
    [fileURLToPath(new URL('./check.mjs', import.meta.url)), '--config', config, '--json', '-'],
    { input: JSON.stringify({ issues }), encoding: 'utf8' },
  )
  assert.equal(checked.status, 0, checked.stderr)
  const result = JSON.parse(checked.stdout).results.find(
    ({ id }) => id === 'epic-without-criterion',
  )
  assert.deepEqual(
    result.violations.map(({ id }) => id),
    ['empty'],
  )
})

test('contextual readiness requires implemented evidence and checkout ancestry, and honors stage acceptance', (t) => {
  const repo = checkout()
  t.after(() => rmSync(repo.path, { recursive: true, force: true }))
  const rows = [
    nativeIssue('feature', 'epic', 'open'),
    nativeIssue('feature.1', 'epic', 'open', 'feature', {
      comments: [{ id: 1, text: `accepted: ${repo.revision} -- reviewed checks`, created_at: '1' }],
    }),
    nativeIssue('feature.2', 'epic', 'open', 'feature'),
    nativeIssue('feature.1.impl', 'task', 'implemented', 'feature.1', {
      comments: [
        { id: 2, text: `implemented: ${repo.revision} -- related checks`, created_at: '2' },
      ],
    }),
    nativeIssue('feature.2.same', 'task', 'implemented', 'feature.2', {
      comments: [
        { id: 3, text: `implemented: ${repo.revision} -- related checks`, created_at: '3' },
      ],
    }),
    nativeIssue('feature.2.ready', 'task', 'open', 'feature.2', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'feature.2' },
        { type: 'blocks', depends_on_id: 'feature.1.impl' },
      ],
    }),
    nativeIssue('feature.2.same-ready', 'task', 'open', 'feature.2', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'feature.2' },
        { type: 'blocks', depends_on_id: 'feature.2.same' },
      ],
    }),
    nativeIssue('feature.2.held', 'task', 'open', 'feature.2', { assignee: 'agent' }),
    nativeIssue('feature.3', 'epic', 'open', 'feature'),
    nativeIssue('feature.3.impl', 'task', 'implemented', 'feature.3', {
      comments: [{ id: 5, text: `implemented: ${repo.revision} -- checks`, created_at: '5' }],
    }),
    nativeIssue('feature.2.unaccepted-stage', 'task', 'open', 'feature.2', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'feature.2' },
        { type: 'blocks', depends_on_id: 'feature.3.impl' },
      ],
    }),
    nativeIssue('feature.2.active-prereq', 'task', 'open', 'feature.2', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'feature.2' },
        { type: 'blocks', depends_on_id: 'feature.1.active' },
      ],
    }),
    nativeIssue('feature.1.active', 'task', 'in_progress', 'feature.1'),
    nativeIssue('feature.2.deferred-prereq', 'task', 'deferred', 'feature.2'),
    nativeIssue('feature.2.submitted', 'task', 'submitted', 'feature.2'),
    nativeIssue('other', 'epic', 'open'),
    nativeIssue('other.1', 'epic', 'open', 'other'),
    nativeIssue('other.1.impl', 'task', 'implemented', 'other.1', {
      comments: [{ id: 4, text: `implemented: ${repo.revision} -- checks`, created_at: '4' }],
    }),
    nativeIssue('feature.2.other-feature', 'task', 'open', 'feature.2', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'feature.2' },
        { type: 'blocks', depends_on_id: 'other.1.impl' },
      ],
    }),
  ]
  assert.deepEqual(contextualReady(rows, 'feature.2', repo.path), [
    'feature.2.ready',
    'feature.2.same-ready',
  ])
  assert.throws(() => contextualReady(rows, 'feature', repo.path), /not a stage epic/)
})

test('earlier-stage acceptance requires an accepted revision in the checkout', (t) => {
  const repo = checkout()
  t.after(() => rmSync(repo.path, { recursive: true, force: true }))
  const rows = [
    nativeIssue('f', 'epic', 'open'),
    nativeIssue('f.1', 'epic', 'open', 'f'),
    nativeIssue('f.2', 'epic', 'open', 'f'),
    nativeIssue('f.1.done', 'task', 'implemented', 'f.1', {
      comments: [
        { id: 1, text: `implemented: ${repo.revision} -- related checks`, created_at: '1' },
      ],
    }),
    nativeIssue('f.2.todo', 'task', 'open', 'f.2', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'f.2' },
        { type: 'blocks', depends_on_id: 'f.1.done' },
      ],
    }),
  ]
  assert.deepEqual(contextualReady(rows, 'f.2', repo.path), [])
  rows[1].comments = [
    { id: 2, text: 'accepted: missing-revision -- reviewed checks', created_at: '2' },
  ]
  assert.deepEqual(contextualReady(rows, 'f.2', repo.path), [])
})

test('stage acceptance covers the prerequisite revision, not an id ordering or an older merge', (t) => {
  const repo = checkout()
  t.after(() => rmSync(repo.path, { recursive: true, force: true }))
  execFileSync('git', [
    '-C',
    repo.path,
    'commit',
    '--allow-empty',
    '-qm',
    'integrated prerequisite',
  ])
  const integrated = execFileSync('git', ['-C', repo.path, 'rev-parse', 'HEAD'], {
    encoding: 'utf8',
  }).trim()
  const rows = [
    nativeIssue('f', 'epic', 'open'),
    nativeIssue('f.z', 'epic', 'open', 'f', {
      comments: [{ id: 1, text: `accepted: ${repo.revision} -- stage checks`, created_at: '1' }],
    }),
    nativeIssue('f.a', 'epic', 'open', 'f'),
    nativeIssue('f.z.done', 'task', 'implemented', 'f.z', {
      comments: [{ id: 2, text: `implemented: ${integrated} -- related checks`, created_at: '2' }],
    }),
    nativeIssue('f.a.todo', 'task', 'open', 'f.a', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'f.a' },
        { type: 'blocks', depends_on_id: 'f.z.done' },
      ],
    }),
  ]
  assert.deepEqual(contextualReady(rows, 'f.a', repo.path), [])
  rows[1].comments[0].text = `accepted: ${integrated} -- stage checks`
  assert.deepEqual(contextualReady(rows, 'f.a', repo.path), ['f.a.todo'])
  rows[1].comments.push({ id: 3, text: 'accepted: damaged', created_at: '3' })
  assert.deepEqual(contextualReady(rows, 'f.a', repo.path), [])
})

test('missing dependency and cycle refuse contextual readiness', (t) => {
  const repo = checkout()
  t.after(() => rmSync(repo.path, { recursive: true, force: true }))
  const feature = nativeIssue('f', 'epic', 'open')
  const stage = nativeIssue('f.1', 'epic', 'open', 'f')
  const candidate = nativeIssue('f.1.a', 'task', 'open', 'f.1', {
    dependencies: [
      { type: 'parent-child', depends_on_id: 'f.1' },
      { type: 'blocks', depends_on_id: 'absent' },
    ],
  })
  assert.throws(
    () => contextualReady([feature, stage, candidate], 'f.1', repo.path),
    /missing prerequisite absent/,
  )
  candidate.dependencies[1].depends_on_id = 'f.1.b'
  const cycle = nativeIssue('f.1.b', 'task', 'open', 'f.1', {
    dependencies: [
      { type: 'parent-child', depends_on_id: 'f.1' },
      { type: 'blocks', depends_on_id: 'f.1.a' },
    ],
  })
  assert.throws(
    () => contextualReady([feature, stage, candidate, cycle], 'f.1', repo.path),
    /cycle/,
  )
})

test('nested deferred, closed and blocked groups exclude their leaves but not independent work', (t) => {
  const repo = checkout()
  t.after(() => rmSync(repo.path, { recursive: true, force: true }))
  const rows = [
    nativeIssue('f', 'epic', 'open'),
    nativeIssue('f.stage', 'epic', 'open', 'f'),
    nativeIssue('f.group', 'epic', 'deferred', 'f.stage'),
    nativeIssue('f.child', 'task', 'open', 'f.group'),
    nativeIssue('f.independent', 'task', 'open', 'f.stage'),
    nativeIssue('blocker', 'task', 'open'),
  ]
  for (const status of ['deferred', 'closed']) {
    rows[2].status = status
    assert.deepEqual(contextualReady(rows, 'f.stage', repo.path), ['f.independent'])
  }
  rows[2].status = 'open'
  rows[2].dependencies.push({ type: 'blocks', depends_on_id: 'blocker' })
  assert.deepEqual(contextualReady(rows, 'f.stage', repo.path), ['f.independent'])
  rows[5].status = 'closed'
  assert.deepEqual(contextualReady(rows, 'f.stage', repo.path), ['f.child', 'f.independent'])
})

test('same-stage consumers require the integrated revision in their checkout', (t) => {
  const repo = checkout()
  t.after(() => rmSync(repo.path, { recursive: true, force: true }))
  const git = (...args) =>
    execFileSync('git', ['-C', repo.path, ...args], { encoding: 'utf8' }).trim()
  const unmerged = git(
    'commit-tree',
    git('rev-parse', 'HEAD^{tree}'),
    '-p',
    repo.revision,
    '-m',
    'not in this checkout',
  )
  const rows = [
    nativeIssue('f', 'epic', 'open'),
    nativeIssue('f.stage', 'epic', 'open', 'f'),
    nativeIssue('f.producer', 'task', 'implemented', 'f.stage', {
      comments: [{ id: 1, text: `implemented: ${unmerged} -- related checks`, created_at: '1' }],
    }),
    nativeIssue('f.consumer', 'task', 'open', 'f.stage', {
      dependencies: [
        { type: 'parent-child', depends_on_id: 'f.stage' },
        { type: 'blocks', depends_on_id: 'f.producer' },
      ],
    }),
    nativeIssue('f.independent', 'task', 'open', 'f.stage'),
  ]
  assert.deepEqual(contextualReady(rows, 'f.stage', repo.path), ['f.independent'])
  git('update-ref', 'HEAD', unmerged)
  assert.deepEqual(contextualReady(rows, 'f.stage', repo.path), ['f.consumer', 'f.independent'])
})
