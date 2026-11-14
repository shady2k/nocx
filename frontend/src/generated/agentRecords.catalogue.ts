/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agentRecords.catalogue.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface AgentRecordsCatalogueResult {
  agents: {
    id: string
    readiness: {
      state: 'not_installed' | 'not_configured' | 'ready'
      message: string
    }
    models: {
      id: string
      name: string
    }[]
  }[]
}
