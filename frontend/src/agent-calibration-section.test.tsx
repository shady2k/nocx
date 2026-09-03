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
import type { AgentCalibration } from './generated/agent.calibration'
import { AgentCalibrationSection, CALIBRATION_POLL_MS } from './agent-calibration-section'

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

function answer(
  over: Partial<NonNullable<AgentCalibration['calibration']>> = {},
): AgentCalibration {
  return {
    panes: [{ sessionId: 'sess-1', agent: 'claude' }],
    calibration: { sessionId: 'sess-1', agent: 'claude', steps: STEPS, ...over },
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

function mount(client: CalibrationClient): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <AgentCalibrationSection client={client} />, { container })
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
})
