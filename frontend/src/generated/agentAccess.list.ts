/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agentAccess.list.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the agentAccess.list JSON-RPC method: every answer a person has given to the 'may this agent use nocx's tools' question, so they can read back what they decided and unmake it (nocx-6jbad). The store was write-only from the product's side — a denial is never silently retried, deliberately, and the only way to reconsider one was editing agent-approvals.json by hand. Only answers under the tool-endpoint scope are listed, because that is the only question this document holds answers to.
 */
export interface AgentAccessList {
  /**
   * The answers, ordered by executable path then digest then workspace. The order is the backend's and it is stable: a list that reshuffles between reads is one a person cannot use to choose what to forget.
   */
  answers: {
    /**
     * The agent's absolute path, as it was when the answer was given.
     */
    executable: string
    /**
     * SHA-256 of that file's bytes. The answer is keyed by it, so a replaced program is a different row and is asked about again.
     */
    digest: string
    /**
     * The workspace the answer covers, by name — not the durable scope key it is part of, whose grammar has one owner in the backend.
     */
    workspace: string
    /**
     * What the person answered. The store's own two words; the surface is what turns them into a sentence.
     */
    answer: 'granted' | 'denied'
  }[]
}
