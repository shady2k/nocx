// What a refused claim MEANS (AD-8's one owner applied to a connection fact).
//
// A claim can fail in three ways and only one of them is news about the SHELL:
//
//   - the backend holds no such session (`unknown-session`): the session is
//     gone, and a replacement shell is the honest next step;
//   - the binding names another incarnation (`foreign-instance`,
//     `foreign-incarnation`): re-claiming under the current binding is the way
//     to the same session;
//   - anything else (a saturated control plane, a dropped socket, a refused
//     replay) is a question that was NOT ANSWERED, and reading it as absence
//     opens a second shell over a process that is still running
//     (nocx-3an1s.1.5).
//
// The discriminator is the typed `data.reason` the backend puts on a -32602,
// never the message, which is prose. This module is the one reading of it:
// the reconnect pass, the adoption of a listed session and the pane's recovery
// all ask here. It lives under `src/connection/` because the answer is a
// connection fact (ADR-ba6xet1b), and because the renderer's connection-owner
// ratchet names this directory as the place such facts are read.

import { RpcError } from '../dispatcher'

/** The claim refusals that mean the BINDING is wrong rather than the session
 *  being gone (nocx-xn63t.6.33). One vocabulary, declared by the backend
 *  (refuseClaim, internal/transport/ws_reclaim.go). "unknown-session" is
 *  deliberately NOT here: that one says this backend holds no such session,
 *  and there is no other incarnation of it to claim. */
const FOREIGN_BINDING_REFUSALS = new Set(['foreign-instance', 'foreign-incarnation'])

/** The typed reason of a refused claim, or undefined for any other failure. */
function claimRefusalReason(err: unknown): string | undefined {
  if (!(err instanceof RpcError) || err.code !== -32602) return undefined
  const data = err.data
  if (typeof data !== 'object' || data === null || !('reason' in data)) return undefined
  return typeof data.reason === 'string' ? data.reason : undefined
}

/** The backend's authoritative answer that it holds no such session
 *  (nocx-3an1s.1.5). Everything else is uncertainty. */
export function isProvenSessionAbsence(err: unknown): boolean {
  return claimRefusalReason(err) === 'unknown-session'
}

/** Definitive absence after a restart, answered by the backend, not inventory inference. */
export function isEndedAfterRestart(err: unknown): boolean {
  return claimRefusalReason(err) === 'session-ended-after-restart'
}

/** The binding is stale, and the session can be claimed again under this
 *  incarnation (nocx-xn63t.6.33). */
export function isForeignBinding(err: unknown): boolean {
  const reason = claimRefusalReason(err)
  return reason !== undefined && FOREIGN_BINDING_REFUSALS.has(reason)
}
