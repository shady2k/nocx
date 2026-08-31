/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.output.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the session.output JSON-RPC method (nocx-22k1c.2): the bytes the backend RECORDED for one session, asked for by stream offset. It is what a client attaching an hour into a run recovers, and the replay ring cannot be: the ring is 256 KiB of transport-side buffering (AD-9, internal/transport/ring.go) and is deliberately not scrollback, so before this method a window that opened an hour late saw about ten screens of an hour's work. THE COORDINATE IS THE ONE THE CLIENT ALREADY SPEAKS. `from`, `offset`, `produced` and every gap bound are byte offsets in the session's output stream — the same offsets the AD-9 ack sends and the ring keys on — so a recording, a ring replay and a client's own cursor are all measured against one origin and can be joined without translation. A second coordinate system for the same stream is the defect this shape exists to avoid. BYTES, NOT A SCREEN. The backend hands back what the session printed and never interprets it (AD-6): there is no grid here, no cursor and no rows. The renderer feeds these bytes to its OWN VT at `effectiveSize` — the size the BACKEND decided this session runs at (nocx-eidfb.1) — and the same bytes plus the same size give the same screen, which is how two clients agree on what a session looks like without the backend ever holding a grid. HOW IT JOINS THE RING. `produced` is the recording's end offset, and the ring may not free a byte the recorder has not passed (ring.go's trim and reclaimRecorded), so `produced` is never below the ring's oldest retained offset while recording is on. A client therefore reads to `produced` and attaches THERE, and the two halves meet with nothing missing between them. When recording is off the recording stops advancing while acks go on freeing the ring, so `produced` can fall behind the `replayFrom` sessions.live reports; the client compares the two, attaches at the later of them, and knows the difference is a hole. That comparison is the renderer's, deliberately: the ring's oldest offset is sessions.live's to state and whether output is being recorded at all is history.status's, and copying either here would be the second owner AD-8 forbids.
 */
export interface SessionOutput {
  /**
   * The session whose recording this is, server-minted and server-authoritative (AD-7). Echoed so a client with several reads in flight can tell them apart without tracking request ids.
   */
  sessionId: string
  effectiveSize: EffectiveSize
  /**
   * The stream offset this answer is measured from — what the caller asked for, or 0 when it asked for nothing. Everything in `runs` and `gaps` lies at or after it, and together they account for every byte of [from, produced) with nothing unstated: what is present is a run, what is gone is a gap. Echoed rather than assumed because a client that pages reads it back to confirm which page it is holding.
   */
  from: number
  /**
   * The stretches of recorded bytes at or after `from`, in stream order, never overlapping. A LIST rather than one blob because the retention bound drops the middle of a long recording: head and tail are two runs with a hole between them, and a single body would silently join bytes that are not adjacent. Never null: a recording with nothing left in the requested span is [], because the renderer maps over it. A run is bounded by the per-answer byte budget, so a recording larger than one answer arrives over several calls — the caller pages by asking again from the end of the last run it received, and knows there is more whenever that end is below `produced`.
   */
  runs: Run[]
  /**
   * The byte ranges inside [from, produced) that the recording no longer holds, in stream order. This is the 'never silently less' half of the answer: the retention bound keeps the head and the tail of a session's output and drops the middle (internal/content/policy.go, and capBody in the renderer cuts a frozen block the same way), so an offset the bound has dropped is answered with what remains AND with the range that is gone, rather than with a shorter run nobody can tell from a shorter session. Never null: a whole recording is []. Derived from the runs that are actually there rather than accumulated as bytes are dropped, so it cannot drift from them. TWO KINDS OF HOLE, AND `reason` IS HOW THEY ARE TOLD APART (nocx-k6p18.2). `cap` means the bytes were recorded and the retention bound evicted them — actionable, since the knob that dropped them is the knob that would have kept them. `unrecorded` means nobody was there to record them at all: no recorder was attached to the stream over that range, so the bound never touched those bytes and naming it would be a false statement in the product. A reader must pick its wording from `reason` and must never assume the cap; a reason it does not recognise is carried through as 'we do not know' rather than turned into a confident answer.
   */
  gaps: Gap[]
  /**
   * How many bytes this session has produced in total, INCLUDING everything the bound dropped — the recording's end offset. It is what makes a hole measurable rather than invisible, it is where the next page starts, and it is the offset a client attaches at once it has read this far. Zero on a session that has printed nothing, and zero on one whose output is not being recorded at all — history.status says which, and this method deliberately does not.
   */
  produced: number
}
/**
 * The geometry the BACKEND decided this session runs at (nocx-eidfb.1). The open params carry {cols, rows, xpixel, ypixel} as the client's MEASUREMENT — only a webview knows its own font metrics and pane geometry — and this carries what was done with it, so a renderer learns the size its session took rather than assuming its own report was adopted. NEVER absent and never zero: a session with no client attached holds a named default (80x24), which is the state this field exists to make expressible — before it, a session whose window had gone away had no size at all. The channel is created at this size and never spawned-then-resized (AD-1's resize contract is unchanged; what moved is who chose the number). Which client's measurement it took is decided in one place: the client that attached LAST is the active one and the shared channel follows it (nocx-eidfb.2), so a session handed from one window to another runs at the geometry of the window someone is actually looking at, and returns to the default when the last of them detaches. It is the SESSION's size, not the window's: rendering at it rather than at the window's own geometry is deliberately not implied here (nocx-eidfb.3).
 */
export interface EffectiveSize {
  /**
   * Columns in the session's grid. At least 1 — a session never runs at zero columns, because the no-client case is the named default rather than the absence of a size.
   */
  cols: number
  /**
   * Rows in the session's grid. At least 1, for the same reason as cols.
   */
  rows: number
  /**
   * Width of the grid in pixels, or 0 when the client reported no pixel geometry. Zero is meaningful here and is not the absence of a size: the cell grid is what a channel is created at, and every client in this repo sends 0 for both pixel fields today.
   */
  xpixel: number
  /**
   * Height of the grid in pixels, or 0 — see xpixel.
   */
  ypixel: number
}
export interface Run {
  /**
   * The stream offset of this run's first byte — the same coordinate an ack carries, so a client can place the run against its own cursor without counting anything.
   */
  offset: number
  /**
   * The bytes, base64-encoded. Bytes and not text: the backend never decodes the stream (AD-6), a UTF-8 rune can straddle any boundary the recorder happened to write at, and a run that arrived as a string would have to be decoded by something that does not own the decoder. Never empty — a run with no bytes is not a run, and its absence is a gap.
   */
  body: string
}
export interface Gap {
  /**
   * First dropped byte offset.
   */
  start: number
  /**
   * Offset just past the last dropped byte.
   */
  end: number
  /**
   * Why the range is missing.
   */
  reason: string
}
