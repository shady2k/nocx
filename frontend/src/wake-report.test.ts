import { beforeEach, describe, expect, it, vi } from 'vitest'

import { startWakeReporter, type WakeObservation, type WakeSource } from './wake-report'
import { log } from './log'

const observation = (over: Partial<WakeObservation> = {}): WakeObservation => ({
  paneId: 'p1',
  sessionId: 's',
  bufferRows: 40,
  rendererLive: true,
  width: 900,
  height: 600,
  editorPresent: true,
  editorVisible: false,
  connection: 'reachable',
  ...over,
})

function fakeSource(): WakeSource & { wake(): void } {
  let cb: (() => void) | null = null
  return {
    subscribe(onWake) {
      cb = onWake
      return () => {
        cb = null
      }
    },
    wake() {
      cb?.()
    },
  }
}

describe('startWakeReporter', () => {
  beforeEach(() => vi.restoreAllMocks())

  it('reports every pane in ONE line, so they describe the same instant', () => {
    const info = vi.spyOn(log, 'info').mockImplementation(() => {})
    const src = fakeSource()
    startWakeReporter(
      () => [observation({ paneId: 'p1' }), observation({ paneId: 'p2' })],
      src,
      () => 0,
    )

    src.wake()

    expect(info).toHaveBeenCalledTimes(1)
    const [, payload] = info.mock.calls[0] as [string, { panes: WakeObservation[] }]
    expect(payload.panes.map((p) => p.paneId)).toEqual(['p1', 'p2'])
  })

  // A lid opening delivers visibilitychange and focus within a frame or two,
  // and two identical lines make the record harder to read, not richer.
  it('treats two signals in quick succession as one wake', () => {
    const info = vi.spyOn(log, 'info').mockImplementation(() => {})
    const src = fakeSource()
    let clock = 1000
    startWakeReporter(
      () => [observation()],
      src,
      () => clock,
    )

    src.wake()
    clock += 100
    src.wake()
    expect(info).toHaveBeenCalledTimes(1)

    clock += 5000
    src.wake()
    expect(info).toHaveBeenCalledTimes(2)
  })

  it('says nothing when there is no pane to describe', () => {
    const info = vi.spyOn(log, 'info').mockImplementation(() => {})
    const src = fakeSource()
    startWakeReporter(
      () => [],
      src,
      () => 0,
    )
    src.wake()
    expect(info).not.toHaveBeenCalled()
  })

  it('stops watching when disposed', () => {
    const info = vi.spyOn(log, 'info').mockImplementation(() => {})
    const src = fakeSource()
    const stop = startWakeReporter(
      () => [observation()],
      src,
      () => 0,
    )
    stop()
    src.wake()
    expect(info).not.toHaveBeenCalled()
  })
})
