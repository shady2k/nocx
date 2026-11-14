/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agentRecords.detect.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface AgentRecordsDetectResult {
  agents: Agent[]
}
export interface Agent {
  id: string
  displayName: string
  found: boolean
  version: string
  readiness: 'not_installed' | 'installed' | 'ready' | 'not_ready' | 'invalid' | 'unknown'
}
