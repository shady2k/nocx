/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.feed.changed.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the notify.feed.changed notification: the feed's new revision and nothing else. Carrying only the revision is what makes the notification droppable without loss — it rides the refreshable outbound queue, and a dropped one costs the renderer one refetch rather than a row it never learns about (nocx-sb3f). A renderer applies it only when it is exactly its own revision plus one; any gap means it missed one and must refetch.
 */
export interface NotifyFeedChanged {
  /**
   * The feed revision after the mutation that prompted this notification.
   */
  revision: number
}
