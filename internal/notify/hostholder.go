package notify

import (
	"context"
	"sync"
)

// HostHolder is an AttentionHost whose implementation is bound after the
// routing table is built. It exists because of an ordering problem with one
// correct answer.
//
// The table is fixed at NewRouter: the router is the only holder of "where"
// (ADR-0047 §2.3), and a table that could grow rows later would put that
// authority somewhere else. But the desktop attention surface cannot be
// constructed at the same moment — the Wails runtime locates the frontend
// through a context value that exists only inside the lifecycle hooks, so
// the real host is born in main.go's startup, after the composition root has
// already built the router.
//
// The holder resolves that without moving any authority: the ROUTE is
// decided once, at construction, and stays decided. What arrives late is the
// implementation behind the port, never the destination. Until Set is
// called the holder is UnavailableHost, so a raise on a host that never
// binds one — cmd/devharness, the dev-web harness, an e2e run — produces a
// visible failed delivery rather than a silent drop.
//
// Safe for concurrent use: Set happens once during startup while raises can
// already be arriving from a reattached session.
type HostHolder struct {
	mu   sync.RWMutex
	host AttentionHost
}

// Set binds the real attention surface. Calling it more than once replaces
// the previous host; calling it never leaves UnavailableHost in place.
func (h *HostHolder) Set(host AttentionHost) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.host = host
}

// current returns the bound host, or UnavailableHost when none is bound.
func (h *HostHolder) current() AttentionHost {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.host == nil {
		return UnavailableHost{}
	}
	return h.host
}

func (h *HostHolder) Banner(ctx context.Context, ev Event) error {
	return h.current().Banner(ctx, ev)
}

func (h *HostHolder) Badge(ctx context.Context, count int) error {
	return h.current().Badge(ctx, count)
}

func (h *HostHolder) Bounce(ctx context.Context) error {
	return h.current().Bounce(ctx)
}
