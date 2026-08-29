/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.catalogue.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of notify.catalogue: the backend-owned vocabulary for notification kinds. The renderer uses these words for every kind badge, filter option and tooltip rather than duplicating the catalogue in TypeScript.
 */
export interface NotifyCatalogue {
  /**
   * Every declared notification kind, including one whose trust bound leaves it with no offered delivery pair, in catalogue order.
   */
  kinds: Kind[]
}
export interface Kind {
  /**
   * The wire value stamped on a notification occurrence.
   */
  kind:
    | 'block.finished'
    | 'session.ended'
    | 'transfer.finished'
    | 'program.notify'
    | 'bell'
    | 'pane.workFinished'
  /**
   * The noun phrase shown wherever the notification kind is named.
   */
  label: string
  /**
   * The sentence explaining what produces this notification kind.
   */
  description: string
}
