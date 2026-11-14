import { expect, it } from 'vitest'
import { agentStatusLine, credentialLine } from './agent-status-line'
it('shows harness readiness and sends a missing model to its agent settings', () => {
  expect(agentStatusLine(null)).toBeNull()
  expect(
    agentStatusLine({
      answering: { ready: true, reason: null, agentId: 'pi', model: null },
      message: null,
    }),
  ).toEqual({ tone: 'success', text: 'Ready' })
  expect(
    agentStatusLine({
      answering: { ready: false, reason: 'model-gone', agentId: 'pi', model: null },
      message: 'Chosen model is gone',
    }),
  ).toEqual({
    tone: 'danger',
    text: 'Chosen model is gone',
    fix: { label: 'Check agent settings', page: 'agent-installed' },
  })
})
it('keeps endpoint credential copy independent of assistant readiness', () => {
  expect(credentialLine('not-required')).toBeNull()
  expect(credentialLine('sealed')?.tone).toBe('warning')
})
