// @vitest-environment jsdom

import { describe, it, expect, vi } from 'vitest'
import { AgentInputTarget } from './agent-ask'
import type { GrantBlock } from './ask-entry'
import type { AnswerBlockHandle, RunningBlockActions } from './scrollback/blocks'

class FakeDispatcher {
  calls: { method: string; params: unknown }[] = []
  private subs = new Map<string, (params: unknown) => void>()
  next = { run: 7, answerEntry: 'answer-1' }

  call<T = unknown>(method: string, params: unknown): Promise<T> {
    this.calls.push({ method, params })
    if (method === 'agent.ask') {
      const res = {
        runId: this.next.run,
        entryId: this.next.answerEntry,
        state: 'prepared',
        ingestSeq: 1,
        replayed: false,
        model: 'qwen3',
      }
      this.next.run += 1
      this.next.answerEntry = `answer-${this.next.run}`
      return Promise.resolve(res) as Promise<T>
    }
    return Promise.reject(new Error(`unexpected call ${method}`))
  }

  subscribe(method: string, handler: (params: unknown) => void): () => void {
    this.subs.set(method, handler)
    return () => this.subs.delete(method)
  }

  emit(method: string, params: unknown): void {
    this.subs.get(method)?.(params)
  }
}

function grant(
  itemId: string,
  command: string,
  state: GrantBlock['state'] = 'exited',
  window?: { start: number; count: number },
): GrantBlock {
  const blockEl = document.createElement('div')
  blockEl.className = state === 'running' ? 'cmd-block cmd-block-running' : 'cmd-block'
  blockEl.dataset.entryId = itemId
  return { itemId, blockEl, command, state, ...window }
}

function makeTarget(grants: GrantBlock[] = []) {
  const dispatcher = new FakeDispatcher()
  const handle: AnswerBlockHandle = {
    id: 1,
    el: document.createElement('div'),
    append: vi.fn(),
    toolCall: vi.fn(),
    reasoning: vi.fn(),
    close: vi.fn(),
  }
  const onRefusal = vi.fn()
  const target = new AgentInputTarget({
    dispatcher: dispatcher as never,
    cancel: vi.fn(() =>
      Promise.resolve({ runId: 0, state: 'cancelled' as const, cancelled: true as const }),
    ),
    sessionId: () => 'session-a',
    cwd: () => '/repo',
    grants: () => grants,
    openAnswer: vi.fn(() => handle),
    onRefusal,
  })
  return { dispatcher, handle, onRefusal, target }
}

describe('AgentInputTarget', () => {
  it('sends whole-block and row-window grant facts on the ask payload', async () => {
    const { dispatcher, handle, target } = makeTarget([
      grant('item-1', 'git status', 'running'),
      grant('item-2', 'npm test', 'exited', { start: 4, count: 3 }),
    ])
    await target.submit('what does this screen mean?')

    expect(dispatcher.calls.some((call) => call.method === 'agent.captureFrame')).toBe(false)
    const ask = dispatcher.calls.find((call) => call.method === 'agent.ask')
    const params = ask!.params as {
      question: string
      attachedContent: {
        itemId: string
        command: string
        state: string
        start?: number
        count?: number
      }[]
    }
    expect(params.question).toBe('what does this screen mean?')
    expect(params.attachedContent).toEqual([
      { itemId: 'item-1', command: 'git status', state: 'running' },
      { itemId: 'item-2', command: 'npm test', state: 'exited', start: 4, count: 3 },
    ])
    expect(ask!.params).not.toHaveProperty('rows')
    expect(handle.el.dataset.entryId).toBe('answer-1')
  })

  it('sends an explicit empty grant list for a general question', async () => {
    const { dispatcher, target } = makeTarget()
    await target.submit('what is the capital of France?')

    expect(dispatcher.calls.some((call) => call.method === 'agent.captureFrame')).toBe(false)
    const ask = dispatcher.calls.find((call) => call.method === 'agent.ask')
    expect((ask!.params as { attachedContent: unknown[] }).attachedContent).toEqual([])
  })

  it('routes runDelta to the run’s block by runId and entryId', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')
    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      blockId: 'text-1',
      seq: 0,
      text: 'hello',
    })
    expect(handle.append).toHaveBeenCalledWith('hello', 'text-1')
  })

  it('routes runDelta to the run’s block by runId and entryId', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      blockId: 'text-1',
      seq: 0,
      text: 'hello',
    })
    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      blockId: 'text-1',
      seq: 1,
      text: ' world',
    })
    expect(handle.append).toHaveBeenCalledTimes(2)
    expect(handle.append).toHaveBeenNthCalledWith(1, 'hello', 'text-1')
    expect(handle.append).toHaveBeenNthCalledWith(2, ' world', 'text-1')
  })

  it('hands on the BLOCK the chunk belongs to, so the renderer never cuts prose itself', async () => {
    // The boundary between two runs of prose is the BACKEND's (ADR-0040):
    // it opens a `text` child on the first delta after a call and seals it
    // when the next call arrives. The renderer's only job is to notice that
    // the id changed — which it cannot do if the id never reaches it.
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      blockId: 'text-1',
      seq: 0,
      text: 'before',
    })
    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      blockId: 'text-2',
      seq: 1,
      text: 'after',
    })
    expect(handle.append).toHaveBeenNthCalledWith(1, 'before', 'text-1')
    expect(handle.append).toHaveBeenNthCalledWith(2, 'after', 'text-2')
  })

  it('routes a tool call to the run’s block, ahead of the deltas written from it', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('what went wrong?')

    // The order the backend emits is the order the block is driven in — the
    // whole point of putting the call on the wire (nocx-shxv0).
    dispatcher.emit('agent.runToolCall', {
      runId: 7,
      entryId: 'answer-1',
      callId: 'call_1',
      tool: 'files.read',
      args: { path: '/repo/a.txt', start: 3 },
      effect: 'observe',
      actionEntryId: 'entry-action-1',
      resource: { kind: 'path', id: '/repo/a.txt' },
    })
    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      blockId: 'text-1',
      seq: 0,
      text: 'line 3',
    })

    // The ARGUMENTS come with it: they are what tells two calls of one tool
    // apart, and the block that draws the call is named from them
    // (ADR-0040).
    expect(handle.toolCall).toHaveBeenCalledWith({
      callId: 'call_1',
      tool: 'files.read',
      args: { path: '/repo/a.txt', start: 3 },
      effect: 'observe',
      resource: { kind: 'path', id: '/repo/a.txt' },
    })
    const callOrder = (handle.toolCall as ReturnType<typeof vi.fn>).mock.invocationCallOrder[0]
    const deltaOrder = (handle.append as ReturnType<typeof vi.fn>).mock.invocationCallOrder[0]
    expect(callOrder).toBeLessThan(deltaOrder)
  })

  it('a call that named no resource is handed on as absent, never as an empty one', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('is the tree clean?')

    dispatcher.emit('agent.runToolCall', {
      runId: 7,
      entryId: 'answer-1',
      callId: 'call_9',
      tool: 'git.status',
      args: {},
      effect: 'observe',
      actionEntryId: 'entry-action-9',
      resource: null,
    })
    expect(handle.toolCall).toHaveBeenCalledWith({
      callId: 'call_9',
      tool: 'git.status',
      args: {},
      effect: 'observe',
      resource: undefined,
    })
  })

  it('routes reasoning to the run’s block, and never through append', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('why?')

    dispatcher.emit('agent.runReasoning', { runId: 7, entryId: 'answer-1', text: 'thinking...' })
    expect(handle.reasoning).toHaveBeenCalledWith('thinking...')
    expect(handle.append).not.toHaveBeenCalled()
  })

  it('ignores a tool call and a reasoning chunk whose entryId does not match', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runToolCall', {
      runId: 7,
      entryId: 'some-other-answer',
      callId: 'call_1',
      tool: 'files.read',
      effect: 'observe',
      actionEntryId: 'entry-action-1',
    })
    dispatcher.emit('agent.runReasoning', {
      runId: 7,
      entryId: 'some-other-answer',
      text: 'stale',
    })
    expect(handle.toolCall).not.toHaveBeenCalled()
    expect(handle.reasoning).not.toHaveBeenCalled()
  })

  it('ignores a delta whose entryId does not match the run’s answer entry', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'some-other-answer',
      seq: 0,
      text: 'stale',
    })
    expect(handle.append).not.toHaveBeenCalled()
  })

  it('passes the live turn Stop action through to agent.cancel with the minted run id', async () => {
    const dispatcher = new FakeDispatcher()
    const handle: AnswerBlockHandle = {
      id: 1,
      el: document.createElement('div'),
      append: vi.fn(),
      toolCall: vi.fn(),
      reasoning: vi.fn(),
      close: vi.fn(),
    }
    let actions: RunningBlockActions | undefined
    const cancel = vi.fn(() =>
      Promise.resolve({ runId: 7, state: 'cancelled' as const, cancelled: true as const }),
    )
    const target = new AgentInputTarget({
      dispatcher: dispatcher as never,
      sessionId: () => 'session-a',
      cwd: () => '/repo',
      grants: () => [],
      openAnswer: vi.fn(
        (_question: string, _cwd: string, provided?: RunningBlockActions): AnswerBlockHandle => {
          actions = provided
          return handle
        },
      ),
      cancel,
      onRefusal: vi.fn(),
    })

    await target.submit('stop this turn')
    actions?.stop()
    await Promise.resolve()

    expect(cancel).toHaveBeenCalledTimes(1)
    expect(cancel).toHaveBeenCalledWith(7)
    expect(dispatcher.calls.some((call) => call.method === 'agent.cancel')).toBe(false)
    dispatcher.emit('agent.runState', { runId: 7, state: 'cancelled' })
    expect(handle.close).toHaveBeenCalledWith('cancelled')
  })

  it('closes the block completed on the terminal state; failed carries the renderable reason', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runState', { runId: 7, state: 'completed' })
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3')
    expect(handle.close).toHaveBeenCalledTimes(1)

    await target.submit('q2')
    dispatcher.emit('agent.runState', {
      runId: 8,
      state: 'failed',
      error: 'the model returned no text',
    })
    expect(handle.close).toHaveBeenLastCalledWith('failure', 'the model returned no text')

    await target.submit('q3')
    dispatcher.emit('agent.runState', { runId: 9, state: 'cancelled' })
    expect(handle.close).toHaveBeenLastCalledWith('cancelled')
  })

  it('closes the block on a runState with NO prior delta (failure before any text)', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')
    dispatcher.emit('agent.runState', {
      runId: 7,
      state: 'failed',
      error: 'the connection was lost',
    })
    expect(handle.close).toHaveBeenCalledWith('failure', 'the connection was lost')
  })
})

describe('AgentInputTarget waiting seam (nocx-ex636)', () => {
  it('opens the answer block BEFORE the ask resolves, so the wait covers the run-start RPC', async () => {
    const dispatcher = new FakeDispatcher()
    const handle: AnswerBlockHandle = {
      id: 1,
      el: document.createElement('div'),
      append: vi.fn(),
      toolCall: vi.fn(),
      reasoning: vi.fn(),
      close: vi.fn(),
    }
    const openAnswer = vi.fn(() => handle)
    const target = new AgentInputTarget({
      dispatcher: dispatcher as never,
      cancel: vi.fn(() =>
        Promise.resolve({ runId: 0, state: 'cancelled' as const, cancelled: true as const }),
      ),
      sessionId: () => 's',
      cwd: () => '/',
      grants: () => [],
      openAnswer,
      onRefusal: vi.fn(),
    })
    // The ask is still in flight the moment submit returns its promise:
    // the block already exists — a slow run start is not silence.
    const pending = target.submit('q')
    expect(openAnswer).toHaveBeenCalledTimes(1)
    await pending
  })
  it('a refusal removes the speculative block — no run, no entry, no phantom question', async () => {
    const failDispatcher = {
      calls: [] as { method: string; params: unknown }[],
      call<T = unknown>(method: string): Promise<T> {
        if (method === 'agent.captureFrame') {
          return Promise.resolve({ frameId: 'frame-1' }) as Promise<T>
        }
        const err = new Error('no endpoint configured') as Error & { code?: number }
        err.code = -32603
        return Promise.reject(err)
      },
      subscribe: () => () => {},
    }
    const el = document.createElement('div')
    document.body.appendChild(el)
    const handle: AnswerBlockHandle = {
      id: 1,
      el,
      append: vi.fn(),
      toolCall: vi.fn(),
      reasoning: vi.fn(),
      close: vi.fn(),
    }
    const openAnswer = vi.fn(() => handle)
    const onRefusal = vi.fn()
    const target = new AgentInputTarget({
      dispatcher: failDispatcher as never,
      cancel: vi.fn(() =>
        Promise.resolve({ runId: 0, state: 'cancelled' as const, cancelled: true as const }),
      ),
      sessionId: () => 's',
      cwd: () => '/',
      grants: () => [],
      openAnswer,
      onRefusal,
    })
    await expect(target.submit('q')).rejects.toThrow('no endpoint configured')
    expect(openAnswer).toHaveBeenCalledTimes(1)
    expect(onRefusal).toHaveBeenCalledWith('no endpoint configured')
    // The block that was opened in case the ask succeeded is gone.
    expect(el.isConnected).toBe(false)
  })
})

describe('AgentInputTarget refusal', () => {
  it('surfaces a no-endpoint refusal through onRefusal — the renderable condition, not a silent throw', async () => {
    const failDispatcher = {
      calls: [] as { method: string; params: unknown }[],
      call<T = unknown>(method: string): Promise<T> {
        if (method === 'agent.captureFrame') {
          return Promise.resolve({ frameId: 'frame-1' }) as Promise<T>
        }
        const err = new Error('no endpoint configured') as Error & { code?: number }
        err.code = -32603
        return Promise.reject(err)
      },
      subscribe: () => () => {},
    }
    const handle: AnswerBlockHandle = {
      id: 1,
      el: document.createElement('div'),
      append: vi.fn(),
      toolCall: vi.fn(),
      reasoning: vi.fn(),
      close: vi.fn(),
    }
    const onRefusal = vi.fn()
    const target = new AgentInputTarget({
      dispatcher: failDispatcher as never,
      cancel: vi.fn(() =>
        Promise.resolve({ runId: 0, state: 'cancelled' as const, cancelled: true as const }),
      ),
      sessionId: () => 's',
      cwd: () => '/',
      grants: () => [],
      openAnswer: () => handle,
      onRefusal,
    })
    await expect(target.submit('q')).rejects.toThrow('no endpoint configured')
    expect(onRefusal).toHaveBeenCalledWith('no endpoint configured')
  })
})

describe('AgentInputTarget approval routing', () => {
  it('awaiting_approval keeps the block open and routable; the resume closes it (nocx-z9hj4)', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('will this need approval?')
    const runId = dispatcher.next.run - 1 // the run the ask minted
    expect(handle.el.dataset.entryId).toBe('answer-1')

    // The run suspends: the block stays OPEN (nothing closed) and the run
    // stays routable — the question is being decided elsewhere.
    dispatcher.emit('agent.runState', { runId, state: 'awaiting_approval' })
    expect(handle.close).not.toHaveBeenCalled()

    // The person approves; the resumed run streams into the SAME block —
    // a run deleted at awaiting_approval would drop these deltas.
    dispatcher.emit('agent.runDelta', {
      runId,
      entryId: 'answer-1',
      blockId: 'text-1',
      seq: 0,
      text: 'approved answer',
    })
    expect(handle.append).toHaveBeenCalledWith('approved answer', 'text-1')

    // The run completes: the block closes once.
    dispatcher.emit('agent.runState', { runId, state: 'completed' })
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3')
  })
})

describe('AgentInputTarget dropped-delta gap (nocx-dw3.1)', () => {
  it('marks the block when the terminal state names dropped chunks, then closes with the run’s earned state', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('will the stream hold?')
    const runId = dispatcher.next.run - 1 // the run the ask minted

    // Some deltas got through…
    dispatcher.emit('agent.runDelta', {
      runId,
      entryId: 'answer-1',
      seq: 0,
      text: 'partial answer',
    })

    // …and the terminal state names the count. The block marks the gap —
    // the full answer is saved, the live view is the incomplete part — and
    // closes SUCCESS: a dropped live delta is a visible bound, never a
    // reason to fail a run whose durable answer is whole (nocx-dw3.1).
    dispatcher.emit('agent.runState', { runId, state: 'completed', droppedDeltas: 2 })
    expect(handle.append).toHaveBeenCalledWith(
      '— 2 chunks of the answer were dropped while streaming; the full answer was saved —',
    )
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3')
  })

  it('names a single dropped chunk in the singular, and a clean close marks nothing', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')
    const runId = dispatcher.next.run - 1
    dispatcher.emit('agent.runState', { runId, state: 'completed', droppedDeltas: 1 })
    expect(handle.append).toHaveBeenCalledWith(
      '— part of the answer was dropped while streaming; the full answer was saved —',
    )

    // A completed run with no drops keeps the happy path's shape: no
    // marker, no body change — the gap sentence must never appear on an
    // intact stream.
    const clean = makeTarget()
    await clean.target.submit('clean')
    clean.dispatcher.emit('agent.runState', {
      runId: clean.dispatcher.next.run - 1,
      state: 'completed',
    })
    expect(clean.handle.append).not.toHaveBeenCalled()
    expect(clean.handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3')
  })

  it('marks the gap on a failed run too — the reason and the gap are both facts', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')
    const runId = dispatcher.next.run - 1
    dispatcher.emit('agent.runState', {
      runId,
      state: 'failed',
      error: 'the model stopped mid-answer',
      droppedDeltas: 1,
    })
    expect(handle.append).toHaveBeenCalledWith(
      '— part of the answer was dropped while streaming; the full answer was saved —',
    )
    expect(handle.close).toHaveBeenCalledWith('failure', 'the model stopped mid-answer')
  })
})
