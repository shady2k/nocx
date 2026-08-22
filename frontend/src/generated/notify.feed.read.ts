/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.feed.read.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of notify.feed.read: the authoritative snapshot of the notification centre's in-memory feed. The renderer reconciles against revision — it applies a notify.feed.changed hint only when the hint is exactly its own revision plus one, and refetches on any gap, so a change notification dropped by the refreshable outbound queue costs one refetch rather than a lost row (nocx-sb3f). Occurrences are newest first. trust is deliberately absent: it is a routing capability bound (ADR-0029 §3) and not something a surface renders, and carrying it would invite a renderer to act on a decision the router already made.
 */
export interface NotifyFeedRead {
  /**
   * Monotonic, in-memory, bumped on every feed mutation — an add, a mark-read, or an eviction. Never persisted and never reset except by process restart, which the renderer sees as a reconnect.
   */
  revision: number
  /**
   * Occurrences whose read flag is false. This is the ONE number the bell badge and the dock badge both read (design §6); the tab activity dot answers a different question and keeps reading hasActivity.
   */
  unreadCount: number
  /**
   * The feed, newest first.
   */
  occurrences: Occurrence[]
  dropped: Dropped
}
export interface Occurrence {
  /**
   * Stable identity of one occurrence, minted by ingress. Opaque to the renderer and monotonic within the backend process.
   */
  id: string
  /**
   * RFC 3339 instant, stamped once by ingress.
   */
  at: string
  /**
   * Untrusted presentation data (ADR-0029 §2.3). Rendered as text, never as markup.
   */
  title: string
  /**
   * Untrusted presentation data, same guarantees as title. Empty when the source had none.
   */
  body: string
  /**
   * The event kind, stamped by the source adapter from the method invoked — never carried on the wire inbound.
   */
  kind: 'block.finished' | 'session.ended' | 'program.notify' | 'bell' | 'pane.workFinished'
  /**
   * Severity, stamped by nocx: a program cannot forge danger.
   */
  level: 'info' | 'success' | 'warning' | 'danger'
  /**
   * How many occurrences this row collapsed. 1 for a lone occurrence. A row's count rises only while consecutive repeats of its collapse key arrive inside the collapse window.
   */
  count: number
  /**
   * True once the user marked the feed read. A row whose count rises becomes unread again — the count changed, so there is something new to see.
   */
  read: boolean
  /**
   * Which backend raised this — "local" for this machine, the same vocabulary internal/commandnames.LocalRoute uses. Present from the first commit because nocx-if6 phase A makes session identity (backendId, sessionId).
   */
  backendId: string
  /**
   * Addressing: which session this came from. The renderer resolves it to a tab; the backend cannot, and does not try.
   */
  sessionId: string
  /**
   * The host the session speaks for, stamped from the registry.
   */
  host: string
}
/**
 * The visible half of eviction. It lives outside the occurrence budget and is never itself evicted: a soft degrade must be visible in the product, not only in a log.
 */
export interface Dropped {
  /**
   * Occurrences evicted since the process started. Zero means nothing has been lost.
   */
  count: number
  /**
   * RFC 3339 instant of the oldest evicted occurrence, or the empty string when count is 0.
   */
  oldest: string
  /**
   * RFC 3339 instant of the newest evicted occurrence, or the empty string when count is 0.
   */
  newest: string
}
