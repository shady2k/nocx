import { test } from 'node:test'
import assert from 'node:assert/strict'

import { judgeFrames } from './frame-budget.mts'

const repeat = (value: number, count: number): number[] =>
  Array.from({ length: count }, () => value)
const idle60 = repeat(16.7, 120)

test('a scroll at half the refresh rate fails, although a tenth of its frames were on time', () => {
  // The review's example (nocx-2v80t.3.39): judged against its own tenth
  // percentile this scroll passed, with nine frames in ten missed.
  const scroll = [...repeat(16.7, 17), ...repeat(33.4, 162)]
  const verdict = judgeFrames(idle60, scroll)
  assert.equal(verdict.baselineMs, 16.7)
  assert.equal(verdict.medianMs, 33.4)
  assert.notDeepEqual(verdict.failures, [])
})

test('a smooth Chromium scroll passes', () => {
  const scroll = [...repeat(16.7, 170), ...repeat(16.8, 9)]
  assert.deepEqual(judgeFrames(idle60, scroll).failures, [])
})

test('a smooth headless WebKit scroll passes: whole milliseconds and one early callback', () => {
  // Measured shape (2026-09-25): 16s and 17s, an 11 and a 21 around one late
  // frame, and the occasional 3-4 ms callback, idle and scrolling alike.
  const idle = [...repeat(16, 100), ...repeat(17, 18), 3, 4]
  const scroll = [...repeat(16, 150), ...repeat(17, 25), 21, 11, 21, 12]
  const verdict = judgeFrames(idle, scroll)
  assert.equal(verdict.baselineMs, 16)
  assert.deepEqual(verdict.failures, [])
})

test('one dropped frame in twenty passes, a dropped frame in every ten does not', () => {
  const few = [...repeat(16.7, 171), ...repeat(33.4, 8)]
  assert.deepEqual(judgeFrames(idle60, few).failures, [])
  const many = [...repeat(16.7, 150), ...repeat(50.1, 29)]
  assert.notDeepEqual(judgeFrames(idle60, many).failures, [])
})

test('an idle page that cannot reach a refresh rate fails on its own', () => {
  // Otherwise a busy page hands a slow baseline to the scroll it judges.
  const idle = repeat(33.4, 120)
  const verdict = judgeFrames(idle, repeat(33.4, 179))
  assert.notDeepEqual(verdict.failures, [])
})

test('no samples is a failure, not a pass', () => {
  assert.notDeepEqual(judgeFrames([], []).failures, [])
})
