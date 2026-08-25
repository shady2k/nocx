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
  /**
   * The request's OWN variables — rows of name, value and enabled, the shape `query` and `headers` already have, because the model grows by one more list of the same thing rather than by a new idea.
   *
   * WHY A REQUEST HAS ANY. A variable could only live in an environment, and that is the wrong home for half of them: `id` in `/users/{{id}}` belongs to the REQUEST, since two requests legitimately want different ones and an environment carrying both would be a place to keep other people's values. The environment's are INHERITED — a name the request answers wins, everything else falls through — so nothing that resolves today resolves differently.
   *
   * ONE GRAMMAR, `{{name}}`. Postman spells a path variable `:id`, and what it gets right is the SCOPE and not the syntax; a second spelling would be two owners of "a hole in the address", agreeing until the day somebody wrote both. The importer rewrites `:id` into this grammar and says it did.
   *
   * Never null — a request with none is []. The file is allowed to omit the key, which is what `omitempty` is right for on disk; the wire is not, because the renderer's first .map on a null throws.
   *
   * A row whose name the environment declares SECRET is refused at send time rather than resolved: a credential belongs in the vault and a request file goes into git (design §8).
   */
  variables: Param[]
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
   * The basic-auth username, or the api-key header name. Text — a literal or a {{variable}}.
   */
  user: string
  /**
   * The bearer token or the api-key value. TEXT, like every other field in the format (nocx-6hg2w.20): a literal the person pasted is sent and is written to their file, and a {{name}} written into it resolves through the same substitution as the URL, a header or the body. The plain-vs-vault distinction is by construction, not by heuristic. Design §8 still holds: there is no syntax in which a FILE names a secret — a vault identifier typed here is the literal it is, and the binding from a name to a stored value lives in the binding document, nowhere in this collection.
   */
  token: string
  /**
   * The basic-auth password, the same text.
   */
  password: string
}
