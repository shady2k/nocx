// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentInputTarget } from './agent-ask'
import { BlockManager } from './scrollback/blocks'
import { CommandSnapshotStore } from './command-snapshot'
import type { AgentAsk } from './generated/agent.ask'
import type { LedgerGet, Caused, Entry, Artifact } from './generated/ledger.get'
import type { LedgerArtifact } from './generated/ledger.artifact'

function entry(id: string, kind: Entry['kind']): Entry {
  return {
    id,
    kind,
    seq: 1,
    environmentId: 'local',
    host: '',
    cwd: '/repo',
    source: kind === 'ask' ? 'user' : 'assistant',
    intent: kind === 'ask' ? 'question' : '',
    phase: 'closed',
    status: 'success',
    unreconciled: null,
    submittedAt: 1,
    startedAt: 1,
    endedAt: 20,
    durationMs: 19,
    exitCode: null,
    terminationReason: 'completed',
    maskedCount: 0,
    maskedKinds: [],
    redactions: [],
  }
}

function artifact(id: string): Artifact {
  return {
    id,
    executionId: 1,
    mediaType: 'text/plain',
    derivedFrom: null,
    state: 'sealed',
    byteLen: 3,
    chunkCount: 2,
    pinned: false,
    truncated: null,
    captureMethod: 'none',
    captureVersion: 1,
    terminalCols: null,
    terminalRows: null,
    stream: null,
    byteOffset: null,
    byteEnd: null,
    encoding: 'utf-8',
    gaps: [],
    payload: {},
  }
}

function cause(entryId: string, position: number, kind: Caused['kind'] = 'text'): Caused {
  return {
    entryId,
    position,
    kind,
    source: 'assistant',
    intent: kind === 'action' ? 'files.read' : '',
    args: kind === 'action' ? { path: '/repo/a' } : null,
    effect: kind === 'action' ? 'observe' : null,
    resource: null,
    opensBlock: false,
  }
}

function turn(
  state: AgentAsk['state'],
  caused: Caused[],
  engine = 'pydantic-deepagents',
): LedgerGet {
  return {
    entry: entry('turn-1', 'ask'),
    artifacts: [],
    edges: [],
    proseEvicted: false,
    caused,
    agentRuns: [
      { runId: 7, attempt: 1, state, model: 'model/alias', engine, runtimeVersion: 'manifest-v1' },
    ],
  }
}

class ReplayWire {
  ask: AgentAsk = {
    runId: 7,
    entryId: 'turn-1',
    state: 'completed',
    ingestSeq: 1,
    replayed: true,
    model: 'model/alias',
    engine: 'pydantic-deepagents',
    runtimeVersion: 'manifest-v1',
  }
  entries = new Map<string, LedgerGet>()
  bodies = new Map<string, LedgerArtifact>()
  private subscriptions = new Map<string, (value: unknown) => void>()
  calls: { method: string; id?: string }[] = []
  intercept?: (method: string, params: { id?: string }) => Promise<unknown> | undefined

  call<T>(method: string, params: unknown): Promise<T> {
    const id =
      params !== null &&
      typeof params === 'object' &&
      'id' in params &&
      typeof params.id === 'string'
        ? params.id
        : undefined
    const p = { id }
    this.calls.push({ method, id })
    const intercepted = this.intercept?.(method, p)
    if (intercepted) return intercepted as Promise<T>
    if (method === 'agent.ask') return Promise.resolve(this.ask) as Promise<T>
    if (method === 'ledger.get' && this.entries.has(p.id!))
      return Promise.resolve(this.entries.get(p.id!)) as Promise<T>
    if (method === 'ledger.artifact' && this.bodies.has(p.id!))
      return Promise.resolve(this.bodies.get(p.id!)) as Promise<T>
    return Promise.reject(new Error(`unexpected ${method} ${p.id}`))
  }

  subscribe(method: string, callback: (value: unknown) => void): () => void {
    this.subscriptions.set(method, callback)
    return () => this.subscriptions.delete(method)
  }

  emit(method: string, value: unknown): void {
    this.subscriptions.get(method)?.(value)
  }

  prose(id: string, body: string, lastSeq: number): void {
    const art = artifact(`artifact-${id}`)
    this.entries.set(id, {
      entry: entry(id, 'text'),
      artifacts: [art],
      edges: [],
      caused: [],
      proseEvicted: false,
      agentRuns: [],
    })
    this.bodies.set(art.id, {
      id: art.id,
      mediaType: 'text/plain',
      body,
      byteLen: body.length,
      truncated: null,
      lastSeq,
    })
  }
}

function harness(wire: ReplayWire) {
  const root = document.createElement('div')
  const live = document.createElement('div')
  root.appendChild(live)
  document.body.appendChild(root)
  const manager = new BlockManager(root, live, { snapshotStore: new CommandSnapshotStore() })
  const ended = vi.fn()
  const cancel = vi.fn(() =>
    Promise.resolve({
      runId: 7,
      state: 'cancelled' as const,
      cancelled: true as const,
    }),
  )
  const target = new AgentInputTarget({
    dispatcher: wire as never,
    sessionId: () => 'session-1',
    cwd: () => '/repo',
    grants: () => [],
    openAnswer: (question, cwd, actions) => manager.addAnswerBlock(question, cwd, actions),
    findAnswer: (id, actions) => manager.answerForEntry(id, actions),
    drawCommand: () => Promise.resolve(null),
    onRefusal: (message) => {
      throw new Error(message)
    },
    onTurnEnd: ended,
    cancel,
  })
  return { root, manager, target, ended, cancel }
}

function proseText(root: HTMLElement, id: string): string {
  return root.querySelector(`[data-entry-id="${id}"] [data-answer-body]`)?.textContent ?? ''
}

afterEach(() => {
  document.body.replaceChildren()
})

describe('accepted ask replay', () => {
  it('reuses one durable turn, settles terminal metadata, and keeps unknown engine separate from the model', async () => {
    const wire = new ReplayWire()
    wire.ask.engine = 'future-engine'
    wire.entries.set('turn-1', turn('completed', [cause('text-a', 0)], 'future-engine'))
    wire.prose('text-a', 'saved answer', 7)
    const { root, manager, target, ended } = harness(wire)
    const existing = manager.addAnswerBlock('question', '/repo')
    existing.el.dataset.entryId = 'turn-1'
    existing.append('stale painted bytes', 'text-a')
    await target.submit('question')
    await target.submit('question')
    expect(root.querySelectorAll('[data-entry-id="turn-1"]')).toHaveLength(1)
    expect(root.querySelector('[data-entry-id="turn-1"]')).toBe(existing.el)
    expect(proseText(root, 'text-a')).toBe('saved answer')
    expect(existing.el.dataset.outcome).toBe('success')
    expect(existing.el.dataset.answeredBy).toBe('model/alias')
    expect(existing.el.dataset.engine).toBe('future-engine')
    expect(existing.el.textContent).toContain('Unknown engine: future-engine')
    expect(existing.el.textContent).not.toContain('Thinking')
    expect(existing.el.querySelector('.cmd-answer-typing')).toBeNull()
    expect(ended).toHaveBeenCalledTimes(2)
  })

  it('finalizes replaced markdown on every replay without reopening a completed turn', async () => {
    const wire = new ReplayWire()
    wire.entries.set('turn-1', turn('completed', [cause('text-a', 0)]))
    wire.prose('text-a', '```sh\necho done\n```', 7)
    const { root, target } = harness(wire)
    for (let attempt = 0; attempt < 2; attempt++) {
      await target.submit('question')
      const answer = root.querySelector<HTMLElement>('[data-entry-id="turn-1"]')!
      expect(answer.querySelectorAll('[data-fence-delim="close"]')).toHaveLength(1)
      expect(answer.dataset.outcome).toBe('success')
      expect(answer.querySelector('.cmd-answer-typing')).toBeNull()
    }
  })

  it('buffers pre-ack events, merges actual sequence holes, and rereads final children when terminal arrives during hydration', async () => {
    const wire = new ReplayWire()
    wire.ask.state = 'streaming'
    wire.entries.set('turn-1', turn('streaming', [cause('text-a', 0)]))
    wire.prose('text-a', 'AB', 7)
    // The repository targets ES2020, before Promise.withResolvers.
    let accept!: (value: AgentAsk) => void
    let finishRead!: (value: LedgerArtifact) => void
    let markReading!: () => void
    const acceptance = new Promise<AgentAsk>((resolve) => {
      accept = resolve
    })
    const firstRead = new Promise<LedgerArtifact>((resolve) => {
      finishRead = resolve
    })
    const reading = new Promise<void>((resolve) => {
      markReading = resolve
    })
    let blocked = false
    wire.intercept = (method, params) => {
      if (method === 'agent.ask') return acceptance
      if (method === 'ledger.artifact' && params.id === 'artifact-text-a' && !blocked) {
        blocked = true
        markReading()
        return firstRead
      }
      return undefined
    }
    const { root, target, ended } = harness(wire)
    const submitting = target.submit('question')
    const tool = {
      runId: 7,
      entryId: 'turn-1',
      actionEntryId: 'action-1',
      callId: 'call-1',
      tool: 'files.read',
      args: { path: '/repo/a' },
      effect: 'observe',
      opensBlock: false,
    }
    wire.emit('agent.runDelta', {
      runId: 7,
      entryId: 'turn-1',
      blockId: 'text-a',
      seq: 7,
      text: 'B',
    })
    wire.emit('agent.runToolCall', tool)
    accept(wire.ask)
    await reading
    wire.emit('agent.runDelta', {
      runId: 7,
      entryId: 'turn-1',
      blockId: 'text-a',
      seq: 9,
      text: 'C',
    })
    wire.emit('agent.runToolCall', tool)
    wire.emit('agent.runDelta', {
      runId: 7,
      entryId: 'turn-1',
      blockId: 'text-b',
      seq: 12,
      text: 'D',
    })
    wire.entries.set(
      'turn-1',
      turn('completed', [cause('text-a', 0), cause('action-1', 1, 'action'), cause('text-b', 2)]),
    )
    wire.prose('text-a', 'ABC', 9)
    wire.prose('text-b', 'D', 12)
    wire.emit('agent.runState', { runId: 7, state: 'completed' })
    finishRead({
      id: 'artifact-text-a',
      mediaType: 'text/plain',
      body: 'AB',
      byteLen: 2,
      truncated: null,
      lastSeq: 7,
    })
    await submitting
    const answer = root.querySelector<HTMLElement>('[data-entry-id="turn-1"]')!
    expect(proseText(root, 'text-a')).toBe('ABC')
    expect(proseText(root, 'text-b')).toBe('D')
    expect(answer.querySelectorAll('[data-entry-id="action-1"]')).toHaveLength(1)
    expect(
      Array.from(
        answer.querySelectorAll<HTMLElement>(':scope > .cmd-children > [data-entry-id]'),
      ).map((el) => el.dataset.entryId),
    ).toEqual(['text-a', 'action-1', 'text-b'])
    expect(
      wire.calls.filter((call) => call.method === 'ledger.get' && call.id === 'turn-1'),
    ).toHaveLength(2)
    expect(answer.dataset.outcome).toBe('success')
    expect(answer.textContent).toContain('Pydantic agent · Experimental')
    expect(answer.querySelector('.cmd-answer-typing')).toBeNull()
    expect(ended).toHaveBeenCalledTimes(1)
    wire.emit('agent.runState', { runId: 7, state: 'failed', error: 'late terminal' })
    expect(answer.dataset.outcome).toBe('success')
    expect(ended).toHaveBeenCalledTimes(1)
  })

  it('keeps a parked replay live and appends only deltas above its saved block cursor before Stop settles it', async () => {
    const wire = new ReplayWire()
    wire.ask.state = 'awaiting_approval'
    wire.entries.set('turn-1', turn('awaiting_approval', [cause('text-a', 0)]))
    wire.prose('text-a', 'saved', 11)
    const { root, manager, target, ended, cancel } = harness(wire)
    const restored = manager.addAnswerBlock('question', '/repo')
    restored.el.dataset.entryId = 'turn-1'
    await target.submit('question')
    const answer = root.querySelector<HTMLElement>('[data-entry-id="turn-1"]')!
    expect(answer).toBe(restored.el)
    expect(ended).not.toHaveBeenCalled()
    wire.emit('agent.runDelta', {
      runId: 7,
      entryId: 'turn-1',
      blockId: 'text-a',
      seq: 11,
      text: 'saved',
    })
    wire.emit('agent.runDelta', {
      runId: 7,
      entryId: 'turn-1',
      blockId: 'text-a',
      seq: 15,
      text: ' continued',
    })
    expect(proseText(root, 'text-a')).toBe('saved continued')
    answer.querySelector<HTMLElement>('[data-block-actions]')!.click()
    const stop = document.querySelector<HTMLElement>('[data-item-id="stop"]')
    expect(stop).not.toBeNull()
    stop!.click()
    await vi.waitFor(() => expect(answer.dataset.outcome).toBe('cancelled'))
    expect(cancel).toHaveBeenCalledWith(7)
    expect(ended).toHaveBeenCalledTimes(1)
    answer.querySelector<HTMLElement>('[data-block-actions]')!.click()
    expect(document.querySelector('[data-item-id="stop"]')).toBeNull()
    manager.closeOverflowMenus()
  })
})
