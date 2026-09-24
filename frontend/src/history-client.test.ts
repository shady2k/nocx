// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { queryHistory, subscribeHistoryRecorded } from './history-client'
import type { WSClient } from './ipc'

function fakeClient() {
  let listener: ((params: unknown) => void) | undefined
  const unsubscribe = vi.fn()
  const client = {
    call: vi.fn(),
    dispatcher: {
      subscribe: vi.fn((_method: string, handler: (params: unknown) => void) => {
        listener = handler
        return unsubscribe
      }),
    },
  }
  return {
    client: client as unknown as WSClient,
    call: client.call,
    subscribe: client.dispatcher.subscribe,
    unsubscribe,
    emit: (value: unknown) => listener?.(value),
  }
}

describe('subscribeHistoryRecorded', () => {
  it('binds the server session and rejects receipts for other sessions', () => {
    const f = fakeClient()
    const handler = vi.fn()
    const sub = subscribeHistoryRecorded(f.client, handler)

    f.emit({ sessionId: 'sid-other', attemptId: 'att-1' })
    expect(handler).not.toHaveBeenCalled()
    sub.bindSession('sid-current')
    f.emit({ sessionId: 'sid-other', attemptId: 'att-2' })
    f.emit({ sessionId: 'sid-current', attemptId: 'att-3', entryId: 'e3' })
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith({
      sessionId: 'sid-current',
      attemptId: 'att-3',
      entryId: 'e3',
    })
  })

  it('unsubscribes exactly once and rejects rebinding', () => {
    const f = fakeClient()
    const sub = subscribeHistoryRecorded(f.client, () => {})
    sub.bindSession('sid')
    expect(() => sub.bindSession('sid-again')).toThrow('already bound')
    sub.unsubscribe()
    sub.unsubscribe()
    expect(f.unsubscribe).toHaveBeenCalledTimes(1)
  })
})

describe('queryHistory', () => {
  it('uses the everywhere rung for text search and returns the server page', async () => {
    const f = fakeClient()
    const page = { entries: [], scope: 'everywhere', exhausted: true, source: 'store' }
    f.call.mockResolvedValue(page)
    await expect(queryHistory(f.client, 'pane', '/repo', '', 'deploy', 'pane-1')).resolves.toBe(
      page,
    )
    expect(f.call).toHaveBeenCalledWith(
      'history.query',
      { scope: 'everywhere', text: 'deploy' },
      undefined,
    )
  })

  it('keeps pane scope and identity when no text filter is present', async () => {
    const f = fakeClient()
    f.call.mockResolvedValue({ entries: [], scope: 'pane', exhausted: true, source: 'store' })
    await queryHistory(f.client, 'pane', '/repo', '', '', 'pane-1')
    expect(f.call).toHaveBeenCalledWith(
      'history.query',
      { scope: 'pane', paneId: 'pane-1' },
      undefined,
    )
  })
})
