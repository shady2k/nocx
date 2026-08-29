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
   * Every retained occurrence with readAt == null. This is the feed-wide wire count; the renderer's bell badge and dock badge derive a separate count restricted to kinds the centre shows (design §6).
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
  kind:
    | 'block.finished'
    | 'session.ended'
    | 'transfer.finished'
    | 'program.notify'
    | 'bell'
    | 'pane.workFinished'
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
  /**
   * The constituents this row still holds, NEWEST FIRST — the same direction as occurrences, so the renderer draws an expansion in the order it receives it. A row that collapsed nothing holds exactly one member: itself, so an expansion never has to special-case a run of one. Bounded: the feed retains the newest MaxRunRetained constituents and counts the rest in runDropped, because retaining all of them would restore exactly the unbounded growth collapse exists to prevent.
   */
  run: RunMember[]
  /**
   * How many constituents this row no longer holds. count == run.length + runDropped for every occurrence at all times, which is what lets an expansion say "20 of 4310 shown" rather than presenting a truncation as the whole. Zero when nothing has fallen off the tail.
   */
  runDropped: number
}
/**
 * One constituent of a collapsed row. It carries an instant because an expansion whose rows share one timestamp is not worth opening, a title because a run's titles differ (which is why the row keeps the newest one), and its own read flag because each constituent keeps its own unread mark: marking the feed read marks the row and every member it holds, and a later join clears only the ROW's mark.
 *
 * It deliberately carries no trust, no level and no body. The row owns severity and detail; a member that could disagree with its row would be a second answer to one question.
 */
export interface RunMember {
  /**
   * This constituent's own identity, minted by the feed on the add that created it — distinct from the row's id, which the join does not take.
   */
  id: string
  /**
   * RFC 3339 instant of this constituent's own arrival, not the row's.
   */
  at: string
  /**
   * The title this constituent arrived with. Untrusted presentation data (ADR-0029 §2.3), same guarantees as the row's: rendered as text, never as markup.
   */
  title: string
  /**
   * True once the user marked the feed read while this constituent was held. A join into the row does NOT clear it — it was seen and the new arrival was not, and that difference is the whole reason an expansion shows individual marks.
   */
  read: boolean
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
