/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operation.row.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The operation.row JSON-RPC notification: one row in the Operations list for work the BACKEND is doing inside a person's call (ADR-8eej3e66 §8). A first helper install is the case it exists for: the catalogue declares the install as work a person is watching (`row: always`), and without this the renderer can see only the call it made — "Opening your terminal" — for the several minutes a 15.3 MB upload takes on a slow link. It is an INDICATOR and never a ledger: frames are droppable, address the connection that made the call, and are not replayed, so nothing may be derived from having seen one. The row's identity is the REQUEST: the renderer keys it on the same JSON-RPC id as the call that carries it, which is what makes one operation one row, and the Cancel on that row is the ordinary rpc.cancel for that id. The row exists from its first frame until operation.rowDone, and it leaves no finished entry — only transfers have an outcome worth keeping.
 */
export interface OperationRow {
  /**
   * The JSON-RPC id of the call this work belongs to, exactly as the caller sent it (a number from this product's renderer, or a string from another client). It is the row's identity and the id a Cancel names, so a renderer never has to map between two identities for one piece of work.
   */
  requestId: number | string
  /**
   * What sort of work the row is, in the renderer's own vocabulary (frontend/src/ui/operation.ts, OperationKind). The backend names it because the backend started the work; a renderer that had to guess from the title would be a second author of the row's glyph and its cancel.
   */
  kind: 'install'
  /**
   * What the work is doing right now, in the person's terms. It may change while the row stays one row — "Copying the nocx helper" while bytes move, "Checking the nocx helper" while the verification read runs — because the row's identity is the request and not the sentence.
   */
  title: string
  /**
   * Which machine the work is on, as a person names it (frontend/src/machine-name.ts): user@host, or the bare host when no account is known. The list is global, so a row that named no machine would be ambiguous the moment a second connection was open. Empty only where the destination is genuinely unknown.
   */
  machine: string
  /**
   * How many bytes of the work have been accepted so far. It is the count after the last chunk the REMOTE accepted, so it can only move forwards. Zero is legitimate — it is the state at the first frame, which is what tells a person the transfer just started.
   */
  done: number
  /**
   * The size the work has to move, in bytes, repeated on every frame on purpose: a client that missed earlier frames can still draw a complete bar from the one it received. Zero means the stage counts nothing — the verification read is one read of the whole binary, so a row shows no bar rather than one that is full or empty by accident.
   */
  total: number
}
