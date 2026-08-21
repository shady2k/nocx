// @vitest-environment jsdom
// The one seam that ships command history over the control plane
// (nocx-rtg0.13). These tests pin the wire shape the renderer sends: the
// full fact set of a completed command, nothing the session owns and no
// output bytes — and the rung params history.query asks for.
import { describe, it, expect, vi } from 'vitest'
import { historyOutbox, recordCommand, queryHistory } from './history-client'
import type { CommandRecord } from './command-ledger'
import type { HistoryQuery } from './generated/history.query'
import type { ExecutionAttempt, LifecycleFact } from './lifecycle/state'
import { mintDomain, type IntegrationDomain } from './lifecycle/domains'
import type { WSClient } from './ipc'
import { log } from './log'
function fakeClient(): { call: ReturnType<typeof vi.fn> } {
  // The ack confirms the minted author (a schema-valid minimal shape); a
  // test that exercises the mismatch path overrides it.
  return { call: vi.fn().mockResolvedValue({ author: 'shell' }) }
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
    author: 'shell',
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
    void recordCommand(client as unknown as WSClient, 'tab-1', rec, completedAttempt())
    expect(client.call).toHaveBeenCalledTimes(1)
    const [method, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(method).toBe('history.record')
    expect(params).toEqual({
      command: 'make deploy',
      cwd: '/repo',
      host: '',
      author: 'shell',
      status: 'success',
      exitCode: 0,
      // performance.now() floats are rounded at the wire boundary — the
      // store persists int64.
      startedAt: 1000,
      endedAt: 1201,
      paneId: 'tab-1',
    })
  })

  it("the record's author crosses verbatim — minted at submit, never re-derived (nocx-iadtt)", () => {
    // An agent-submitted command's record carries author 'agent'; the wire
    // fact carries the same value, so the backend never has to derive the
    // author from a lane or a run state.
    const client = fakeClient()
    client.call.mockResolvedValue({ author: 'agent' })
    const rec = completedRecord({ author: 'agent' })
    void recordCommand(client as unknown as WSClient, 'tab-1', rec, completedAttempt())
    const [, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(params.author).toBe('agent')
  })

  it('never sends the session-owned fields (id, lineOf, disposed) or output', () => {
    const client = fakeClient()
    void recordCommand(
      client as unknown as WSClient,
      'tab-1',
      completedRecord(),
      completedAttempt(),
    )
    const [, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(params).not.toHaveProperty('id')
    expect(params).not.toHaveProperty('lineOf')
    expect(params).not.toHaveProperty('disposed')
    expect(params).not.toHaveProperty('output')
    expect(Object.keys(params as object).sort()).toEqual([
      'author',
      'command',
      'cwd',
      'endedAt',
      'exitCode',
      'host',
      'paneId',
      'startedAt',
      'status',
    ])
  })

  it('KEEPS a record the socket refused, and sends it when the socket is back', async () => {
    // The wiring check, not the outbox's own unit test (history-outbox.test.
    // ts has those): a record that failed must be IN the shared outbox, or
    // the queue is a module nothing reaches — the shape this repo has already
    // shipped once and paid for.
    const before = historyOutbox.stats().pending
    const down = { call: vi.fn().mockRejectedValue(new Error('socket closed')) }
    await recordCommand(down as unknown as WSClient, 'tab-1', completedRecord(), completedAttempt())
    expect(historyOutbox.stats().pending).toBe(before + 1)

    // And a drain delivers exactly what was kept, through the same call the
    // record was built with.
    const sent: unknown[] = []
    down.call.mockImplementation((method: string, params: unknown) => {
      sent.push([method, params])
      return Promise.resolve({ maskedCount: 0, maskedKinds: [], captures: [] })
    })
    await historyOutbox.drain()

    expect(historyOutbox.stats().pending).toBe(0)
    expect(sent).toHaveLength(before + 1)
    expect((sent[0] as unknown[])[0]).toBe('history.record')
  })

  it('swallows a rejected call: a dropped record never throws', async () => {
    const client = { call: vi.fn().mockRejectedValue(new Error('socket closed')) }
    await expect(
      new Promise<void>((resolve) => {
        void recordCommand(
          client as unknown as WSClient,
          'tab-1',
          completedRecord(),
          completedAttempt(),
        )
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
    void recordCommand(client as unknown as WSClient, 'tab-1', rec, completedAttempt())
    const [, params] = client.call.mock.calls[0] as [string, Record<string, unknown>]
    expect(params.command).toBe('make deploy {{secret:ci-token}}')
  })

  it('returns the ack when it confirms the minted author — the echo is the verification (nocx-iadtt)', async () => {
    const client = fakeClient()
    const ack = {
      author: 'shell',
      maskedCount: 0,
      maskedKinds: [],
      entryId: '',
      redactions: [],
      maskedCommand: 'make deploy',
      captures: [],
    }
    client.call.mockResolvedValue(ack)
    await expect(
      recordCommand(client as unknown as WSClient, 'tab-1', completedRecord(), completedAttempt()),
    ).resolves.toEqual(ack)
  })

  it('refuses an ack whose author contradicts the minted record — a wire-integrity failure, not a recoverable difference (nocx-iadtt)', async () => {
    const client = fakeClient()
    client.call.mockResolvedValue({ author: 'shell' })
    const warn = vi.spyOn(log, 'warn').mockImplementation(() => {})
    try {
      await expect(
        recordCommand(
          client as unknown as WSClient,
          'tab-1',
          completedRecord({ author: 'agent' }),
          completedAttempt(),
        ),
      ).resolves.toBeNull()
      expect(warn).toHaveBeenCalledTimes(1)
      expect(warn.mock.calls[0]?.[0]).toContain('ack author mismatch')
    } finally {
      warn.mockRestore()
    }
  })

  it('an open or abandoned attempt persists nothing — only a completed attempt is authority', async () => {
    const client = fakeClient()
    const rec = completedRecord()
    await expect(
      recordCommand(
        client as unknown as WSClient,
        'tab-1',
        rec,
        completedAttempt({ state: 'open' }),
      ),
    ).resolves.toBeNull()
    await expect(
      recordCommand(
        client as unknown as WSClient,
        'tab-1',
        rec,
        completedAttempt({ state: 'unknown' }),
      ),
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
