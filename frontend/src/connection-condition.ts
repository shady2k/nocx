// What a pane says about reaching its host, derived in ONE place.
//
// Three facts arrive separately and answer one question, so they are folded
// here rather than at each surface that draws them. The pane's corner
// indicator and the tab's mark must never disagree about whether a host is
// answering, and two derivations of one concept is the shape AGENTS.md calls
// this repository's most recurrent defect.
//
// NOTHING IS THRESHOLDED HERE. `slow` is the backend's grade (it has
// hysteresis, so the milliseconds alone cannot reproduce it — see
// contracts/session.liveness.schema.json); this function only orders the
// facts by severity.

import type { ConnectionCondition } from './ui/connection-indicator'
import type { SessionLiveness } from './generated/session.liveness'

export type { ConnectionCondition }

/** The last thing the backend said about reaching this pane's host, plus the
 *  one fact the wire does not carry: that the session itself has ended. */
export interface ConnectionFacts {
  /** True once the session is gone. Terminal for this pane, and it outranks
   *  every reachability statement — a host we cannot reach and a session that
   *  no longer exists are different things, and the second is final. */
  sessionLost: boolean
  /** The last session.liveness notification, or null when none has arrived —
   *  which is the ordinary state of a LOCAL pane, whose host is this machine
   *  and is never probed. */
  liveness: SessionLiveness | null
}

/**
 * Fold the facts into the one condition a surface draws.
 *
 * Severity order, and each step is a different KIND of statement rather than
 * a louder one:
 *
 *   lost        — the session ended. Terminal, and the only one with an action.
 *   unreachable — the host stopped answering and NOTHING has ended, so the
 *                 work on the far side is very likely still running.
 *   slow        — it is answering, just late. Not a failure.
 *   reachable   — nothing to say, and nothing is drawn.
 */
export function connectionCondition(facts: ConnectionFacts): ConnectionCondition {
  if (facts.sessionLost) return 'lost'
  const l = facts.liveness
  if (!l) return 'reachable'
  if (l.liveness === 'unknown') return 'unreachable'
  return l.slow === true ? 'slow' : 'reachable'
}

/** The round trip behind the condition, or null when nothing measured one.
 *  Absent and zero are the same statement — "no measurement" — and a probe
 *  that never answered must not read as an instant reply. */
export function connectionRoundTripMs(facts: ConnectionFacts): number | null {
  const ms = facts.liveness?.roundTripMs
  return ms != null && ms > 0 ? ms : null
}
