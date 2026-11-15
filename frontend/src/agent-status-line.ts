import type { AgentStatusResult } from './generated/agent.status'
import { t } from './i18n/locale-model'
export interface AgentStatusLine {
  tone: 'neutral' | 'warning' | 'danger' | 'success'
  text: string
  fix?: { label: string; page: 'agent-installed' }
}
/** The credential fact → its sentence, one owner (AD-8). The endpoints
 *  row reuses this for a row whose credential cannot resolve — the row
 *  must say the same words the ask chip says, or the two surfaces drift.
 *  'resolvable' has no sentence here: it is the absence of a problem, and
 *  the caller falls through to its own next fact (lastProbe, Ready). */
export function credentialLine(
  credential: 'resolvable' | 'not-required' | 'none' | 'deleted' | 'sealed' | 'unavailable' | null,
): AgentStatusLine | null {
  switch (credential) {
    case 'not-required':
      return null
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

export function agentStatusLine(st: AgentStatusResult | null): AgentStatusLine | null {
  if (!st) return null
  if (st.answering.ready) return { tone: 'success', text: t('agents.status.ready', 'Ready') }
  return {
    tone: 'danger',
    text: st.message ?? t('agents.status.unavailable', 'The answering agent is unavailable.'),
    fix: { label: t('agents.status.manage', 'Check agent settings'), page: 'agent-installed' },
  }
}
