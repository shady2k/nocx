// NotifyFeedClient — the notify.feed.* control-plane seam. One method per
// wire call, every result a GENERATED type: the renderer declares nothing of
// its own, because a hand-written type can want a field the wire does not
// carry, which is exactly how vault.status shipped one nobody sent.
//
// Sibling of NotifyClient (notify.raise). That one reports that a program
// asked for a notification; this one reads what was raised. AD-8: the Feed
// is the only holder of "what happened", the Router of "where it went", and
// neither method here can ask the other's question.
import type { Dispatcher } from '../dispatcher'
import type { NotifyFeedRead } from '../generated/notify.feed.read'
import type { NotifyFeedMarkRead } from '../generated/notify.feed.markRead'

export class NotifyFeedClient {
  constructor(private dispatcher: Dispatcher) {}

  /** The authoritative snapshot: revision, unread count, the occurrences
   *  newest first, and what eviction has already dropped. */
  read(): Promise<NotifyFeedRead> {
    return this.dispatcher.call<NotifyFeedRead>('notify.feed.read', {})
  }

  /** Mark every unread occurrence read. The returned revision is
   *  authoritative — the notify.feed.changed hint that follows is a hint. */
  markRead(): Promise<NotifyFeedMarkRead> {
    return this.dispatcher.call<NotifyFeedMarkRead>('notify.feed.markRead', {})
  }
}
