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
    /**
     * The MACHINE this answer is about (nocx-50w7p.16). A person's yes admits an executable ON ONE MACHINE: the same path and the same bytes on two hosts, or under two accounts on one host, is a different answer to give, so the machine is part of what the durable answer is keyed by. DERIVED BY THE BACKEND from the session's own route — its kind, its host, the account the connection authenticated as, and the host key it was accepted under — and never from a probe, a caller, or anything the agent says about itself. Four facts rather than one composed string, because the renderer is what words them: a composed value would make every surface parse a key whose grammar has one owner in the backend, the same rule this contract already states for `workspace`. The same object is declared in host.request.schema.json and in both agentAccess schemas, and internal/transport's TestTheMachineShapeIsDeclaredOnce holds the three copies identical and both branches exact.
     */
    machine:
      | {
          /**
           * The machine this backend runs on.
           */
          kind: 'local'
        }
      | {
          /**
           * A machine reached over a connection the backend authenticated.
           */
          kind: 'ssh'
          /**
           * The host as dialed.
           */
          host: string
          /**
           * The account the connection authenticated as. Two accounts on one host are two machines, because the agent being admitted runs as one of them.
           */
          account: string
          /**
           * The SHA-256 fingerprint of the host key the connection was accepted under, in the spelling every host-key error and every known_hosts line carries. It is IN the answer's identity for the reason ADR-0023 gives for helper consent: a machine whose key changed is a different answer to give, so the stored yes stops matching and the question is asked again.
           */
          hostKey: string
        }
  }[]
}
