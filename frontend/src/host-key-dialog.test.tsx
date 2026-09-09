// @vitest-environment jsdom
/**
 * AgentApprovalDialog tests — the consent surface that admits an executable
 * process tree to the tool endpoint (nocx-rowqt.12).
 *
 * What a user can do: read WHICH executable and WHICH scope they are being
 * asked about, then allow or deny. The facts are the whole point of the
 * dialog — a person who cannot read the digest cannot give the consent this
 * surface claims to collect — so they are asserted as the kit's fact rows
 * rather than as text somewhere on the page. The row is what carries
 * `overflow-wrap: anywhere`; the two hand-rolled paragraphs it replaced
 * carried nothing, and a 64-character digest left the dialog through a
 * horizontal scrollbar with its closing bracket (nocx-39x9n).
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { AgentApprovalDialog } from './host-key-dialog'

// The shape the backend actually sends: agent_approval.go composes the path
// and the digest into one string, so the value the dialog receives is long,
// unbroken and the reason the wrapping matters.
const EXECUTABLE = '/run/current-system/sw/bin/claude'
const DIGEST = '55640c4f3b8769e625c91e6aeaac3032c713a8bd0b83e04c9265772d7cb40825'
const WORKSPACE = 'default'

function open(busy = false) {
  const onDecide = vi.fn()
  const view = render(() => (
    <AgentApprovalDialog
      executable={EXECUTABLE}
      digest={DIGEST}
      workspace={WORKSPACE}
      busy={busy}
      onDecide={onDecide}
    />
  ))
  return { view, onDecide }
}

describe('AgentApprovalDialog', () => {
  afterEach(cleanup)

  it('names the agent and its fingerprint as fact rows a long value can wrap in', () => {
    const { view } = open()
    const values = Array.from(
      view.container.querySelectorAll('.ui-fact-list__value'),
      (el) => el.textContent,
    )
    // toContain, not toBe: a row's value element carries the note beside the
    // value, which is the point of the note — it cannot drift from what it
    // qualifies.
    expect(values[0]).toBe(EXECUTABLE)
    expect(values[1]).toContain(DIGEST)
    const names = Array.from(
      view.container.querySelectorAll('.ui-fact-list__name'),
      (el) => el.textContent,
    )
    expect(names).toEqual(['Agent', 'Fingerprint', 'Applies to', 'Lasts'])
  })

  // The four questions a person has, and the dialog used to answer none of
  // them (nocx-fu18z). Asserted as text a person can read rather than as the
  // presence of a row, because the row was never the missing part.
  it('says what a yes allows, how far it reaches, how long it lasts and what a no costs', () => {
    const { view } = open()
    const text = view.container.textContent ?? ''
    expect(text).toContain('start other agents in new tabs')
    expect(text).toContain(`Every tab in the ${WORKSPACE} workspace`)
    expect(text).toContain('no way to undo it yet')
    expect(text).toContain('the agent still runs')
  })

  // Vocabulary nobody outside this repository has met. It named the internal
  // surface and never what the surface lets an agent do.
  it('never calls it "the tool endpoint"', () => {
    const { view } = open()
    expect(view.container.textContent ?? '').not.toContain('tool endpoint')
  })

  // D14's wording, asserted because it is the part that tells a person the
  // grant reaches past the process they typed.
  it('says the grant covers the commands the agent launches', () => {
    const { view } = open()
    expect(view.container.textContent).toContain('and commands it launches')
  })

  it('reports allow and deny as the answer the person gave', () => {
    const { view, onDecide } = open()
    fireEvent.click(view.getByText('Allow'))
    expect(onDecide).toHaveBeenCalledWith(true)
    fireEvent.click(view.getByText('Deny'))
    expect(onDecide).toHaveBeenCalledWith(false)
  })

  // A decision in flight must not be given twice: the store write is what
  // takes the time, and a second click would ask a second question.
  it('refuses a second answer while the first is being recorded', () => {
    const { view, onDecide } = open(true)
    fireEvent.click(view.getByText('Saving…'))
    fireEvent.click(view.getByText('Deny'))
    expect(onDecide).not.toHaveBeenCalled()
  })
})
