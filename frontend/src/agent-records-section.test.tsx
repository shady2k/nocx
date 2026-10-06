// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { AgentRecordsSection } from './agent-records-section'
import type { Agent } from './generated/agentRecords.list'
import type { AgentRecordDraft, AgentRecordsClient } from './agent-records-client'

const CLAUDE: Agent = {
  id: 'claude',
  builtin: true,
  state: 'shipped',
  problem: '',
  displayName: 'Claude Code',
  command: 'claude',
  args: ['--print'],
  icon: '',
  colour: '',
  disabled: false,
  env: [],
  resume: {
    sessionIdArgs: ['--session-id', '{UUID}'],
    resumeIdArgs: ['--resume', '{UUID}'],
    resumeCwdArgs: [],
  },
}

const custom = (draft: AgentRecordDraft): Agent => ({
  ...draft,
  builtin: false,
  state: 'user',
  problem: '',
})

function client(overrides: Partial<AgentRecordsClient> = {}): AgentRecordsClient {
  return {
    list: vi.fn().mockResolvedValue({ agents: [CLAUDE] }),
    save: vi.fn((draft: AgentRecordDraft) =>
      Promise.resolve({
        agent:
          draft.id === CLAUDE.id ? { ...CLAUDE, ...draft, state: 'user' as const } : custom(draft),
      }),
    ),
    remove: vi.fn().mockResolvedValue({ removed: true }),
    ...overrides,
  } as unknown as AgentRecordsClient
}

describe('AgentRecordsSection', () => {
  afterEach(cleanup)

  it('lets a person add an agent through the live record client, including args and environment', async () => {
    const list = vi.fn().mockResolvedValue({ agents: [] })
    const save = vi.fn((draft: AgentRecordDraft) => Promise.resolve({ agent: custom(draft) }))
    const api = client({ list, save })
    const view = render(() => <AgentRecordsSection client={api} />)
    await waitFor(() => expect(list).toHaveBeenCalled())
    fireEvent.input(screen.getByLabelText(/^Agent ID/), { target: { value: 'reviewer' } })
    fireEvent.input(screen.getByLabelText('Display name'), { target: { value: 'Review agent' } })
    fireEvent.input(screen.getByLabelText(/^Command/), { target: { value: '/usr/bin/reviewer' } })
    fireEvent.input(screen.getByLabelText('Arguments (one per line)'), {
      target: { value: '--task\n{WORKSPACE_NAME}' },
    })
    fireEvent.input(screen.getByLabelText('Environment (KEY=VALUE, one per line)'), {
      target: { value: 'MODE=review' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add agent' }))
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({
          id: 'reviewer',
          displayName: 'Review agent',
          command: '/usr/bin/reviewer',
          args: ['--task', '{WORKSPACE_NAME}'],
          env: ['MODE=review'],
          disabled: false,
        }),
      ),
    )
    await waitFor(() => expect(view.container.textContent).toContain('Agent ID: reviewer'))
  })

  it('edits built-in command and arguments but never offers to remove the built-in', async () => {
    const save = vi.fn((draft: AgentRecordDraft) =>
      Promise.resolve({ agent: { ...CLAUDE, ...draft, state: 'user' as const } }),
    )
    const api = client({ save })
    render(() => <AgentRecordsSection client={api} />)
    await waitFor(() => expect(screen.getByText('Agent ID: claude · Built in')).toBeTruthy())
    const commands = screen.getAllByLabelText(/^Command/)
    const args = screen.getAllByLabelText('Arguments (one per line)')
    fireEvent.input(commands[0], { target: { value: '/opt/claude' } })
    fireEvent.input(args[0], { target: { value: '--resume\n{UUID}' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save agent' }))
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({
          id: 'claude',
          command: '/opt/claude',
          args: ['--resume', '{UUID}'],
          resume: CLAUDE.resume,
        }),
      ),
    )
    expect(screen.queryByRole('button', { name: 'Remove agent' })).toBeNull()
  })

  it('writes the offering switch immediately through the record API', async () => {
    const save = vi.fn((draft: AgentRecordDraft) =>
      Promise.resolve({ agent: { ...CLAUDE, ...draft, state: 'user' as const } }),
    )
    const api = client({ save })
    render(() => <AgentRecordsSection client={api} />)
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: 'Offer this agent in new shells' })).toBeTruthy(),
    )
    fireEvent.click(screen.getByRole('switch', { name: 'Offer this agent in new shells' }))
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(expect.objectContaining({ id: 'claude', disabled: true })),
    )
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: 'Offer this agent in new shells' })).toHaveProperty(
        'checked',
        false,
      ),
    )
  })

  it('removes a custom record and removes it from the offered list', async () => {
    const own = custom({
      id: 'reviewer',
      displayName: 'Review agent',
      command: 'reviewer',
      args: [],
      icon: '',
      colour: '',
      disabled: false,
      env: [],
      resume: { sessionIdArgs: [], resumeIdArgs: [], resumeCwdArgs: [] },
    })
    const list = vi.fn().mockResolvedValue({ agents: [own, CLAUDE] })
    const remove = vi.fn().mockResolvedValue({ removed: true })
    const api = client({ list, remove })
    const view = render(() => <AgentRecordsSection client={api} />)
    await waitFor(() => expect(view.container.textContent).toContain('Agent ID: reviewer'))
    const removeButtons = screen.getAllByRole('button', { name: 'Remove agent' })
    fireEvent.click(removeButtons[0])
    await waitFor(() => expect(remove).toHaveBeenCalledWith('reviewer'))
    await waitFor(() => expect(view.container.textContent).not.toContain('Agent ID: reviewer'))
  })

  it('keeps an unreadable built-in visibly repairable but not offered or removable', async () => {
    const unreadable: Agent = {
      ...CLAUDE,
      state: 'unreadable',
      command: '',
      problem: 'invalid document',
    }
    const api = client({ list: vi.fn().mockResolvedValue({ agents: [unreadable] }) })
    render(() => <AgentRecordsSection client={api} />)
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('invalid document'))
    const offering = screen.getByRole('switch', { name: 'Offer this agent in new shells' })
    expect((offering as HTMLInputElement).disabled).toBe(true)
    expect((offering as HTMLInputElement).checked).toBe(false)
    expect(screen.queryByRole('button', { name: 'Remove agent' })).toBeNull()
  })

  it('makes an unavailable record list visible instead of drawing an empty registry', async () => {
    const api = client({
      list: vi.fn().mockRejectedValue(new Error('record directory unavailable')),
    })
    render(() => <AgentRecordsSection client={api} />)
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain('record directory unavailable'),
    )
  })
})
