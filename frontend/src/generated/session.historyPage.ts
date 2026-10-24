/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.historyPage.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The session.historyPage JSON-RPC result: the facts of one page of a session's live history, read as the emulator holds it (nocx-zg3k3.10.3). The rows themselves ride the binary data plane on the screen frame's own carrier, keyed by pageId — terminal presentation data does not travel inside a JSON-RPC result (AD-1, ADR-0066, ADR-0073). The interval, the floor and more describe one instant of the buffer, read under one lock beside the rows they name.
 */
export interface SessionHistoryPage {
  /**
   * Minted for this call; the session.historyPageRows document carrying this page's rows names the same id. A client matches document to result by it and never by arrival order.
   */
  pageId: string
  /**
   * The absolute history row number of the page's FIRST row, the inclusive lower bound of the delivered interval. Output arriving preserves positions within the same buffer instance and column geometry; a column reflow may renumber them, so clients invalidate pages and anchors on column or buffer-identity changes and apply floor for pruning or ED3. An alternate-screen excursion does not permit joining separate buffer identities. When the page is empty, start is the cursor the page was taken at and end equals it.
   */
  start: number
  /**
   * One past the page's LAST row: start + the number of rows the page delivered, which the carrier's document holds. A client paging backwards continues with before = start.
   */
  end: number
  /**
   * The absolute row number below which NOTHING is retained: the oldest row a further page could reach is floor. A cursor at or below floor is answered with the empty interval at that cursor — an explicit floor, never a short list a caller could read as a short history. After `clear` (ED3) the floor is the head: the emulator's history is what it holds.
   */
  floor: number
  /**
   * True when retained rows remain below start: a further page with before = start will deliver rows. False does not mean the history is short — floor says where it ends.
   */
  more: boolean
  /**
   * RESERVED for the live↔durable join (nocx-zg3k3.10.3 reserves it; a later task gives it meaning): how far the durable record of the session reaches, in its own cursor space, so a client can one day tell where the live tier's history hands over to the durable tier's. null until that join exists, which is every answer today.
   */
  durableThrough: number | null
  /**
   * The active buffer whose absolute rows the page carries.
   */
  bufferKind: 'normal' | 'alternate'
  /**
   * The active buffer identity. Pages from another instance cannot be joined by matching row numbers alone.
   */
  bufferInstance: string
  /**
   * The absolute row number of the active screen's first row at this read. Pages may include rows at or beyond baseRow in the same screen coordinate. A client painting a different accepted live frame caps historical rows at the lesser of this baseRow and its accepted frame's baseRow, after qualifying buffer identity and columns, so rows still visible live cannot also appear in history.
   */
  baseRow: number
  /**
   * The active buffer's committed column count at the instant the page was read.
   */
  cols: number
  /**
   * The active buffer's committed screen row count at the instant the page was read.
   */
  rows: number
}
