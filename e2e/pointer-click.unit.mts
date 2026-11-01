import { test } from 'node:test'
import assert from 'node:assert/strict'

import { clickGesture, sgrMouseReports } from './pointer-click.mts'

/** The fixture prints a chunk per read; `hex` is what one of them carries. */
function hex(text: string): string {
  return [...Buffer.from(text, 'latin1')].map((b) => b.toString(16).padStart(2, '0')).join('')
}

const KEYS = 'earlyQ7\x1b[B'
const PRESS = '\x1b[<0;11;11M'
const RELEASE = '\x1b[<0;11;11m'

test('mouse reports are read out of the keys around them', () => {
  assert.deepEqual(sgrMouseReports([hex(KEYS + PRESS + RELEASE)]), [
    { button: 0, col: 11, row: 11, press: true },
    { button: 0, col: 11, row: 11, press: false },
  ])
})

test('a report split across two reads is still one report', () => {
  const all = hex(KEYS + PRESS + RELEASE)
  assert.deepEqual(sgrMouseReports([all.slice(0, 29), all.slice(29)]), [
    { button: 0, col: 11, row: 11, press: true },
    { button: 0, col: 11, row: 11, press: false },
  ])
})

test('motion is reported, and is not a click half', () => {
  const motion = '\x1b[<35;11;11M'
  assert.deepEqual(sgrMouseReports([hex(motion)]), [{ button: 35, col: 11, row: 11, press: true }])
  assert.equal(clickGesture([hex(KEYS + motion)]), null)
})

test('a press alone is a click still travelling, not a click', () => {
  assert.equal(clickGesture([hex(KEYS + PRESS)]), null)
})

test('a release alone is not a click either', () => {
  assert.equal(clickGesture([hex(RELEASE)]), null)
})

test('the whole click is the press and the release that follows it', () => {
  const gesture = clickGesture([hex(KEYS), hex(PRESS), hex('\x1b[<35;11;12M'), hex(RELEASE)])
  assert.deepEqual(gesture, {
    press: { button: 0, col: 11, row: 11, press: true },
    release: { button: 0, col: 11, row: 11, press: false },
  })
})
