// node --test .githooks/backlog-gate/merge-gate.test.mjs
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { newInBoth } from './merge-gate.mjs'

const report = (...vs) => ({
  results: [
    {
      id: 'time-work-unclaimed',
      severity: 'error',
      violations: vs.map(([id, isNew]) => ({ id, isNew })),
    },
    { id: 'stale-idea', severity: 'warning', violations: [{ id: 'w', isNew: true }] },
  ],
})

test('debt either parent already carries is not new in the merge', () => {
  // a came in from main, b from the branch: each is old against one parent.
  const againstHead = report(['a', true], ['b', false])
  const againstMergeHead = report(['a', false], ['b', true])
  assert.deepEqual(newInBoth(againstHead, againstMergeHead), [])
})

test('a violation neither parent had is new, and only errors count', () => {
  const againstHead = report(['a', true], ['c', true])
  const againstMergeHead = report(['a', false], ['c', true])
  assert.deepEqual(newInBoth(againstHead, againstMergeHead), [
    { rule: 'time-work-unclaimed', id: 'c', note: undefined },
  ])
})
