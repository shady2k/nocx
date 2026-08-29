/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.toast.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the notify.toast server-to-client notification: one transient in-window message the renderer presents with the kit's existing toast (frontend/src/ui/toast.tsx). It is the wire half of the toast SINK (ADR-0047, plan D2) — a toast is not a special case the pipeline routes around but a sink like any other, reached through a port the transport satisfies, so the router stays the only holder of 'where'. Title and body are untrusted presentation data written by whatever the user ran (ADR-0047 Section 2.3): they are rendered as text and never spliced into any other syntax. Level is stamped by nocx, never by the program — a program cannot forge danger — and its four values are the toast kit's own levels, so the renderer maps them without inventing a fifth.
 */
export interface NotifyToast {
  /**
   * The notification's title. Empty for an OSC 9 request, which carries one field: the renderer then presents the body alone rather than inventing a title the terminal did not send.
   */
  title: string
  /**
   * The notification's body. May be empty.
   */
  body: string
  /**
   * The severity nocx stamped, in the toast kit's own vocabulary.
   */
  level: 'info' | 'success' | 'warning' | 'danger'
}
