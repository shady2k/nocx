// session.focus — the backend asking for the pane holding a session to be
// brought to the front (nocx-jiwq.1, plan D1).
//
// It arrives when a person clicks the OS banner a notification raised. The
// shell raises the window; this is the other half, and it has to be the
// renderer's because the renderer is the only side that knows which tab holds
// which session — the backend's Attribution.Tab is a WebSocket connection id
// rather than a tab (nocx-wyp3p). So the push carries a session id and
// nothing else, and the resolution below is the ONE lookup the product has:
// PaneManager.findBySession, which the notification panel's own row
// activation already uses. A second lookup here would be a second answer to
// "which tab holds this session", and the two would agree everywhere anyone
// looked.
//
// A session no pane holds is not an error. The tab was closed, and there is
// nothing to focus — the click moved the window and that is the whole of what
// it could honestly do.

import type { Dispatcher } from '../dispatcher'
import type { SessionFocus } from '../generated/session.focus'

/** The subset of Dispatcher this needs. Narrowed rather than re-declared so
 *  the dispatcher stays the single source of truth for the signature. */
export type FocusDispatcherLike = Pick<Dispatcher, 'subscribe'>

/**
 * Subscribe to focus requests. `focus` receives the session id and resolves
 * it with the pane manager — the composition root passes the same closure
 * shape the notification panel's onActivate uses, so a banner click and a
 * feed row land through one path.
 *
 * Returns the unsubscribe.
 */
export function subscribeSessionFocus(
  dispatcher: FocusDispatcherLike,
  focus: (sessionId: string) => void,
): () => void {
  return dispatcher.subscribe('session.focus', (params: unknown) => {
    // Server-initiated and unsolicited, so nothing correlated it and nothing
    // checked its shape at a call site. The contract says sessionId is a
    // non-empty string; anything else is not addressing and is ignored rather
    // than passed to a lookup that would answer "no pane" anyway.
    const request = params as SessionFocus | null
    if (typeof request?.sessionId !== 'string' || request.sessionId === '') return
    focus(request.sessionId)
  })
}
