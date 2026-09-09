// @vitest-environment jsdom
/**
 * User-path tests for the emitting view (nocx-02uci).
 *
 * What a person does here: they open Settings, pick the pane their agent is
 * running in, and see the screen the detection rule is reading TOGETHER WITH
 * the rule's own reading of it. The acceptance criterion is falsified if the
 * view shows raw text without that reading — "that is a terminal, and they
 * already have one" — so most of what is asserted below is the second half:
 * which anchor bound where, which branch answered, and for a branch that did
 * not, the predicate it stopped at.
 *
 * The interval is the other thing under test, and it has both ends: the view
 * polls while it is mounted and asks nothing after it is gone. A live view
 * that keeps reading a pane after its surface closes is the same defect as a
 * backend polling a pane nobody is looking at.
 */
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { EmittingClient } from './emitting-client'
import type { AgentEmitting } from './generated/agent.emitting'
import { AgentEmittingSection, EMITTING_POLL_MS } from './agent-emitting-section'

/** A four-row screen carrying the shape a claude rule reads: a rule row, the
 *  input row the cursor sits on, a second rule row, and a mode line. */
function frame(): NonNullable<AgentEmitting['reading']>['frame'] {
  const rule = (n: number): string[] => Array.from({ length: n }, () => '─')
  const text = (s: string, n: number): string[] => Array.from({ length: n }, (_, i) => s[i] ?? ' ')
  return {
    cols: 8,
    rows: 4,
    cursorX: 2,
    cursorY: 1,
    altScreen: false,
    lines: [
      { cells: rule(8), rule: '─', opensAt: 0 },
      { cells: text('❯ hi', 8), opensAt: 0 },
      { cells: rule(8), rule: '─', opensAt: 0 },
      { cells: text('  mode', 8), opensAt: 2 },
    ],
  }
}

const READING: NonNullable<AgentEmitting['reading']> = {
  sessionId: 'sess-1',
  instanceId: 'inst-1',
  sessionEpoch: 1,
  agent: 'claude',
  hasRule: true,
  state: 'free_text',
  fallback: 'free_text',
  frame: frame(),
  anchors: [
    { name: 'bottomRule', kind: 'searchUp', bound: true, row: 2 },
    { name: 'prompt', kind: 'offset', from: 'bottomRule', bound: false },
  ],
  branches: [
    {
      state: 'permission_choice',
      reached: true,
      matched: false,
      predicates: [
        { kind: 'cursorOn', detail: 'glyph="❯"', evaluated: true, held: true },
        { kind: 'cursorOpensItsRow', evaluated: true, held: false },
        { kind: 'numberedOptionAfterCursor', evaluated: false, held: false },
      ],
    },
    {
      state: 'working',
      reached: true,
      matched: false,
      predicates: [
        {
          kind: 'regionAny',
          anchor: 'bottomRule',
          detail: 'text="… (" up=true maxRows=4',
          evaluated: true,
          held: false,
          region: { from: 0, to: 1 },
        },
      ],
    },
  ],
  extractors: [
    {
      name: 'subagents',
      anchor: 'bottomRule',
      region: { from: 3, to: 3 },
      rows: [{ fields: [{ name: 'name', value: 'Explore' }] }],
    },
  ],
}

const ANSWER: AgentEmitting = {
  panes: [{ sessionId: 'sess-1', agent: 'claude' }],
  reading: READING,
}

function fakeClient(answers: AgentEmitting[]) {
  const client = new EmittingClient(new Dispatcher(fixedEndpoint(9876)))
  let n = 0
  const read = vi.spyOn(client, 'read').mockImplementation(() => {
    const answer = answers[Math.min(n, answers.length - 1)]
    n++
    return Promise.resolve(structuredClone(answer))
  })
  return { client, read }
}

function mount(client: EmittingClient, nameOf?: (id: string) => string | null): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <AgentEmittingSection client={client} nameOf={nameOf} />, { container })
  return container
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

/** Solid's effects and a mocked promise both settle on the microtask queue,
 *  which fake timers do not drive. */
async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0)
}

describe('the emitting view', () => {
  it('shows the frame AND the rule reading it, not raw text alone', async () => {
    const { client } = fakeClient([ANSWER])
    const container = mount(client)
    await settle()

    // The screen.
    expect(container.querySelectorAll('.st-emitting__row')).toHaveLength(4)
    // The rows the input box is FOUND by, said as words and not only drawn.
    expect(container.querySelectorAll('.st-emitting__row[data-rule="true"]')).toHaveLength(2)
    // Where the cursor is — the one thing an agent cannot forge. The cell it
    // names is chosen by COLUMN index, which is why the row's cells arrive one
    // per column: the third column of `❯ hi` is `h`, and a renderer counting
    // characters instead would land elsewhere the first time a row carried a
    // double-width grapheme.
    const cursor = container.querySelector('.st-emitting__cursor')
    expect(cursor?.textContent).toBe('h')
    expect(cursor?.closest('.st-emitting__row')?.getAttribute('data-row')).toBe('1')

    // AND THE READING, which is what keeps this from being a terminal.
    expect(container.querySelectorAll('.st-emitting__branch')).toHaveLength(2)
    expect(container.querySelector('.ui-badge')?.textContent).toBe('free_text')
  })

  it('says which branch produced the value, or that none did', async () => {
    const { client } = fakeClient([ANSWER])
    const container = mount(client)
    await settle()
    // Nothing matched here, so the answer is the fall-through — and saying
    // "branch 0" would send a person to repair a branch that did not run.
    expect(container.textContent).toContain('no branch matched')
    expect(container.textContent).toContain('free_text')

    cleanup()
    const matched = structuredClone(ANSWER)
    matched.reading!.matchedBranch = 1
    matched.reading!.branches[1].matched = true
    matched.reading!.state = 'working'
    const second = fakeClient([matched])
    const c2 = mount(second.client)
    await settle()
    expect(c2.textContent).toContain('from branch 1')
    expect(c2.querySelector('.st-emitting__branch[data-matched="true"]')).toBeTruthy()
  })

  it('says where each branch stopped, and what was never asked', async () => {
    const { client } = fakeClient([ANSWER])
    const container = mount(client)
    await settle()
    const first = container.querySelector('.st-emitting__branch[data-branch="0"]')!
    expect(first.textContent).toContain('stopped at predicate 1')
    // The predicate after the failure was never asked. Reporting it as
    // merely "did not hold" would point at the wrong line.
    const never = first.querySelector('.st-emitting__predicate[data-evaluated="false"]')
    expect(never?.textContent).toContain('never asked')
  })

  it('says where the anchors bound, and marks the ones that did not', async () => {
    const { client } = fakeClient([ANSWER])
    const container = mount(client)
    await settle()
    expect(container.textContent).toContain('bottomRule')
    expect(container.textContent).toContain('row 2')
    // An anchor that did not bind is ABSENT rather than at row zero, which is
    // a real row a person would go and look at.
    expect(container.textContent).toContain('did not bind')
  })

  it('polls while it is open, and stops when it is gone', async () => {
    const { client, read } = fakeClient([ANSWER])
    mount(client)
    await settle()
    expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(EMITTING_POLL_MS)
    expect(read).toHaveBeenCalledTimes(2)

    // THE SECOND END. A view that goes on reading a pane after its surface
    // closed is the same defect as a backend polling a pane nobody is
    // looking at, only harder to see.
    cleanup()
    const after = read.mock.calls.length
    await vi.advanceTimersByTimeAsync(EMITTING_POLL_MS * 4)
    expect(read).toHaveBeenCalledTimes(after)
  })

  it('says so when nothing is enrolled, rather than showing an empty grid', async () => {
    const { client } = fakeClient([{ panes: [] }])
    const container = mount(client)
    await settle()
    expect(container.textContent).toContain('No pane is enrolled')
    expect(container.querySelector('.st-emitting__row')).toBeNull()
  })

  it('lets a person choose the pane, and reads that one', async () => {
    const { client, read } = fakeClient([{ panes: ANSWER.panes }, ANSWER])
    const container = mount(client, () => 'my agent tab')
    await settle()
    // The window's own name for the pane, not the session id.
    expect(container.textContent).toContain('my agent tab')

    const select = container.querySelector('select')!
    fireEvent.change(select, { target: { value: 'sess-1' } })
    await settle()
    expect(read).toHaveBeenLastCalledWith('sess-1')
    expect(container.querySelectorAll('.st-emitting__row')).toHaveLength(4)
  })

  it('lets go of a pane whose observation closed', async () => {
    const { client } = fakeClient([ANSWER, { panes: [] }])
    const container = mount(client)
    await settle()
    expect(container.querySelectorAll('.st-emitting__row')).toHaveLength(4)
    await vi.advanceTimersByTimeAsync(EMITTING_POLL_MS)
    // The observation ended. The frame goes with it rather than freezing at
    // whatever it last showed, which would look live and be a stopped clock.
    expect(container.querySelector('.st-emitting__row')).toBeNull()
    expect(container.textContent).toContain('No pane is enrolled')
  })

  it('tells a rule that could not read the screen from an agent with no rule', async () => {
    const noRule = structuredClone(ANSWER)
    noRule.reading!.hasRule = false
    noRule.reading!.state = 'unknown'
    noRule.reading!.anchors = []
    noRule.reading!.branches = []
    noRule.reading!.extractors = []
    const { client } = fakeClient([noRule])
    const container = mount(client)
    await settle()
    expect(container.textContent).toContain('no rule for this agent')
    // The screen is still legible, which is the state a person WRITING a rule
    // starts in.
    expect(container.querySelectorAll('.st-emitting__row')).toHaveLength(4)
  })

  it('reports a refused read instead of showing a stale frame', async () => {
    const client = new EmittingClient(new Dispatcher(fixedEndpoint(9876)))
    vi.spyOn(client, 'read').mockRejectedValue(new Error('pane observation not wired'))
    const container = mount(client)
    await settle()
    expect(container.textContent).toContain('pane observation not wired')
    expect(container.querySelector('.st-emitting__row')).toBeNull()
  })
})
