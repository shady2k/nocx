// The renderer's far-helper press (ADR-727g63tr decision 2). It draws nothing
// yet: this is the typed call a later surface will make, and the answer it
// refuses rather than half-accepts.

import { describe, expect, it, vi } from 'vitest'

import type { WSClient } from '../ipc'
import { ConnectionClient } from './connection-client'

function clientWith(dispatcherCall: ReturnType<typeof vi.fn>): ConnectionClient {
  return new ConnectionClient({ dispatcher: { call: dispatcherCall } } as unknown as WSClient)
}

describe('ConnectionClient.refreshHelper', () => {
  it('presses with the pane and the observation epoch and answers the connection', async () => {
    const call = vi.fn().mockResolvedValue({ connectionId: 'conn-1', refreshed: true })
    const client = clientWith(call)

    await expect(client.refreshHelper('pane-1', 9)).resolves.toBe('conn-1')
    expect(call).toHaveBeenCalledWith('connection.helperRefresh', {
      paneId: 'pane-1',
      connectionEpoch: 9,
    })
  })

  it('refuses an answer that did not accept the refresh', async () => {
    for (const answer of [
      null,
      {},
      { connectionId: 'conn-1', refreshed: false },
      { refreshed: true },
    ]) {
      const client = clientWith(vi.fn().mockResolvedValue(answer))
      await expect(client.refreshHelper('pane-1', 9)).rejects.toThrow(
        'connection.helperRefresh did not accept the refresh',
      )
    }
  })
})
