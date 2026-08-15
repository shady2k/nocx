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

/** Map agent.status facts to the readiness sentence a surface shows. A
 *  soft degrade — no endpoint, an unresolvable credential, a failed probe —
 *  is a visible sentence, never only a log line. null means no status has
 *  been read yet (a surface shows its placeholder, not a lie). */
export function agentStatusLine(st: AgentStatusResult | null): AgentStatusLine | null {
  if (!st) return null
  if (!st.endpointConfigured) {
    return { tone: 'neutral', text: 'No endpoint configured yet' }
  }
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
