// What a pane says about reaching its host, derived in ONE place.
//
// The backend answers with one observation (pane.connection), and several
// surfaces draw it: the pane's corner mark, the tab's warning, the head's
// paragraph, the pane's own agent indicator. A second derivation of "is this
// connection in trouble" is the defect ADR-ba6xet1b exists to make impossible,
// so every one of them reads this function and nothing else.
//
// NOTHING IS THRESHOLDED HERE. `slow` is the backend's grade (it has
// hysteresis, so the milliseconds alone cannot reproduce it), and `stalled`
// is the backend's own verdict about silence. This module only orders the
// states into the four words a surface knows.

import type { ConnectionCondition } from './ui/connection-indicator'
import type { PaneConnection } from './generated/pane.connection'
import { t } from './i18n/locale-model'

export type { ConnectionCondition }

/**
 * Fold one observation into the one condition a surface draws.
 *
 * Severity order, and each step is a different KIND of statement rather than
 * a louder one:
 *
 *   lost        — the connection is finished with: failed, ended, or its
 *                 session ended under it. Terminal for this connection.
 *   unreachable — the host stopped answering and NOTHING has ended, so the
 *                 work on the far side is very likely still running.
 *   slow        — it is answering, just late. Not a failure.
 *   reachable   — nothing to say, and nothing is drawn. This is also the
 *                 answer while opening, waiting for a person and reconnecting:
 *                 those are the head's business, and a corner mark that said
 *                 "not connected" during every open would be a mark nobody
 *                 reads.
 */
export function connectionCondition(observation: PaneConnection | null): ConnectionCondition {
  if (observation === null) return 'reachable'
  switch (observation.transport.state) {
    case 'failed':
    case 'ended':
      return 'lost'
    case 'stalled':
      return 'unreachable'
    case 'connected':
      return observation.quality?.slow === true ? 'slow' : 'reachable'
    default:
      return 'reachable'
  }
}

/** The round trip behind the condition, or null when nothing measured one.
 *  Absent and zero are the same statement — "no measurement" — and a probe
 *  that never answered must not read as an instant reply. */
export function connectionRoundTripMs(observation: PaneConnection | null): number | null {
  const ms = observation?.quality?.roundTripMs
  return ms != null && ms > 0 ? ms : null
}

/**
 * Whether observations about this pane are still ARRIVING. A slow host is
 * observed — its bytes are late, not absent — so only the two conditions that
 * stop the stream count. `panes.ts` reads this for its agent indicator, which
 * must stop asserting what it last observed over a pipe that is gone.
 */
export function connectionObserves(condition: ConnectionCondition): boolean {
  return condition !== 'unreachable' && condition !== 'lost'
}

/** How old the observation is, in words, when it is old enough to matter.
 *  The renderer DISPLAYS observedAt and never derives a belief from it. */
export function observedAgo(observedAt: string | undefined, now = Date.now()): string | undefined {
  if (observedAt === undefined) return undefined
  const ms = now - Date.parse(observedAt)
  if (!Number.isFinite(ms) || ms < 60_000) return undefined
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 60) return `since ${minutes} min ago`
  return `since ${Math.floor(minutes / 60)} h ago`
}

/** The tab strip's warning from the same fold, or null when the connection
 *  has nothing to warn about. The two words are the vocabulary the tab strip
 *  already speaks. */
export function connectionWarning(observation: PaneConnection | null): { label: string } | null {
  // A healthy transport whose shell failed is not a connection loss; it is a
  // shell the person can retry from the head. The mark says which, so a tab
  // never reports "Connection lost" for a connection that is answering.
  if (observation?.shellFailure !== undefined) {
    return { label: t('connection.shellFailed', 'Shell failed') }
  }
  const condition = connectionCondition(observation)
  if (condition === 'lost') return { label: t('connection.lost', 'Connection lost') }
  if (condition === 'unreachable') {
    return { label: t('connection.notAnswering', 'Host not answering') }
  }
  return null
}
