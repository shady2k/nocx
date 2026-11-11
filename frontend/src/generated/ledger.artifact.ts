/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ledger.artifact.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the ledger.artifact JSON-RPC method (nocx-m3fqk, design §6) — ONE artifact's body, joined from its chunks in seq order. It is a separate method from ledger.get on purpose: the recall read must not haul bytes (ADR-0019 §6), so the page and the detail carry metadata and whoever actually wants a body asks for it one at a time. This is what a restored block's output is drawn from. A read that names a row range returns ONE PAGE of a stored-rows artifact and the artifact's own coordinates (nocx-t7xhe): `firstRow`, `lastRow` and `rowCount` describe the whole block, and `rangeStart`, `rangeCount`, `hasMoreBefore` and `hasMoreAfter` describe the page. Those coordinates are null (and the counts zero) for a body that is not stored rows, because a row index has no meaning there. The block's whole geometry rides with every page (nocx-t7xhe, ADR-y45sp41c): `firstRow`..`endRow` is the stream span the block ever held, and `retainedRanges` names the runs the store keeps inside it. A reader reserves that span once, spends one fixed notice on each gap between runs, and never lets a page change the height.
 */
export interface LedgerArtifact {
  /**
   * The artifact asked for, echoed back.
   */
  id: string
  /**
   * What the body is: application/vt carries the SGR a restored block draws, text/plain the derived text search and copy read, and application/x-nocx-rows is a streamed block's output — JSON Lines whose line shape ledger.blockRows.schema.json declares, one row of the screen frame's cell vocabulary per line.
   */
  mediaType: string
  /**
   * The chunks joined in seq order. Empty is a real answer and means the command printed nothing — it is NOT the same as an artifact that is not there, which is a body retention has evicted and which this method reports by refusing the id (ADR-0019 §7).
   */
  body: string
  /**
   * Why the body is not the whole of what was printed, or null when it is: 'cap' when the middle was dropped at the per-command limit, 'gap' when a range was lost, 'suppressed' when capture was refused.
   */
  truncated: 'cap' | 'gap' | 'suppressed' | null
  /**
   * The logical content bytes the store holds for this artifact — the retention budget's unit, and what a reader can check its join against.
   */
  byteLen: number
  /**
   * Absolute index of the artifact's first stored row, or null when the body is not stored rows or holds no row.
   */
  firstRow: number | null
  /**
   * Absolute index of the artifact's last stored row, or null as above.
   */
  lastRow: number | null
  /**
   * How many stored rows the artifact holds — the block's height, which a reader reserves before any page of it arrives. 0 for a body that is not stored rows.
   */
  rowCount: number
  /**
   * Absolute index of this page's first row; null for a body that is not stored rows. A page past the end answers one past the artifact's last row, so 'nothing here' is distinguishable from row zero.
   */
  rangeStart: number | null
  /**
   * Rows in this page. 0 for a body that is not stored rows.
   */
  rangeCount: number
  /**
   * Whether stored rows exist before this page. Always false for a body that is not stored rows.
   */
  hasMoreBefore: boolean
  /**
   * Whether stored rows exist after this page. Always false for a body that is not stored rows.
   */
  hasMoreAfter: boolean
  /**
   * One past the block's whole stream span. Null for a body that is not stored rows. Fixed at close: a page can never widen it, so the rows a reader reserved do not move when a page arrives.
   */
  endRow: number | null
  /**
   * The runs of stream rows the artifact holds, ascending, each [from, to) with to exclusive. A cap eviction leaves a head run and a tail run; an emulator loss can split a run. The gap between two runs is ONE fixed-size notice, whatever its row count, so the block's height is decided by this geometry and not by what happened to load. Empty for a body that is not stored rows.
   */
  retainedRanges: {
    from: number
    to: number
  }[]
}
