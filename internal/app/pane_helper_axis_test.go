package app

// The axis's two seams (ADR-727g63tr decision 2): internal/farhelper decides
// per connection, panedriver declares the source it reads, and the composition
// root is the only place the two neutral shapes meet. A registry with no
// remote seams is enough to prove the wiring: an unanswered mode and consent is
// a decision it reaches without a probe, and the pane must be told it.

import (
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
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
