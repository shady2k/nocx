import { describe, expect, it, vi } from 'vitest'
import { mountRunCommandHandler, type AgentRunCompletion } from './run-command'
import type { Dispatcher } from './dispatcher'

// The renderer half of the run tool (nocx-tjppv): the backend asks
// (agent.runRequest), the renderer submits the command through the SAME
// submit path a person uses — block, ledger entry, attempt, artifact — and
// answers (agent.runResolved) with the completed run body. These tests
// mirror read-screen.test.ts: the handler mounts on the dispatcher, routes
// by session, and answers honestly when the content cannot submit.

function scriptedDispatcher() {
  const handlers = new Map<string, (params: unknown) => void>()
  const calls: { method: string; params: unknown }[] = []
  const dispatcher = {
    subscribe: (method: string, handler: (params: unknown) => void) => {
      handlers.set(method, handler)
      return () => handlers.delete(method)
    },
    call: vi.fn((method: string, params: unknown) => {
      calls.push({ method, params })
      return Promise.resolve({ refused: true })
    }),
  }
  return {
    ...dispatcher,
    handlers,
    calls,
  }
}

const completion: AgentRunCompletion = {
  entryId: 'entry-7',
  exitCode: 0,
  status: 'success',
  total: 2,
  start: 0,
  end: 2,
  text: 'file1\nfile2',
}

describe('mountRunCommandHandler — the renderer half of the run tool', () => {
  it('submits the command through the content seam and resolves completed with the run body', async () => {
    const d = scriptedDispatcher()
    const submitAgentCommand = vi.fn(() => Promise.resolve(completion))
    const content = { sessionId: () => 'session-a', submitAgentCommand }
    mountRunCommandHandler(d as unknown as Dispatcher, (sid) =>
      sid === 'session-a' ? content : null,
    )

    d.handlers.get('agent.runRequest')!({
      requestId: 'req-1',
      sessionId: 'session-a',
      command: 'ls -la',
    })

    await vi.waitFor(() => expect(submitAgentCommand).toHaveBeenCalledWith('ls -la'))
    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    expect(d.calls[0].method).toBe('agent.runResolved')
    const params = d.calls[0].params as Record<string, unknown>
    expect(params.requestId).toBe('req-1')
    expect(params.outcome).toBe('completed')
    expect(params.entryId).toBe('entry-7')
    expect(params.exitCode).toBe(0)
    expect(params.status).toBe('success')
    expect(params.text).toBe('file1\nfile2')
  })

  it('answers failed honestly when no tab holds the session', async () => {
    const d = scriptedDispatcher()
    mountRunCommandHandler(d as unknown as Dispatcher, () => null)

    d.handlers.get('agent.runRequest')!({ requestId: 'req-2', sessionId: 'gone', command: 'ls' })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    const params = d.calls[0].params as Record<string, unknown>
    expect(d.calls[0].method).toBe('agent.runResolved')
    expect(params.outcome).toBe('failed')
    expect(String(params.error)).toContain('no such session')
  })

  it('answers failed honestly when the submission itself fails', async () => {
    const d = scriptedDispatcher()
    const submitAgentCommand = vi.fn(() =>
      Promise.reject(new Error('the agent lane is not prompt-ready')),
    )
    mountRunCommandHandler(d as unknown as Dispatcher, () => ({
      sessionId: () => 'session-a',
      submitAgentCommand,
    }))

    d.handlers.get('agent.runRequest')!({
      requestId: 'req-3',
      sessionId: 'session-a',
      command: 'ls',
    })

    await vi.waitFor(() => expect(d.call).toHaveBeenCalled())
    const params = d.calls[0].params as Record<string, unknown>
    expect(d.calls[0].method).toBe('agent.runResolved')
    expect(params.outcome).toBe('failed')
    expect(String(params.error)).toContain('not prompt-ready')
  })

  it('ignores a malformed request (no requestId, sessionId or command)', async () => {
    const d = scriptedDispatcher()
    const submitAgentCommand = vi.fn()
    mountRunCommandHandler(d as unknown as Dispatcher, () => ({
      sessionId: () => 'session-a',
      submitAgentCommand,
    }))

    d.handlers.get('agent.runRequest')!({ requestId: 'req-4' })
    d.handlers.get('agent.runRequest')!({ sessionId: 'session-a' })
    d.handlers.get('agent.runRequest')!({ requestId: 'req-5', sessionId: 'session-a' })

    // Flush the microtask queue: a malformed request must schedule nothing,
    // so no waitFor is possible — the flush proves no async work was started.
    await Promise.resolve()
    await Promise.resolve()
    expect(submitAgentCommand).not.toHaveBeenCalled()
    expect(d.call).not.toHaveBeenCalled()
  })
})
