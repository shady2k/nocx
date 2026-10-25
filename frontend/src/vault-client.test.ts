// VaultClient contract tests: the resolveLine seam (the renderer half of
// "the ledger keeps the reference") and the widened RPC boundary — the
// client must speak over ANY caller that can route control-plane methods
// (the real Dispatcher in the app, WSClient's `call` in TerminalContent),
// so the constructor type is the seam, tested through a fake.
import { describe, it, expect } from 'vitest'
import { VaultClient, type VaultRpc } from './vault-client'

/** A fake control-plane seam that records every request. */
function fakeRpc(
  resolveWith?: unknown,
): VaultRpc & { calls: Array<{ method: string; params: unknown }> } {
  const calls: Array<{ method: string; params: unknown }> = []
  const call = <T>(method: string, params: unknown): Promise<T> => {
    calls.push({ method, params })
    return Promise.resolve(resolveWith as T)
  }
  const rpc: VaultRpc = { call }
  return Object.assign(rpc, { calls })
}

describe('VaultClient inventory intent', () => {
  it('marks only the passive inventory read as a report on the shared RPC seam', async () => {
    const calls: unknown[][] = []
    const client = new VaultClient({
      call: <T>(...args: Parameters<VaultRpc['call']>): Promise<T> => {
        calls.push(args)
        return Promise.resolve({ entries: [] } as T)
      },
    })
    await expect(client.reportInventory()).resolves.toEqual({ entries: [] })
    await expect(client.inventory()).resolves.toEqual({ entries: [] })
    expect(calls).toEqual([
      ['vault.inventory', {}, undefined, 'report'],
      ['vault.inventory', {}],
    ])
  })
})

describe('VaultClient.resolveLine', () => {
  it('sends the exact { line } request', async () => {
    const rpc = fakeRpc({ line: 'x', refs: [] })
    const client = new VaultClient(rpc)
    await client.resolveLine('curl -H "Authorization: Bearer {{secret:openai-key}}" https://x')
    expect(rpc.calls).toEqual([
      {
        method: 'vault.resolveLine',
        params: { line: 'curl -H "Authorization: Bearer {{secret:openai-key}}" https://x' },
      },
    ])
  })

  it('resolves with the generated wire type', async () => {
    const rpc = fakeRpc({
      line: 'curl -H "Authorization: Bearer sk-live-abc" https://x',
      refs: [{ name: 'openai-key', resolved: true }],
    })
    const client = new VaultClient(rpc)
    await expect(client.resolveLine('x')).resolves.toEqual({
      line: 'curl -H "Authorization: Bearer sk-live-abc" https://x',
      refs: [{ name: 'openai-key', resolved: true }],
    })
  })
})

describe('VaultClient over the widened RPC seam', () => {
  it('constructs over a bare { call } double (the WSClient shape)', async () => {
    // WSClient satisfies VaultRpc structurally: `call` only, no sealed hook.
    const calls: unknown[] = []
    const rpc: VaultRpc = {
      call: <T>(method: string, params: unknown): Promise<T> => {
        calls.push(method, params)
        return Promise.resolve({ entries: [] } as T)
      },
    }
    const client = new VaultClient(rpc)
    await expect(client.inventory()).resolves.toEqual({ entries: [] })
  })

  it('keeps the sealed hook optional and forwardable (the dispatcher shape)', () => {
    const onVaultSealed = () => Promise.resolve()
    const rpc: VaultRpc = {
      call: <T>(method: string, params: unknown): Promise<T> => {
        void method
        void params
        return Promise.resolve({} as T)
      },
      onVaultSealed,
    }
    const client = new VaultClient(rpc)
    expect(client.dispatcher.onVaultSealed).toBe(onVaultSealed)
  })

  it('createSecret sends name, kind and value', async () => {
    const rpc = fakeRpc({})
    const client = new VaultClient(rpc)
    await client.createSecret({ name: 'openai-key', kind: 'password', value: 'sk-live-abc' })
    expect(rpc.calls).toEqual([
      {
        method: 'vault.createSecret',
        params: { name: 'openai-key', kind: 'password', value: 'sk-live-abc' },
      },
    ])
  })

  it('unlockResolved sends requestId and outcome', async () => {
    const rpc = fakeRpc({})
    const client = new VaultClient(rpc)
    await client.unlockResolved({ requestId: 'abc123', outcome: 'unsealed' })
    expect(rpc.calls).toEqual([
      {
        method: 'vault.unlockResolved',
        params: { requestId: 'abc123', outcome: 'unsealed' },
      },
    ])
  })
})
