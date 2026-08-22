/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.feed.markRead.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of notify.feed.markRead: every unread occurrence becomes read and the feed's revision advances. The renderer applies the returned revision directly rather than waiting for the change notification it will also receive — the notification is a hint and the result is authoritative.
 */
export interface NotifyFeedMarkRead {
  /**
   * The feed revision after the mark. Monotonic.
   */
  revision: number
}
