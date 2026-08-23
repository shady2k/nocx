// Notify RPC client — the renderer's path to notify.raise (ADR-0029).
// Sibling of DialogClient/LifecycleClient over the same Dispatcher.
//
// A program in a pane asks nocx to present a message by writing OSC 9 or
// OSC 777; the renderer parses it (osc-notification.ts) and raises it here.
// What crosses is exactly what the program supplied plus the addressing the
// backend needs: sessionId, title and body and NOTHING else. kind, trust,
// level, attribution and at are stamped by the backend from the method
// invoked and its own session registry — a schema proves a record's shape,
// never who assigned a field, so the protected fields are absent from the
// wire rather than validated on it.
//
// This is also why the renderer never says where a notification should go.
// It reports that a program asked; the router decides the destination, and
// there is no argument here by which a request could name one.

import type { Dispatcher } from './dispatcher'
import type { NotifyBell } from './generated/notify.bell'
import type { NotifyPaneWorkFinished } from './generated/notify.paneWorkFinished'
import type { NotifyRaise } from './generated/notify.raise'

export class NotifyClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Raise one program-requested notification for a session. Resolves when
   *  the backend has accepted it. Rejects when the method is unavailable
   *  (-32601, a backend built without the notify pipeline), when the session
   *  is not live on this connection (-32602), or when the pipeline refused
   *  the delivery (-32603) — every one of which the caller must treat as
   *  "this notification did not happen" rather than retrying. */
  raise(params: NotifyRaise): Promise<void> {
    return this.dispatcher.call('notify.raise', params)
  }

  /** Report that a program printed BEL in a session. Resolves when the
   *  backend has accepted it, and rejects on the same three finals raise()
   *  does — the method missing, the session not live on this connection, or
   *  the pipeline refusing the delivery.
   *
   *  A SECOND METHOD, not a kind argument on raise(). The event's kind is
   *  stamped from the method invoked (ADR-0029 §2.2), and ingress authority
   *  is closed: no renderer-callable method may produce an attested event.
   *  A `kind` parameter would hand the choice to the caller, which is the
   *  same forging one level up — so the method name is the choice, and this
   *  client has no argument by which a bell could become anything else.
   *
   *  It carries a session id and nothing else because BEL carries nothing
   *  else: there is no title and no body on the wire, and the backend stamps
   *  the words. Do not add fields here — a program supplying the text of a
   *  bell would have raise()'s payload without raise()'s kind. */
  bell(params: NotifyBell): Promise<void> {
    return this.dispatcher.call('notify.bell', params)
  }

  /** Report that a pane's work seems to have finished: its title went
   *  working → idle and stayed idle for the settle window
   *  (pane-work-finished.ts, design §3.4). Resolves when the backend has
   *  accepted it, and rejects on the same three finals the other two have —
   *  the method missing, the session not live on this connection, or the
   *  pipeline refusing the delivery.
   *
   *  A THIRD METHOD, for the reason there is a second: the event's kind is
   *  stamped from the method invoked (ADR-0029 §2.2), and here that also
   *  stamps its TRUST. This is the pipeline's only heuristic source, and
   *  heuristic is what confines it to local attention and keeps it off push
   *  (§3.1) — so a caller able to choose its kind could have chosen one
   *  that reaches a phone. The method name is the choice, and this client
   *  has no argument by which the choice could be made differently.
   *
   *  It carries a session id and nothing else, and the omission is sharper
   *  than the bell's. A bell has no text; this source has text within reach
   *  — the pane title the inference was drawn from — and that title is a
   *  string a PROGRAM wrote. Do not add it here. */
  paneWorkFinished(params: NotifyPaneWorkFinished): Promise<void> {
    return this.dispatcher.call('notify.paneWorkFinished', params)
  }
}
