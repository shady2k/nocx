package app

// The coordinator's half of the write-session journal (nocx-oalo1.12.4): a
// helper that starts while an earlier generation's temporary is stuck on disk
// has to TELL somebody, and the party that can is the coordinator that just
// dialled it. These tests drive `farHelperDial.Dial` itself — a real local
// helper whose lane bridges to a real far helper — because "after the hello" is
// the claim: an inventory asked before the handshake would be a request the
// helper has no connection for, and one asked by a test instead of by Dial would
// not be a claim about the product at all.
//
// The far side registers a service reduced to the one op under test: what the
// real `fs` service answers off a real socket is the contract test's subject
// (`fs_service_contract_test.go`), and asserting it twice would be two owners of
// one answer.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// inventoryOnlyFS is the far helper's fs service reduced to the inventory op.
// asked counts the requests, which is how a test proves the op was asked rather
// than inferred from a warning that a fixture produced anyway.
type inventoryOnlyFS struct {
	answer proto.FSInventoryResult
	err    error
	asked  atomic.Int32
}

func (*inventoryOnlyFS) Name() string  { return proto.ServiceFS }
func (*inventoryOnlyFS) Ops() []string { return []string{proto.OpInventory} }
func (s *inventoryOnlyFS) ParamsSchema(string) *helperhost.Schema {
	return helperhost.SchemaFor(proto.FSInventoryParams{})
}

func (s *inventoryOnlyFS) Call(_ context.Context, op string, _ json.RawMessage) (any, error) {
	if op != proto.OpInventory {
		return nil, fmt.Errorf("inventory fixture: no op %q", op)
	}
	s.asked.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.answer, nil
}

// dialThroughALane wires farHelperDial the way the composition root does: a
// local helper holds the lane, a far helper serves on the other end of it, and
// the dialer reaches the far one exactly as it reaches a remote install.
//
// The lane fixture is far_helper_reader_liveness_test.go's own — a second copy
// of "bridge a channel between two in-process helpers" would be a second owner
// of that harness.
func dialThroughALane(t *testing.T, ctx context.Context, logger *slog.Logger, farService helperhost.Service) *farHelperDial {
	t.Helper()
	channel, err := proto.ParseChannelID("0000000000000000000000000000000b")
	if err != nil {
		t.Fatal(err)
	}
	farConn, bridge := net.Pipe()
	farHost := helperhost.New(farConn, farConn, "far-generation", "far-instance", logger)
	if farService != nil {
		farHost.Register(farService)
	}
	farDone := make(chan error, 1)
	go func() { farDone <- farHost.Serve(ctx) }()

	lane := &readerLaneService{id: channel, bridge: bridge, closed: make(chan proto.ChannelID, 1), pumpDone: make(chan struct{})}
	serverConn, clientConn := net.Pipe()
	localHost := helperhost.New(serverConn, serverConn, "local-generation", "local-instance", logger)
	localHost.Register(lane)
	localDone := make(chan error, 1)
	go func() { localDone <- localHost.Serve(ctx) }()

	local, err := helperclient.Dial(ctx, helperclient.Config{
		Exec: helperclient.NewSocketConn(clientConn), ExpectHash: "local-generation", Log: logger,
	})
	if err != nil {
		t.Fatalf("dialling the local helper: %v", err)
	}
	t.Cleanup(func() {
		_ = local.Close()
		_ = clientConn.Close()
		lane.stop.Do(func() { _ = bridge.Close() })
		_ = farConn.Close()
		<-lane.pumpDone
		<-localDone
		<-farDone
	})

	opener := &localHelperOpener{log: logger, farConnections: newFarHelperConnections()}
	opener.installedLocalGeneration(helperlocal.Installed{Binary: "/nonexistent/nocx-helper", Generation: "local-generation"})
	opener.dir = t.TempDir()
	opener.mu.Lock()
	opener.client = local
	opener.mu.Unlock()

	return &farHelperDial{over: &sshOverHelper{local: opener, log: logger}, log: logger}
}

// farInventoryTarget is one resolved connection: the destination is carried in
// the warning so a person with several hosts knows which disk to look at.
func farInventoryTarget() farhelper.Target {
	return farhelper.Target{
		ConnectionID:       "conn-1",
		Destination:        proto.SSHDestination{Host: "far.example", Port: 22, User: "dev"},
		HostKeyFingerprint: "SHA256:far",
	}
}

// dialFarHelperForInventory runs one Dial against a far helper that answers
// inventory with answer (or with err, or not at all when answer is nil and no
// service is registered), and returns the warnings it logged.
func dialFarHelperForInventory(t *testing.T, farService helperhost.Service) []logtest.Record {
	t.Helper()
	logger := logtest.Slog(t)
	ctx := nocxlog.WithLogger(context.Background(), nocxlog.NewSlogAdapter(logger))
	dial := dialThroughALane(t, ctx, logger, farService)
	peer, err := dial.Dial(ctx, farInventoryTarget(), farhelper.Installed{
		Dir: "/home/dev/.nocx/helper", Generation: "far-generation",
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	var warnings []logtest.Record
	for _, rec := range logtest.RecordsSlog(logger) {
		if rec.Message == strandedTemporaryMessage {
			warnings = append(warnings, rec)
		}
	}
	return warnings
}

// strandedTemporaryMessage is the warning's text, spelled once so the assertions
// and the product cannot drift apart silently.
const strandedTemporaryMessage = "far helper: a write session left a temporary file behind"

// TestFarHelperDialWarnsAboutEveryStrandedTemporary is the feature's proof at
// the seam a person reaches: the dial asks the helper, and every file it could
// not remove is a warning naming the host, the path and the reason.
func TestFarHelperDialWarnsAboutEveryStrandedTemporary(t *testing.T) {
	service := &inventoryOnlyFS{answer: proto.FSInventoryResult{Stranded: []proto.FSStrandedTemp{
		{Path: "/srv/project/.nocx-write-0123456789abcdef0123456789abcdef", Reason: "read-only file system"},
		{Path: "/srv/logs/.nocx-write-fedcba9876543210fedcba9876543210", Reason: "operation not permitted"},
	}}}
	warnings := dialFarHelperForInventory(t, service)

	if got := service.asked.Load(); got != 1 {
		t.Fatalf("inventory was asked %d times after one dial, want 1", got)
	}
	if len(warnings) != 2 {
		t.Fatalf("%d stranded temporary warning(s), want one per file", len(warnings))
	}
	for i, want := range service.answer.Stranded {
		if got := recordAttr(warnings[i], "path"); got != want.Path {
			t.Errorf("warning %d path = %q, want %q", i, got, want.Path)
		}
		if got := recordAttr(warnings[i], "reason"); got != want.Reason {
			t.Errorf("warning %d reason = %q, want %q", i, got, want.Reason)
		}
		if got := recordAttr(warnings[i], "host"); got != farInventoryTarget().Destination.Host {
			t.Errorf("warning %d host = %q, want the destination's", i, got)
		}
	}
}

// TestFarHelperDialWithNothingStrandedWarnsAboutNothing is the ordinary host:
// the op is asked (a clean answer is an answer), and a person sees no warning.
func TestFarHelperDialWithNothingStrandedWarnsAboutNothing(t *testing.T) {
	service := &inventoryOnlyFS{}
	warnings := dialFarHelperForInventory(t, service)
	if got := service.asked.Load(); got != 1 {
		t.Fatalf("inventory was asked %d times after one dial, want 1", got)
	}
	if len(warnings) != 0 {
		t.Fatalf("a clean host produced %d warning(s)", len(warnings))
	}
}

// TestFarHelperDialSucceedsWhenTheHelperHasNoInventory is the generation fence:
// a helper older than this app answers `unknown_service` (or a broken one
// answers an internal failure), and neither may be a reason to refuse the pane.
func TestFarHelperDialSucceedsWhenTheHelperHasNoInventory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service helperhost.Service
	}{
		{name: "no fs service at all", service: nil},
		{name: "fs service refuses the op", service: &inventoryOnlyFS{err: errInventoryRefused}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if warnings := dialFarHelperForInventory(t, tc.service); len(warnings) != 0 {
				t.Fatalf("an unanswered inventory produced %d warning(s)", len(warnings))
			}
		})
	}
}

// errInventoryRefused stands in for the refusal an older generation gives; the
// coordinator's behaviour must be the same for any error, because the only
// thing it can do about one is not fail the dial.
var errInventoryRefused = fmt.Errorf("helper: unknown service %q", proto.ServiceFS)
