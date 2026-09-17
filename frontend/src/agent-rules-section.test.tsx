// @vitest-environment jsdom
/**
 * User-path tests for agent rules (nocx-y6w66).
 *
 * What a person does here: they open Settings, find the agent nocx reads
 * wrongly, edit the document that rule is written in, or switch detection off
 * for an agent it should leave alone, or remove their document to go back to
 * the shipped rule.
 *
 * The assertions are about what the surface SHOWS and what it SENDS, because
 * everything that decides which rule reads a pane is in the backend: what this
 * page can be wrong about is drawing a state the wire is not in, offering a
 * control that does nothing, or losing the text a person just typed when a
 * write is refused.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, fireEvent, render, waitFor } from '@solidjs/testing-library'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { AgentRulesClient } from './agent-rules-client'
import type { AgentRules } from './agent-rules-client'
import { AgentRulesSection } from './agent-rules-section'
import type { AgentRule } from './generated/agent.rules'

const DIRECTORY = '/home/someone/.config/nocx-dev/agent-rules'

/** The rule this build ships, as the wire hands it over for editing. */
const SHIPPED_DOCUMENT =
  '{\n  "agent": "claude",\n  "anchors": [],\n  "branches": [],\n  "default": "free_text"\n}'

function row(over: Partial<AgentRule> = {}): AgentRule {
  return { agent: 'claude', state: 'shipped', document: SHIPPED_DOCUMENT, ...over }
}

function answer(...rules: AgentRule[]): AgentRules {
  return { directory: DIRECTORY, rules }
}

type Call = { method: 'set' | 'setEnabled' | 'remove'; agent: string; argument?: string | boolean }

/**
 * A client whose three writes answer with the next queued result — the wire's
 * own contract, where a write IS a read of the state it produced. `fail` makes
 * the write refuse, which is what a document that could not compile looks like
 * from here.
 */
function fakeClient(initial: AgentRules, written: AgentRules[] = []) {
  const client = new AgentRulesClient(new Dispatcher(fixedEndpoint(9876)))
  const calls: Call[] = []
  vi.spyOn(client, 'read').mockImplementation(() => Promise.resolve(structuredClone(initial)))
  const take = (): AgentRules =>
    structuredClone(written.length === 0 ? initial : written[Math.min(taken, written.length - 1)])
  let taken = 0
  const after = () => {
    const next = take()
    taken++
    return next
  }
  vi.spyOn(client, 'set').mockImplementation((agent, document) => {
    calls.push({ method: 'set', agent, argument: document })
    return Promise.resolve(after())
  })
  vi.spyOn(client, 'setEnabled').mockImplementation((agent, enabled) => {
    calls.push({ method: 'setEnabled', agent, argument: enabled })
    return Promise.resolve(after())
  })
  vi.spyOn(client, 'remove').mockImplementation((agent) => {
    calls.push({ method: 'remove', agent })
    return Promise.resolve(after())
  })
  return { client, calls }
}

function mount(client: AgentRulesClient): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <AgentRulesSection client={client} />, { container })
  return container
}

/** The document field for an agent, which is the control the page is about. */
function field(container: HTMLElement, agent = 'claude'): HTMLTextAreaElement {
  return container.querySelector<HTMLTextAreaElement>(`#rule-document-${agent}`)!
}

function button(container: HTMLElement, action: string): HTMLButtonElement {
  return container.querySelector<HTMLButtonElement>(`[data-rule-action="${action}"]`)!
}

afterEach(() => cleanup())

describe('agent rules', () => {
  it('shows the rule that reads the agent, and where the documents live', async () => {
    const { client } = fakeClient(answer(row()))
    const container = mount(client)

    await waitFor(() => expect(field(container).value).toBe(SHIPPED_DOCUMENT))
    expect(container.textContent).toContain('shipped rule')
    // The shipped rule is what a person edits FROM, so the field holds it and
    // the page says where keeping a copy of their own would put it.
    expect(container.textContent).toContain(DIRECTORY)
  })

  it('saves the edited document and draws the state the write produced', async () => {
    const mine = '{"agent":"claude","anchors":[],"branches":[],"default":"error"}'
    const { client, calls } = fakeClient(answer(row()), [
      answer(row({ state: 'user', document: mine })),
    ])
    const container = mount(client)
    await waitFor(() => expect(field(container).value).toBe(SHIPPED_DOCUMENT))

    fireEvent.input(field(container), { target: { value: mine } })
    fireEvent.click(button(container, 'save'))

    await waitFor(() => expect(container.textContent).toContain('your rule'))
    expect(calls).toEqual([{ method: 'set', agent: 'claude', argument: mine }])
    expect(field(container).value).toBe(mine)
  })

  it('switches detection off and says the pane reports unknown', async () => {
    const { client, calls } = fakeClient(answer(row()), [
      answer(row({ state: 'disabled', document: SHIPPED_DOCUMENT })),
    ])
    const container = mount(client)
    await waitFor(() => expect(field(container).value).toBe(SHIPPED_DOCUMENT))

    const toggle = container.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    expect(toggle.checked).toBe(true)
    fireEvent.click(toggle)

    await waitFor(() => expect(container.textContent).toContain('detection off'))
    // Off is not delete, and the page says which it was: the pane reports
    // unknown, so nocx will not type into it, and their document is still
    // here.
    expect(container.textContent).toContain('unknown')
    expect(field(container).value).toBe(SHIPPED_DOCUMENT)
    expect(calls).toEqual([{ method: 'setEnabled', agent: 'claude', argument: false }])
  })

  it('offers removal only when there is something of the person’s to remove', async () => {
    const shipped = fakeClient(answer(row()))
    const first = mount(shipped.client)
    await waitFor(() => expect(field(first).value).toBe(SHIPPED_DOCUMENT))
    expect(button(first, 'remove').disabled).toBe(true)

    cleanup()
    const mine = '{"agent":"claude","anchors":[],"branches":[],"default":"error"}'
    const owned = fakeClient(answer(row({ state: 'user', document: mine })), [answer(row())])
    const second = mount(owned.client)
    await waitFor(() => expect(field(second).value).toBe(mine))
    expect(button(second, 'remove').disabled).toBe(false)

    fireEvent.click(button(second, 'remove'))
    await waitFor(() => expect(second.textContent).toContain('shipped rule'))
    expect(owned.calls).toEqual([{ method: 'remove', agent: 'claude' }])
    expect(field(second).value).toBe(SHIPPED_DOCUMENT)
  })

  it('keeps the document a person typed when the write is refused, and says why', async () => {
    const broken = '{"agent":"claude","default":"probably_idle"}'
    const { client } = fakeClient(answer(row()))
    vi.spyOn(client, 'set').mockImplementation(() =>
      Promise.reject(new Error('agent.rules.set: document default "probably_idle" is not a state')),
    )
    const container = mount(client)
    await waitFor(() => expect(field(container).value).toBe(SHIPPED_DOCUMENT))

    fireEvent.input(field(container), { target: { value: broken } })
    fireEvent.click(button(container, 'save'))

    // The refusal is shown where the person is looking — beside the document
    // that was refused — and their text is still in the field, because a
    // toast would take it away from the box they are editing in.
    await waitFor(() => expect(container.textContent).toContain('is not a state'))
    expect(field(container).value).toBe(broken)
    expect(container.textContent).toContain('shipped rule')
  })

  it('shows an unreadable document as a problem with a way out', async () => {
    const { client } = fakeClient(
      answer(row({ state: 'unreadable', document: '', problem: 'the file is empty' })),
    )
    const container = mount(client)

    await waitFor(() => expect(container.textContent).toContain('cannot be read'))
    expect(container.textContent).toContain('the file is empty')
    // No editor: the text on disk is not readable and the shipped rule is not
    // what is in force, so a field filled with either would invite a save that
    // replaces a file its author cannot see.
    expect(field(container, 'claude')).toBeNull()
    expect(button(container, 'save').disabled).toBe(true)
    // And the way out is offered, because it is the one repair that exists.
    expect(button(container, 'remove').disabled).toBe(false)
  })

  it('says so when this window cannot reach the rules at all', async () => {
    const { client } = fakeClient(answer(row()))
    vi.spyOn(client, 'read').mockImplementation(() => Promise.reject(new Error('not available')))
    const container = mount(client)

    await waitFor(() => expect(container.textContent).toContain('not available'))
    // A page that could only spin, or that fell through to the empty state,
    // would read as "nocx ships no rules" — a claim about the BUILD, which a
    // window that could not ask has no business making.
    expect(container.textContent).not.toContain('No agent has a rule')
    expect(container.querySelector('[data-rule-action="reread"]')).toBeTruthy()
  })
})
