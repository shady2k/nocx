// The readScreen pull handler (nocx-ljfwz): a request arrives on the
// dispatcher, the renderer produces the frame, and the resolution is sent —
// asserted from the renderer side (criterion 4: the renderer's half is a
// real handler). The dispatcher and the content are scripted; the frame is
// a real minted frame (the same code the wire conversion runs on).

import { describe, expect, it, vi } from 'vitest'
import { mountReadScreenHandler } from './read-screen'
import { CaptureAbortedError, ReadScreenRangeError } from './frame/capture-identity'
import { mintLiveFrame } from './frame/mint'
import { CaptureIdentityTracker } from './frame/capture-identity'
import { seedSource } from './frame/test-source'
import { DEFAULT_SNAPSHOT } from './scrollback/serializer'
import type { CapturedFrame } from './frame/types'
import type { Dispatcher } from './dispatcher'

function mintedFrame(texts: string[]): CapturedFrame {
  const source = seedSource(texts)
  source.cols = texts[0].length
  source.rows = texts.length
  const tracker = new CaptureIdentityTracker(source)
  const identity = tracker.identity()
  return mintLiveFrame(
    identity,
    { start: 0, end: texts.length },
    {
      getLine: (y) => source.getBufferLine(y),
      cursor: { line: 0, col: 0 },
      snapshot: DEFAULT_SNAPSHOT,
    },
  )
}

interface ScriptedDispatcher {
  subscribe: (method: string, h: (params: unknown) => void) => () => void
  call: ReturnType<typeof vi.fn>
  calls: { method: string; params: unknown }[]
  handlers: Map<string, (params: unknown) => void>
}

function scriptedDispatcher(): ScriptedDispatcher {
  const handlers = new Map<string, (params: unknown) => void>()
  const calls: { method: string; params: unknown }[] = []
  const subscribe = (method: string, h: (params: unknown) => void) => {
    handlers.set(method, h)
    return () => {
      handlers.delete(method)
    }
  }
  const call = vi.fn((method: string, params: unknown) => {
    calls.push({ method, params })
    return Promise.resolve({})
  })
  return { subscribe, call, calls, handlers }
}

function contentFor(sessionId: string, frame: CapturedFrame) {
  return {
    sessionId: () => sessionId,
    captureLiveFrame: vi.fn(() => Promise.resolve(frame)),
  }
}

describe('mountReadScreenHandler — the renderer half of the pull', () => {
  it('answers a request with the frame, produced by the same mint and wire code', async () => {
    const d = scriptedDispatcher()
    const frame = mintedFrame(['hello', 'world'])
    const content = contentFor('session-a', frame)
    mountReadScreenHandler(d as unknown as Dispatcher, (sid) =>
      sid === 'session-a' ? content : null,
    )

    d.handlers.get('agent.readScreenRequest')!({
      requestId: 'req-1',
      sessionId: 'session-a',
      region: { start: 1, end: 2 },
    })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    const method = d.calls[0].method
    const params = d.calls[0].params as Record<string, unknown>
    expect(method).toBe('agent.readScreenResolved')
    expect(params.requestId).toBe('req-1')
    expect(params.outcome).toBe('frame')
    // The frame body is the minted frame's cells + cursor + identity + range
    // — the same vocabulary the captureFrame push uses. Assert the chars in
    // order and the identity facts; the per-cell attributes are the
    // serializer's own output, asserted elsewhere.
    const rows = params.rows as { kind: string; cells: { char: string }[] }[]
    expect(rows.map((r) => r.cells.map((c) => c.char).join(''))).toEqual(['hello', 'world'])
    expect(rows.every((r) => r.kind === 'cells')).toBe(true)
    const identity = params.identity as {
      buffer: { kind: string }
      cols: number
      rows: number
      generation: number
    }
    expect(identity).toMatchObject({ buffer: { kind: 'normal' }, cols: 5, rows: 2 })
    expect(typeof identity.generation).toBe('number')
    expect(params.range).toEqual({ start: 0, end: 2 })
    expect(content.captureLiveFrame).toHaveBeenCalledWith({ start: 1, end: 2 })
  })

  it('answers a request for an unknown session as failed, honestly — never a hang', async () => {
    const d = scriptedDispatcher()
    mountReadScreenHandler(d as unknown as Dispatcher, () => null)

    d.handlers.get('agent.readScreenRequest')!({ requestId: 'req-2', sessionId: 'gone' })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    const params = d.calls[0].params as Record<string, unknown>
    expect(params.outcome).toBe('failed')
    expect(String(params.error)).toContain('no such session: gone')
  })

  it('answers a capture the renderer cannot produce as failed, with the reason', async () => {
    const d = scriptedDispatcher()
    const content = {
      sessionId: () => 'session-a',
      captureLiveFrame: vi.fn(() =>
        Promise.reject(
          new ReadScreenRangeError('region [100, 200) is past the end of the buffer (30 rows)'),
        ),
      ),
    }
    mountReadScreenHandler(d as unknown as Dispatcher, () => content)

    d.handlers.get('agent.readScreenRequest')!({ requestId: 'req-3', sessionId: 'session-a' })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    const params = d.calls[0].params as Record<string, unknown>
    expect(params.outcome).toBe('failed')
    expect(String(params.error)).toContain('past the end of the buffer')
  })

  it('answers a capture aborted by disposal as failed, not a hang', async () => {
    const d = scriptedDispatcher()
    const content = {
      sessionId: () => 'session-a',
      captureLiveFrame: vi.fn(() => Promise.reject(new CaptureAbortedError())),
    }
    mountReadScreenHandler(d as unknown as Dispatcher, () => content)

    d.handlers.get('agent.readScreenRequest')!({ requestId: 'req-4', sessionId: 'session-a' })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    const params = d.calls[0].params as Record<string, unknown>
    expect(params.outcome).toBe('failed')
    expect(String(params.error)).toContain('frame capture aborted')
  })

  it('a resolution the broker refuses is not a crash — a stale request answers itself', async () => {
    const d = scriptedDispatcher()
    d.call.mockRejectedValueOnce(new Error('Unknown request id'))
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const content = contentFor('session-a', mintedFrame(['x']))
    mountReadScreenHandler(d as unknown as Dispatcher, () => content)

    d.handlers.get('agent.readScreenRequest')!({ requestId: 'req-stale', sessionId: 'session-a' })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })
})
