// @vitest-environment jsdom
/**
 * AgentAccessSection tests (nocx-6jbad).
 *
 * What a user can do that they could not before: SEE which agents they
 * admitted or refused, and unmake one of those answers. Before this page the
 * store was write-only from the product's side — a denial is never silently
 * retried, and the only way back was editing agent-approvals.json by hand.
 *
 * Asserted through the seam a person reaches: the row is there, the button
 * on it is enabled from the state the page loads in, pressing it reaches the
 * client's forget with the answer's own three facts, and the list is read
 * again afterwards so the row is gone.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent, waitFor } from '@solidjs/testing-library'
import { AgentAccessSection } from './agent-access-section'
import type { AgentAccessClient } from './agent-access-client'

const CLAUDE = {
  executable: '/run/current-system/sw/bin/claude',
  digest: '55640c4f3b8769e625c91e6aeaac3032c713a8bd0b83e04c9265772d7cb40825',
  workspace: 'default',
  answer: 'denied' as const,
}

function client(overrides: Partial<AgentAccessClient> = {}) {
  return {
    list: vi.fn().mockResolvedValue({ answers: [CLAUDE] }),
    forget: vi.fn().mockResolvedValue({ forgotten: true }),
    ...overrides,
  } as unknown as AgentAccessClient
}

describe('AgentAccessSection', () => {
  afterEach(cleanup)

  it('shows the answer a person gave, naming the agent and what they said', async () => {
    const view = render(() => <AgentAccessSection client={client()} />)
    await waitFor(() => expect(view.container.textContent).toContain(CLAUDE.executable))
    expect(view.container.textContent).toContain('Denied')
    expect(view.container.textContent).toContain('Every tab in the default workspace')
  })

  // The whole point of the page: the decision is unmakeable from inside the
  // product. Pressing Forget must reach the client with the answer's own
  // facts — the same three the store keys it by.
  it('unmakes an answer and reads the list again', async () => {
    const list = vi
      .fn()
      .mockResolvedValueOnce({ answers: [CLAUDE] })
      .mockResolvedValueOnce({ answers: [] })
    const forget = vi.fn().mockResolvedValue({ forgotten: true })
    const view = render(() => <AgentAccessSection client={client({ list, forget })} />)

    await waitFor(() => expect(view.container.textContent).toContain(CLAUDE.executable))
    fireEvent.click(view.getByText('Forget'))

    await waitFor(() => expect(forget).toHaveBeenCalledWith(CLAUDE))
    await waitFor(() => expect(view.container.textContent).not.toContain(CLAUDE.executable))
    expect(list).toHaveBeenCalledTimes(2)
  })

  // forgotten:false is a success — the answer was already gone. The page
  // refreshes rather than raising, because its list may predate somebody
  // else's forget.
  it('treats "there was nothing to forget" as done, not as a failure', async () => {
    const forget = vi.fn().mockResolvedValue({ forgotten: false })
    const list = vi
      .fn()
      .mockResolvedValueOnce({ answers: [CLAUDE] })
      .mockResolvedValueOnce({ answers: [] })
    const view = render(() => <AgentAccessSection client={client({ list, forget })} />)

    await waitFor(() => expect(view.container.textContent).toContain(CLAUDE.executable))
    fireEvent.click(view.getByText('Forget'))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    expect(view.container.textContent).not.toContain(CLAUDE.executable)
  })

  // An empty list is a sentence, not a blank page: a person who has answered
  // nothing must be told that is the state, and told when the question comes.
  it('says nothing has been asked yet rather than showing an empty page', async () => {
    const view = render(() => (
      <AgentAccessSection client={client({ list: vi.fn().mockResolvedValue({ answers: [] }) })} />
    ))
    await waitFor(() => expect(view.container.textContent).toContain('No agent has asked yet'))
  })

  // A read that failed is not "you have decided nothing". Drawing the empty
  // state over an unreadable document is the silent degrade AGENTS.md names.
  it('does not draw the empty state when the read failed', async () => {
    const list = vi.fn().mockRejectedValue(new Error('socket closed'))
    const view = render(() => <AgentAccessSection client={client({ list })} />)
    await waitFor(() => expect(list).toHaveBeenCalled())
    expect(view.container.textContent).not.toContain('No agent has asked yet')
  })
})
