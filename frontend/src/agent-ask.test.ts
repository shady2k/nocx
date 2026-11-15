// @vitest-environment jsdom

import { describe, it, expect, vi } from 'vitest'
import { AgentInputTarget } from './agent-ask'
import type { GrantBlock } from './ask-entry'
import type { AnswerBlockHandle, RunningBlockActions } from './scrollback/blocks'
import { BlockNotice } from './ui/block-notice'
import { PersonMessage, type PersonMessageState } from './ui/person-message'

class FakeDispatcher {
  calls: { method: string; params: unknown }[] = []
  /** What agent.steer answers: into the turn, or kept for the next question. */
  steerOutcome: 'steered' | 'next_question' = 'steered'
  private subs = new Map<string, (params: unknown) => void>()
  private refusals = new Map<string, string>()
  next = { run: 7, answerEntry: 'answer-1' }

  /** The next call of `method` answers with an error, as a backend refusal. */
  refuse(method: string, message: string): void {
    this.refusals.set(method, message)
  }

  call<T = unknown>(method: string, params: unknown): Promise<T> {
    this.calls.push({ method, params })
    const refusal = this.refusals.get(method)
    if (refusal !== undefined) {
      this.refusals.delete(method)
      return Promise.reject(new Error(refusal))
    }
    if (method === 'agent.steer') {
      const { runId } = params as { runId: number }
      return Promise.resolve({ runId, outcome: this.steerOutcome }) as Promise<T>
    }
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
  // The lines the turn drew for the person's messages, in the order they were
  // sent: the surface restates exactly these when a cancel drops one.
  const steeredLines: PersonMessage[] = []
  const handle: AnswerBlockHandle = {
    id: 1,
    el: document.createElement('div'),
    append: vi.fn(),
    toolCall: vi.fn(),
    reasoning: vi.fn(),
    notice: vi.fn(() => new BlockNotice({ text: '' })),
    steer: vi.fn((text: string, state: PersonMessageState = 'steered') => {
      const line = new PersonMessage(
        text,
        { label: 'You', aria: 'your message', queued: 'asked when this answer ends' },
        state,
      )
      steeredLines.push(line)
      return line
    }),
    close: vi.fn(),
  }
  const onRefusal = vi.fn()
  const consumeGrants = vi.fn()
  const target = new AgentInputTarget({
    dispatcher: dispatcher as never,
    cancel: vi.fn(() =>
      Promise.resolve({ runId: 0, state: 'cancelled' as const, cancelled: true as const }),
    ),
    steer: (runId: number, text: string) => dispatcher.call('agent.steer', { runId, text }),
    sessionId: () => 'session-a',
    cwd: () => '/repo',
    grants: () => grants,
    consumeGrants,
    openAnswer: vi.fn(() => handle),
    onRefusal,
  })
  return { dispatcher, handle, onRefusal, consumeGrants, steeredLines, target }
}

describe('AgentInputTarget', () => {
  it('forwards terminal usage facts without deriving counts', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')
    const usage = { input: 104, output: 27, cacheRead: 40, cacheWrite: 8 }
    dispatcher.emit('agent.runState', { runId: 7, state: 'completed', usage })
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', usage)
  })

  it('sends whole-block and row-window grant facts on the ask payload', async () => {
    const { dispatcher, handle, consumeGrants, target } = makeTarget([
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
    // The question carried the marks, so it takes them: the chip a person
    // sees before pressing Enter is spent by the question that used it.
    expect(consumeGrants).toHaveBeenCalledTimes(1)
  })
  it('preserves automatic frozen-frame provenance on the ask payload', async () => {
    const frozen = { ...grant('frame-1', 'top', 'running'), automatic: true as const }
    const { dispatcher, target } = makeTarget([frozen])

    await target.submit('what is on this frozen screen?')

    const ask = dispatcher.calls.find((call) => call.method === 'agent.ask')
    expect((ask!.params as { attachedContent: unknown[] }).attachedContent).toEqual([
      { itemId: 'frame-1', command: 'top', state: 'running', automatic: true },
    ])
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
      // And the LEDGER handle: the call's expansion reaches its result
      // through this entry rather than through a second copy of the bytes
      // on this wire (nocx-hp8p2.13).
      actionEntryId: 'entry-action-1',
      opensBlock: undefined,
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
      actionEntryId: 'entry-action-9',
      opensBlock: undefined,
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

  it('passes Stop to agent.cancel and closes from its reserved response without runState', async () => {
    const dispatcher = new FakeDispatcher()
    const handle: AnswerBlockHandle = {
      id: 1,
      el: document.createElement('div'),
      append: vi.fn(),
      toolCall: vi.fn(),
      reasoning: vi.fn(),
      notice: vi.fn(() => new BlockNotice({ text: '' })),
      steer: vi.fn(),
      close: vi.fn(),
    }
    let actions: RunningBlockActions | undefined
    const cancel = vi.fn(() =>
      Promise.resolve({
        runId: 7,
        state: 'cancelled' as const,
        cancelled: true as const,
        error: 'no local process group to signal — the execution keeps running',
        droppedDeltas: 2,
      }),
    )
    const onTurnEnd = vi.fn()
    const target = new AgentInputTarget({
      dispatcher: dispatcher as never,
      sessionId: () => 'session-a',
      cwd: () => '/repo',
      steer: vi.fn(() => Promise.resolve({ runId: 0, outcome: 'steered' as const })),
      grants: () => [],
      consumeGrants: vi.fn(),
      openAnswer: vi.fn(
        (_question: string, _cwd: string, provided?: RunningBlockActions): AnswerBlockHandle => {
          actions = provided
          return handle
        },
      ),
      cancel,
      onRefusal: vi.fn(),
      onTurnEnd,
    })

    await target.submit('stop this turn')
    actions?.stop()
    await Promise.resolve()

    expect(cancel).toHaveBeenCalledTimes(1)
    expect(cancel).toHaveBeenCalledWith(7)
    expect(dispatcher.calls.some((call) => call.method === 'agent.cancel')).toBe(false)
    await vi.waitFor(() => expect(handle.close).toHaveBeenCalledTimes(1))
    expect(handle.append).toHaveBeenCalledWith(
      '— 2 chunks of the answer were dropped while streaming; the full answer was saved —',
    )
    expect(handle.close).toHaveBeenCalledWith(
      'cancelled',
      'no local process group to signal — the execution keeps running',
      undefined,
      undefined,
    )
    expect(onTurnEnd).toHaveBeenCalledTimes(1)
    dispatcher.emit('agent.runState', { runId: 7, state: 'cancelled' })
    expect(handle.close).toHaveBeenCalledTimes(1)
    expect(onTurnEnd).toHaveBeenCalledTimes(1)
  })

  it('terminalizes once when runState wins the cancel-response race', async () => {
    const dispatcher = new FakeDispatcher()
    const handle: AnswerBlockHandle = {
      id: 1,
      el: document.createElement('div'),
      append: vi.fn(),
      toolCall: vi.fn(),
      reasoning: vi.fn(),
      notice: vi.fn(() => new BlockNotice({ text: '' })),
      steer: vi.fn(),
      close: vi.fn(),
    }
    let actions: RunningBlockActions | undefined
    let resolveCancel!: (value: {
      runId: number
      state: 'cancelled'
      cancelled: true
      droppedDeltas?: number
    }) => void
    const cancel = vi.fn(
      () =>
        new Promise<{ runId: number; state: 'cancelled'; cancelled: true; droppedDeltas?: number }>(
          (resolve) => {
            resolveCancel = resolve
          },
        ),
    )
    const onTurnEnd = vi.fn()
    const target = new AgentInputTarget({
      dispatcher: dispatcher as never,
      sessionId: () => 'session-a',
      cwd: () => '/repo',
      steer: vi.fn(() => Promise.resolve({ runId: 0, outcome: 'steered' as const })),
      grants: () => [],
      consumeGrants: vi.fn(),
      openAnswer: vi.fn(
        (_q: string, _cwd: string, provided?: RunningBlockActions): AnswerBlockHandle => {
          actions = provided
          return handle
        },
      ),
      cancel,
      onRefusal: vi.fn(),
      onTurnEnd,
    })

    await target.submit('race the stop')
    actions?.stop()
    dispatcher.emit('agent.runState', { runId: 7, state: 'cancelled', droppedDeltas: 1 })
    resolveCancel({ runId: 7, state: 'cancelled', cancelled: true, droppedDeltas: 1 })
    await Promise.resolve()

    expect(handle.append).toHaveBeenCalledTimes(1)
    expect(handle.close).toHaveBeenCalledTimes(1)
    expect(onTurnEnd).toHaveBeenCalledTimes(1)
  })

  it('closes the block completed on the terminal state; failed carries the renderable reason', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runState', { runId: 7, state: 'completed' })
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
    expect(handle.close).toHaveBeenCalledTimes(1)

    await target.submit('q2')
    dispatcher.emit('agent.runState', {
      runId: 8,
      state: 'failed',
      error: 'the model returned no text',
    })
    expect(handle.close).toHaveBeenLastCalledWith(
      'failure',
      'the model returned no text',
      undefined,
      undefined,
    )

    await target.submit('q3')
    dispatcher.emit('agent.runState', { runId: 9, state: 'cancelled' })
    expect(handle.close).toHaveBeenLastCalledWith('cancelled', undefined, undefined, undefined)
  })

  it('closes the block on a runState with NO prior delta (failure before any text)', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')
    dispatcher.emit('agent.runState', {
      runId: 7,
      state: 'failed',
      error: 'the connection was lost',
    })
    expect(handle.close).toHaveBeenCalledWith(
      'failure',
      'the connection was lost',
      undefined,
      undefined,
    )
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
      notice: vi.fn(() => new BlockNotice({ text: '' })),
      steer: vi.fn(),
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
      steer: vi.fn(() => Promise.resolve({ runId: 0, outcome: 'steered' as const })),
      grants: () => [],
      consumeGrants: vi.fn(),
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
      notice: vi.fn(() => new BlockNotice({ text: '' })),
      steer: vi.fn(),
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
      steer: vi.fn(() => Promise.resolve({ runId: 0, outcome: 'steered' as const })),
      grants: () => [],
      consumeGrants: vi.fn(),
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
      notice: vi.fn(() => new BlockNotice({ text: '' })),
      steer: vi.fn(),
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
      steer: vi.fn(() => Promise.resolve({ runId: 0, outcome: 'steered' as const })),
      grants: () => [],
      consumeGrants: vi.fn(),
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
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
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
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
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
    expect(clean.handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
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
    expect(handle.close).toHaveBeenCalledWith(
      'failure',
      'the model stopped mid-answer',
      undefined,
      undefined,
    )
  })
  it('shows named unavailable lease bounds while preserving a successful run', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('run without shell integration')
    const runId = dispatcher.next.run - 1

    dispatcher.emit('agent.runState', {
      runId,
      state: 'completed',
      unarmedBounds: [
        'the inactivity bound is not active because shell integration is unavailable',
        'the output bound is not active because shell integration is unavailable',
      ],
    })

    expect(handle.append).toHaveBeenCalledWith(
      '— the inactivity bound is not active because shell integration is unavailable; the output bound is not active because shell integration is unavailable; only the wall-clock deadline remains active —',
    )
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
  })
  /**
   * What the RUN says about ITSELF (nocx-4yjwk.1, design §5.3). Repeated
   * scope-widening asks are capped, and a run that stopped asking must say
   * so: a bound that goes quiet and reports nothing is the soft degrade
   * AGENTS.md forbids — the person is left to infer it from questions that
   * never arrive.
   */
  it('shows the notices a settled run states about itself', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('read every file under /repo')
    const runId = dispatcher.next.run - 1

    dispatcher.emit('agent.runState', {
      runId,
      state: 'completed',
      notices: ['this run stopped asking to widen a scope after three asks'],
    })

    expect(handle.append).toHaveBeenCalledWith(
      '— this run stopped asking to widen a scope after three asks —',
    )
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
  })

  it('says nothing extra when a run states no notices', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('a quiet run')
    const runId = dispatcher.next.run - 1

    dispatcher.emit('agent.runState', { runId, state: 'completed' })

    expect(handle.append).not.toHaveBeenCalled()
    expect(handle.close).toHaveBeenCalledWith('success', undefined, 'qwen3', undefined)
  })

  /**
   * Two different facts, two lines. A bound that could not be ARMED and a
   * bound the run stopped ASKING about are not the same sentence, and a
   * joined one would state the wrong one of them.
   */
  it('keeps notices and unarmed bounds as separate lines', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('a run with both')
    const runId = dispatcher.next.run - 1

    dispatcher.emit('agent.runState', {
      runId,
      state: 'completed',
      notices: ['this run stopped asking to widen a scope after three asks'],
      unarmedBounds: [
        'the inactivity bound is not active because shell integration is unavailable',
      ],
    })

    const lines = (handle.append as unknown as { mock: { calls: unknown[][] } }).mock.calls.map(
      (c) => c[0] as string,
    )
    expect(lines).toHaveLength(2)
    expect(lines).toContain('— this run stopped asking to widen a scope after three asks —')
    expect(lines).toContain(
      '— the inactivity bound is not active because shell integration is unavailable; only the wall-clock deadline remains active —',
    )
  })
})

describe('AgentInputTarget standing-answer receipt routing (nocx-2019q)', () => {
  const saved = (over: Record<string, unknown> = {}) => ({
    runId: '7',
    entryId: 'answer-1',
    approved: true,
    scope: 'always',
    rule: 'df -h',
    effect: 'observe',
    ruleId: 'rule-42',
    ...over,
  })

  it('draws the receipt on the turn that asked the question', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('will this need approval?')
    const runId = dispatcher.next.run - 1
    handle.el.dataset.entryId = 'answer-1'

    dispatcher.emit('agent.standingAnswerSaved', saved({ runId: String(runId) }))

    expect(handle.notice).toHaveBeenCalledTimes(1)
  })

  it('drops a receipt for a run this pane does not hold', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('will this need approval?')

    // Another pane's run. Routed by id exactly as a delta is, so it lands
    // where the question was asked and nowhere else.
    dispatcher.emit('agent.standingAnswerSaved', saved({ runId: '999' }))

    expect(handle.notice).not.toHaveBeenCalled()
  })

  /**
   * An empty entry is not a wildcard. The contract requires a non-empty one
   * (minLength 1) and the backend refuses to send a receipt without it, so
   * anything arriving with none is off-contract — and drawing it on whatever
   * block the run id found is the wrong block exactly as often as the right
   * one.
   */
  it('drops a receipt that names no entry at all', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('will this need approval?')
    const runId = dispatcher.next.run - 1
    handle.el.dataset.entryId = 'answer-1'

    dispatcher.emit('agent.standingAnswerSaved', saved({ runId: String(runId), entryId: '' }))

    expect(handle.notice).not.toHaveBeenCalled()
  })

  it('drops a receipt whose entry names a different block', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('will this need approval?')
    const runId = dispatcher.next.run - 1

    // A stale or misrouted notification: the same guard the deltas use, and
    // for the same reason — it must never be drawn on the wrong turn.
    dispatcher.emit(
      'agent.standingAnswerSaved',
      saved({ runId: String(runId), entryId: 'answer-elsewhere' }),
    )

    expect(handle.notice).not.toHaveBeenCalled()
  })
})

it('opens agent settings before allocating an answer or calling ask when no agent is ready', async () => {
  const dispatcher = new FakeDispatcher()
  const openAnswer = vi.fn()
  const ensureReady = vi.fn(() => Promise.resolve(false))
  const target = new AgentInputTarget({
    dispatcher: dispatcher as never,
    cancel: vi.fn(),
    sessionId: () => 'session-a',
    cwd: () => '/repo',
    steer: vi.fn(() => Promise.resolve({ runId: 0, outcome: 'steered' as const })),
    grants: () => [],
    consumeGrants: vi.fn(),
    openAnswer,
    onRefusal: vi.fn(),
    ensureReady,
  })
  await expect(target.submit('help')).rejects.toThrow('The answering agent is not ready')
  expect(ensureReady).toHaveBeenCalledOnce()
  expect(openAnswer).not.toHaveBeenCalled()
  expect(dispatcher.calls).toEqual([])
})

describe('AgentInputTarget steering (nocx-a62cl.6.1)', () => {
  it('sends a message typed while the turn runs as agent.steer, not a second ask', async () => {
    const { dispatcher, handle, consumeGrants, target } = makeTarget()
    await target.submit('what is in the log?')
    // The question spent the marks it carried; from here the pane is clear,
    // so anything taken below was taken by the steer.
    consumeGrants.mockClear()

    await target.submit('also check the second log')

    const steer = dispatcher.calls.find((call) => call.method === 'agent.steer')
    expect(steer!.params).toEqual({ runId: 7, text: 'also check the second log' })
    expect(dispatcher.calls.filter((call) => call.method === 'agent.ask')).toHaveLength(1)
    // The same turn shows it: no second answer block, and the message lands in
    // the block that is still being written.
    expect(handle.steer).toHaveBeenCalledWith('also check the second log', 'steered')
    // The marks the person made belong to their next QUESTION: this submit
    // carried none of them, so dropping them would throw away a choice
    // nothing used.
    expect(consumeGrants).not.toHaveBeenCalled()
  })

  it('asks again once the turn has ended', async () => {
    const { dispatcher, target } = makeTarget()
    await target.submit('first question')
    dispatcher.emit('agent.runState', { runId: 7, state: 'completed' })

    await target.submit('second question')

    expect(dispatcher.calls.filter((call) => call.method === 'agent.ask')).toHaveLength(2)
    expect(dispatcher.calls.some((call) => call.method === 'agent.steer')).toBe(false)
  })

  it('says so when the message could not be delivered, and does not draw it', async () => {
    const { dispatcher, handle, onRefusal, target } = makeTarget()
    await target.submit('what is in the log?')
    dispatcher.refuse('agent.steer', 'harness: the turn is not running')

    await expect(target.submit('too late')).rejects.toThrow('the turn is not running')

    expect(onRefusal).toHaveBeenCalledWith('harness: the turn is not running')
    expect(handle.steer).not.toHaveBeenCalled()
  })
})

describe('AgentInputTarget and a message the turn could not take (nocx-a62cl.6.3)', () => {
  it('draws it as kept, and asks it as the next question when the turn ends', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('what is in the log?')
    dispatcher.steerOutcome = 'next_question'

    await target.submit('ask me the other thing instead')

    // The message is drawn where the person sent it, marked as kept — this
    // answer will not be its answer.
    expect(handle.steer).toHaveBeenCalledWith('ask me the other thing instead', 'next_question')
    // Nothing asked it yet: the conversation still has the turn that could not
    // take it.
    expect(dispatcher.calls.filter((call) => call.method === 'agent.ask')).toHaveLength(1)

    dispatcher.emit('agent.runState', { runId: 7, state: 'completed' })

    // Its question opens a block of its own, after the turn it could not join.
    await expect
      .poll(() => dispatcher.calls.filter((call) => call.method === 'agent.ask').length)
      .toBe(2)
    const next = dispatcher.calls.filter((call) => call.method === 'agent.ask')[1]
    expect((next.params as { question: string }).question).toBe('ask me the other thing instead')
  })

  it('asks it after a cancelled turn too — the message is not lost', async () => {
    const { dispatcher, target } = makeTarget()
    await target.submit('what is in the log?')
    dispatcher.steerOutcome = 'next_question'
    await target.submit('ask me the other thing instead')

    dispatcher.emit('agent.runState', { runId: 7, state: 'cancelled' })

    await expect
      .poll(() => dispatcher.calls.filter((call) => call.method === 'agent.ask').length)
      .toBe(2)
  })

  it('keeps the order of several kept messages', async () => {
    const { dispatcher, target } = makeTarget()
    await target.submit('first question')
    dispatcher.steerOutcome = 'next_question'
    await target.submit('one')
    await target.submit('two')

    dispatcher.emit('agent.runState', { runId: 7, state: 'completed' })

    await expect
      .poll(() => dispatcher.calls.filter((call) => call.method === 'agent.ask').length)
      .toBeGreaterThanOrEqual(2)
    const questions = dispatcher.calls
      .filter((call) => call.method === 'agent.ask')
      .map((call) => (call.params as { question: string }).question)
    expect(questions[1]).toBe('one')
  })
})

describe('a message the agent took and the cancel dropped (nocx-a62cl.6.3)', () => {
  it('restates its line as waiting and asks it as the next question', async () => {
    const { dispatcher, handle, steeredLines, target } = makeTarget()
    await target.submit('what is in the log?')
    await target.submit('ask me the other thing instead')
    expect(handle.steer).toHaveBeenCalledWith('ask me the other thing instead', 'steered')
    const line = steeredLines[0]
    const markQueued = vi.spyOn(line, 'markQueued')

    // The agent had it queued; the cancel dropped it and named it.
    dispatcher.emit('agent.runState', {
      runId: 7,
      state: 'cancelled',
      droppedSteers: ['ask me the other thing instead'],
    })

    // The line already on screen says so now — the same words, marked — and
    // the message is asked as the conversation's next question.
    expect(markQueued).toHaveBeenCalledTimes(1)
    await expect
      .poll(() => dispatcher.calls.filter((call) => call.method === 'agent.ask').length)
      .toBe(2)
    const next = dispatcher.calls.filter((call) => call.method === 'agent.ask')[1]
    expect((next.params as { question: string }).question).toBe('ask me the other thing instead')
  })

  it('asks a dropped message it did not draw — it is still the person\u2019s', async () => {
    const { dispatcher, target } = makeTarget()
    await target.submit('what is in the log?')

    dispatcher.emit('agent.runState', {
      runId: 7,
      state: 'cancelled',
      droppedSteers: ['an extension queued this'],
    })

    await expect
      .poll(() => dispatcher.calls.filter((call) => call.method === 'agent.ask').length)
      .toBe(2)
    const next = dispatcher.calls.filter((call) => call.method === 'agent.ask')[1]
    expect((next.params as { question: string }).question).toBe('an extension queued this')
  })
})
