package app

// Two rules about a far helper's open, both bought by the owner's stuck panes
// (nocx-398ca).
//
// THE FIRST IS THE STARTUP DEADLINE. A far helper's daemon is started by the
// bridge the coordinator's lane runs, and it used to retire when a deadline
// measured from its own launch expired with no session. Between that launch
// and the first spawn the coordinator does network work — the shell
// integration publish and the tool socket's directory, both SFTP round trips
// — so a host with a real round trip lost the race and a lucky one won it.
// The daemon now counts a CONNECTED client as the end of the question: the
// startup deadline asks whether anybody ever reached it, and ConnectedIdleGrace
// bounds a client that reached it and then did nothing. This test fires the
// startup deadline at the exact moment the coordinator is between connecting
// and spawning, which is the moment a duration could only approximate, and
// requires the open to succeed.
//
// THE SECOND IS THAT AN OPEN THAT CANNOT COMPLETE FAILS. A helper that is
// live and silent used to hold the open — and with it one of the execution
// lane's eight permits — for the life of the server. The open now has a bound
// and returns an error; the test below makes a helper accept the spawn and
// never answer it.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	hostsvc "github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
)

// scheduledDeadline is one deadline a daemon asked a fake timer for.
type scheduledDeadline struct {
	after time.Duration
	fire  func()
}

// recordingTimers is the LifecycleTimers seam with a pen: it records every
// deadline and fires one when the test says so. Nothing waits for a clock.
type recordingTimers struct {
	mu    sync.Mutex
	deadl []scheduledDeadline
}

func newRecordingTimers() *recordingTimers { return &recordingTimers{} }

func (r *recordingTimers) AfterFunc(after time.Duration, fire func()) helpersession.LifecycleTimer {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := len(r.deadl)
	r.deadl = append(r.deadl, scheduledDeadline{after: after, fire: fire})
	return &recordedTimer{timers: r, index: index}
}

// fireDeadline fires every deadline scheduled for exactly this duration. A
// deadline that was stopped is fired anyway: Stop losing a race to a timer
// that already fired is the case the daemon's rule must survive, and it is
// the case a real clock produces under load.
func (r *recordingTimers) fireDeadline(t *testing.T, after time.Duration) int {
	t.Helper()
	r.mu.Lock()
	var fires []func()
	for _, d := range r.deadl {
		if d.after == after {
			fires = append(fires, d.fire)
		}
	}
	r.mu.Unlock()
	if len(fires) == 0 {
		t.Fatalf("no deadline of %s was scheduled (recorded: %v)", after, r.durations())
	}
	for _, fire := range fires {
		fire()
	}
	return len(fires)
}

func (r *recordingTimers) durations() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]time.Duration, 0, len(r.deadl))
	for _, d := range r.deadl {
		out = append(out, d.after)
	}
	return out
}

type recordedTimer struct {
	timers *recordingTimers
	index  int
}

// Stop reports that the deadline will not fire on its own. The fake still
// lets a test fire it afterwards on purpose: Stop losing a race to a timer
// that already fired is the case the daemon's rule must survive, and it is
// the case a real clock produces under load.
func (t *recordedTimer) Stop() bool { return true }

// gatedTools is a far tool surface whose preparation is held until the test
// releases it. The hold is the network work between the helper's launch and
// its first spawn, made observable.
type gatedTools struct {
	preparing chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newGatedTools() *gatedTools {
	return &gatedTools{preparing: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedTools) PrepareFarPaneToolSocket(ctx context.Context, _ string, _ []ssh.ConnectOption, name string) (farPaneToolRoutes, error) {
	g.once.Do(func() { close(g.preparing) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return farPaneToolRoutes{}, ctx.Err()
	}
	return farPaneToolRoutes{
		Dir:    "/home/far/.nocx/run/" + name,
		Path:   "/home/far/.nocx/run/" + name + "/tool.sock",
		Target: "/run/nocx/tool.sock",
	}, nil
}

func (g *gatedTools) OpenFarPaneToolSocket(context.Context, string, []ssh.ConnectOption, farPaneToolRoutes, string) (proto.ForwardID, error) {
	return proto.ForwardID{0x42}, nil
}

func (g *gatedTools) CloseFarPaneToolSocket(context.Context, proto.ForwardID) error { return nil }

func (g *gatedTools) RemoveFarPaneToolSocketDir(context.Context, string, []ssh.ConnectOption, farPaneToolRoutes) error {
	return nil
}

func (g *gatedTools) RecordPaneBearer(session.ID, string) {}

// silentSpawner accepts every spawn and never answers it: a helper that is
// alive, has read the request, and has stopped making progress. It is what a
// wedged far side looks like from here, and it is the state that pinned the
// execution lane.
type silentSpawner struct {
	entered chan struct{}
	once    sync.Once
	block   chan struct{}
}

func newSilentSpawner() *silentSpawner {
	return &silentSpawner{entered: make(chan struct{}), block: make(chan struct{})}
}

func (s *silentSpawner) Spawn(helpersession.SpawnRequest) (helpersession.Process, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.block
	return nil, errors.New("silent spawner released")
}

func (s *silentSpawner) SpawnSSH(context.Context, helpersession.SSHSpawnRequest) (helpersession.Process, error) {
	return nil, errors.New("this stand spawns locally; spawn-ssh is this machine's helper's route")
}

// helperLifecycleStand is a far helper the coordinator reaches over the same fake lane the
// other far-route tests use, with its daemon's lifecycle under the test's
// control.
type helperLifecycleStand struct {
	reg      *helperRegistry
	timers   *recordingTimers
	provider *fakeLaneProvider
}

func startFarStand(t *testing.T, spawner interface {
	helpersession.Spawner
	helpersession.SSHSpawner
}, connectedGrace time.Duration, onDrained func(),
) *helperLifecycleStand {
	t.Helper()
	timers := newRecordingTimers()
	svc := helpersession.New(helpersession.Options{
		Generation:      proto.GenerationID(syntheticArtifactHash),
		Spawner:         spawner,
		SSHSpawner:      spawner,
		Log:             discardLogger(t),
		StartupGrace:    helpersession.MeasuredStartupGrace,
		ConnectedGrace:  connectedGrace,
		LifecycleTimers: timers,
		OnDrained:       onDrained,
	})
	t.Cleanup(svc.Close)
	peer := func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, syntheticArtifactHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
	provider := &fakeLaneProvider{peer: peer, home: t.TempDir()}
	store, installs := testConsentStores(t)
	_, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, discardLogger(t))
	reg.registry = session.New(log.NewSlogAdapter(discardLogger(t)), &reachPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(discardLogger(t)))})
	t.Cleanup(func() {
		endSessions(reg.registry)
		endHelperChannels(reg)
	})
	return &helperLifecycleStand{reg: reg, timers: timers, provider: provider}
}

// TestAFarHelperIsNotRetiredWhileItsCoordinatorIsOnItsWay is defect 1: the
// startup deadline arrives while the coordinator is between its connection
// and its first spawn, and the open must still complete.
func TestAFarHelperIsNotRetiredWhileItsCoordinatorIsOnItsWay(t *testing.T) {
	stand := startFarStand(t, &recordingSpawner{}, helpersession.ConnectedIdleGrace, func() {})
	tools := newGatedTools()
	stand.reg.tools = tools

	type result struct {
		opened   transport.HostedSessionOpen
		selected bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		opened, selected, err := stand.reg.OpenHosted(context.Background(), farPaneConfig(), "claim-on-its-way")
		done <- result{opened: opened, selected: selected, err: err}
	}()

	waittest.WaitForTimeout(t, "the open to reach the tool socket", 30*time.Second, func() bool {
		select {
		case <-tools.preparing:
			return true
		default:
			return false
		}
	})
	// The deadline the daemon armed at its own launch fires NOW, with a
	// client connected and no session yet: exactly the window the owner's
	// network work occupied.
	stand.timers.fireDeadline(t, helpersession.MeasuredStartupGrace)
	close(tools.release)

	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the open never returned after the startup deadline fired: a connected client did not keep its helper")
	}
	if got.err != nil {
		t.Fatalf("opening a far pane whose helper's startup deadline arrived mid-open: %v", got.err)
	}
	if !got.selected || got.opened.Session == nil {
		t.Fatalf("the far helper did not host the pane: selected=%v session=%v", got.selected, got.opened.Session != nil)
	}
	t.Cleanup(func() { _ = got.opened.Session.Close() })
}

// TestAFarHelperThatStopsAnsweringFailsTheOpenInsteadOfHoldingIt is defect 2:
// a live-but-silent helper must not own the open for ever.
func TestAFarHelperThatStopsAnsweringFailsTheOpenInsteadOfHoldingIt(t *testing.T) {
	// The production budget is the shell-setup one; the rule is what this
	// test is about, so the answer is shrunk rather than waited out (the same
	// seam gitOpenTimeout has).
	previous := farHelperAnswerTimeout
	farHelperAnswerTimeout = 2 * time.Second
	t.Cleanup(func() { farHelperAnswerTimeout = previous })

	spawner := newSilentSpawner()
	// The request stays unanswered for the length of the assertion; releasing
	// it at the end is what lets the stand's own helper handler finish.
	t.Cleanup(func() { close(spawner.block) })
	stand := startFarStand(t, spawner, helpersession.ConnectedIdleGrace, func() {})
	stand.reg.tools = &immediateTools{}

	type result struct {
		selected bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, selected, err := stand.reg.OpenHosted(context.Background(), farPaneConfig(), "claim-silent")
		done <- result{selected: selected, err: err}
	}()

	waittest.WaitForTimeout(t, "the helper to read the spawn", 30*time.Second, func() bool {
		select {
		case <-spawner.entered:
			return true
		default:
			return false
		}
	})
	// The request is in the helper's hands and will never be answered. The
	// open must end on its own bound, not on the test's patience.
	var got result
	select {
	case got = <-done:
	case <-time.After(farHelperAnswerTimeout + 30*time.Second):
		t.Fatalf("an open against a silent far helper never returned: it holds an execution-lane permit for the life of the server (bound %s)", farHelperAnswerTimeout)
	}
	if got.err == nil {
		t.Fatal("a silent far helper reported a successful open")
	}
	if !strings.Contains(got.err.Error(), "did not answer the spawn") {
		t.Fatalf("the failure does not say which answer never came: %v", got.err)
	}
	if !got.selected {
		t.Fatal("the far helper declined a destination it hosted, so the refusal says nothing about the silent helper")
	}
}

// immediateTools is the same surface without a gate: the open reaches its
// spawn as fast as the coordinator can ask for it.
type immediateTools struct{}

func (*immediateTools) PrepareFarPaneToolSocket(_ context.Context, _ string, _ []ssh.ConnectOption, name string) (farPaneToolRoutes, error) {
	return farPaneToolRoutes{
		Dir:    "/home/far/.nocx/run/" + name,
		Path:   "/home/far/.nocx/run/" + name + "/tool.sock",
		Target: "/run/nocx/tool.sock",
	}, nil
}

func (*immediateTools) OpenFarPaneToolSocket(context.Context, string, []ssh.ConnectOption, farPaneToolRoutes, string) (proto.ForwardID, error) {
	return proto.ForwardID{0x42}, nil
}

func (*immediateTools) CloseFarPaneToolSocket(context.Context, proto.ForwardID) error { return nil }

func (*immediateTools) RemoveFarPaneToolSocketDir(context.Context, string, []ssh.ConnectOption, farPaneToolRoutes) error {
	return nil
}

func (*immediateTools) RecordPaneBearer(session.ID, string) {}

// TestAFarHelperThatRefusesTheSpawnFailsTheOpen is the refusing half of
// defect 2: a helper that has stopped admitting answers the spawn with
// `draining`, and the open must report it rather than wait.
func TestAFarHelperThatRefusesTheSpawnFailsTheOpen(t *testing.T) {
	stand := startFarStand(t, &recordingSpawner{}, helpersession.ConnectedIdleGrace, func() {})
	tools := newGatedTools()
	stand.reg.tools = tools

	type result struct {
		selected bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, selected, err := stand.reg.OpenHosted(context.Background(), farPaneConfig(), "claim-refused")
		done <- result{selected: selected, err: err}
	}()

	waittest.WaitForTimeout(t, "the open to reach the tool socket", 30*time.Second, func() bool {
		select {
		case <-tools.preparing:
			return true
		default:
			return false
		}
	})
	// The connected-idle bound arrives: the helper stops admitting, and its
	// connection stays up (OnDrained above does nothing), which is the state
	// a retiring generation can be in while an admission it already granted
	// finishes.
	stand.timers.fireDeadline(t, helpersession.ConnectedIdleGrace)
	close(tools.release)

	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("an open refused by a draining far helper never returned")
	}
	if got.err == nil {
		t.Fatal("a far helper that refused the spawn reported a successful open")
	}
	if !got.selected {
		t.Fatal("the far helper declined a destination it hosted, so the refusal says nothing about draining")
	}
}

// TestAFarHelperThatDiedIsReplacedByTheNextOpen is the recovery half of the
// owner's report (nocx-398ca, nocx-3an1s.1.1): killing the helper left the
// server refusing every action until it was killed too. What made that
// permanent was the execution lane — stuck opens held all eight permits — but
// what must also hold is the next open: a helper that is gone is started
// again rather than refused for ever.
//
// The stand runs a FRESH helper process per lane, which is what the far side
// does: `endpoint.Ensure` starts one when nothing serves, and the generation's
// socket is the authority on whether one is already there.
func TestAFarHelperThatDiedIsReplacedByTheNextOpen(t *testing.T) {
	var mu sync.Mutex
	var services []*helpersession.Service
	peer := func(in io.Reader, out io.Writer) int {
		svc := helpersession.New(helpersession.Options{
			Generation: proto.GenerationID(syntheticArtifactHash),
			Spawner:    &recordingSpawner{},
			SSHSpawner: &recordingSpawner{},
			Log:        discardLogger(t),
		})
		mu.Lock()
		services = append(services, svc)
		mu.Unlock()
		t.Cleanup(svc.Close)
		h := host.New(in, out, syntheticArtifactHash, "instance-1", discardLogger(t))
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
	provider := &fakeLaneProvider{peer: peer, home: t.TempDir()}
	store, installs := testConsentStores(t)
	_, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, discardLogger(t))
	reg.registry = session.New(log.NewSlogAdapter(discardLogger(t)), &reachPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(discardLogger(t)))})
	reg.tools = &immediateTools{}
	t.Cleanup(func() {
		endSessions(reg.registry)
		endHelperChannels(reg)
	})

	first, selected, err := reg.OpenHosted(context.Background(), farPaneConfig(), "claim-before-the-kill")
	if err != nil || !selected || first.Session == nil {
		t.Fatalf("the first far open: selected=%v err=%v", selected, err)
	}
	t.Cleanup(func() { _ = first.Session.Close() })
	mu.Lock()
	started := len(services)
	mu.Unlock()
	if started != 1 {
		t.Fatalf("%d far helpers were started for one pane, want 1", started)
	}

	// THE HELPER DIES: its process ends, which is its connection ending here.
	provider.mu.Lock()
	lanes := append([]*fakeLaneConn(nil), provider.conns...)
	provider.mu.Unlock()
	for _, lane := range lanes {
		_ = lane.Close()
	}
	mu.Lock()
	for _, svc := range services {
		svc.Close()
	}
	mu.Unlock()

	// THE NEXT OPEN STARTS A NEW ONE, rather than refusing because a helper
	// it remembers is gone.
	second, selected, err := reg.OpenHosted(context.Background(), farPaneConfig(), "claim-after-the-kill")
	if err != nil || !selected || second.Session == nil {
		t.Fatalf("the far open after its helper died: selected=%v err=%v", selected, err)
	}
	t.Cleanup(func() { _ = second.Session.Close() })
	mu.Lock()
	started = len(services)
	mu.Unlock()
	if started != 2 {
		t.Fatalf("%d far helpers were started across a kill and a reopen, want 2 (a new helper serves a dead one's destination)", started)
	}
}
