// @vitest-environment jsdom
import { afterEach, expect, it } from 'vitest'
import { Show } from 'solid-js'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { recordApprovalDecision } from './agent-approval-decision'
import type { Dispatcher } from './dispatcher'
import { createApprovalQueue } from './agent-approval-queue'
import { AgentApprovalPrompt } from './agent-approval-prompt'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'

const policy: AgentApprovalRequested = {
  runId: 'run',
  attempt: 1,
  tool: 'files.read',
  callId: 'call',
  argHash: 'hash',
  arguments: '{"path":"/repo/a"}',
  reason: 'policy',
  effect: 'observe',
  standing: { available: false, rule: '', reason: '' },
}
const egress: AgentApprovalRequested = {
  ...policy,
  reason: 'egress',
  findings: [],
  wasError: false,
}
afterEach(cleanup)

it('retires an expired or withdrawn question without waiting for a stale RPC answer', async () => {
  const queue = createApprovalQueue()
  let resolve!: (recorded: boolean) => void
  const response = new Promise<boolean>((done) => {
    resolve = done
  })
  queue.add(policy)
  const answering = queue.answer(() => response)
  queue.add(egress)
  queue.retire(policy)
  expect(queue.active()?.reason).toBe('egress')
  expect(queue.busy()).toBe(false)
  let resolveNext!: (recorded: boolean) => void
  const nextAnswer = queue.answer(
    () =>
      new Promise<boolean>((done) => {
        resolveNext = done
      }),
  )
  resolve(false)
  await answering
  expect(queue.active()?.reason).toBe('egress')
  expect(queue.busy()).toBe(true)
  resolveNext(true)
  await nextAnswer
  queue.retire(egress)
  expect(queue.active()).toBeNull()
})

it('shows the next phase even when it arrives before the previous answer response', async () => {
  const queue = createApprovalQueue()
  let resolve!: (recorded: boolean) => void
  const response = new Promise<boolean>((done) => {
    resolve = done
  })
  const calls: unknown[] = []
  const dispatcher = {
    call: (_method: string, params: unknown) => {
      calls.push(params)
      return response.then(() => ({ state: 'running' }))
    },
  } as unknown as Dispatcher
  render(() => (
    <Show when={queue.active()} keyed>
      {(ask) => (
        <AgentApprovalPrompt
          open
          ask={ask}
          busy={queue.busy()}
          onDecide={(approved, scope) =>
            void queue.answer((question) =>
              recordApprovalDecision(question, approved, scope, {
                dispatcher,
                onWarning: () => {},
                onError: () => {},
              }),
            )
          }
        />
      )}
    </Show>
  ))
  queue.add(policy)
  fireEvent.click(screen.getByRole('button', { name: /^Allow once/ }))
  expect(calls).toHaveLength(1)
  queue.add(egress)
  resolve(true)
  await waitFor(() => {
    expect(
      screen.getByRole('dialog', { name: 'A tool result contained secret-shaped material' }),
    ).toBeTruthy()
    expect(screen.getByRole('button', { name: /^Allow once/ }).hasAttribute('disabled')).toBe(false)
  })
})

it('does not duplicate one exact question but retains different calls and attempts', () => {
  const queue = createApprovalQueue()
  queue.add(policy)
  queue.add({ ...policy })
  queue.add({ ...policy, callId: 'second' })
  queue.add({ ...policy, attempt: 2 })
  return queue
    .answer(() => Promise.resolve(true))
    .then(async () => {
      expect(queue.active()?.callId).toBe('second')
      await queue.answer(() => Promise.resolve(true))
      expect(queue.active()?.attempt).toBe(2)
    })
})
