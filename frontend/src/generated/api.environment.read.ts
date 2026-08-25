/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.environment.read.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.environment.read JSON-RPC method: ONE environment whole — what it is called, what it answers, which of its variables are secret by name, and how a request under it gets there (design §6.5). The collection listing names environments so a person can choose one and deliberately carries no values; this is the editor's read of a single file, so what it copies lives exactly as long as the ask that opened it. There is no field here in which a secret value or an identifier for one can be spelled — that is §8 restated as a field list rather than as a redaction step.
 */
export interface ApiEnvironmentReadResult {
  environment: Environment
}
export interface Environment {
  /**
   * The name the FILE declares, which is what a person wrote and never derived from the file's path.
   */
  name: string
  /**
   * The plain variables this environment answers. Never null — an environment that declares none is {}, because the renderer's first Object.entries on a null throws. The stored form omits the key entirely, which is right for a file and wrong for a panel.
   */
  values: {
    [k: string]: string
  }
  /**
   * The NAMES of the variables whose values are held outside the file. Never null — none is []. A name here is a declaration that the value lives in the vault under a binding, never the value and never an identifier for it.
   */
  secretVars: string[]
  route: Route
}
/**
 * How a request under this environment gets there (§6.5). It lives on the environment beside the address it belongs with, so switching environment moves the two together and a production address cannot go out around its bastion.
 */
export interface Route {
  /**
   * direct sends from this machine; connection leases the named SSH profile. A file that omits the route reads as direct — the zero value is spelled out here rather than sent as "", so the renderer meets one spelling of one state.
   */
  kind: 'direct' | 'connection'
  /**
   * The connection a `connection` route leases, and "" on a direct one.
   */
  profileId: string
  /**
   * Send WITHOUT verifying the server's certificate — for a development host with a self-signed certificate. It is on the ROUTE because it is part of how a destination is reached, and it is per ENVIRONMENT rather than per app on purpose: a person turns it on for dev and cannot thereby turn it off for production, which is what a global switch would do the moment they forgot it was set. It is written into the collection file, so a colleague sees it in review, and every run that went out under it says so on the run — it can never be quietly on.
   */
  insecureTls: boolean
}
