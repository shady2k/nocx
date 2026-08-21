/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.environment.write.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.environment.write JSON-RPC method: the environment file on disk now holds what was sent, and the file is created when nothing occupied the name — a write to a free name is how an environment comes to exist, so there is no second create method. An empty result is still a contract: the file is the truth (design §6.4), so there is nothing for the write to echo back that a read would not answer more honestly.
 */
export interface ApiEnvironmentWriteResult {}
