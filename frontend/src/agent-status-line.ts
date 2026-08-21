// THE derivation of "agent.status → readiness sentence" (AD-8: one owner
// per behaviour — the owner is whoever already has it). The ask chip
// renders this; a second inline mapping would be two derivations that agree
// everywhere and disagree somewhere they did not check. The sentences are
// the product's words.
//
// The credential fact is an enum, not a boolean (ADR-0032): 'none' (no
// reference at all), 'deleted' (the secret is gone) and 'sealed' (the vault
// cannot answer right now) each get their own sentence. An endpoint with no
// key is not told to unlock a vault — that was the three-facts conflation
// this mapping exists to prevent.
import type { AgentStatusResult } from './generated/agent.status'

export interface AgentStatusLine {
  tone: 'neutral' | 'warning' | 'danger' | 'success'
  text: string
  /** Where this rung is fixed. Absent when nothing is broken — and absent
   *  for 'unavailable' too, because no page repairs an unreadable store and
   *  a button that leads nowhere is worse than no button. */
  fix?: { label: string; page: 'endpoints' | 'roles' }
}

/** The credential fact → its sentence, one owner (AD-8). The endpoints
 *  row reuses this for a row whose credential cannot resolve — the row
 *  must say the same words the ask chip says, or the two surfaces drift.
 *  'resolvable' has no sentence here: it is the absence of a problem, and
 *  the caller falls through to its own next fact (lastProbe, Ready). */
export function credentialLine(
  credential: AgentStatusResult['credential'],
): AgentStatusLine | null {
  switch (credential) {
    case 'sealed':
      return { tone: 'warning', text: 'The vault is locked — unlock it to use the assistant' }
    case 'deleted':
      return { tone: 'warning', text: "The endpoint's key was deleted — add it again" }
    case 'none':
      return { tone: 'warning', text: 'The endpoint has no key yet' }
    case 'unavailable':
      return { tone: 'warning', text: 'The credential is unavailable right now' }
  }
  return null
}

/** The rung of the ladder a person is on when the answering role does not
 *  resolve: one sentence and the ONE page that fixes it (nocx-rikz5). Six
 *  reasons, six sentences — the copy is verbatim and is reused unchanged by
 *  the Roles page and the editor's model chip, because two surfaces inventing
 *  their own wording for one state is how one rung says different things in
 *  two places. */
function unresolvedRoleLine(
  reason: AgentStatusResult['answering']['reason'],
): AgentStatusLine | null {
  switch (reason) {
    case 'no-endpoints':
      // Not "choose a model": sending a person to an empty picker is the one
      // answer worse than saying nothing.
      return {
        tone: 'neutral',
        text: 'Add an endpoint first',
        fix: { label: 'Add an endpoint first', page: 'endpoints' },
      }
    case 'no-models':
      // An endpoint exists and offers nothing, so the fix is on Endpoints —
      // Roles would open a picker with no rows in it.
      return {
        tone: 'warning',
        text: 'That endpoint offers no models — check it',
        fix: { label: 'Check the endpoint', page: 'endpoints' },
      }
    case 'unassigned':
      return {
        tone: 'warning',
        text: 'Choose a model',
        fix: { label: 'Choose a model', page: 'roles' },
      }
    case 'endpoint-gone':
      return {
        tone: 'danger',
        text: "The model's endpoint is gone — choose another",
        fix: { label: 'Choose a model', page: 'roles' },
      }
    case 'model-gone':
      return {
        tone: 'danger',
        text: 'That model is no longer offered — choose another',
        fix: { label: 'Choose a model', page: 'roles' },
      }
    case 'unavailable':
      // No fix: a store that could not be read is not repaired on any page.
      return { tone: 'danger', text: 'Settings could not be read — the assistant is unavailable' }
  }
  return null
}

/** Map agent.status facts to the readiness sentence a surface shows. A
 *  soft degrade — an unresolved role, an unresolvable credential, a failed
 *  probe — is a visible sentence, never only a log line. null means no status
 *  has been read yet (a surface shows its placeholder, not a lie). */
export function agentStatusLine(st: AgentStatusResult | null): AgentStatusLine | null {
  if (!st) return null
  // THE ROLE FIRST (nocx-rikz5). This used to open on endpointConfigured, so
  // an endpoint with a valid key and no model chosen reported "Ready" and the
  // refusal arrived at the first question. Readiness is whether the role the
  // ask will use can resolve; the endpoint and the credential are reasons it
  // cannot, not a separate headline. An unresolved role has no endpoint
  // either, so the credential below would be a sentence about an endpoint
  // nobody chose.
  if (!st.answering.ready) {
    const rung = unresolvedRoleLine(st.answering.reason)
    if (rung) return rung
  }
  // A resolvable role still needs a usable credential: a key that is gone
  // stops the ask just as surely as an unassigned role, whatever the last
  // probe said.
  const line = credentialLine(st.credential)
  if (line) return line
  const p = st.lastProbe
  if (p && !p.ok) {
    return { tone: 'danger', text: `Last test failed: ${p.error}` }
  }
  if (p && p.ok) {
    return { tone: 'success', text: `Last test ok (${p.model})` }
  }
  return { tone: 'success', text: 'Ready' }
}
