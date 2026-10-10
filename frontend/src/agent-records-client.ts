import { Dispatcher } from './dispatcher'
import type {
  Agent as ListedAgent,
  AgentRecordsListResult,
  Resume as ListedResume,
} from './generated/agentRecords.list'
import type { AgentRecordsRemoveResult } from './generated/agentRecords.remove'
import type {
  Agent as SavedAgent,
  AgentRecordsSaveResult,
  Resume as SavedResume,
} from './generated/agentRecords.save'

/** The editable fields of one existing agent record. Identity and build facts are not editable. */
export type AgentRecordDraft = Pick<
  ListedAgent,
  'id' | 'displayName' | 'command' | 'args' | 'icon' | 'colour' | 'disabled' | 'env'
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
}
