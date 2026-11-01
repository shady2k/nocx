// node --test .githooks/backlog-gate/publish-gate.test.mjs
//
// The backlog gate at the publish boundary (nocx-fg79x.3): the published store
// head against the last store head the gate accepted, with the check's own
// block-new strength.
import { after, before, test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), 'publish-gate.mjs')

const issue = (id, status, extra = {}) => ({
  id,
  title: id,
  issue_type: 'task',
  status,
  labels: ['infra'],
  description: '',
  acceptance_criteria: '',
  dependencies: [],
  comments: [],
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
  ...extra,
})

let root, repo, config
const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim()
const gate = (baseline, head) =>
  spawnSync(
    process.execPath,
    [SCRIPT, '--repo', repo, '--baseline', baseline, '--head', head, '--config', config],
    { cwd: repo, encoding: 'utf8' },
  )

// One published store commit holding exactly `issues`, on the code's main line.
const storeCommit = (issues, message) => {
  writeFileSync(
    join(repo, '.beads/issues.jsonl'),
    issues.map((row) => JSON.stringify(row)).join('\n') + '\n',
  )
  git('add', '-A')
  git('commit', '-q', '-m', message)
  return git('rev-parse', 'HEAD')
}

before(() => {
  root = mkdtempSync(join(tmpdir(), 'publish-gate-'))
  repo = join(root, 'repo')
  execFileSync('git', ['init', '-q', '-b', 'main', repo])
  execFileSync('git', ['-C', repo, 'config', 'user.email', 'gate-test@example.invalid'])
  execFileSync('git', ['-C', repo, 'config', 'user.name', 'Gate Test'])
  execFileSync('git', ['-C', repo, 'config', 'commit.gpgsign', 'false'])
  writeFileSync(join(repo, 'proof'), 'proof\n')
  git('add', 'proof')
  git('commit', '-q', '-m', 'chore: seed')
  mkdirSync(join(repo, '.beads'))
  config = join(root, 'config.json')
  writeFileSync(config, JSON.stringify({ strength: 'block-new', areaLabels: ['infra'] }))
})

after(() => rmSync(root, { recursive: true, force: true }))

test('a store revision without an export is refused by name', () => {
  const empty = git('rev-parse', 'HEAD') // the seed commit: no .beads/issues.jsonl
  const run = gate(empty, empty)
  assert.equal(run.status, 2, run.stdout + run.stderr)
  assert.match(run.stderr, /holds no \.beads\/issues\.jsonl/)
})

test('a violation the baseline does not carry is new and red', () => {
  const base = storeCommit([issue('nocx-a', 'open')], 'tracker: base')
  const head = storeCommit(
    [issue('nocx-a', 'open'), issue('nocx-b', 'in_progress')],
    'tracker: an unheld active leaf',
  )
  const run = gate(base, head)
  assert.equal(run.status, 1, run.stdout + run.stderr)
  assert.match(run.stdout, /tracker publish gate: /)
  assert.match(run.stdout, /FAIL {2}stale-hold {2}\(1, 1 new\)/)
  assert.match(run.stdout, /NEW nocx-b: active, and nobody holds it/)
  assert.match(run.stdout, /new errors: 1/)
})

test('the same violation already in the baseline is debt: printed, green, nothing new', () => {
  const base = git('rev-parse', 'HEAD') // carries nocx-b, unheld and active
  const head = storeCommit(
    [issue('nocx-a', 'open'), issue('nocx-b', 'in_progress'), issue('nocx-c', 'closed')],
    'tracker: an unrelated closed leaf',
  )
  const run = gate(base, head)
  assert.equal(run.status, 0, run.stdout + run.stderr)
  assert.match(run.stdout, /FAIL {2}stale-hold {2}\(1, 0 new\)/)
  assert.match(run.stdout, /new errors: 0/)
  assert.match(run.stdout, /green/)
})

test('a publish that changes nothing is green, and the gate compares states not titles', () => {
  const head = git('rev-parse', 'HEAD')
  const run = gate(head, head)
  assert.equal(run.status, 0, run.stdout + run.stderr)
  assert.match(run.stdout, /\(0 published commit\(s\) since\)/)
  assert.match(run.stdout, /new errors: 0/)
})

test('misuse names the missing side and exits 2', () => {
  const head = git('rev-parse', 'HEAD')
  const one = spawnSync(process.execPath, [SCRIPT, '--repo', repo, '--head', head], {
    cwd: repo,
    encoding: 'utf8',
  })
  assert.equal(one.status, 2)
  assert.match(one.stderr, /both --baseline and --head are required/)
  const unknown = spawnSync(process.execPath, [SCRIPT, '--baseline'], {
    cwd: repo,
    encoding: 'utf8',
  })
  assert.equal(unknown.status, 2)
  assert.match(unknown.stderr, /unknown or incomplete argument/)
})
