package notify

import (
	"context"
)

// HostSink bridges the router to the AttentionHost port: Deliver presents
// the event through the host's banner, and every host failure — including
// ErrUnavailable from an unavailable host — is a failed delivery the router
// records in the outcome. It never selects where an event goes (ADR-0047
// §2.3): the host is bound at construction.
//
// It lives here, in the Wails-free package, deliberately: the composition
// root wires it against the desktop host in main.go, but cmd/devharness and
// the dev-web harness import internal/app and must never reach the Wails
// module — a sink that only touches the AttentionHost port keeps that
// boundary airtight.
type HostSink struct {
	// Host is the bound attention surface. Bind UnavailableHost on hosts
	// with no desktop surface: the sink then reports unavailable instead
	// of stalling the pipeline.
	Host AttentionHost
}

func (s HostSink) Deliver(ctx context.Context, d Delivery) error {
	return s.Host.Banner(ctx, d.Event)
}

// LeavesMachine is false: a banner leaves the machine nowhere.
func (HostSink) LeavesMachine() bool { return false }
