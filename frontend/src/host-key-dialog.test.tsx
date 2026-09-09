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
const EXECUTABLE =
  '/nix/store/7a60q5dgnv6z96c279rc1nalyiw4mgqn-bash-interactive-5.3p15/bin/bash' +
  ' (sha256:4e8ad350dcb1c859cb4c7f0d694a90f7a5734465b1096f3a2b0c7d4e8f1a2b3c4)'
const SCOPE = 'tool-endpoint:workspace:default'

function open(busy = false) {
  const onDecide = vi.fn()
  const view = render(() => (
    <AgentApprovalDialog executable={EXECUTABLE} scope={SCOPE} busy={busy} onDecide={onDecide} />
  ))
  return { view, onDecide }
}

describe('AgentApprovalDialog', () => {
  afterEach(cleanup)

  it('names the executable and the scope as fact rows a long value can wrap in', () => {
    const { view } = open()
    const values = Array.from(
      view.container.querySelectorAll('.ui-fact-list__value'),
      (el) => el.textContent,
    )
    expect(values).toEqual([EXECUTABLE, SCOPE])
    const names = Array.from(
      view.container.querySelectorAll('.ui-fact-list__name'),
      (el) => el.textContent,
    )
    expect(names).toEqual(['Executable', 'Scope'])
  })

  // D14's wording, asserted because it is the part that tells a person the
  // grant reaches past the process they typed.
  it('says the grant covers the commands the agent launches', () => {
    const { view } = open()
    expect(view.container.textContent).toContain('and commands it launches')
  })

  it('reports allow and deny as the answer the person gave', () => {
    const { view, onDecide } = open()
    fireEvent.click(view.getByText('Allow this agent and commands it launches'))
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
