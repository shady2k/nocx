package app

// A command typed into a pane taken back from a helper produces a block
// (nocx-k6p18.31).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE. nocx-k6p18.30 gave them the
// pane back — live output, the ledger's blocks restored — and the next command
// they ran produced nothing at all: no block, no exit chip, no duration, and
// nothing anywhere saying why. These tests watch the seam a block is made
// from: an authenticated attempt opened and completed with the shell's real
// exit status, published on the lane the returned pane is bound to.
//
// WHERE THE TEST STOPS, stated rather than implied. The block itself is drawn
// by the renderer from this published fact, and the end-to-end check that
// watches a person type into a returned pane and see the block appear is the
// epic's own acceptance spec (e2e/), which is not this package's to write. The
// backend half is what is asserted here, and it is the half that was missing:
// before this, the fact never existed to be rendered.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── the harness: a helper whose shells actually hold a lifecycle channel ──

// lifecycleSpawner is scriptedSpawner's twin for a session that asked for
// shell integration: the process it returns satisfies LifecycleProcess, so the
// helper allocates the lifecycle window and pumps the carrier. The far end of
// the socketpair is handed back to the test, which plays the shell.
type lifecycleSpawner struct {
	mu      sync.Mutex
	spawned int
	procs   []*lifecycleProcess
	launch  *proto.LifecycleLaunch
}

func (s *lifecycleSpawner) Spawn(req helpersession.SpawnRequest) (helpersession.Process, error) {
	helperEnd, shellEnd := net.Pipe()
	s.mu.Lock()
	s.spawned++
	p := &lifecycleProcess{
		idleProcess: idleProcess{done: make(chan struct{}), pid: 5000 + s.spawned, id: req.SessionID},
		carrier:     helperEnd,
		shell:       shellEnd,
	}
	s.procs = append(s.procs, p)
	s.launch = req.Lifecycle
	s.mu.Unlock()
	return p, nil
}

// theShell is the far end of the one lifecycle channel this spawner made, and
// the launch the coordinator handed it at spawn: exactly what a real shell
// holds in its own memory, and goes on holding across a coordinator
// replacement.
func (s *lifecycleSpawner) theShell(t *testing.T) (*shellSide, *proto.LifecycleLaunch) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.procs) != 1 {
		t.Fatalf("%d processes were spawned, want exactly 1", len(s.procs))
	}
	if s.launch == nil {
		t.Fatal("the helper was given no lifecycle launch, so there is no channel to take over")
	}
	return &shellSide{conn: s.procs[0].shell, launch: *s.launch}, s.launch
}

type lifecycleProcess struct {
	idleProcess
	carrier net.Conn
	shell   net.Conn
}

func (p *lifecycleProcess) Lifecycle() io.ReadWriteCloser { return p.carrier }

// shellSide speaks the lifecycle protocol as the far shell does: the identity
// it was handed at spawn, and a sequence counter it never resets — because
// nothing ever tells it to.
type shellSide struct {
	conn   net.Conn
	launch proto.LifecycleLaunch
	seq    uint64
}

func (s *shellSide) send(t *testing.T, evt lifecycle.Event) {
	t.Helper()
	var capability lifecycle.Capability
	raw, err := hex.DecodeString(s.launch.Capability)
	if err != nil || len(raw) != len(capability) {
		t.Fatalf("the launch capability is not 32 hex bytes: %q", s.launch.Capability)
	}
	copy(capability[:], raw)
	s.seq++
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lifecycle.LaneID(s.launch.Lane),
		Domain:     lifecycle.DomainID(s.launch.Domain),
		Epoch:      s.launch.Epoch,
		Sequence:   s.seq,
		Capability: capability,
		Event:      evt,
	}
	done := make(chan error, 1)
	go func() {
		_, werr := lifecyclecodec.Encode(s.conn, env)
		done <- werr
	}()
	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("the shell could not write %s: %v", evt.Kind, werr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the shell's %s was never read by anything", evt.Kind)
	}
}

// drain reads whatever the coordinator writes back (accepts, refresh
// requests). A real shell reads its channel at every prompt; without this the
// net.Pipe would block the coordinator's writer.
func (s *shellSide) drain() {
	go func() { _, _ = io.Copy(io.Discard, s.conn) }()
}

// recordingEmitter is the renderer's side of the publication boundary: it
// acknowledges every establishment immediately (decision 9 — a renderer that
// commits the editor presentation on receipt) and keeps every fact, which is
// what a block is drawn from.
type recordingEmitter struct {
	pub *lifecyclepub.Publisher

	mu    sync.Mutex
	facts []lifecyclepub.Fact
}

func (e *recordingEmitter) PublishLifecycle(f lifecyclepub.Fact) {
	e.mu.Lock()
	e.facts = append(e.facts, f)
	e.mu.Unlock()
	if f.Generation == "" || f.Domain == "" {
		return
	}
	_ = e.pub.AcknowledgeEstablishment(
		lifecycle.LaneID(f.Lane), lifecycle.DomainID(f.Domain), f.Epoch, f.Generation)
}

func (e *recordingEmitter) completed() []lifecyclepub.Fact {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []lifecyclepub.Fact
	for _, f := range e.facts {
		if f.Attempt != nil && f.Attempt.State == lifecyclepub.AttemptCompleted {
			out = append(out, f)
		}
	}
	return out
}

func (e *recordingEmitter) any() []lifecyclepub.Fact {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]lifecyclepub.Fact(nil), e.facts...)
}

// integratedCoordinator is a coordinator whose helper registry has a lifecycle
// kernel behind it — which is what makes its helper-hosted sessions ask for
// shell integration at all.
type integratedCoordinator struct {
	*coordinator
	emitter *recordingEmitter
}

func newIntegratedCoordinator(t *testing.T, provider *fakeLaneProvider) *integratedCoordinator {
	t.Helper()
	c := newCoordinator(t, provider)
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	em := &recordingEmitter{pub: pub}
	pub.SetEmitter(em)
	c.reg.lifecycle = pub
	return &integratedCoordinator{coordinator: c, emitter: em}
}

// helperWithIntegratedShells builds the shared daemon whose one shell holds a
// lifecycle channel.
func helperWithIntegratedShells(spawner *lifecycleSpawner) *helpersession.Service {
	return helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    spawner,
		Log:        discardLogger(),
	})
}

// ── the happy path ────────────────────────────────────────────────────────

func TestACommandRunAfterTheReturnProducesABlock(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	first := newIntegratedCoordinator(t, provider)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()

	// The shell completes its handshake with the coordinator that started it,
	// and then runs one command — the state a user is in when they quit nocx.
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated with the coordinator that started it", func() bool {
		return len(first.emitter.any()) > 0
	})

	first.quit()
	second := newIntegratedCoordinator(t, provider)
	adopter := &stubAdopter{}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second.coordinator, routesFor(binding), adopter), time.Hour, quietLogger())

	if adopted := adopter.adoptedIDs(); len(adopted) != 1 {
		t.Fatalf("the session was not taken back at all (%v); there is nothing to run a command in", adopter.failure())
	}

	// THE COMMAND A USER TYPES AFTER THE RETURN. The shell knows nothing about
	// the replacement: same domain, same epoch, same capability, and a
	// sequence that carries on from where it was.
	attempt := lifecycle.AttemptID("shell-after-return")
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &attempt, Command: "make"},
	})
	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete,
		Complete: &lifecycle.Complete{
			ExitCode: &code,
			Fence:    lifecycle.FenceNonce{1, 2, 3},
		},
	})

	waittest.WaitFor(t, "the command run in the returned pane produced a completed block", func() bool {
		return len(second.emitter.completed()) > 0
	})
	done := second.emitter.completed()
	last := done[len(done)-1]
	if last.Attempt.Command != "make" {
		t.Fatalf("block command = %q, want make", last.Attempt.Command)
	}
	if last.Attempt.ExitCode == nil || *last.Attempt.ExitCode != 0 {
		t.Fatalf("block exit code = %v, want 0 — a block with no outcome is the defect, one step later", last.Attempt.ExitCode)
	}
	if last.Lane != binding.SessionID && last.Domain == "" {
		t.Fatalf("the block was published on no domain: %+v", last)
	}
}

// The pane says it is integrated, because it is: the axis is what the product
// renders, and a returned pane that produced blocks while the axis said
// nothing would be the same silence in the other direction.
func TestATakenBackPaneReportsItsIntegrationAndItsLane(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	first := newIntegratedCoordinator(t, provider)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, launch := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated", func() bool { return len(first.emitter.any()) > 0 })

	first.quit()
	second := newIntegratedCoordinator(t, provider)
	adopter := &stubAdopter{}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second.coordinator, routesFor(binding), adopter), time.Hour, quietLogger())

	open := adopter.lastOpen()
	if open.LifecycleLane != lifecycle.LaneID(launch.Lane) {
		t.Fatalf("lane = %q, want the lane the shell was launched on (%q) — an unregistered lane drops every fact",
			open.LifecycleLane, launch.Lane)
	}
	if open.IntegrationStatus != transport.IntegrationStarting {
		t.Fatalf("integration status = %q, want %q", open.IntegrationStatus, transport.IntegrationStarting)
	}
	if open.IntegrationShell == "" {
		t.Fatal("the axis was given no shell name, so nothing is registered and the pane says nothing")
	}
}

// ── the degrades, each stated ─────────────────────────────────────────────

// An older helper generation is resident beside a newer one by design and does
// not know the op. The session still comes back — output live, ledger
// restored — and the pane STATES that its blocks are gone rather than falling
// silent, which is the whole of what this bead refuses to ship.
func TestAGenerationThatCannotHandBackTheChannelSaysSoInTheProduct(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	first := newIntegratedCoordinator(t, provider)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated", func() bool { return len(first.emitter.any()) > 0 })
	first.quit()

	// The generation that answers now is one from before adopt-lifecycle
	// existed: it serves the frozen ABI and refuses everything newer.
	provider.peer = olderGenerationPeer(svc)
	second := newIntegratedCoordinator(t, provider)
	adopter := &stubAdopter{}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second.coordinator, routesFor(binding), adopter), time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want live — a channel that cannot be re-established is not a session that is gone", rec.applied)
	}
	if adopted := adopter.adoptedIDs(); len(adopted) != 1 {
		t.Fatalf("the session was not taken back (%v); a lifecycle channel it cannot re-establish must not cost it the pane", adopter.failure())
	}
	open := adopter.lastOpen()
	if open.IntegrationStatus != transport.IntegrationConventional {
		t.Fatalf("integration status = %q, want %q — the pane must say the blocks are gone",
			open.IntegrationStatus, transport.IntegrationConventional)
	}
	if open.IntegrationReason != ssh.ReasonChannelUnavailable {
		t.Fatalf("reason = %q, want %q", open.IntegrationReason, ssh.ReasonChannelUnavailable)
	}
	if open.LifecycleLane != "" {
		t.Fatalf("a lane was registered for a channel that was never adopted: %q", open.LifecycleLane)
	}
}

// A session that never had a lifecycle channel says NOTHING, and that is the
// right answer rather than a missing one: absence on the axis is how
// "conventional by design" is expressed, and a pane that never offered blocks
// has nothing to have lost.
func TestAConventionalSessionTakenBackSaysNothingAboutIntegration(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	first := newIntegratedCoordinator(t, provider)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	first.quit()

	second := newIntegratedCoordinator(t, provider)
	adopter := &stubAdopter{}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second.coordinator, routesFor(binding), adopter), time.Hour, quietLogger())

	open := adopter.lastOpen()
	if open.IntegrationStatus != "" {
		t.Fatalf("integration status = %q, want empty — a session that never asked for integration must not be nagged about",
			open.IntegrationStatus)
	}
	if open.LifecycleLane != "" {
		t.Fatalf("a lane was registered for a session with no channel: %q", open.LifecycleLane)
	}
}

// THE REPLAY GUARD. The helper retains the lifecycle bytes the previous
// coordinator already consumed, and the adopted domain keeps the capability
// those bytes are stamped with — so attaching at the window's BASE would
// re-deliver commands that already ran, into a kernel that would authenticate
// every one of them. The attachment resumes at the head instead, and this is
// what says so.
func TestTheAdoptedChannelDoesNotReplayCommandsThatAlreadyRan(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	first := newIntegratedCoordinator(t, provider)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	waittest.WaitFor(t, "the shell integrated", func() bool { return len(first.emitter.any()) > 0 })

	before := lifecycle.AttemptID("shell-before-the-replacement")
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &before, Command: "rm -rf /tmp/scratch"},
	})
	code := 0
	shell.send(t, lifecycle.Event{
		Kind:     lifecycle.KindComplete,
		Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{9}},
	})
	waittest.WaitFor(t, "the first coordinator saw the command that ran under it", func() bool {
		return len(first.emitter.completed()) > 0
	})

	first.quit()
	second := newIntegratedCoordinator(t, provider)
	adopter := &stubAdopter{}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second.coordinator, routesFor(binding), adopter), time.Hour, quietLogger())
	if adopted := adopter.adoptedIDs(); len(adopted) != 1 {
		t.Fatalf("the session was not taken back: %v", adopter.failure())
	}

	// Give the carrier every chance to deliver a replay if it were going to:
	// the shell runs a NEW command, and only that one may appear.
	after := lifecycle.AttemptID("shell-after-the-replacement")
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &after, Command: "echo hello"},
	})
	shell.send(t, lifecycle.Event{
		Kind:     lifecycle.KindComplete,
		Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{2}},
	})
	waittest.WaitFor(t, "the new command produced a block", func() bool {
		return len(second.emitter.completed()) > 0
	})
	for _, f := range second.emitter.completed() {
		if f.Attempt.Command == "rm -rf /tmp/scratch" {
			t.Fatal("a command that ran under the previous coordinator was replayed into the new kernel " +
				"and authenticated there; the attachment resumed at the window's base")
		}
	}
}

// olderGenerationPeer serves the SAME daemon — the sessions are still there
// and still reachable — behind a service that does not declare
// adopt-lifecycle. That is what a coordinator meets when the generation
// holding its sessions predates the op, which is a state the design
// guarantees: two generations are resident at once, and one lingers for as
// long as it holds a session.
func olderGenerationPeer(svc *helpersession.Service) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", discardLogger())
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(&generationWithoutAdoptLifecycle{Service: svc})
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// generationWithoutAdoptLifecycle is the session service as an older
// generation shipped it: the frozen ABI, and unknown_op for anything newer.
type generationWithoutAdoptLifecycle struct {
	*helpersession.Service
}

func (g *generationWithoutAdoptLifecycle) Ops() []string {
	out := make([]string, 0, len(g.Service.Ops()))
	for _, op := range g.Service.Ops() {
		if op == proto.OpAdoptLifecycle {
			continue
		}
		out = append(out, op)
	}
	return out
}

func (g *generationWithoutAdoptLifecycle) ParamsSchema(op string) *host.Schema {
	if op == proto.OpAdoptLifecycle {
		return nil
	}
	return g.Service.ParamsSchema(op)
}

func (g *generationWithoutAdoptLifecycle) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	if op == proto.OpAdoptLifecycle {
		return nil, errors.New("session: no op " + op)
	}
	return g.Service.Call(ctx, op, params)
}

var _ = session.ID("")
