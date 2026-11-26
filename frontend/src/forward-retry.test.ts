// The retry policy on a taken local port (nocx-oalo1.13.5): the trigger is
// the wire's typed `address-in-use` cause, never the message. Both call
// sites — the Ports panel row and the palette's "Forward a port" — go through
// this one function; ports.test.tsx covers the panel wiring and this file
// pins the policy itself, including the exactly-once rule.

import { describe, expect, it, vi } from 'vitest'
import { RpcError } from './dispatcher'
import { openForwardWithFallback } from './forward-retry'
import type { TunnelOpenResult } from './generated/tunnel.open'

function record(port: number): TunnelOpenResult {
  return {
    id: `t${port}`,
    direction: 'local',
    requestedBind: { host: '127.0.0.1', port: 0 },
    actualBind: { host: '127.0.0.1', port },
    destination: 'host.example:6768',
    scope: 'pane:1',
    caveat: '',
    state: 'running',
    stopReason: null,
    error: null,
  }
}

/** The backend's typed refusal: the local port is held. */
function addressInUse(): RpcError {
  return new RpcError('listen tcp 127.0.0.1:6768: bind: address already in use', -32603, {
    reason: 'address-in-use',
  })
}

describe('openForwardWithFallback', () => {
  it('opens on the requested port when nothing is in the way', async () => {
    const open = vi.fn().mockResolvedValue(record(6768))

    await expect(openForwardWithFallback(open, 6768)).resolves.toEqual(record(6768))
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith(6768)
  })

  it('the typed address-in-use cause retries on an allocated port exactly once', async () => {
    const open = vi.fn().mockRejectedValueOnce(addressInUse()).mockResolvedValueOnce(record(43210))

    await expect(openForwardWithFallback(open, 6768)).resolves.toEqual(record(43210))
    expect(open).toHaveBeenCalledTimes(2)
    expect(open).toHaveBeenNthCalledWith(1, 6768)
    expect(open).toHaveBeenNthCalledWith(2, 0)
  })

  it('a second typed refusal is the answer, not another attempt', async () => {
    const open = vi.fn().mockRejectedValueOnce(addressInUse()).mockRejectedValueOnce(addressInUse())

    await expect(openForwardWithFallback(open, 6768)).rejects.toThrow(/address already in use/)
    expect(open).toHaveBeenCalledTimes(2)
  })

  it('a message saying EADDRINUSE without the typed cause never retries', async () => {
    // Exactly the prose the old regex matched; an untyped Error is not the
    // backend's cause and must not be treated as one.
    const open = vi.fn().mockRejectedValue(new Error('listen tcp 127.0.0.1:6768: bind: EADDRINUSE'))

    await expect(openForwardWithFallback(open, 6768)).rejects.toThrow(/EADDRINUSE/)
    expect(open).toHaveBeenCalledTimes(1)
  })

  it('another typed cause never retries', async () => {
    const open = vi.fn().mockRejectedValue(new RpcError('no such profile', -32603))

    await expect(openForwardWithFallback(open, 6768)).rejects.toThrow(/no such profile/)
    expect(open).toHaveBeenCalledTimes(1)
  })

  it('a caller already asking for port 0 has nothing to fall back to', async () => {
    const open = vi.fn().mockRejectedValue(addressInUse())

    await expect(openForwardWithFallback(open, 0)).rejects.toThrow(/address already in use/)
    expect(open).toHaveBeenCalledTimes(1)
  })
})
