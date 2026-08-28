/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.dump.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface AgentDump {
  request: Drive[]
  response: Drive[]
}
export interface Drive {
  text: string
  truncated: boolean
}
