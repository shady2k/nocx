/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.request.read.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.request.read JSON-RPC method: one request, exactly as its file has it. Nothing is resolved on the way out — a {{variable}} stays a {{variable}} and a body naming a file still names it — because the file is the truth and both the form and the line are projections of it (design §6.4).
 */
export interface ApiRequestReadResult {
  request: Request
}
export interface Request {
  id: string
  name: string
  method: string
  url: string
  /**
   * Never null — a request with no headers is []. The stored form omits the key entirely, which is right for a file and wrong for the renderer's first .map.
   */
  headers: Header[]
  /**
   * Never null — a request with no query is [].
   */
  query: Param[]
  body: Body
  auth: Auth
}
export interface Header {
  name: string
  value: string
  /**
   * A disabled row is a row the user keeps: deleting it to turn it off loses the value they will want back.
   */
  enabled: boolean
}
export interface Param {
  name: string
  value: string
  enabled: boolean
}
export interface Body {
  /**
   * A body too large or too awkward for the line projection is NAMED by a file rather than lost (design §6.4).
   */
  kind: 'none' | 'raw' | 'form' | 'file'
  text: string
  /**
   * A path WITHIN the collection, read on send under the handle's path rules — never a path the renderer supplies (design §13.1).
   */
  fileRef: string
}
export interface Auth {
  kind: 'none' | 'bearer' | 'basic' | 'apikey'
  /**
   * A VARIABLE NAME, never a secret and never an identifier for one. There is deliberately no field here in which stored credential material can be spelled, which is the whole of design §8: the attack is unspellable rather than guarded. A file that puts a vault identifier here has merely named a variable nobody bound, and the send is blocked as unresolved.
   */
  var: string
  user: string
}
