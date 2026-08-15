// @vitest-environment jsdom
// The one seam that ships command history over the control plane
// (nocx-rtg0.13). These tests pin the wire shape the renderer sends: the
// full fact set of a completed command, nothing the session owns and no
// output bytes — and the rung params history.query asks for.
import { describe, it, expect, vi } from 'vitest'
import { recordCommand, queryHistory } from './history-client'
import type { CommandRecord } from './command-ledger'
import type { HistoryQuery } from './generated/history.query'
import type { ExecutionAttempt, LifecycleFact } from './lifecycle/state'
import { mintDomain, type IntegrationDomain } from './lifecycle/domains'
import type { WSClient } from './ipc'
function fakeClient(): { call: ReturnType<typeof vi.fn> } {
  return { call: vi.fn().mockResolvedValue({}) }
}

// The authority: an authenticated attempt. Its domain is branded (only the
// kernel can mint one); its command is the SHELL's wire line, which may
// carry vault-resolved values — and must never cross to the store.
const FACT: LifecycleFact = { lane: 'l', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 }
const domain = mintDomain(FACT) as IntegrationDomain

function completedAttempt(overrides: Partial<ExecutionAttempt> = {}): ExecutionAttempt {
  return {
    id: 'att-7',
    domain,
    state: 'completed',
    exitCode: 0,
    command: 'make deploy --token sk-live-1234',
    origin: 'shell',
    startedAt: '2026-08-08T12:00:00Z',
    completedAt: '2026-08-08T12:00:02Z',
    fence: 'a'.repeat(64),
    ...overrides,
  }
}

function completedRecord(overrides: Partial<CommandRecord> = {}): CommandRecord {
  return {
    id: 7,
    command: 'make deploy',
    cwd: '/repo',
    host: '',
    status: 'success',
    exitCode: 0,
    startedAt: 1000.4,
    endedAt: 1200.6,
    lineOf: () => 3,
    disposed: false,
    ...overrides,
  }
}

describe('recordCommand', () => {
  it('sends the full fact set over history.record, timestamps rounded to ints', () => {
    const client = fakeClient()
    const rec = completedRecord()
    void recordCommand(client as unknown as WSClient, rec, completedAttempt())
    expect(client.call).toHaveBeenCalledTimes(1)
    const [method, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(method).toBe('history.record')
    expect(params).toEqual({
      command: 'make deploy',
      cwd: '/repo',
      host: '',
      status: 'success',
      exitCode: 0,
      // performance.now() floats are rounded at the wire boundary — the
      // store persists int64.
      startedAt: 1000,
      endedAt: 1201,
    })
  })

  it('never sends the session-owned fields (id, lineOf, disposed) or output', () => {
    const client = fakeClient()
    void recordCommand(client as unknown as WSClient, completedRecord(), completedAttempt())
    const [, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(params).not.toHaveProperty('id')
    expect(params).not.toHaveProperty('lineOf')
    expect(params).not.toHaveProperty('disposed')
    expect(params).not.toHaveProperty('output')
    expect(Object.keys(params as object).sort()).toEqual([
      'command',
      'cwd',
      'endedAt',
      'exitCode',
      'host',
      'startedAt',
      'status',
    ])
  })

  it('swallows a rejected call: a dropped record never throws', async () => {
    const client = { call: vi.fn().mockRejectedValue(new Error('socket closed')) }
    await expect(
      new Promise<void>((resolve) => {
        void recordCommand(client as unknown as WSClient, completedRecord(), completedAttempt())
        resolve()
      }),
    ).resolves.toBeUndefined()
  })

  it('persists the record command, never the attempt command — the privacy rule (ADR-0024 §5)', () => {
    // The record carries the app-owned text (references intact); the
    // attempt's command is the shell's wire line, which may carry
    // vault-resolved values. What crosses is the record's text.
    const client = fakeClient()
    const rec = completedRecord({ command: 'make deploy {{secret:ci-token}}' })
    void recordCommand(client as unknown as WSClient, rec, completedAttempt())
    const [, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(params.command).toBe('make deploy {{secret:ci-token}}')
  })

  it('an open or abandoned attempt persists nothing — only a completed attempt is authority', async () => {
    const client = fakeClient()
    const rec = completedRecord()
    await expect(
      recordCommand(client as unknown as WSClient, rec, completedAttempt({ state: 'open' })),
    ).resolves.toBeNull()
    await expect(
      recordCommand(client as unknown as WSClient, rec, completedAttempt({ state: 'unknown' })),
    ).resolves.toBeNull()
    expect(client.call).not.toHaveBeenCalled()
  })
})

describe('queryHistory', () => {
  it('directory rung carries cwd and host', async () => {
    const client = fakeClient()
    client.call.mockResolvedValue({
      entries: [],
      scope: 'directory',
      exhausted: true,
      source: 'store',
    })
    await queryHistory(client as unknown as WSClient, 'directory', '/repo', '')
    expect(client.call).toHaveBeenCalledWith('history.query', {
      scope: 'directory',
      cwd: '/repo',
      host: '',
    })
  })

  it('host rung carries host only', async () => {
    const client = fakeClient()
    client.call.mockResolvedValue({ entries: [], scope: 'host', exhausted: true, source: 'store' })
    await queryHistory(client as unknown as WSClient, 'host', '/repo', 'prod')
    expect(client.call).toHaveBeenCalledWith('history.query', { scope: 'host', host: 'prod' })
  })

  it('everywhere rung carries nothing but the scope', async () => {
    const client = fakeClient()
    client.call.mockResolvedValue({
      entries: [],
      scope: 'everywhere',
      exhausted: true,
      source: 'store',
    })
    await queryHistory(client as unknown as WSClient, 'everywhere', '/repo', '')
    expect(client.call).toHaveBeenCalledWith('history.query', { scope: 'everywhere' })
  })

  it('text filter rides on every rung when present', async () => {
    const client = fakeClient()
    client.call.mockResolvedValue({
      entries: [],
      scope: 'everywhere',
      exhausted: true,
      source: 'store',
      coverage: null,
    })
    await queryHistory(client as unknown as WSClient, 'everywhere', '/repo', '', 'deploy')
    expect(client.call).toHaveBeenCalledWith('history.query', {
      scope: 'everywhere',
      text: 'deploy',
    })
  })

  it('omits text when empty: no filter is one state on the wire, not two', async () => {
    const client = fakeClient()
    client.call.mockResolvedValue({
      entries: [],
      scope: 'everywhere',
      exhausted: true,
      source: 'store',
      coverage: null,
    })
    await queryHistory(client as unknown as WSClient, 'everywhere', '/repo', '', '')
    expect(client.call).toHaveBeenCalledWith('history.query', { scope: 'everywhere' })
  })

  it('returns the store page with its coverage', async () => {
    const client = fakeClient()
    const page: HistoryQuery = {
      entries: [
        {
          id: '9',
          command: 'ls',
          cwd: '/repo',
          host: '',
          status: 'success',
          maskedCount: 0,
          maskedKinds: [],
          startedAt: 1,
          endedAt: 2,
        },
      ],
      scope: 'directory',
      exhausted: true,
      source: 'store',
      coverage: 1,
    }
    client.call.mockResolvedValue(page)
    const got = await queryHistory(client as unknown as WSClient, 'directory', '/repo', '')
    expect(got.coverage).toBe(1)
    expect(got.entries[0]?.startedAt).toBe(1)
  })

  it('returns the store page the socket answered', async () => {
    const client = fakeClient()
    const page: HistoryQuery = {
      entries: [
        {
          id: '9',
          command: 'ls',
          cwd: '/repo',
          host: '',
          status: 'success',
          endedAt: 1,
          maskedCount: 0,
          maskedKinds: [],
        },
      ],
      scope: 'directory',
      exhausted: true,
      source: 'store',
      coverage: null,
    }
    client.call.mockResolvedValue(page)
    const got = await queryHistory(client as unknown as WSClient, 'directory', '/repo', '')
    expect(got).toBe(page)
  })
})
