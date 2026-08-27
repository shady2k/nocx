package transport

// notify.toast (nocx-c6ef, plan D2): the in-window toast, as a SINK's
// destination rather than a special case in the renderer.
//
// The decision this file implements is that a toast is a sink like any other.
// internal/notify declares the ToastPresenter port and a ToastSink that hands
// its event to it, exactly as HostSink hands its event to AttentionHost; the
// router resolves the route once, before any sink runs, and stays the only
// holder of "where" (ADR-0029 §2.3). What is left for the transport is the
// implementation of that port: a JSON-RPC notification on the existing
// control plane (AD-1), which the renderer presents with the kit's own toast.
//
// Nothing here selects a target, and nothing here decides whether a
// notification deserves a toast. Both were settled when the routing table was
// built (internal/app/app.go).

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/notify"
)

// notifyToastParams is the params object of the notify.toast notification
// (contracts/notify.toast.schema.json). Contracted like every other
// unsolicited notification: a server-initiated frame has no request to
// correlate against and nothing checking its shape at the call site.
//
// The event's PROTECTED fields are absent, and that absence is the same rule
// notify.raise's params obey from the other direction: kind, trust and
// attribution are backend-owned facts about provenance (ADR-0029 §2.2), and a
// toast renders none of them. Level is here because nocx stamps it and the
// toast is drawn differently for each — a program cannot forge danger.
type notifyToastParams struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Level string `json:"level"`
}

// toastParamsFor projects one event onto the wire shape. The level is a
// CLOSED enum on the wire, so an event carrying none — the zero value, which
// no nocx-stamped event should have — becomes info rather than an empty
// string the renderer's generated type could not accept. Defaulting here is
// the honest place for it: the contract is what demands one of four, and
// inventing a fifth spelling downstream would be a second vocabulary for one
// concept.
func toastParamsFor(ev notify.Event) notifyToastParams {
	level := string(ev.Level)
	switch ev.Level {
	case notify.LevelInfo, notify.LevelSuccess, notify.LevelWarning, notify.LevelDanger:
	default:
		level = string(notify.LevelInfo)
	}
	return notifyToastParams{Title: ev.Title, Body: ev.Body, Level: level}
}

// Toast implements notify.ToastPresenter: it presents one event as a toast in
// every attached renderer. *WSServer satisfies the port without an adapter —
// the same signature-identical shape NotifyRaiser and NotifyFeed use from the
// other direction.
//
// Every attached renderer, because a toast is a window-level surface and a
// second window that missed it would be a window the notification never
// reached. TryNotify, not a blocking write: a client whose queue is full must
// not hold a sink invocation open.
//
// With NO renderer attached this reports notify.ErrUnavailable, which is the
// same word the unavailable attention host uses for "this host has no such
// surface". That is deliberate: the composition root's one exemption from the
// failure feed is exactly that word (internal/app/app.go's result handler),
// and a row per notification saying nobody was looking would be noise nothing
// the user does can change — while the notification itself is in the feed,
// which is the surface that exists for precisely this.
func (s *WSServer) Toast(ctx context.Context, ev notify.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.connsMu.Lock()
	// Copy under lock so enqueues happen outside the critical section.
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()
	if len(conns) == 0 {
		return fmt.Errorf("%w: no renderer is attached to present a toast", notify.ErrUnavailable)
	}

	params := mustMarshal(toastParamsFor(ev))
	delivered := 0
	for _, wc := range conns {
		if err := wc.TryNotify("notify.toast", params); err != nil {
			s.log.Debug("write notify.toast", "conn", wc.id, "error", err)
			continue
		}
		delivered++
	}
	if delivered == 0 {
		// Every attached renderer refused the frame. Nothing was presented,
		// so this is a failed delivery the router records — not unavailable,
		// because the surface exists and the message was lost on it.
		return fmt.Errorf("notify: no attached renderer accepted the toast")
	}
	return nil
}
