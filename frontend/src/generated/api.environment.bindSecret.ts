/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.environment.bindSecret.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.environment.bindSecret JSON-RPC method: the VALUE of one secret variable is now in the vault, under the binding this collection and environment own, and the environment file is untouched — it declares the NAME and there is no field in it a value could be written into (design §8). An empty result is still a contract: additionalProperties: false on an empty shape is what makes "returns nothing" enforceable, and a renderer that wanted a field here — the id, an echo of the value — could not be written. THE RESULT IS EMPTY BECAUSE THERE IS NOTHING SAFE TO SAY. The identifier for stored credential material never leaves internal/apibind (ADR-0011, §8.1), and the value came from the caller, so echoing either would be this method handing back the one thing it exists to take away. What a person sees afterwards is the environment listing, where the variable is named and its value is not.
 */
export interface ApiEnvironmentBindSecretResult {}
