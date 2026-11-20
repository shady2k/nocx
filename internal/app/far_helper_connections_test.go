package app

// The far helper's connection source is the local helper's own table
// (nocx-oalo1.6.2, design §5.10 scenario 7).
//
// WHAT THESE PROVE THAT internal/farhelper's OWN TESTS CANNOT. That package is
// handed a source; these are about the source the composition root supplies.
// Three facts used to be stand-ins there — a locally counted generation, a
// list fed only by the panes this process bound, and a shell count with no
// connection behind it — and a registry test over a fake source keeps passing
// whatever the real adapter does. So the assertions here are made at the seam
// a person reaches: the same value the helper's `connections` op answers, and
// the same `connect` a pane's open calls.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// farHelperTableStub is the local helper's `connections` answer, without a
// daemon: the answer and the instance the hello would have named.
type farHelperTableStub struct {
	instance  string
	summaries []proto.ConnectionSummary
	err       error
	calls     int
}

func (s *farHelperTableStub) InstanceID() string { return s.instance }

func (s *farHelperTableStub) Connections(context.Context) ([]proto.ConnectionSummary, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.summaries, nil
}

// farHelperTestLogger is the private test sink the registry takes as its log.
func farHelperTestLogger(t *testing.T) log.Logger {
	t.Helper()
	_, logger := logtest.New(t)
	return logger
}

// farHelperSummary is one entry of that table, in the shape the helper sends:
// the observation the event stream carries, plus the destination and the key
// its link was verified against.
func farHelperSummary(id string, seq, generation uint64, state string, shells int, host, user, fingerprint string) proto.ConnectionSummary {
	return proto.ConnectionSummary{
		Connection: proto.ConnectionStateEvent{
			ConnectionID: id,
			Seq:          seq,
			Generation:   generation,
			State:        state,
			PlainShells:  shells,
		},
		Destination:        &proto.SSHDestination{Host: host, Port: 22, User: user},
		HostKeyFingerprint: fingerprint,
	}
}

// farHelperListed answers the source's list by id, so an assertion names the
// connection it is about.
func farHelperListed(source *farHelperConnections) map[string]farhelper.Connection {
	out := make(map[string]farhelper.Connection)
	for _, c := range source.List() {
		out[c.ID] = c
	}
	return out
}

// TestTheFarHelperRebuildsFromTheHelpersWholeConnectionTable is scenario 7: a
// coordinator that restarted knows nothing, and what it rebuilds from is the
// helper's table — including the connection no pane of this process rides.
func TestTheFarHelperRebuildsFromTheHelpersWholeConnectionTable(t *testing.T) {
	source := newFarHelperConnections()
	table := &farHelperTableStub{instance: "helper-7", summaries: []proto.ConnectionSummary{
		farHelperSummary("with-pane", 4, 2, proto.ConnectionConnected, 1, "pane.example", "dev", "SHA256:pane"),
		farHelperSummary("tunnel", 1, 1, proto.ConnectionConnected, 0, "tunnel.example", "dev", "SHA256:tunnel"),
		// Not connected, but it has a link: it is the registry's to pause, not
		// the table's to hide.
		farHelperSummary("stalled", 3, 2, proto.ConnectionStalled, 1, "stalled.example", "dev", "SHA256:stalled"),
		// THE TABLE'S DEAD HALF: the helper never prunes a supervisor, so the
		// answer is every connection it has ever created. Neither of these is
		// something to probe or write to.
		farHelperSummary("ended", 7, 4, proto.ConnectionEnded, 0, "gone.example", "dev", "SHA256:gone"),
		farHelperSummary("never-dialled", 1, 0, proto.ConnectionOpening, 0, "unused.example", "dev", ""),
	}}
	if err := source.Sync(context.Background(), table); err != nil {
		t.Fatalf("reading the helper's connection table: %v", err)
	}

	got := farHelperListed(source)
	pane, ok := got["with-pane"]
	if !ok {
		t.Fatal("the connection a pane could ride is missing from the source")
	}
	if pane.Destination.Host != "pane.example" || pane.HostKeyFingerprint != "SHA256:pane" {
		t.Fatalf("with-pane = %+v, want the destination and key the table named", pane)
	}
	if pane.Generation != 2 || pane.State != hostconn.Connected || pane.Shells != 1 {
		t.Fatalf("with-pane = %+v, want generation 2, connected and one shell", pane)
	}
	tunnel, ok := got["tunnel"]
	if !ok {
		t.Fatal("the connection NO pane rides is missing from the source: it is the whole of scenario 7")
	}
	if tunnel.Destination.Host != "tunnel.example" || tunnel.HostKeyFingerprint != "SHA256:tunnel" {
		t.Fatalf("tunnel = %+v, want the destination and key the table named", tunnel)
	}
	if tunnel.Generation != 1 || tunnel.State != hostconn.Connected || tunnel.Shells != 0 {
		t.Fatalf("tunnel = %+v, want generation 1, connected and no shell", tunnel)
	}
	if stalled, ok := got["stalled"]; !ok || stalled.State != hostconn.Stalled {
		t.Fatalf("stalled = %+v, want a connection with a link to reach the source", stalled)
	}
	if _, ok := got["ended"]; ok {
		t.Fatal("a connection the helper has already ended was unfolded: the table is never pruned and a dead host is not something to probe")
	}
	if _, ok := got["never-dialled"]; ok {
		t.Fatal("a connection that never had a link was unfolded: there is nothing to decide about and nothing to write to")
	}

	// AND THE REGISTRY BUILDS AN ENTRY FOR IT WITHOUT A PANE EVER BINDING IT.
	// The mode is unanswered and the consent unknown, so the axis is absent for
	// a reason a person can act on — which is only reachable when the
	// connection arrived with a destination and a fingerprint.
	registry := farhelper.New(farhelper.Config{Connections: source, Log: farHelperTestLogger(t)})
	defer registry.Close()
	var status farhelper.Status
	waittest.WaitFor(t, "the zero-pane connection to be decided", func() bool {
		current, ok := registry.Axis("tunnel")
		status = current
		return ok && current.State != farhelper.StateStarting
	})
	if status.State != farhelper.StateUnavailable || status.Reason != farhelper.ReasonConsentPending {
		t.Fatalf("the zero-pane axis = %+v, want unavailable(consent-pending)", status)
	}
}

// TestTheFarHelpersGenerationAndShellsAreTheHelpersOwn is the pair to the
// local counter that stood here until stage A: a stall and its recovery are ONE
// transport, and only the helper's own number may say otherwise.
func TestTheFarHelpersGenerationAndShellsAreTheHelpersOwn(t *testing.T) {
	source := newFarHelperConnections()
	source.Observe(farhelper.Connection{
		ID:                 "c1",
		Destination:        proto.SSHDestination{Host: "far.example", Port: 22, User: "dev"},
		HostKeyFingerprint: "SHA256:far",
		State:              hostconn.Opening,
	})
	apply := func(seq, generation uint64, state string, shells int) {
		source.Apply("helper-1", proto.ConnectionStateEvent{
			ConnectionID: "c1", Seq: seq, Generation: generation, State: state, PlainShells: shells,
		})
	}

	apply(2, 4, proto.ConnectionConnected, 2)
	if c := farHelperListed(source)["c1"]; c.Generation != 4 || c.Shells != 2 {
		t.Fatalf("c1 = %+v, want generation 4 and two shells", c)
	}
	// THE SAME LINK THROUGH A STALL AND BACK: the state returns, the link does
	// not change, and a counter of transitions would have said it did.
	apply(3, 4, proto.ConnectionStalled, 0)
	apply(4, 4, proto.ConnectionConnected, 2)
	if c := farHelperListed(source)["c1"]; c.Generation != 4 {
		t.Fatalf("the generation moved to %d across a stall and its recovery, want 4: a stall is one transport", c.Generation)
	}
	// A REPLACED LINK: the helper's number, not this adapter's.
	apply(5, 5, proto.ConnectionConnected, 1)
	if c := farHelperListed(source)["c1"]; c.Generation != 5 || c.Shells != 1 {
		t.Fatalf("c1 = %+v, want the helper's generation 5 and one shell", c)
	}
	// AND AN OLDER OBSERVATION IS REFUSED BY ITS SEQUENCE, not by its
	// generation: a late frame must not take the connection backwards.
	apply(4, 4, proto.ConnectionStalled, 0)
	if c := farHelperListed(source)["c1"]; c.State != hostconn.Connected || c.Generation != 5 {
		t.Fatalf("c1 = %+v, want the newer observation to stand", c)
	}
}

// TestARestartedHelperStartsTheObservationsOver is the other restart: not the
// coordinator's, the HELPER's. A replacement daemon numbers its supervisors
// from zero again, so a fence built on the previous process's numbers would
// refuse every observation for ever — and a decision would go on riding a client
// whose helper is gone.
func TestARestartedHelperStartsTheObservationsOver(t *testing.T) {
	source := newFarHelperConnections()
	var ended []string
	stop := source.Watch(func(c farhelper.Connection) {
		if c.State == hostconn.Ended {
			ended = append(ended, c.ID)
		}
	})
	defer stop()

	first := &farHelperTableStub{instance: "helper-1", summaries: []proto.ConnectionSummary{
		farHelperSummary("c1", 9, 3, proto.ConnectionConnected, 1, "far.example", "dev", "SHA256:far"),
	}}
	if err := source.Sync(context.Background(), first); err != nil {
		t.Fatalf("the first helper's table: %v", err)
	}
	second := &farHelperTableStub{instance: "helper-2", summaries: []proto.ConnectionSummary{
		farHelperSummary("c1", 1, 1, proto.ConnectionConnected, 1, "far.example", "dev", "SHA256:far"),
	}}
	if err := source.Sync(context.Background(), second); err != nil {
		t.Fatalf("the replacement helper's table: %v", err)
	}
	if len(ended) != 1 || ended[0] != "c1" {
		t.Fatalf("the replaced helper's entries ended as %v, want exactly c1", ended)
	}
	if c := farHelperListed(source)["c1"]; c.Generation != 1 || c.Seq != 1 {
		t.Fatalf("c1 = %+v, want the replacement's own numbering", c)
	}

	// THE DEAD CLIENT'S LAST QUEUED OBSERVATION IS REFUSED.
	source.Apply("helper-1", proto.ConnectionStateEvent{
		ConnectionID: "c1", Seq: 10, Generation: 4, State: proto.ConnectionStalled,
	})
	if c := farHelperListed(source)["c1"]; c.State != hostconn.Connected || c.Generation != 1 {
		t.Fatalf("c1 = %+v, want an observation from a replaced helper to change nothing", c)
	}
	// AND THE NEW HELPER'S, NUMBERED FROM ONE, IS APPLIED.
	source.Apply("helper-2", proto.ConnectionStateEvent{
		ConnectionID: "c1", Seq: 2, Generation: 1, State: proto.ConnectionStalled,
	})
	if c := farHelperListed(source)["c1"]; c.State != hostconn.Stalled {
		t.Fatalf("c1 = %+v, want the replacement's observation applied", c)
	}
}

// connectionsOnlySSHService is the ssh service reduced to the one op this
// adapter asks: the table. It is enough to prove the path from `connect` to the
// helper's answer; the ssh service's own tests own everything else it does.
type connectionsOnlySSHService struct {
	summaries []proto.ConnectionSummary
	mu        sync.Mutex
	calls     int
}

func (s *connectionsOnlySSHService) Name() string  { return proto.ServiceSSH }
func (s *connectionsOnlySSHService) Ops() []string { return []string{proto.OpConnections} }

func (s *connectionsOnlySSHService) ParamsSchema(op string) *helperhost.Schema {
	if op == proto.OpConnections {
		return helperhost.SchemaFor(proto.ConnectionsParams{})
	}
	return nil
}

func (s *connectionsOnlySSHService) Call(context.Context, string, json.RawMessage) (any, error) {
	s.mu.Lock()
	s.calls++
	summaries := s.summaries
	s.mu.Unlock()
	return proto.ConnectionsResult{Connections: summaries}, nil
}

func (s *connectionsOnlySSHService) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestReachingForTheHelperReadsItsConnectionTableOnce proves the READ IS WIRED
// and that it is one read per helper process: the person's first action that
// needs this machine's helper is what makes the far helper's entries exist, and
// the next pane open must not ask the same question again.
func TestReachingForTheHelperReadsItsConnectionTableOnce(t *testing.T) {
	logger := logtest.Slog(t)
	service := &connectionsOnlySSHService{summaries: []proto.ConnectionSummary{
		farHelperSummary("tunnel", 1, 1, proto.ConnectionConnected, 0, "tunnel.example", "dev", "SHA256:tunnel"),
	}}
	client := dialInProcessHelper(t, logger, service)

	source := newFarHelperConnections()
	opener := &localHelperOpener{log: logger, farConnections: source}
	opener.installedLocalGeneration(helperlocal.Installed{Binary: "/nonexistent/nocx-helper", Generation: "generation-under-test"})
	opener.dir = t.TempDir()
	opener.mu.Lock()
	opener.client = client
	opener.mu.Unlock()

	if _, _, err := opener.connect(context.Background()); err != nil {
		t.Fatalf("reaching for the local helper: %v", err)
	}
	tunnel, ok := farHelperListed(source)["tunnel"]
	if !ok {
		t.Fatal("the helper's table was answered but never reached the far helper's source")
	}
	if tunnel.Destination.Host != "tunnel.example" || tunnel.HostKeyFingerprint != "SHA256:tunnel" {
		t.Fatalf("tunnel = %+v, want the destination and key the helper answered", tunnel)
	}

	// THE SAME CLIENT IS THE SAME TABLE. A second reach answers from the opener
	// without a second request, and without a second subscription.
	if _, _, err := opener.connect(context.Background()); err != nil {
		t.Fatalf("reaching for the local helper again: %v", err)
	}
	if got := service.callCount(); got != 1 {
		t.Fatalf("the connection table was asked for %d times, want 1 per helper client", got)
	}
}

// dialInProcessHelper boots one helper over a pipe and completes this app's
// hello against it. It is the same stand the inventory tests use, reduced to the
// one service these tests register.
func dialInProcessHelper(t *testing.T, logger *slog.Logger, service helperhost.Service) *helperclient.Client {
	t.Helper()
	const generation = "generation-under-test"
	serverConn, clientConn := net.Pipe()
	peer := helperhost.New(serverConn, serverConn, generation, "instance-1", logger)
	peer.Register(service)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background()) }()
	client, err := helperclient.Dial(context.Background(), helperclient.Config{
		Exec:        helperclient.NewSocketConn(clientConn),
		ExpectHash:  generation,
		SentinelTTL: time.Second,
		Log:         logger,
	})
	if err != nil {
		_ = serverConn.Close()
		t.Fatalf("dial the in-process helper: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		if err := <-serveDone; err != nil {
			t.Errorf("in-process helper: %v", err)
		}
	})
	return client
}

func TestReviewAuthenticatedFingerprintReachesTheRegistry(t *testing.T) {
	source := newFarHelperConnections()
	_, logger := logtest.New(t)
	registry := farhelper.New(farhelper.Config{
		Connections: source,
		Modes:       &reviewModeStore{mode: hostmode.Raw},
		Consents:    &reviewConsentStore{},
		Log:         logger,
	})
	defer registry.Close()
	var observed farhelper.Connection
	source.Watch(func(c farhelper.Connection) { observed = c })
	// bind without a pin, then the authenticated connection event, then the
	// open path's Observe carrying the verified host key.
	source.Observe(farhelper.Connection{ID: "review", Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"}, State: hostconn.Opening})
	source.Apply("", proto.ConnectionStateEvent{ConnectionID: "review", Seq: 1, Generation: 1, State: proto.ConnectionConnected})
	source.Observe(farhelper.Connection{ID: "review", Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"}, HostKeyFingerprint: "SHA256:x"})
	if observed.HostKeyFingerprint != "SHA256:x" || observed.Seq != 1 || observed.State != hostconn.Connected {
		t.Fatalf("registry observation = %+v, want authenticated fingerprint on connected seq 1", observed)
	}
	var status farhelper.Status
	waittest.WaitFor(t, "raw-mode decision after authenticated observation", func() bool {
		status, _ = registry.Axis("review")
		return status.State != farhelper.StateStarting
	})
	if status.State != farhelper.StateUnavailable || status.Reason != farhelper.ReasonModeRaw || status.Action != farhelper.ActionHostSettings {
		t.Fatalf("axis = %+v, want unavailable/mode-raw/host-settings", status)
	}
}

func TestFarHelperEndedEventCannotBeResurrectedByOlderCallback(t *testing.T) {
	source := newFarHelperConnections()
	source.Observe(farhelper.Connection{ID: "race", Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"}, State: hostconn.Opening})
	source.Apply("", proto.ConnectionStateEvent{ConnectionID: "race", Seq: 6, Generation: 2, State: proto.ConnectionEnded})
	source.Apply("", proto.ConnectionStateEvent{ConnectionID: "race", Seq: 5, Generation: 2, State: proto.ConnectionConnected})
	if got := farHelperListed(source)["race"]; got.State != hostconn.Ended || got.Seq != 6 {
		t.Fatalf("race record = %+v, want ended seq 6", got)
	}
}

func TestFarHelperCloserUnfreezesOnSessionCloseFailure(t *testing.T) {
	source := newFarHelperConnections()
	source.Observe(farhelper.Connection{ID: "d25", Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"}, HostKeyFingerprint: "SHA256:d25", State: hostconn.Connected, Generation: 1})
	registry := farhelper.New(farhelper.Config{Connections: source, Log: farHelperTestLogger(t)})
	defer registry.Close()
	helperReg := &helperRegistry{closing: map[string]struct{}{"SHA256:d25": {}}}
	closer := farHelperCloser{far: registry, reg: helperReg}
	if err := closer.CloseHelpersFor(context.Background(), "SHA256:d25"); err == nil {
		t.Fatal("expected close-in-progress error")
	}
	status, ok := registry.Axis("d25")
	if !ok || status.Reason == farhelper.ReasonHelperLost {
		t.Fatalf("axis remained frozen after failed close: %+v", status)
	}
}

type reviewModeStore struct{ mode hostmode.Mode }

func (s *reviewModeStore) Lookup(string) (hostmode.Mode, bool) { return s.mode, true }
func (*reviewModeStore) Watch(func()) func()                   { return func() {} }

type reviewConsentStore struct{}

func (*reviewConsentStore) Lookup(string) (consent.Answer, bool) { return consent.Granted, true }
func (*reviewConsentStore) Watch(func()) func()                  { return func() {} }

type reviewInstall struct{ installs int }

func (*reviewInstall) Find(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, bool, error) {
	return farhelper.Installed{Dir: "/helper", Generation: "installed"}, true, nil
}

func (i *reviewInstall) Install(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, error) {
	i.installs++
	return farhelper.Installed{Dir: "/helper", Generation: "installed"}, nil
}

type reviewProber struct{}

func (reviewProber) Probe(context.Context, farhelper.Target) (farhelper.Platform, error) {
	return farhelper.Platform{GOOS: "linux", GOARCH: "amd64"}, nil
}

type reviewHelloService struct{}

func (reviewHelloService) Name() string  { return "review" }
func (reviewHelloService) Ops() []string { return []string{"noop"} }
func (reviewHelloService) ParamsSchema(string) *helperhost.Schema {
	return helperhost.SchemaFor(struct{}{})
}

func (reviewHelloService) Call(context.Context, string, json.RawMessage) (any, error) {
	return struct{}{}, nil
}

type reviewDialer struct {
	t      *testing.T
	logger *slog.Logger
}

func (d reviewDialer) Dial(context.Context, farhelper.Target, farhelper.Installed) (*helperclient.Client, error) {
	return dialInProcessHelper(d.t, d.logger, reviewHelloService{}), nil
}

func TestReviewInstalledBuildServesWithoutPlainShells(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(fmt.Sprintf("slow=%v", slow), func(t *testing.T) {
			logger := logtest.Slog(t)
			_, registryLogger := logtest.New(t)
			source := newFarHelperConnections()
			installer := &reviewInstall{}
			registry := farhelper.New(farhelper.Config{
				Connections: source,
				Modes:       &reviewModeStore{mode: hostmode.Helper},
				Consents:    &reviewConsentStore{},
				Probe:       reviewProber{}, Install: installer,
				Dial: reviewDialer{t: t, logger: logger}, Log: registryLogger,
			})
			defer registry.Close()
			const id = "review-installed"
			source.Observe(farhelper.Connection{ID: id, Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"}, HostKeyFingerprint: "SHA256:installed", State: hostconn.Connected, Generation: 1, Shells: 0, Slow: slow})
			var status farhelper.Status
			waittest.WaitFor(t, "installed helper to serve without a plain shell", func() bool {
				status, _ = registry.Axis(id)
				return status.State == farhelper.StateServing
			})
			if installer.installs != 0 {
				t.Fatalf("Install calls = %d, want 0", installer.installs)
			}
		})
	}
}
