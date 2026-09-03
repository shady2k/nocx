/**
 * What the calibration client puts ON THE WIRE (nocx-etejh).
 *
 * The section's tests spy on this client's methods, so they see what the
 * surface asked for and not what was sent. This one is the other half, and it
 * is the half the bead's first falsifier lives in: a label reaching the
 * backend from here would be a label the person did not produce that frame
 * for. The backend refuses an unknown field outright, so a client that grew
 * one would fail every call — this asserts it before that happens rather than
 * after.
 */
import { describe, it, expect, vi } from 'vitest'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { CalibrationClient } from './calibration-client'

function spied(): { client: CalibrationClient; calls: [string, unknown][] } {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  const calls: [string, unknown][] = []
  vi.spyOn(dispatcher, 'call').mockImplementation((method: string, params?: unknown) => {
    calls.push([method, params])
    return Promise.resolve({ panes: [] })
  })
  return { client: new CalibrationClient(dispatcher), calls }
}

describe('the calibration client', () => {
  it('sends a pane, an action and a step — and nothing that names a label', async () => {
    const { client, calls } = spied()
    await client.answer('sess-1', 'capture', 2)
    await client.answer('sess-1', 'skip', 3)
    await client.answer('sess-1', 'redo', 3)
    for (const [method, params] of calls) {
      expect(method).toBe('agent.calibration.answer')
      expect(Object.keys(params as object).sort()).toEqual(['action', 'sessionId', 'step'])
    }
    expect(calls.map(([, p]) => (p as { step: number }).step)).toEqual([2, 3, 3])
  })

  it('omits the step for the two actions that answer no question', async () => {
    const { client, calls } = spied()
    await client.answer('sess-1', 'begin')
    await client.answer('sess-1', 'abandon')
    for (const [, params] of calls) {
      // Absent rather than zero: zero is a real step, and begin answering
      // step zero is a different claim from begin answering nothing.
      expect(Object.keys(params as object).sort()).toEqual(['action', 'sessionId'])
    }
  })

  it('asks for the pane list alone before a pane has been picked', async () => {
    const { client, calls } = spied()
    await client.read()
    await client.read('sess-1')
    expect(calls[0]).toEqual(['agent.calibration', {}])
    expect(calls[1]).toEqual(['agent.calibration', { sessionId: 'sess-1' }])
  })
})
