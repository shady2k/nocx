/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/host.request.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the host.request notification (nocx-uo1k6, design D3): the coordinator asks an attached client to perform one native-host capability it cannot perform itself. The coordinator is a daemon with no window (design §1), so a native file picker, a browser open, a desktop banner and a window raise are all reached by asking a client. requestId is the broker-minted correlation id the client echoes back in host.resolved; capability names which effect is asked for, from a closed server vocabulary. The remaining members are that capability's arguments and are absent for capabilities that take none: url for shell.openUrl; title, body and sessionId for attention.banner; count for attention.badge; executable, digest and workspace for agent.approval.
 */
export interface HostRequest {
  /**
   * The broker-minted request id; echoed back in host.resolved.
   */
  requestId: string
  /**
   * Which native-host effect the client is asked to perform.
   */
  capability:
    | 'dialog.file'
    | 'dialog.directory'
    | 'shell.openUrl'
    | 'attention.banner'
    | 'attention.badge'
    | 'attention.bounce'
    | 'window.focus'
    | 'agent.approval'
  /**
   * shell.openUrl: the http(s) URL to open. Already validated by the transport.
   */
  url?: string
  /**
   * attention.banner: the banner title, passed to the OS verbatim.
   */
  title?: string
  /**
   * attention.banner: the banner body, passed to the OS verbatim.
   */
  body?: string
  /**
   * attention.banner: the session the banner is about, carried back on a click (AD-7 — session-id is server-authoritative).
   */
  sessionId?: string
  /**
   * attention.badge: the dock badge count; 0 clears it.
   */
  count?: number
  /**
   * agent.approval: the absolute path of the agent the person is being asked to admit. The PATH alone, because the renderer words the facts and a value carrying two of them cannot be given a row each.
   */
  executable?: string
  /**
   * agent.approval: the SHA-256 of that file's bytes. Sent so the person can be told why it is shown - the answer is remembered against these bytes, so a replaced binary is asked about again - rather than being left to wonder what an unverifiable hex string is for.
   */
  digest?: string
  /**
   * agent.approval: the workspace the answer covers. The NAME, not the durable scope key it is part of: 'tool-endpoint:workspace:default' is an identifier this surface must not parse to find a word for a person, and the grammar of that key has one owner in the backend.
   */
  workspace?: string
  /**
   * The MACHINE this answer is about (nocx-50w7p.16). A person's yes admits an executable ON ONE MACHINE: the same path and the same bytes on two hosts, or under two accounts on one host, is a different answer to give, so the machine is part of what the durable answer is keyed by. DERIVED BY THE BACKEND from the session's own route — its kind, its host, the account the connection authenticated as, and the host key it was accepted under — and never from a probe, a caller, or anything the agent says about itself. Four facts rather than one composed string, because the renderer is what words them: a composed value would make every surface parse a key whose grammar has one owner in the backend, the same rule this contract already states for `workspace`. The same object is declared in host.request.schema.json and in both agentAccess schemas, and internal/transport's TestTheMachineShapeIsDeclaredOnce holds the three copies identical and both branches exact.
   */
  machine?:
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
}
