// @vitest-environment jsdom
/**
 * User-path tests for the guided calibration (nocx-etejh).
 *
 * What a person does here: they open Settings, pick the pane their agent is
 * running in, start a calibration, are asked for one named state at a time,
 * and end with a labelled set. The two things asserted hardest are the two the
 * bead is falsified by, and both are about what this surface CANNOT do:
 *
 *   - it cannot name a label. Every call it makes carries a pane, an action
 *     and the step it is showing, and nothing else — so there is no way to
 *     attach a label to a frame the person did not produce for it.
 *   - it cannot skip a required state. The button is not drawn for one, and
 *     the backend's refusal is shown as it came when one is attempted anyway.
 */
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { CalibrationClient } from './calibration-client'
import { TypingClient } from './typing-client'
import type { AgentType } from './generated/agent.type'
import type { AgentCalibration } from './generated/agent.calibration'
import {
  AgentCalibrationSection,
  CALIBRATION_POLL_MS,
  TEST_LINE,
} from './agent-calibration-section'

const STEPS: NonNullable<AgentCalibration['calibration']>['steps'] = [
  { label: 'idle', required: true, ask: 'Leave the agent waiting for input.', expect: 'free_text' },
  { label: 'working', required: true, ask: 'Give the agent something to do.', expect: 'working' },
  {
    label: 'asks-you',
    required: true,
    ask: 'Let the agent ask you to approve a tool.',
    expect: 'permission_choice',
  },
  {
    label: 'menu-open',
    required: false,
    ask: 'Optional: open one of the agent’s own menus.',
    expect: 'modal_choice',
  },
]

/** The verdict a never-calibrated agent has: no authority, and a reason
 *  saying why. There is no "absent" case to fixture — the wire always carries
 *  one, so a surface never has to invent the safe reading of nothing. */
const UNVERIFIED: NonNullable<AgentCalibration['calibration']>['verification'] = {
  mayType: false,
  labelled: 0,
  agreed: 0,
  disagreements: [],
  reason: 'claude has never been calibrated, so there is nothing to check its rule against',
}

function answer(
  over: Partial<NonNullable<AgentCalibration['calibration']>> = {},
): AgentCalibration {
  return {
    panes: [{ sessionId: 'sess-1', agent: 'claude' }],
    calibration: {
      sessionId: 'sess-1',
      agent: 'claude',
      steps: STEPS,
      verification: UNVERIFIED,
      ...over,
    },
  }
}

function fakeClient(read: AgentCalibration, answers: AgentCalibration[] = []) {
  const client = new CalibrationClient(new Dispatcher(fixedEndpoint(9876)))
  const reads = vi
    .spyOn(client, 'read')
    .mockImplementation(() => Promise.resolve(structuredClone(read)))
  let n = 0
  const calls: { action: string; step?: number; sessionId: string }[] = []
  const act = vi.spyOn(client, 'answer').mockImplementation((sessionId, action, step) => {
    calls.push({ sessionId, action, step })
    const next = answers[Math.min(n, answers.length - 1)]
    n++
    return next ? Promise.resolve(structuredClone(next)) : Promise.reject(new Error('refused'))
  })
  return { client, calls, act, reads }
}

/** The typing primitive, spied. What a test asserts about it is what the
 *  surface ASKED for — the pane and the text — because everything that decides
 *  whether a keystroke is sent is in the backend, on a frame it reads itself. */
function fakeTyping(result?: AgentType, fail?: Error) {
  const client = new TypingClient(new Dispatcher(fixedEndpoint(9876)))
  const calls: { sessionId: string; text: string }[] = []
  vi.spyOn(client, 'type').mockImplementation((sessionId, text) => {
    calls.push({ sessionId, text })
    return fail ? Promise.reject(fail) : Promise.resolve(result!)
  })
  return { client, calls }
}

function mount(client: CalibrationClient, typing?: TypingClient): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <AgentCalibrationSection client={client} typing={typing ?? fakeTyping().client} />, {
    container,
  })
  return container
}

async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0)
}

/** Pick the pane, the way a person does, so the surface has something to
 *  calibrate. */
async function choosePane(container: HTMLElement): Promise<void> {
  const select = container.querySelector('select')!
  fireEvent.change(select, { target: { value: 'sess-1' } })
  await settle()
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('the guided calibration', () => {
  it('asks for one named state and offers to capture the pane as it is', async () => {
    const walking = answer({ walk: { pending: 0, given: [] } })
    const { client, calls } = fakeClient(answer(), [walking])
    const container = mount(client)
    await settle()
    await choosePane(container)

    fireEvent.click(container.querySelector('[data-calibration-action="begin"]')!)
    await settle()

    const ask = container.querySelector('.st-calibration__ask')!
    expect(ask.getAttribute('data-label')).toBe('idle')
    expect(ask.textContent).toContain('Leave the agent waiting for input.')
    expect(calls).toEqual([{ sessionId: 'sess-1', action: 'begin', step: undefined }])
  })

  it('never sends a label — only the pane, the action and the step it is showing', async () => {
    // Mid-walk on purpose: a surface that sent a fixed step would answer the
    // step it is not showing, which is the stale answer the backend refuses.
    const walking = answer({
      walk: { pending: 1, given: [{ label: 'idle', skipped: false, atMs: 0 }] },
    })
    const next = answer({
      walk: {
        pending: 2,
        given: [
          { label: 'idle', skipped: false, atMs: 0 },
          { label: 'working', skipped: false, atMs: 1 },
        ],
      },
    })
    const { client, calls } = fakeClient(walking, [next])
    const container = mount(client)
    await settle()
    await choosePane(container)

    fireEvent.click(container.querySelector('[data-calibration-action="capture"]')!)
    await settle()

    // The whole falsifier, at the only place it could be broken from: if a
    // label could travel from here, it could be pointed at any frame at all.
    expect(calls).toEqual([{ sessionId: 'sess-1', action: 'capture', step: 1 }])
    for (const call of calls) {
      expect(Object.keys(call).sort()).toEqual(['action', 'sessionId', 'step'])
    }
    // And the surface moved on to the next question rather than staying on
    // one a person could answer twice.
    expect(container.querySelector('.st-calibration__ask')!.getAttribute('data-label')).toBe(
      'asks-you',
    )
  })

  it('offers no way to skip a required state, and offers one for an optional state', async () => {
    const required = answer({ walk: { pending: 0, given: [] } })
    const { client } = fakeClient(required)
    const container = mount(client)
    await settle()
    await choosePane(container)
    expect(container.querySelector('[data-calibration-action="skip"]')).toBeNull()
    expect(container.textContent).toContain('cannot be skipped')

    cleanup()
    const optional = answer({
      walk: {
        pending: 3,
        given: [
          { label: 'idle', skipped: false, atMs: 0 },
          { label: 'working', skipped: false, atMs: 1 },
          { label: 'asks-you', skipped: false, atMs: 2 },
        ],
      },
    })
    const second = fakeClient(optional)
    const c2 = mount(second.client)
    await settle()
    await choosePane(c2)
    expect(c2.querySelector('[data-calibration-action="skip"]')).toBeTruthy()
    // And it says what declining costs, in the vocabulary the rest of nocx
    // uses: uncalibrated reads as unknown, which is treated as busy.
    expect(c2.textContent).toContain('uncalibrated')
  })

  it('shows the backend refusal when a step cannot be answered', async () => {
    const walking = answer({ walk: { pending: 0, given: [] } })
    // No answers queued, so the client rejects — which is what a required
    // step being skipped, or a stale step being answered, looks like here.
    const { client } = fakeClient(walking)
    const container = mount(client)
    await settle()
    await choosePane(container)

    fireEvent.click(container.querySelector('[data-calibration-action="capture"]')!)
    await settle()
    expect(container.textContent).toContain('refused')
    // The step is still the one being asked: a refusal does not advance the
    // walk, so a person retries the question they were on.
    expect(container.querySelector('.st-calibration__ask')!.getAttribute('data-label')).toBe('idle')
  })

  it('offers the previous step again rather than a way to re-point a label', async () => {
    const walking = answer({
      walk: { pending: 1, given: [{ label: 'idle', skipped: false, atMs: 0 }] },
    })
    const back = answer({ walk: { pending: 0, given: [] } })
    const { client, calls } = fakeClient(walking, [back])
    const container = mount(client)
    await settle()
    await choosePane(container)

    fireEvent.click(container.querySelector('[data-calibration-action="redo"]')!)
    await settle()
    expect(calls).toEqual([{ sessionId: 'sess-1', action: 'redo', step: 1 }])
    // Back to being ASKED for idle, which is the point: the label is written
    // by answering the question again, never by choosing it.
    expect(container.querySelector('.st-calibration__ask')!.getAttribute('data-label')).toBe('idle')
  })

  it('says what the stored set holds, and that a declined state stays uncalibrated', async () => {
    const stored = answer({
      stored: {
        complete: true,
        labels: [
          { label: 'idle', skipped: false, atMs: 0 },
          { label: 'working', skipped: false, atMs: 1 },
          { label: 'asks-you', skipped: false, atMs: 2 },
          { label: 'menu-open', skipped: true },
        ],
      },
    })
    const { client } = fakeClient(stored)
    const container = mount(client)
    await settle()
    await choosePane(container)
    expect(container.textContent).toContain('Calibrated: 3 labelled states')
    expect(container.textContent).toContain('declined')
    const row = container.querySelector('.st-calibration__row[data-label="menu-open"]')!
    expect(row.textContent).toContain('declined')
    // A state nobody was ever asked for is not the same claim, and the row
    // for it says so.
    const untouched = answer({ stored: { complete: false, labels: [] } })
    cleanup()
    const second = fakeClient(untouched)
    const c2 = mount(second.client)
    await settle()
    await choosePane(c2)
    expect(c2.querySelector('.st-calibration__row[data-label="menu-open"]')!.textContent).toContain(
      'not asked yet',
    )
  })

  it('stops asking once the surface is gone', async () => {
    const { client, reads } = fakeClient(answer())
    const container = mount(client)
    await settle()
    await choosePane(container)
    const before = reads.mock.calls.length
    cleanup()
    await vi.advanceTimersByTimeAsync(CALIBRATION_POLL_MS * 4)
    expect(reads.mock.calls.length).toBe(before)
  })

  // ── the verdict, and its consequence (nocx-jse6x) ──────────────────────

  it('says what a verified rule may DO, not merely that it verified', async () => {
    const { client } = fakeClient(
      answer({
        stored: { complete: true, labels: [] },
        verification: { mayType: true, labelled: 3, agreed: 3, disagreements: [] },
      }),
    )
    const container = mount(client)
    await settle()
    await choosePane(container)

    const verdict = container.querySelector('#calibration-verdict')!
    expect(verdict.querySelector('[data-may-type]')!.getAttribute('data-may-type')).toBe('true')
    expect(verdict.textContent).toContain('Verified against 3 of 3 labelled states')
    expect(verdict.textContent).toContain('nocx may type into a pane running claude')
  })

  // ── AND THE CLAIM IS CHECKABLE WHERE IT IS MADE (nocx-dkawo.1) ─────────

  // The user path this bead exists for, as far as a person can walk it here:
  // a rule that has earned typing authority, a button, and a line that lands
  // in the agent's input box with nothing pressed after it.
  it('offers to type a test line once the rule has earned it, and presses nothing', async () => {
    const { client } = fakeClient(
      answer({
        stored: { complete: true, labels: [] },
        verification: { mayType: true, labelled: 3, agreed: 3, disagreements: [] },
      }),
    )
    const typing = fakeTyping({
      sessionId: 'sess-1',
      agent: 'claude',
      outcome: 'typed',
      state: 'free_text',
    })
    const container = mount(client, typing.client)
    await settle()
    await choosePane(container)

    fireEvent.click(container.querySelector('[data-typing-action="try"]')!)
    await settle()

    expect(typing.calls).toHaveLength(1)
    expect(typing.calls[0].sessionId).toBe('sess-1')
    expect(typing.calls[0].text).toBe(TEST_LINE)
    const said = container.querySelector('[data-typing-outcome]')!
    expect(said.getAttribute('data-typing-outcome')).toBe('typed')
    expect(said.textContent).toContain('nothing was sent')
  })

  // THE REFUSAL IS SAID OUT LOUD. It arrives as a result rather than an error
  // precisely so it can be: a pane asking the person to approve a tool is not
  // a malformed request, and a control that silently does nothing is
  // indistinguishable from a broken one.
  it('says which state refused when nothing was typed', async () => {
    const { client } = fakeClient(
      answer({
        stored: { complete: true, labels: [] },
        verification: { mayType: true, labelled: 3, agreed: 3, disagreements: [] },
      }),
    )
    const typing = fakeTyping({
      sessionId: 'sess-1',
      agent: 'claude',
      outcome: 'refused',
      state: 'permission_choice',
      reason:
        'that pane is asking you to approve something, and nocx types only into a pane that is waiting for input',
    })
    const container = mount(client, typing.client)
    await settle()
    await choosePane(container)

    fireEvent.click(container.querySelector('[data-typing-action="try"]')!)
    await settle()

    const said = container.querySelector('[data-typing-outcome]')!
    expect(said.getAttribute('data-typing-outcome')).toBe('refused')
    expect(said.getAttribute('data-typing-state')).toBe('permission_choice')
    expect(said.textContent).toContain('asking you to approve something')
  })

  // And the button is not there at all for a rule that has not earned it, so
  // the page never offers what the backend would refuse.
  it('does not offer to type into a pane whose rule has not earned it', async () => {
    const { client } = fakeClient(answer({ stored: { complete: true, labels: [] } }))
    const container = mount(client)
    await settle()
    await choosePane(container)

    expect(container.querySelector('[data-typing-action="try"]')).toBeNull()
  })

  // The soft degrade stated in the product rather than in a log: a rule that
  // has not classified its labels still lights the indicator, and the page
  // says so — including which label stopped classifying, because that is what
  // a person repairs.
  it('states the consequence of an unverified rule, and names what stopped classifying', async () => {
    const { client } = fakeClient(
      answer({
        stored: { complete: true, labels: [] },
        verification: {
          mayType: false,
          labelled: 3,
          agreed: 2,
          disagreements: [{ label: 'asks-you', expected: 'permission_choice', got: 'free_text' }],
          reason:
            "claude's rule answered 1 of the 3 labelled states with something other than the state they were produced for",
        },
      }),
    )
    const container = mount(client)
    await settle()
    await choosePane(container)

    const verdict = container.querySelector('#calibration-verdict')!
    expect(verdict.querySelector('[data-may-type]')!.getAttribute('data-may-type')).toBe('false')
    expect(verdict.textContent).toContain('indicator only')
    expect(verdict.textContent).toContain('will not type into it')
    const row = verdict.querySelector('[data-disagreement="asks-you"]')!
    expect(row.textContent).toContain('free_text')
    expect(row.textContent).toContain('permission_choice')
  })

  // A complete set is evidence, never authority. The two sentences sit in two
  // sections precisely so a person can see which of them is the problem.
  it('does not read a complete set as permission to type', async () => {
    const { client } = fakeClient(
      answer({
        stored: { complete: true, labels: [{ label: 'idle', skipped: false, atMs: 0 }] },
        verification: {
          mayType: false,
          labelled: 3,
          agreed: 0,
          disagreements: [],
          reason: 'nothing in this build knows how to read claude\u2019s screen',
        },
      }),
    )
    const container = mount(client)
    await settle()
    await choosePane(container)

    expect(container.querySelector('#calibration-stored')!.textContent).toContain('Calibrated:')
    expect(
      container
        .querySelector('#calibration-verdict [data-may-type]')!
        .getAttribute('data-may-type'),
    ).toBe('false')
  })
})
