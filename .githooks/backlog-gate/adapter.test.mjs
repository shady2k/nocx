// node --test .githooks/backlog-gate/adapter.test.mjs
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { normalize } from './adapter.mjs'

const row = (comments) => ({
  id: 'nocx-a',
  title: 'A',
  issue_type: 'task',
  status: 'in_progress',
  created_at: '2026-09-26T10:00:00Z',
  updated_at: '2026-09-26T10:00:00Z',
  comments,
})

test('a work record comment is exported raw, with the tracker id, time and author', () => {
  // Damaged on purpose: the gate judges a record, so the adapter must not.
  const body = '[shady2k-time v1] claim\nitem: nocx-a\nspan: 1234abcd\n  trailing  \n'
  const [issue] = normalize([
    row([
      { id: 7, author: 'claude-worker', text: body, created_at: '2026-09-26T10:01:00Z' },
      { id: 8, author: 'dev', text: 'an ordinary note', created_at: '2026-09-26T10:02:00Z' },
    ]),
  ])
  assert.deepEqual(issue.comments, [
    { id: '7', at: '2026-09-26T10:01:00Z', author: 'claude-worker', body },
  ])
})

test('an issue with no work record carries an empty comment list', () => {
  const [issue] = normalize([row([{ id: 1, author: 'dev', text: 'note', created_at: 'x' }])])
  assert.deepEqual(issue.comments, [])
  const [bare] = normalize([row(undefined)])
  assert.deepEqual(bare.comments, [])
})

test('the execution markers still decide the status beside the records', () => {
  const [issue] = normalize([
    row([{ id: 1, author: 'c', text: 'submitted: abc123 -- go vet', created_at: 't1' }]),
  ])
  assert.equal(issue.status, 'submitted')
  assert.deepEqual(issue.delivery, { revision: 'abc123', evidence: 'go vet' })
})
