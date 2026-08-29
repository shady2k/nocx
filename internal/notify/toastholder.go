package notify

import (
	"context"
	"sync"
)

// ToastHolder is a ToastPresenter whose implementation is bound after the
// routing table is built. It exists for exactly the reason HostHolder does,
// and the reason is worth repeating rather than cross-referencing: the table
// is fixed at NewRouter because the router is the only holder of "where"
// (ADR-0047 §2.3), and a table that could grow rows later would put that
// authority somewhere else.
//
// The toast's implementation is a push to the renderer, so it needs the
// WebSocket server — which the composition root builds AFTER the router,
// because the server is constructed with the pipeline already wired into it.
// The holder resolves that ordering without moving any authority: the ROUTE
// is decided once, at construction, and what arrives late is the
// implementation behind the port, never the destination.
//
// Until Set is called the holder is UnavailableToast, so a raise on a host
// that never binds one produces a visible failed delivery rather than a
// silent drop.
//
// Safe for concurrent use: Set happens once during startup while raises can
// already be arriving from a reattached session.
type ToastHolder struct {
	mu        sync.RWMutex
	presenter ToastPresenter
}

// Set binds the real toast surface. Calling it more than once replaces the
// previous presenter; calling it never leaves UnavailableToast in place.
func (h *ToastHolder) Set(p ToastPresenter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.presenter = p
}

// current returns the bound presenter, or UnavailableToast when none is.
func (h *ToastHolder) current() ToastPresenter {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.presenter == nil {
		return UnavailableToast{}
	}
	return h.presenter
}

func (h *ToastHolder) Toast(ctx context.Context, ev Event) error {
	return h.current().Toast(ctx, ev)
}
