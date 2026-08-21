// @vitest-environment jsdom

import { describe, it, expect, vi } from 'vitest'
import { AgentInputTarget } from './agent-ask'
import type { ReferenceChip } from './ask-entry'
import type { AnswerBlockHandle } from './scrollback/blocks'

/** A fake dispatcher: records agent.* calls and delivers subscriptions on
 *  demand. The wire contract the target produces is asserted from the
 *  RECORDED params — never from what the target echoes back. */
class FakeDispatcher {
  calls: { method: string; params: unknown }[] = []
  private subs = new Map<string, (params: unknown) => void>()
  next = { frameId: 'frame-1', run: 7, answerEntry: 'answer-1' }

  call<T = unknown>(method: string, params: unknown): Promise<T> {
    this.calls.push({ method, params })
    if (method === 'agent.captureFrame')
      return Promise.resolve({ frameId: this.next.frameId }) as Promise<T>
    if (method === 'agent.ask') {
      const res = {
        runId: this.next.run,
        questionId: 'ask-1',
        answerEntryId: this.next.answerEntry,
        state: 'prepared',
        ingestSeq: 1,
        replayed: false,
        model: 'qwen3',
      }
      // Each ask is a new run with a new answer entry (two overlapping
      // asks stream concurrently; ids never repeat).
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

/** A fake finished block whose output text is "line one\nline two" — the
 *  text the frozen mint will derive. */
function blockEl(command = 'ls'): HTMLElement {
  const el = document.createElement('div')
  el.className = 'cmd-block'
  el.dataset.blockId = command
  const header = document.createElement('div')
  const headerText = document.createElement('span')
  headerText.className = 'cmd-header-text'
  headerText.textContent = command
  header.appendChild(headerText)
  const output = document.createElement('div')
  output.className = 'cmd-output'
  const l1 = document.createElement('span')
  l1.className = 'term-line'
  l1.textContent = 'line one'
  const l2 = document.createElement('span')
  l2.className = 'term-line'
  l2.textContent = 'line two'
  output.append(l1, l2)
  el.append(header, output)
  return el
}

let chipSeq = 0
function chipOf(block: HTMLElement, rowStart: number, rowEnd: number): ReferenceChip {
  return {
    id: `chip-${++chipSeq}`,
    blockEl: block,
    label: `ls · rows ${rowStart + 1}–${rowEnd}`,
    rowStart,
    rowEnd,
  }
}

function makeTarget(chips: ReferenceChip[] = []) {
  const dispatcher = new FakeDispatcher()
  const handle: AnswerBlockHandle = {
    id: 1,
    el: document.createElement('div'),
    append: vi.fn(),
    close: vi.fn(),
  }
  const onRefusal = vi.fn()
  const target = new AgentInputTarget({
    dispatcher: dispatcher as never,
    sessionId: () => 'session-a',
    cwd: () => '/repo',
    chips: () => chips,
    openAnswer: vi.fn(() => handle),
    onRefusal,
  })
  return { dispatcher, handle, onRefusal, target }
}

describe('AgentInputTarget', () => {
  it('mints one frozen frame PER CHIP BLOCK and references the chip’s region (the ONE derivation)', async () => {
    const block = blockEl()
    const { dispatcher, handle, target } = makeTarget([chipOf(block, 0, 2)])
    await target.submit('what does this screen mean?')

    const captures = dispatcher.calls.filter((c) => c.method === 'agent.captureFrame')
    expect(captures).toHaveLength(1)
    const p = captures[0].params as {
      captureId: string
      sessionId: string
      source: string
      rows: { kind: string; text: string }[]
      serializerVersion: number
      cwd: string
    }
    expect(p.source).toBe('frozen')
    expect(p.sessionId).toBe('session-a')
    expect(p.serializerVersion).toBe(1)
    expect(p.rows).toEqual([
      { kind: 'text', text: 'line one' },
      { kind: 'text', text: 'line two' },
    ])
    // A frozen frame carries no cursor, no identity, no range — the backend
    // enforces exactly this.
    expect(p).not.toHaveProperty('cursor')
    expect(p).not.toHaveProperty('identity')
    expect(p).not.toHaveProperty('range')

    const ask = dispatcher.calls.find((c) => c.method === 'agent.ask')
    const a = ask!.params as {
      askId: string
      question: string
      references: { frameId: string; region: { rowStart: number; rowEnd: number } }[]
    }
    expect(a.question).toBe('what does this screen mean?')
    // The reference carries the CHIP's region — a sub-row selection is a
    // sub-row reference, never a silent whole-block reference.
    expect(a.references).toEqual([{ frameId: 'frame-1', region: { rowStart: 0, rowEnd: 2 } }])

    // The answer block opened, associated with the run AND the answer entry
    // id BEFORE the first delta (a no-delta failure still closes the right
    // block).
    expect(handle.el.dataset.answerEntryId).toBe('answer-1')
  })

  it('a question carries the chips that are in the line and NO others — two blocks selected, one unrelated block absent', async () => {
    const blockA = blockEl('ls')
    const blockB = blockEl('git log')
    const unrelated = blockEl('sleep 1')
    const { dispatcher, target } = makeTarget([chipOf(blockA, 0, 1), chipOf(blockB, 1, 2)])
    await target.submit('how are these related?')

    const captures = dispatcher.calls.filter((c) => c.method === 'agent.captureFrame')
    // Exactly the two chip blocks were captured — the unrelated block's
    // rows never left the DOM.
    expect(captures).toHaveLength(2)
    const frames = captures.map((c) =>
      (c.params as { rows: { text: string }[] }).rows.map((r) => r.text),
    )
    expect(frames).toContainEqual(['line one', 'line two'])
    // The unrelated block's rows never left the DOM.
    expect(Array.from(unrelated.querySelectorAll('.term-line')).map((l) => l.textContent)).toEqual([
      'line one',
      'line two',
    ])
    expect(frames.some((f) => f.includes('line one') && f.length === 1)).toBe(false)

    const ask = dispatcher.calls.find((c) => c.method === 'agent.ask')
    const a = ask!.params as {
      references: { frameId: string; region: { rowStart: number; rowEnd: number } }[]
    }
    expect(a.references).toHaveLength(2)
    expect(a.references).toEqual([
      { frameId: 'frame-1', region: { rowStart: 0, rowEnd: 1 } },
      { frameId: 'frame-1', region: { rowStart: 1, rowEnd: 2 } },
    ])
  })

  it('two chips into the SAME block share one frame and carry two regions', async () => {
    const block = blockEl()
    const { dispatcher, target } = makeTarget([chipOf(block, 0, 1), chipOf(block, 1, 2)])
    await target.submit('q')

    const captures = dispatcher.calls.filter((c) => c.method === 'agent.captureFrame')
    expect(captures).toHaveLength(1)
    const ask = dispatcher.calls.find((c) => c.method === 'agent.ask')
    const a = ask!.params as {
      references: { frameId: string; region: { rowStart: number; rowEnd: number } }[]
    }
    expect(a.references).toEqual([
      { frameId: 'frame-1', region: { rowStart: 0, rowEnd: 1 } },
      { frameId: 'frame-1', region: { rowStart: 1, rowEnd: 2 } },
    ])
  })

  it('a GENERAL question — no chips — is just the ask with zero references', async () => {
    const { dispatcher, target } = makeTarget([])
    await target.submit('what is the capital of France?')

    expect(dispatcher.calls.some((c) => c.method === 'agent.captureFrame')).toBe(false)
    const ask = dispatcher.calls.find((c) => c.method === 'agent.ask')
    const a = ask!.params as { question: string; references: unknown[] }
    expect(a.question).toBe('what is the capital of France?')
    expect(a.references).toEqual([])
  })

  it('routes runDelta to the run’s block by runId and entryId', async () => {
    const { dispatcher, handle, target } = makeTarget()
    await target.submit('q')

    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      seq: 0,
      text: 'hello',
    })
    dispatcher.emit('agent.runDelta', {
      runId: 7,
      entryId: 'answer-1',
      seq: 1,
      text: ' world',
    })
    expect(handle.append).toHaveBeenCalledTimes(2)
    expect(handle.append).toHaveBeenNthCalledWith(1, 'hello')
    expect(handle.append).toHaveBeenNthCalledWith(2, ' world')
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
      close: vi.fn(),
    }
    const openAnswer = vi.fn(() => handle)
    const target = new AgentInputTarget({
      dispatcher: dispatcher as never,
      sessionId: () => 's',
      cwd: () => '/',
      chips: () => [],
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
      close: vi.fn(),
    }
    const openAnswer = vi.fn(() => handle)
    const onRefusal = vi.fn()
    const target = new AgentInputTarget({
      dispatcher: failDispatcher as never,
      sessionId: () => 's',
      cwd: () => '/',
      chips: () => [],
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
    const block = blockEl()
    const handle: AnswerBlockHandle = {
      id: 1,
      el: document.createElement('div'),
      append: vi.fn(),
      close: vi.fn(),
    }
    const onRefusal = vi.fn()
    const target = new AgentInputTarget({
      dispatcher: failDispatcher as never,
      sessionId: () => 's',
      cwd: () => '/',
      chips: () => [chipOf(block, 0, 2)],
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
    expect(handle.el.dataset.answerEntryId).toBe('answer-1')

    // The run suspends: the block stays OPEN (nothing closed) and the run
    // stays routable — the question is being decided elsewhere.
    dispatcher.emit('agent.runState', { runId, state: 'awaiting_approval' })
    expect(handle.close).not.toHaveBeenCalled()

    // The person approves; the resumed run streams into the SAME block —
    // a run deleted at awaiting_approval would drop these deltas.
    dispatcher.emit('agent.runDelta', {
      runId,
      entryId: 'answer-1',
      seq: 0,
      text: 'approved answer',
    })
    expect(handle.append).toHaveBeenCalledWith('approved answer')

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
