import { test } from 'node:test'
import assert from 'node:assert/strict'

import { switchFenceWindow, type SwitchTraceEvent } from './alt-switch-fence.mts'

// THE SAME READING THE LIVE SPEC MAKES, decided by traces already in hand.
//
// The alternate-screen spec asserts this window against a real PTY, where the
// ordering depends on when a receipt happens to land. These cases fix the
// orderings that matter — including the one the release runner actually
// produced — so the reading is decided by the trace rather than by the race
// (nocx-1ebrs.66.1.25).
const frame = (bufferKind: 'normal' | 'alternate'): SwitchTraceEvent => ({
  at: 0,
  kind: 'frame',
  state: { bufferKind },
})
const resizeSend = (id: number): SwitchTraceEvent => ({ at: 0, kind: 'send', method: 'resize', id })
const intentSend = (id: number): SwitchTraceEvent => ({
  at: 0,
  kind: 'send',
  method: 'session.intent',
  id,
  params: { kind: 'key', payload: '' },
})
const applied = (id: number): SwitchTraceEvent => ({
  at: 0,
  kind: 'resize-receipt',
  id,
  size: {},
  result: {},
  error: undefined,
})
const superseded = (id: number): SwitchTraceEvent => ({
  at: 0,
  kind: 'resize-receipt',
  id,
  size: {},
  result: undefined,
  error: { code: -32013, message: 'superseded' },
})

test('a receipt for a resize sent before the switch releases nothing', () => {
  // RECORDED FROM A RUN OF THE LIVE SPEC (webkit, "after the resize"): the
  // settle timer's resize left the socket first, the fixture switched when
  // it applied, the fence sent its own request — and the timer's receipt
  // arrived between that send and its answer. Reading the first applied
  // receipt after the switch then named the OLD request as the release, and
  // the spec reported the switch's own request as unanswered while the wire
  // had answered it two milliseconds later.
  const trace = [
    frame('normal'),
    resizeSend(49),
    frame('alternate'),
    resizeSend(50),
    applied(49),
    applied(50),
    intentSend(52),
  ]
  const window = switchFenceWindow(trace)
  assert.equal(window.firstResize, 3)
  assert.equal(window.switchResizeId, 50)
  assert.equal(
    window.releaseId,
    // "the receipt that released the fence answers the resize the switch sent"
    50,
  )
  assert.equal(window.release, 5)
  assert.equal(window.inputsBeforeRelease.length, 0)
  assert.equal(window.firstInput, 6)
})

test('a superseded switch request is answered, and the release is the applied one', () => {
  // The wire shape of nocx-u1dts: the fence's first request is superseded by
  // a later one and answered with -32013, and only the applied receipt may
  // release it.
  const trace = [
    frame('alternate'),
    resizeSend(57),
    intentSend(58),
    superseded(57),
    resizeSend(69),
    applied(69),
    intentSend(70),
  ]
  const window = switchFenceWindow(trace)
  assert.equal(window.firstResize, 1)
  assert.equal(window.switchResizeId, 57)
  assert.equal(window.release, 5)
  assert.equal(window.releaseId, 69)
  assert.deepEqual(
    window.inputsBeforeRelease.map((event) => event.id),
    [58],
  )
  assert.equal(window.answered, 3)
})

test('input the pane holds is the input between the send and the applied receipt', () => {
  const trace = [
    frame('normal'),
    frame('alternate'),
    resizeSend(70),
    intentSend(71),
    applied(70),
    intentSend(72),
  ]
  const window = switchFenceWindow(trace)
  assert.equal(window.release, 4)
  assert.deepEqual(
    window.inputsBeforeRelease.map((event) => event.id),
    // "the input that beat the release is what the spec must complain about"
    [71],
  )
  assert.equal(window.firstInput, 3)
})

test('a switch resize that was never answered has no release', () => {
  const trace = [frame('alternate'), resizeSend(80), intentSend(81), intentSend(82)]
  const window = switchFenceWindow(trace)
  assert.equal(window.firstResize, 1)
  // "a silently dropped resize cannot answer the fence"
  assert.equal(window.release, -1)
  assert.equal(window.switchResizeId, 80)
})

test('a trace that never reached the alternate buffer has no window', () => {
  const window = switchFenceWindow([frame('normal'), resizeSend(90)])
  assert.equal(window.switchIndex, -1)
  assert.equal(window.firstResize, -1)
  assert.equal(window.release, -1)
})
