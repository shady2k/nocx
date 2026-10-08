/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agentRecords.save.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface AgentRecordsSaveResult {
  agent: Agent
}
export interface Agent {
  id: string
  builtin: boolean
  state: 'shipped' | 'user' | 'unreadable'
  problem: string
  displayName: string
  command: string
  args: string[]
  icon: string
  colour: string
  disabled: boolean
  env: string[]
  resume: Resume
}
export interface Resume {
  sessionIdArgs: string[]
  resumeIdArgs: string[]
  resumeCwdArgs: string[]
}
