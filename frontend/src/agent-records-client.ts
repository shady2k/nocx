import { Dispatcher } from './dispatcher'
import type {
  Agent as ListedAgent,
  AgentRecordsListResult,
  Resume as ListedResume,
} from './generated/agentRecords.list'
import type { AgentRecordsDetectResult } from './generated/agentRecords.detect'
import type { AgentRecordsRemoveResult } from './generated/agentRecords.remove'
import type {
  Agent as SavedAgent,
  AgentRecordsSaveResult,
  Resume as SavedResume,
} from './generated/agentRecords.save'

/** The editable fields of one existing agent record. Identity and build facts are not editable. */
export type AgentRecordDraft = Pick<
  ListedAgent,
  | 'id'
  | 'displayName'
  | 'command'
  | 'args'
  | 'icon'
  | 'colour'
  | 'disabled'
  | 'env'
  | 'nonLaunchSubcommands'
> & { resume: ListedResume }

/** The Settings surface's view of the live agent-record owner. */
export class AgentRecordsClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  list(): Promise<AgentRecordsListResult> {
    return this.dispatcher.call<AgentRecordsListResult>('agentRecords.list', {})
  }

  save(
    agent: AgentRecordDraft,
  ): Promise<AgentRecordsSaveResult & { agent: SavedAgent & { resume: SavedResume } }> {
    return this.dispatcher.call<AgentRecordsSaveResult>('agentRecords.save', agent)
  }

  remove(id: string): Promise<AgentRecordsRemoveResult> {
    return this.dispatcher.call<AgentRecordsRemoveResult>('agentRecords.remove', { id })
  }

  /**
   * Which of the agents the assistant can run are on this computer, with the
   * version each reports and whether it says it can answer
   * (nocx-a62cl.12.2).
   *
   * `refresh` is a parameter of this call rather than a second method,
   * because it is the same question: the backend caches one pass until a
   * caller asks it to run again, and Settings' Refresh control is that
   * caller. The section is the only surface that reads it today; the
   * assistant's entry in 4b reads the same answer.
   */
  detect(refresh = false): Promise<AgentRecordsDetectResult> {
    return this.dispatcher.call<AgentRecordsDetectResult>(
      'agentRecords.detect',
      refresh ? { refresh: true } : {},
    )
  }
}
