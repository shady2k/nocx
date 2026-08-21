// The one frontend seam that ships command history over the control plane
// (nocx-rtg0.13, AD-1 as amended by nocx-m64b). The renderer owns the VT
// state (AD-6) and derives the facts of a completed command from it; these
// two functions are where those facts cross — a small structured record
// after the fact, never a copy of the output. When a fuller event envelope
// lands (nocx-rtg0.3), only this module changes: the ledger and the recall
// overlay call here and nowhere else.
//
// recordCommand takes the authenticated attempt as its authority (ADR-0024
// §5): only an authenticated same-domain completion may persist a history
// record — the `trusted` boolean that used to launder stream-derived
// verdicts is deleted, and what persists is the attempt's
// domain-authenticated status. The persisted command text is ALWAYS the
// record's app-owned text, never the attempt's: for an attached app attempt
// the shell's wire line may carry vault-resolved secrets while the record
// carries references, and a shell-originated attempt opens no record at all
// (the command-text decision of bead nocx-u7uh.7 — an authenticated origin
// does not make a line the user typed a password into safe to store).
import type { CommandAuthor, CommandRecord, CommandStatus } from './command-ledger'
import type { ExecutionAttempt } from './lifecycle/state'
import type { HistoryQuery } from './generated/history.query'
import type { HistoryRecord } from './generated/history.record'
import { HistoryOutbox, payloadBytes } from './history-outbox'
import type { WSClient } from './ipc'
import { log } from './log'
import type { RecallScope } from './recall'

/** The history.record request — the ledger's facts minus what never crosses
 *  (the session-local id, the live marker-line accessor, the disposed flag)
 *  and minus the output, which is never retained (ADR-0008). paneId is the
 *  ONE deliberate exception to "session-local ids never cross the wire"
 *  (nocx-tsajw): the renderer-minted per-tab identity that scopes the
 *  pending-capture registry. It is opaque to the backend — minted once per
 *  tab, never reused, and bound to the connection it arrives on. */
export interface HistoryRecordParams {
  command: string
  cwd: string
  host: string
  /** Who submitted the command, minted at submit by the submitting target
   *  (design §3.1, nocx-iadtt) — the entries.kind vocabulary. The store
   *  side never derives it from anything else. */
  author: CommandAuthor
  status: CommandStatus
  exitCode: number | null
  startedAt: number | null
  endedAt: number | null
  paneId: string
}

/** Send one completed command's facts to the store, authorized by the
 *  authenticated attempt that completed it. Best-effort by design: a
 *  socket drop or an unavailable store loses the entry for this session —
 *  the honest cost of not blocking the terminal — and the recall overlay
 *  still answers from the session ledger until the store comes back.
 *
 *  The attempt is authority, never data: the record's command text is what
 *  persists (app-owned, reference-intact), and the attempt's own command
 *  field never crosses this seam. `trusted` does not exist on the wire.
 *  Only a COMPLETED attempt persists: an open attempt has nothing to
 *  record, and an abandoned one is `unknown` — the ledger keeps it for the
 *  session, but nothing unreported crosses to the store (ADR-0024 §5's
 *  interval: absence of a completion is not a status).
 *  Resolves with the store's ack — what was masked and, when a credential
 *  was detected, the pending-capture offers — or null on failure. The ack
 *  is what lets the block show the masked command and attach the
 *  after-submit receipt; a dropped record must never surface as a terminal
 *  error, so the caller treats null exactly like "nothing to show". */
export function recordCommand(
  client: WSClient,
  paneId: string,
  rec: CommandRecord,
  attempt: ExecutionAttempt,
): Promise<HistoryRecord | null> {
  if (attempt.state !== 'completed') return Promise.resolve(null)
  void attempt // the authority the caller already proved; the params carry only the record
  const params: HistoryRecordParams = {
    command: rec.command,
    cwd: rec.cwd,
    host: rec.host,
    author: rec.author,
    status: rec.status,
    exitCode: rec.exitCode,
    // The ledger clocks wall-clock epoch milliseconds (Date.now()), already
    // integral; the rounding is defensive for an injected fractional clock
    // in tests — the schema says integer, so the wire copy rounds.
    startedAt: rec.startedAt === null ? null : Math.round(rec.startedAt),
    endedAt: rec.endedAt === null ? null : Math.round(rec.endedAt),
    paneId,
  }
  // THROUGH THE OUTBOX, not straight at the socket (nocx-rtg0.4). A drop
  // between a command finishing and this call landing used to lose the
  // command silently — AD-9 replays PTY bytes and says nothing about the
  // control plane. The outbox keeps it and sends it when the socket returns,
  // within the bound nocx-rtg0.10 set.
  //
  // The answer is unchanged: the ack when it lands, null when it did not, so
  // every caller that treats null as "nothing to show" keeps working. A
  // record delivered later reports nothing back, deliberately — by then the
  // block that asked has its answer, and moving a receipt under a person who
  // has stopped looking is worse than not moving it.
  return historyOutbox
    .submit<HistoryRecord>({
      bytes: payloadBytes(params),
      send: () => client.call<HistoryRecord>('history.record', params),
    })
    .then((ack) => {
      // The ack is trusted only when it confirms the author the record was
      // minted with (design §3.1, nocx-iadtt): the backend must keep the
      // fact it was handed, never derive its own from a lane or a run
      // state. A mismatch means the row was accepted under a different
      // author than the renderer minted — a wire-integrity failure, not a
      // recoverable difference. Treated like a dropped record (null:
      // nothing to show; the masked command and capture offers belong to a
      // row the renderer cannot vouch for) and logged for the one place
      // that can act on it.
      if (ack !== null && ack.author !== rec.author) {
        log.warn('history.record: ack author mismatch', { sent: rec.author, acked: ack.author })
        return null
      }
      return ack
    })
}

/**
 * The one outbox, module-scoped for the reason the store's repositories are
 * singletons: a second one would be a second answer to "what have we not
 * managed to record", and the two would disagree about the bound the moment
 * either filled.
 *
 * Its drain is wired to the connection in ipc.ts, which is the only place
 * that knows a socket came back.
 */
export const historyOutbox = new HistoryOutbox()
export async function queryHistory(
  client: WSClient,
  scope: RecallScope,
  cwd: string,
  host: string,
  text?: string,
): Promise<HistoryQuery> {
  const params: Record<string, unknown> = { scope }
  if (scope === 'directory') {
    params.cwd = cwd
    params.host = host
  } else if (scope === 'host') {
    params.host = host
  }
  // The search filter (nocx-ms7v). Omitted when empty rather than sent as "",
  // so "no filter" is one state on the wire and not two.
  if (text !== undefined && text !== '') params.text = text
  return client.call<HistoryQuery>('history.query', params)
}
