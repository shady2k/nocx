// @vitest-environment jsdom
/**
 * AgentApprovalPrompt tests — the renderer half of an approval question
 * (nocx-z9hj4). What a user can do: see ONE kind of question whether the
 * risk was an effect coming in (a policy escalation) or a secret going out
 * (an egress finding), see the tool, the arguments and — for egress — what
 * was found and where, never the material itself; and decide.
 *
 * Since nocx-gycwo the decision has a WIDTH as well as a direction: a policy
 * question offers allow and deny at once, in this session and always, so the
 * place a person is asked is the place they can stop being asked. An egress
 * question keeps two answers, both `once` — "always send secrets to the model
 * provider" is not a standing decision to be made by a button sitting next to
 * five others.
 *
 * What the surface must not overstate (criterion 4): it says what approval
 * covers — this call has not run, and no call after it will — and does NOT
 * claim the domain is untouched.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { AgentApprovalPrompt } from './agent-approval-prompt'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import type { AgentApprove } from './generated/agent.approve'

const POLICY_ASK: AgentApprovalRequested = {
  runId: '7',
  attempt: 1,
  tool: 'files.read',
  callId: 'call_1',
  argHash: 'hash-a',
  arguments: '{"path":"/repo/a.txt"}',
  reason: 'policy',
  // The effect the gate decided on, sent by the backend (nocx-zd1vp). The
  // surface must never work it out from the tool name — that would be a rule
  // keyed by a tool name in everything but storage (ADR-0028 decision 4).
  effect: 'observe',
  resource: { kind: 'path', id: '/repo/a.txt' },
}

const EGRESS_ASK: AgentApprovalRequested = {
  ...POLICY_ASK,
  reason: 'egress',
  wasError: false,
  findings: [
    { source: 'known', secretName: 'github-token', start: 0, end: 5 },
    { source: 'heuristic', kind: 'openai-api-key', start: 11, end: 40 },
  ],
}

function renderPrompt(overrides?: Partial<Parameters<typeof AgentApprovalPrompt>[0]>) {
  const props = {
    open: true,
    ask: POLICY_ASK,
    busy: false,
    onDecide: vi.fn(),
    ...overrides,
  }
  const utils = render(() => <AgentApprovalPrompt {...props} />)
  return { ...utils, props }
}

/** A recorder for the seam the surface actually owns: direction and width. */
function recordDecisions() {
  const decisions: Array<[boolean, AgentApprove['scope']]> = []
  return {
    decisions,
    onDecide: (approved: boolean, scope: AgentApprove['scope']) =>
      decisions.push([approved, scope]),
  }
}

describe('AgentApprovalPrompt', () => {
  afterEach(cleanup)

  it('names the tool, the arguments and the reason — the question a person decides', () => {
    const { container } = renderPrompt()
    expect(container.textContent).toContain('files.read')
    expect(container.textContent).toContain('{"path":"/repo/a.txt"}')
    expect(container.querySelector('.ui-prompt[data-placement="top-sheet"]')).toBeTruthy()
  })

  it('states what approval covers and does not claim the domain is untouched (criterion 4)', () => {
    const { container } = renderPrompt()
    const text = container.textContent ?? ''
    expect(text).toContain('it has not run')
    expect(text).toContain('no call after it in this response will')
    expect(text).toContain('does not promise the terminal is untouched')
  })

  it('renders egress findings — facts, never the material — and distinguishes the sources', () => {
    const { container } = renderPrompt({ ask: EGRESS_ASK })
    const text = container.textContent ?? ''
    expect(text).toContain('Nothing was sent to the model provider')
    expect(text).toContain('Known vault material')
    expect(text).toContain('github-token')
    expect(text).toContain('Heuristic match')
    expect(text).toContain('openai-api-key')
    // The secret VALUE is never on the surface: only facts about it.
    expect(text).not.toContain('sk-')
  })

  it('says when the findings are in an ERROR string rather than a result', () => {
    const { container } = renderPrompt({ ask: { ...EGRESS_ASK, wasError: true } })
    expect(container.textContent).toContain('The tool failed')
  })

  it('a policy question offers three allowances and three refusals, each with its scope', () => {
    const { decisions, onDecide } = recordDecisions()
    const ui = renderPrompt({ onDecide })

    for (const name of [
      'Allow once',
      'Allow in this session',
      'Allow always',
      'Deny once',
      'Deny in this session',
      'Deny always',
    ]) {
      fireEvent.click(ui.getByRole('button', { name }))
    }

    expect(decisions).toEqual([
      [true, 'once'],
      [true, 'session'],
      [true, 'always'],
      [false, 'once'],
      [false, 'session'],
      [false, 'always'],
    ])
  })

  it('groups the allowances apart from the refusals, and leads each group with once', () => {
    const { container } = renderPrompt()
    const groups = Array.from(container.querySelectorAll('.ui-action-group'))
    expect(groups).toHaveLength(2)
    const names = groups.map((g) =>
      Array.from(g.querySelectorAll('.ui-button')).map((b) => b.textContent),
    )
    expect(names).toEqual([
      ['Allow once', 'Allow in this session', 'Allow always'],
      ['Deny once', 'Deny in this session', 'Deny always'],
    ])
  })

  it("names the effect in the product's words, and reads it from effect and never from tool", () => {
    const { container } = renderPrompt()
    expect(container.textContent).toContain('read and inspect')

    cleanup()
    // Same tool name, a different effect: the words must follow `effect`.
    const { container: other } = renderPrompt({
      ask: { ...POLICY_ASK, effect: 'mutate-destructive' },
    })
    expect(other.textContent).toContain('make changes that cannot be undone')
    expect(other.textContent).not.toContain('read and inspect')
  })

  it('says how long a session answer lasts, and never promises the pane', () => {
    const { container } = renderPrompt()
    const text = container.textContent ?? ''
    expect(text).toContain('in this session')
    // The permission binds to the terminal SESSION: restarting the shell in
    // the same pane asks again, so naming the pane would promise a lifetime
    // the answer does not have.
    expect(text).not.toContain('in this pane')
    expect(text).toContain('Agent policy page')
  })

  it('an egress question offers two answers, and both are once', () => {
    const { decisions, onDecide } = recordDecisions()
    const ui = renderPrompt({ ask: EGRESS_ASK, onDecide })

    expect(ui.queryByRole('button', { name: 'Allow always' })).toBeNull()
    expect(ui.queryByRole('button', { name: 'Allow in this session' })).toBeNull()
    expect(ui.queryByRole('button', { name: 'Deny always' })).toBeNull()
    expect(ui.container.querySelectorAll('.ui-button')).toHaveLength(2)

    fireEvent.click(ui.getByRole('button', { name: 'Allow' }))
    fireEvent.click(ui.getByRole('button', { name: 'Deny' }))
    expect(decisions).toEqual([
      [true, 'once'],
      [false, 'once'],
    ])
  })

  it('dismissing the prompt is the NARROWEST refusal, never a standing one', () => {
    const { decisions, onDecide } = recordDecisions()
    const { container } = renderPrompt({ onDecide })
    const overlay = container.querySelector('.ui-prompt-overlay')!
    fireEvent.mouseDown(overlay)
    expect(decisions).toEqual([[false, 'once']])
  })

  it('disables every answer while the decision is in flight', () => {
    const { container } = renderPrompt({ busy: true })
    const buttons = Array.from(container.querySelectorAll('.ui-button'))
    expect(buttons).toHaveLength(6)
    for (const b of buttons) expect((b as HTMLButtonElement).disabled).toBe(true)
  })
})
