// agentStatusLine — the ONE mapping of agent.status facts to the readiness
// sentence (nocx-x8s2.2 extracted it from the endpoints section so the ask
// chip and the settings page cannot drift). The sentences are the product's
// words; this pins them and their precedence: no endpoint outranks an
// unresolvable credential outranks a failed probe.
import { describe, expect, it } from 'vitest'
import { agentStatusLine } from './agent-status-line'
import type { AgentStatusResult } from './generated/agent.status'

const ready: AgentStatusResult = {
  endpointConfigured: true,
  credential: 'resolvable',
  lastProbe: null,
}

describe('agentStatusLine', () => {
  it('null status (nothing read yet) renders nothing — a placeholder, not a lie', () => {
    expect(agentStatusLine(null)).toBeNull()
  })

  it('no endpoint configured outranks everything', () => {
    expect(agentStatusLine({ ...ready, endpointConfigured: false })).toEqual({
      tone: 'neutral',
      text: 'No endpoint configured yet',
    })
  })

  it('a sealed vault is one fact: the unlock offer, not a generic sentence', () => {
    expect(agentStatusLine({ ...ready, credential: 'sealed' })).toEqual({
      tone: 'warning',
      text: 'The vault is locked — unlock it to use the assistant',
    })
  })

  it('a deleted key is another fact: never told to unlock a vault', () => {
    expect(agentStatusLine({ ...ready, credential: 'deleted' })).toEqual({
      tone: 'warning',
      text: "The endpoint's key was deleted — add it again",
    })
  })

  it('no reference at all is a third fact', () => {
    expect(agentStatusLine({ ...ready, credential: 'none' })).toEqual({
      tone: 'warning',
      text: 'The endpoint has no key yet',
    })
  })

  it('a store failure is its own honest sentence, not one of the three', () => {
    expect(agentStatusLine({ ...ready, credential: 'unavailable' })).toEqual({
      tone: 'warning',
      text: 'The credential is unavailable right now',
    })
  })

  it('a failed last probe reports the probe error, with its tone', () => {
    expect(
      agentStatusLine({
        ...ready,
        lastProbe: {
          name: 'local',
          model: 'qwen',
          kind: 'model' as const,
          ok: false,
          error: 'connection refused',
          elapsedMs: 12,
          at: '2026-08-14T00:00:00Z',
        },
      }),
    ).toEqual({ tone: 'danger', text: 'Last test failed: connection refused' })
  })

  it('a successful probe names the model it was measured with', () => {
    expect(
      agentStatusLine({
        ...ready,
        lastProbe: {
          name: 'local',
          model: 'qwen',
          kind: 'model' as const,
          ok: true,
          elapsedMs: 42,
          at: '2026-08-14T00:00:00Z',
        },
      }),
    ).toEqual({ tone: 'success', text: 'Last test ok (qwen)' })
  })

  it('configured, resolvable, never probed → Ready', () => {
    expect(agentStatusLine(ready)).toEqual({ tone: 'success', text: 'Ready' })
  })
})
