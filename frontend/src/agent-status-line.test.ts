// agentStatusLine — the ONE mapping of agent.status facts to the readiness
// sentence (nocx-x8s2.2 extracted it from the endpoints section so the ask
// chip and the settings page cannot drift). The sentences are the product's
// words; this pins them, their fix targets and their precedence: the ROLE
// first (nocx-rikz5) outranks an unresolvable credential outranks a failed
// probe.
//
// A rung is asserted as sentence AND target together. A rung that says the
// right thing and opens the wrong page is exactly the defect the ladder
// exists to prevent, and a test that checks only the text cannot see it.
import { describe, expect, it } from 'vitest'
import { agentStatusLine, credentialLine } from './agent-status-line'
import type { AgentStatusResult } from './generated/agent.status'

const ready: AgentStatusResult = {
  endpointConfigured: true,
  credential: 'resolvable',
  lastProbe: null,
  answering: { ready: true, reason: null, endpoint: 'Local', model: 'qwen3' },
}

/** An unready role on an otherwise healthy install: an endpoint IS stored and
 *  its credential DOES resolve. That is the point — every rung below must
 *  still be the sentence, because readiness is a fact about the role. */
const unready = (
  reason: NonNullable<AgentStatusResult['answering']['reason']>,
): AgentStatusResult => ({
  endpointConfigured: true,
  credential: 'resolvable',
  lastProbe: null,
  answering: { ready: false, reason, endpoint: null, model: null },
})

const okProbe = {
  name: 'local',
  model: 'qwen',
  kind: 'model' as const,
  ok: true,
  elapsedMs: 42,
  at: '2026-08-14T00:00:00Z',
}

describe('agentStatusLine', () => {
  it('null status (nothing read yet) renders nothing — a placeholder, not a lie', () => {
    expect(agentStatusLine(null)).toBeNull()
  })

  describe('the ladder: one rung, one sentence, one target', () => {
    it('sends a person with no endpoints to endpoints, never to an empty model list', () => {
      expect(
        agentStatusLine({
          ...unready('no-endpoints'),
          endpointConfigured: false,
          credential: null,
        }),
      ).toEqual({
        tone: 'neutral',
        text: 'Add an endpoint first',
        fix: { label: 'Add an endpoint first', page: 'endpoints' },
      })
    })

    it('an endpoint offering zero models is checked on Endpoints, not chosen from on Roles', () => {
      expect(agentStatusLine(unready('no-models'))).toEqual({
        tone: 'warning',
        text: 'That endpoint offers no models — check it',
        fix: { label: 'Check the endpoint', page: 'endpoints' },
      })
    })

    it('says to choose a model rather than Ready when nothing is assigned', () => {
      expect(agentStatusLine(unready('unassigned'))).toEqual({
        tone: 'warning',
        text: 'Choose a model',
        fix: { label: 'Choose a model', page: 'roles' },
      })
    })

    it('a vanished endpoint is a danger, fixed by choosing again — never re-pointed silently', () => {
      expect(agentStatusLine(unready('endpoint-gone'))).toEqual({
        tone: 'danger',
        text: "The model's endpoint is gone — choose another",
        fix: { label: 'Choose a model', page: 'roles' },
      })
    })

    it('a vanished model is its own sentence, fixed on Roles', () => {
      expect(agentStatusLine(unready('model-gone'))).toEqual({
        tone: 'danger',
        text: 'That model is no longer offered — choose another',
        fix: { label: 'Choose a model', page: 'roles' },
      })
    })

    it('an unreadable store carries NO fix — no page repairs it', () => {
      const line = agentStatusLine(unready('unavailable'))
      expect(line).toEqual({
        tone: 'danger',
        text: 'Settings could not be read — the assistant is unavailable',
      })
      expect(line?.fix).toBeUndefined()
    })
  })

  describe('precedence', () => {
    it('the role outranks a stored endpoint and a resolvable credential', () => {
      // endpointConfigured true + credential resolvable is precisely the state
      // that used to report "Ready" while the ask was about to be refused.
      expect(agentStatusLine(unready('unassigned'))).toEqual({
        tone: 'warning',
        text: 'Choose a model',
        fix: { label: 'Choose a model', page: 'roles' },
      })
    })

    it('the role outranks a successful probe — a good test of a model nobody chose', () => {
      expect(agentStatusLine({ ...unready('unassigned'), lastProbe: okProbe })).toEqual({
        tone: 'warning',
        text: 'Choose a model',
        fix: { label: 'Choose a model', page: 'roles' },
      })
    })

    it('a resolving role with a broken credential reports the credential, not Ready', () => {
      expect(agentStatusLine({ ...ready, credential: 'deleted' })).toEqual({
        tone: 'warning',
        text: "The endpoint's key was deleted — add it again",
      })
    })

    it('the credential outranks a successful probe — a key that is gone stops the ask', () => {
      expect(agentStatusLine({ ...ready, credential: 'deleted', lastProbe: okProbe })).toEqual({
        tone: 'warning',
        text: "The endpoint's key was deleted — add it again",
      })
    })
  })

  describe('the credential facts of a role that resolves', () => {
    it('a sealed vault is one fact: the unlock offer, not a generic sentence', () => {
      expect(agentStatusLine({ ...ready, credential: 'sealed' })).toEqual({
        tone: 'warning',
        text: 'The vault is locked — unlock it to use the assistant',
      })
    })

    it('no reference at all is a third fact', () => {
      expect(agentStatusLine({ ...ready, credential: 'none' })).toEqual({
        tone: 'warning',
        text: 'The endpoint has no key yet',
      })
    })

    it('says nothing when the endpoint explicitly needs no credential', () => {
      expect(credentialLine('not-required')).toBeNull()
    })

    it('a store failure is its own honest sentence, not one of the three', () => {
      expect(agentStatusLine({ ...ready, credential: 'unavailable' })).toEqual({
        tone: 'warning',
        text: 'The credential is unavailable right now',
      })
    })
  })

  describe('a role that resolves keeps the probe behaviour', () => {
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
      expect(agentStatusLine({ ...ready, lastProbe: okProbe })).toEqual({
        tone: 'success',
        text: 'Last test ok (qwen)',
      })
    })

    it('keeps Ready for a role that resolves', () => {
      expect(agentStatusLine(ready)).toEqual({ tone: 'success', text: 'Ready' })
    })
  })
})
