/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.import.curl.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.import.curl JSON-RPC method: one pasted curl command line converted into a request, plus what the conversion did not carry over. The line is PARSED, never executed (design §10) — there is no shell, so $(...) and backticks are literal text rather than a substitution that has to be defended against — and an import never fires a request: this returns a request value, and sending it is a separate gesture.
 */
export interface ApiImportCurlResult {
  request: Request
  /**
   * Never null — a line that converted whole is [].
   */
  unsupported: Unsupported[]
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
  kind: 'none' | 'raw' | 'json' | 'form' | 'file'
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
export interface Unsupported {
  /**
   * The feature, named: a curl flag in its long form, a Postman field, an item path. It NEVER carries an argument's VALUE — a refused --oauth2-bearer would otherwise itemise the token it refused.
   */
  what: string
  why: string
}
