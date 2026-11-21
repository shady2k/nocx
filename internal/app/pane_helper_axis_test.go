package app

// The axis's two seams (ADR-727g63tr decision 2): internal/farhelper decides
// per connection, panedriver declares the source it reads, and the composition
// root is the only place the two neutral shapes meet. A registry with no
// remote seams is enough to prove the wiring: an unanswered mode and consent is
// a decision it reaches without a probe, and the pane must be told it.

import (
	"context"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/waittest"
)

type helperAxisSnapshots struct {
	mu   sync.Mutex
	list []panedriver.Snapshot
}

func (s *helperAxisSnapshots) add(snapshot panedriver.Snapshot) {
	s.mu.Lock()
	s.list = append(s.list, snapshot)
	s.mu.Unlock()
}

func (s *helperAxisSnapshots) last() panedriver.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list[len(s.list)-1]
}

// helperAxisDialer records real hello clients so the test can end the serving
// peer without changing the registry's decision or the transport connection.
type helperAxisDialer struct {
	reviewDialer
	mu      sync.Mutex
	clients []*helperclient.Client
}

func (d *helperAxisDialer) Dial(ctx context.Context, target farhelper.Target, installed farhelper.Installed) (*helperclient.Client, error) {
	peer, err := d.reviewDialer.Dial(ctx, target, installed)
	if err == nil {
		d.mu.Lock()
		d.clients = append(d.clients, peer)
		d.mu.Unlock()
	}
	return peer, err
}

func (d *helperAxisDialer) peers() []*helperclient.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*helperclient.Client(nil), d.clients...)
}

func TestPaneHelperAxisRefreshDelegatesToRegistry(t *testing.T) {
	source := newFarHelperConnections()
	clock := hostconntest.New()
	dialer := &helperAxisDialer{reviewDialer: reviewDialer{t: t, logger: logtest.Slog(t)}}
	installer := &reviewInstall{}
	registry := farhelper.New(farhelper.Config{
		Connections: source,
		Modes:       &reviewModeStore{mode: hostmode.Helper},
		Consents:    &reviewConsentStore{},
		Probe:       reviewProber{}, Install: installer, Dial: dialer,
		Clock: clock, Log: farHelperTestLogger(t),
	})
	defer registry.Close()

	driver := panedriver.New("instance-1", farHelperTestLogger(t))
	out := &helperAxisSnapshots{}
	driver.SetPublisher(out.add)
	driver.SetHelperSource(paneHelperAxis{reg: registry})
	driver.ApplyFrom("helper-1", proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: 1, Generation: 1, State: proto.ConnectionConnected, PlainShells: 1,
	})
	driver.Bind("pane-1", "conn-a")
	source.Observe(farhelper.Connection{
		ID: "conn-a", Destination: proto.SSHDestination{Host: "far.example", Port: 22, User: "dev"},
		HostKeyFingerprint: "SHA256:far", Generation: 1, State: hostconn.Connected, Shells: 1,
	})
	waittest.WaitFor(t, "the pane's helper to serve", func() bool {
		return out.last().Helper.State == panedriver.HelperServing
	})
	peers := dialer.peers()
	if len(peers) != 1 {
		t.Fatalf("initial helper dials = %d, want 1", len(peers))
	}
	if err := peers[0].Close(); err != nil {
		t.Fatalf("losing the serving helper: %v", err)
	}
	waittest.WaitFor(t, "the pane to offer refresh for its lost helper", func() bool {
		helper := out.last().Helper
		return helper.State == panedriver.HelperUnavailable &&
			helper.Reason == string(farhelper.ReasonHelperLost) &&
			helper.Action == panedriver.HelperActionRefresh
	})
	// The injected clock never advances: only the pane's refresh action can
	// bypass the registry's pending redial backoff.
	lost := out.last()
	if err := driver.RefreshHelper(context.Background(), lost.PaneID, lost.ConnectionEpoch); err != nil {
		t.Fatalf("refreshing the pane's lost helper: %v", err)
	}
	waittest.WaitFor(t, "refresh to redial and serve through the pane adapter", func() bool {
		return out.last().Helper.State == panedriver.HelperServing
	})
	peers = dialer.peers()
	if len(peers) != 2 || peers[0] == peers[1] {
		t.Fatalf("helper clients after refresh = %v, want one replacement dial", peers)
	}
	if installer.installs != 0 {
		t.Fatalf("refresh installed a helper %d times, want 0", installer.installs)
	}
	if state := out.last().Transport.State; state != proto.ConnectionConnected {
		t.Fatalf("transport after helper refresh = %q, want connected", state)
	}
}

func TestTheFarHelpersAxisReachesThePaneThroughTheAdapter(t *testing.T) {
	source := newFarHelperConnections()
	source.Observe(farhelper.Connection{
		ID:                 "conn-a",
		Destination:        proto.SSHDestination{Host: "far.example", Port: 22, User: "dev"},
		HostKeyFingerprint: "SHA256:far",
		State:              hostconn.Connected,
		Shells:             1,
	})
	registry := farhelper.New(farhelper.Config{Connections: source, Log: farHelperTestLogger(t)})
	defer registry.Close()

	driver := panedriver.New("instance-1", farHelperTestLogger(t))
	out := &helperAxisSnapshots{}
	driver.SetPublisher(out.add)
	driver.SetHelperSource(paneHelperAxis{reg: registry})
	driver.ApplyFrom("helper-1", proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: 1, State: proto.ConnectionConnected, StateMs: 1200, PlainShells: 1,
	})
	driver.Bind("pane-1", "conn-a")

	waittest.WaitFor(t, "the axis to reach the pane", func() bool {
		helper := out.last().Helper
		return helper.State == panedriver.HelperUnavailable &&
			helper.Reason == string(farhelper.ReasonConsentPending) &&
			helper.Action == panedriver.HelperActionNone
	})
	if state := out.last().Transport.State; state != proto.ConnectionConnected {
		t.Fatalf("transport = %q: the axis update must not replace the connection's own state", state)
	}
}
