// The editor-side per-target store (ADR-0004 §3, nocx-4ff.7): the drafts
// and session history of every registered input target, keyed by the
// REGISTRY's target id. The seam is the id: a third target added later gets
// its own draft and corpus by registering — nothing in this module names a
// target, and nothing here reads the registry (the host drives it).
//
// Why beside `editorExtensions?()` rather than inside it: the target's
// extensions ride a CM6 compartment that is RECONFIGURED on every switch,
// and a reconfiguration destroys the StateFields it replaces — per-target
// draft state installed there would be reset by the very act of switching.
// This store lives outside the view, keyed by id, so it survives swaps and
// is target-agnostic by construction.

import type { HistoryEntry, HistoryQuery } from './generated/history.query'
import type { RecallScope } from './recall'

/** One target's draft at the moment the person switched away: the document
 *  plus the selection (anchor/head) and the vertical scroll offset, so a
 *  switch away and back restores exactly what was being edited. */
export interface TargetDraft {
  readonly text: string
  readonly from: number
  readonly to: number
  readonly scrollTop: number
}

/** One submission recorded under the active target's id. A shell command
 *  and a question are different corpora; the id is what keeps them apart.
 *  `seq` is the store's own monotonically increasing stamp — the row's
 *  stable handle for the recall overlay (selection survives a filter
 *  change only when a row's id is the same on every page). Stamped by
 *  record(); callers never supply it. */
export interface TargetHistoryRow {
  readonly doc: string
  readonly cwd: string
  readonly host: string
  /** Wall-clock epoch milliseconds (Date.now() units — the ledger's clock). */
  readonly at: number
  readonly seq: number
}

/** The draft-and-history store, keyed by target id. */
export class TargetState {
  private readonly drafts = new Map<string, TargetDraft>()
  private readonly historyByTarget = new Map<string, TargetHistoryRow[]>()
  private nextSeq = 0

  /** Snapshot the editor under `id` — called on switch-out, so the draft
   *  is whatever the editor holds at that moment (a submitted command
   *  cleared the editor, so a later switch saves the empty line). */
  saveDraft(id: string, draft: TargetDraft): void {
    this.drafts.set(id, draft)
  }

  /** The draft saved under `id`, or undefined when none was ever saved. */
  draft(id: string): TargetDraft | undefined {
    return this.drafts.get(id)
  }

  /** Append a submission to `id`'s corpus. Called for every submit through
   *  the editor, whichever target is active — the corpus is keyed by the
   *  id, never chosen by it. */
  record(id: string, row: Omit<TargetHistoryRow, 'seq'>): void {
    const rows = this.historyByTarget.get(id) ?? []
    rows.push({ ...row, seq: this.nextSeq++ })
    this.historyByTarget.set(id, rows)
  }

  /** `id`'s recorded submissions, oldest first. */
  history(id: string): readonly TargetHistoryRow[] {
    return this.historyByTarget.get(id) ?? []
  }
}

/**
 * Serve a history page from one target's recorded corpus, newest first,
 * filtered to the requested rung and text — the same shape and the same
 * filter rules the ledger fallback uses (queryLedgerHistory), so the recall
 * overlay draws the rows exactly as it draws shell rows. `source` is
 * 'session': this corpus is the tab's memory, never the store's.
 */
export function queryTargetHistory(
  rows: readonly TargetHistoryRow[],
  scope: RecallScope,
  cwd: string,
  host: string,
  text?: string,
): HistoryQuery {
  const reversed = [...rows].reverse()
  let coverage: number | null = null
  for (const row of rows) {
    if (coverage === null || row.at < coverage) coverage = row.at
  }
  const needle = text === undefined ? '' : text.toLowerCase()
  const entries: HistoryEntry[] = []
  for (const row of reversed) {
    if (scope === 'directory' && (row.cwd !== cwd || row.host !== host)) continue
    if (scope === 'host' && row.host !== host) continue
    if (needle !== '' && !row.doc.toLowerCase().includes(needle)) continue
    entries.push({
      id: `target-${row.seq}`,
      command: row.doc,
      cwd: row.cwd,
      host: row.host,
      status: 'unknown',
      startedAt: row.at,
      endedAt: row.at,
      // Nothing was masked: the corpus is the renderer's own memory and
      // never crossed the wire, so there is nothing to mask (the same
      // truth the session-ledger fallback states).
      maskedCount: 0,
      maskedKinds: [],
    })
  }
  // The corpus is the whole session: no further pages exist.
  return { entries, scope, exhausted: true, source: 'session', coverage }
}
