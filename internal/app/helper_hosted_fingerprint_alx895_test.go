package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/waittest"
)

// alx895SSHService is this machine's helper's ssh service for the connection
// identity alone: a bind answers a stable id, a lease answers a stable host
// key, and the table is empty. The remote helper's own decisions are a fake
// provider's, not this service's.
type alx895SSHService struct {
	connectionID string
	fingerprint  string
}

func (*alx895SSHService) Refusal(err error) (string, json.RawMessage) {
	if refusal, ok := err.(*proto.Refusal); ok {
		return refusal.Code, refusal.Details
	}
	return "", nil
}
func (*alx895SSHService) Name() string { return proto.ServiceSSH }
func (*alx895SSHService) Ops() []string {
	return []string{proto.OpConnection, proto.OpConnections, proto.OpLease}
}

func (*alx895SSHService) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpConnection:
		return host.SchemaFor(proto.ConnectionParams{})
	case proto.OpConnections:
		return host.SchemaFor(proto.ConnectionsParams{})
	case proto.OpLease:
		return host.SchemaFor(proto.LeaseParams{})
	default:
		return nil
	}
}

func (s *alx895SSHService) Call(_ context.Context, op string, _ json.RawMessage) (any, error) {
	switch op {
	case proto.OpConnection:
		return proto.ConnectionResult{ConnectionID: s.connectionID}, nil
	case proto.OpConnections:
		return proto.ConnectionsResult{}, nil
	case proto.OpLease:
		return proto.LeaseResult{Lease: proto.LeaseID{1}, HostKeyFingerprint: s.fingerprint}, nil
	default:
		return nil, &proto.Refusal{Code: proto.ErrCodeUnknownOp}
	}
}

func alx895PaneStand(t *testing.T) (*paneStand, *alx895SSHService) {
	t.Helper()
	stand := startPaneStand(t)
	dir, err := os.MkdirTemp("", "alx895-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	spawner := &scriptedSpawner{}
	service := &alx895SSHService{connectionID: "alx895-conn", fingerprint: "SHA256:local"}
	daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner, service)
	stand.opener.dir = daemon.dir
	stand.daemon = daemon
	return stand, service
}

func alx895ConnectionAt(t *testing.T, source *farHelperConnections, id string) farhelper.Connection {
	t.Helper()
	for _, connection := range source.List() {
		if connection.ID == id {
			return connection
		}
	}
	t.Fatalf("connection %q is absent from the far-helper source: %+v", id, source.List())
	return farhelper.Connection{}
}

// TestRemoteHelperOpenRecordsTheAuthenticatedFingerprint is the defect
// nocx-oalo1.6.3b exists for: the far host's own helper acknowledges a pane on
// a host whose profile carries no pin, and only the local opener used to hand
// the confirmed key to farConnections. Without it the axis reads an unanswered
// machine (consent-pending) for ever.
func TestRemoteHelperOpenRecordsTheAuthenticatedFingerprint(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeerWithSpawner(t)}
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	remote := newTestRegistry(t, provider, store)
	mustRecordHostMode(t, remote.modes, "SHA256:fake", hostmode.Helper)

	stand, service := alx895PaneStand(t)
	stand.opener.modes = remote.modes
	stand.opener.paneConns = panedriver.New("alx895-fingerprint", nil)
	farConns := newFarHelperConnections()
	// The connection is live before the open, as it is when a pane is opened
	// over an existing link: the point under test is the MISSING fingerprint,
	// not a missing transport.
	farConns.Observe(farhelper.Connection{
		ID:          service.connectionID,
		Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"},
		State:       hostconn.Connected,
	})
	stand.opener.farConnections = farConns

	hosted := &hostedOpeners{local: stand.opener, remote: remote}
	cfg := stand.sshConfig("helper", nil)
	cfg.PaneID = "alx895-pane"
	cfg.ProfileID = "ssh:alx895"
	cfg.Cols, cfg.Rows = 80, 24
	opened, selected, err := hosted.OpenHosted(context.Background(), cfg, "alx895-claim")
	if err != nil {
		t.Fatalf("open through the far host's helper: %v", err)
	}
	if !selected {
		t.Fatal("the remote branch declined a helper host")
	}
	defer func() { _ = opened.Session.Close() }()

	connection := alx895ConnectionAt(t, farConns, service.connectionID)
	if connection.HostKeyFingerprint != "SHA256:fake" {
		t.Errorf("farConnections entry %q has fingerprint %q, want the probe's confirmed SHA256:fake",
			service.connectionID, connection.HostKeyFingerprint)
	}

	seams := &gitFarSeams{t: t, peer: helperPeerWithoutSession(t)}
	far := farhelper.New(farhelper.Config{
		Connections: farConns,
		Modes:       remote.modes,
		Consents:    store,
		Probe:       seams,
		Install:     seams,
		Dial:        seams,
		Log:         farHelperTestLogger(t),
	})
	t.Cleanup(far.Close)
	waittest.WaitFor(t, "the connection's axis leaves consent-pending and serves", func() bool {
		status, known := far.Axis(service.connectionID)
		return known && status.State == farhelper.StateServing
	})
}

// TestScriptOpenRecordsTheAuthenticatedFingerprintFromTheLocalBranch is the
// pair: when the far host declines, the local opener still learns the key from
// its own authenticated lease and records it exactly as before.
func TestScriptOpenRecordsTheAuthenticatedFingerprintFromTheLocalBranch(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeerWithSpawner(t)}
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	remote := newTestRegistry(t, provider, store)
	mustRecordHostMode(t, remote.modes, "SHA256:fake", hostmode.Script)

	stand, service := alx895PaneStand(t)
	stand.opener.modes = remote.modes
	stand.opener.paneConns = panedriver.New("alx895-script-fingerprint", nil)
	farConns := newFarHelperConnections()
	stand.opener.farConnections = farConns

	hosted := &hostedOpeners{local: stand.opener, remote: remote}
	cfg := stand.sshConfig("script", nil)
	cfg.PaneID = "alx895-script-pane"
	cfg.ProfileID = "ssh:alx895-script"
	cfg.Cols, cfg.Rows = 80, 24
	opened, _, err := hosted.OpenHosted(context.Background(), cfg, "alx895-script-claim")
	if err != nil {
		t.Fatalf("open through this machine's helper: %v", err)
	}
	defer func() { _ = opened.Session.Close() }()

	connection := alx895ConnectionAt(t, farConns, service.connectionID)
	if connection.HostKeyFingerprint != service.fingerprint {
		t.Errorf("farConnections entry %q has fingerprint %q, want the local lease's %q",
			service.connectionID, connection.HostKeyFingerprint, service.fingerprint)
	}
}
