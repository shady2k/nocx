/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/exit.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The exit JSON-RPC notification: the backend telling the renderer that a session ended (nocx-ictcq). Server-initiated and unsolicited, so it has no request to correlate against and no caller checking its shape — which is exactly where an addressing or shape defect would hide, and why it gets the same three checks as a method. The cause is the discriminator: an authoritative terminal event — the shell itself exited, carrying its exit status — versus a loss — the channel is gone, the host is unreachable, a handshake expired, a reattach failed, or the session was torn down without an authoritative status. The two are deliberately indistinguishable at the wire today's payload, which is why a tab whose ssh connection dropped silently vanished. A lost tab must be marked, never destroyed; a clean exit closes it exactly as before. The renderer routes the notification by sessionId (AD-7); the cause is a closed set (oneOf, const branches) so a shape that cannot exist cannot be sent. status is present exactly when cause is "exited" and is the shell's own exit status — a loss never carries one.
 */
export type Exit =
  | {
      /**
       * The server-authoritative session that ended (AD-7). One WebSocket carries several terminal tabs, so the renderer must route this notification by session id before any tab may act on it.
       */
      sessionId: string
      /**
       * The backend instance that minted the session that ended (AD-7 — server-authoritative, never minted by the renderer): minted once per backend start, so two instances are never equal. The renderer compares it against the pair the open ack carried, so a late exit for this sessionId out of a previous backend instance is refused instead of closing the current tab.
       */
      instanceId: string
      /**
       * The session's epoch within its backend instance, as minted at open: distinguishes a later session that reuses a sessionId from the incarnation this notification ends. Same vocabulary as the open ack's sessionEpoch.
       */
      sessionEpoch: number
      /**
       * The shell exited on its own: an authoritative terminal event. The exit status is the shell's own report, never the backend's guess.
       */
      cause: 'exited'
      /**
       * The shell's exit status (0-255; the sign-extended form of a signal death is not an exit status and is not sent as one).
       */
      status: number
    }
  | {
      /**
       * The server-authoritative session that ended (AD-7). One WebSocket carries several terminal tabs, so the renderer must route this notification by session id before any tab may act on it.
       */
      sessionId: string
      /**
       * The backend instance that minted the session that ended (AD-7 — server-authoritative, never minted by the renderer): minted once per backend start, so two instances are never equal. The renderer compares it against the pair the open ack carried, so a late exit for this sessionId out of a previous backend instance is refused instead of marking the current tab.
       */
      instanceId: string
      /**
       * The session's epoch within its backend instance, as minted at open: distinguishes a later session that reuses a sessionId from the incarnation this notification ends. Same vocabulary as the open ack's sessionEpoch.
       */
      sessionEpoch: number
      /**
       * A loss: the channel is gone, the host is unreachable, a handshake expired, a reattach failed, or the session was torn down without an authoritative status. The word is the content ledger's (internal/content/ledger.go) — a state chosen rather than an assertion of liveness. No status is carried: a loss must never be dressed up as an exit. The granular detail is deliberately NOT on this wire: internal/lifecyclechannel.LossCause (hello-timeout, end-of-stream, read-error, closed) names losses of the authenticated lifecycle channel and is that vocabulary's single owner (nocx-viil.1), while this notification fires for every session — plain non-integrated ssh and local shells included — where no lifecycle channel may ever have existed. The axis that knows the detail (session.integrationChanged, carrying ssh.RefusalReason) already says it for the sessions that have one; this discriminator's job is mark-vs-close.
       */
      cause: 'interrupted'
    }
  | {
      /**
       * The server-authoritative session that ended (AD-7). One WebSocket carries several terminal tabs, so the renderer must route this notification by session id before any tab may act on it.
       */
      sessionId: string
      /**
       * The backend instance that minted the session that ended (AD-7 — server-authoritative, never minted by the renderer): minted once per backend start, so two instances are never equal. The renderer compares it against the pair the open ack carried, so a late exit for this sessionId out of a previous backend instance is refused instead of marking the current tab.
       */
      instanceId: string
      /**
       * The session's epoch within its backend instance, as minted at open: distinguishes a later session that reuses a sessionId from the incarnation this notification ends. Same vocabulary as the open ack's sessionEpoch.
       */
      sessionEpoch: number
      /**
       * The backend restarted and authoritatively confirmed that the claimed session did not survive. No shell exit status is known.
       */
      cause: 'backend-restarted'
    }
