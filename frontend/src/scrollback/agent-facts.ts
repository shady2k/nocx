import type { Usage } from '../generated/ledger.get'
import { t } from '../i18n/locale-model'
import { createMeta } from '../ui/meta'

export type AgentUsage = Usage

/** Facts stated by the answering agent, shared by live and restored turns. */
export function appendAgentFacts(parent: HTMLElement, model?: string, usage?: AgentUsage): void {
  if (model) {
    const note = document.createElement('div')
    note.className = 'cmd-answer-provenance'
    note.textContent = t('scrollback.answeredBy', 'answered by {model}', { model })
    parent.appendChild(note)
  }
  if (usage !== undefined) {
    const row = document.createElement('div')
    row.className = 'cmd-agent-usage'
    row.appendChild(
      createMeta([
        t('scrollback.usage.input', '{count} input', { count: usage.input }),
        t('scrollback.usage.output', '{count} output', { count: usage.output }),
        t('scrollback.usage.cacheRead', '{count} from cache', { count: usage.cacheRead }),
      ]),
    )
    parent.appendChild(row)
  }
}
