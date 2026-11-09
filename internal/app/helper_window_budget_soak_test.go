package app

// THE HELPER'S WINDOW BUDGET, UNDER A SOAK (nocx-ouru5.1.5).
//
// The owner reports the 512 MiB aggregate exhausted again after a long day
// (GitHub #689): "open a pane on this machine's helper: helper: window_budget:
// session: the helper's aggregate window budget is exhausted: 536870912 of
// 536870912 bytes reserved for output windows and row buffers". nocx-isjh4 made
// a closed pane give its reservation back and nocx-k35e3 closed the symptom;
// the symptom is back, so a path since then either ends a session without
// releasing, never ends one, or reserves without bound.
//
// THE INVARIANT THIS FILE DEFENDS is the one the next pane depends on: the
// aggregate a live helper has committed is EXACTLY what the sessions it holds
// reserve — no more, no less. A pane that is open is counted; a pane that is
// gone is not. Everything below drives the shipped seams (this machine's
// opener, the helper's own service behind a real endpoint socket, the registry
// verb a pane close runs) and asks the helper for its number after every step,
// rather than waiting for the day it refuses.
//
// The shells are scripted, and only the shells: what a PTY's bytes do with the
// row buffer is internal/helper/session's own subject, while which session that
// PTY belongs to, and whether anything ever says it is over, is this one's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// ── the shell, scripted ──────────────────────────────────────────────────

// soakProcess is one pane's shell: bytes the test hands it are what the pane
// prints, it stays alive until it is closed, and — when the spawn asked for
// one — it offers the lifecycle descriptor carrier a real launcher offers.
//
// The carrier is offered EXACTLY when the request named a lifecycle, because
// that is the helper's own rule for a reservation: a spawn that asks for the
// channel reserves a second window for it (helper/session's spawn), and a
// launcher that supplies none gives that half back at once. A fake that always
// supplied one, or never did, would make the arithmetic below a fact about the
// fake rather than about the product.
type soakProcess struct {
	pid int

	out   *io.PipeReader
	sink  *io.PipeWriter
	life  *io.PipeReader
	lifeW *io.PipeWriter
	// carrier is the descriptor channel as the helper reads and writes it.
	carrier io.ReadWriteCloser

	done chan struct{}
	once sync.Once

	mu   sync.Mutex
	wait error
}

func newSoakProcess(pid int, withLifecycle bool) *soakProcess {
	out, sink := io.Pipe()
	p := &soakProcess{pid: pid, out: out, sink: sink, done: make(chan struct{})}
	if withLifecycle {
		p.life, p.lifeW = io.Pipe()
		p.carrier = soakCarrier{reader: p.life, writer: p.lifeW}
	}
	return p
}

// soakCarrier is one pipe read from one end and written at the other, which is
// the shape a launcher's descriptor channel has.
type soakCarrier struct {
	reader *io.PipeReader
	writer *io.PipeWriter
}

func (c soakCarrier) Read(b []byte) (int, error)  { return c.reader.Read(b) }
func (c soakCarrier) Write(b []byte) (int, error) { return c.writer.Write(b) }
func (c soakCarrier) Close() error {
	_ = c.writer.Close()
	return c.reader.Close()
}

func (p *soakProcess) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *soakProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *soakProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (p *soakProcess) Done() <-chan struct{} { return p.done }
func (p *soakProcess) Pid() int              { return p.pid }
func (p *soakProcess) Shell() string         { return "/bin/sh" }
func (p *soakProcess) ForegroundProcessGroup() (int, error) {
	return p.pid, nil
}

// Lifecycle answers the descriptor carrier, and answers nil for a process whose
// spawn never asked for one.
func (p *soakProcess) Lifecycle() io.ReadWriteCloser {
	if p.life == nil {
		return nil
	}
	return p.carrier
}

func (p *soakProcess) WaitErr() (error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wait, p.wait != nil
}

func (p *soakProcess) Close() error {
	p.once.Do(func() {
		close(p.done)
		_ = p.sink.Close()
		_ = p.out.Close()
		if p.lifeW != nil {
			_ = p.lifeW.Close()
			_ = p.life.Close()
		}
	})
	return nil
}

// soakSpawner is the daemon's shell factory: it records what each spawn asked
// for, so the test can say what a helper session's reservation is without
// reading the helper's own fields.
type soakSpawner struct {
	mu       sync.Mutex
	spawned  int
	procs    []*soakProcess
	byID     map[string]*soakProcess
	lifeByID map[string]bool
	// gate, when non-nil, holds every spawn inside the daemon until it is
	// closed: a pane whose shell is slow to start is what a person cancels, and
	// it is also the only moment at which the daemon can learn that the
	// coordinator is still there. entered reports that a spawn reached it.
	gate    chan struct{}
	entered chan struct{}
}

func newSoakSpawner() *soakSpawner {
	return &soakSpawner{byID: map[string]*soakProcess{}, lifeByID: map[string]bool{}}
}

func (s *soakSpawner) make(id string, withLifecycle bool) *soakProcess {
	s.spawned++
	p := newSoakProcess(4000+s.spawned, withLifecycle)
	s.procs = append(s.procs, p)
	s.byID[id] = p
	s.lifeByID[id] = withLifecycle
	return p
}

func (s *soakSpawner) Spawn(req helpersession.SpawnRequest) (helpersession.Process, error) {
	s.waitAtGate()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.make(req.SessionID, req.Lifecycle != nil), nil
}

// waitAtGate parks a spawn at the gate a test holds, after saying it arrived.
func (s *soakSpawner) waitAtGate() {
	s.mu.Lock()
	gate, entered := s.gate, s.entered
	s.mu.Unlock()
	if gate == nil {
		return
	}
	select {
	case entered <- struct{}{}:
	default:
	}
	<-gate
}

func (s *soakSpawner) SpawnSSH(_ context.Context, req helpersession.SSHSpawnRequest) (helpersession.Process, error) {
	s.waitAtGate()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.make(req.SessionID, req.Lifecycle != nil), nil
}

// askedForLifecycle answers whether this session's spawn reserved the second
// window, which is the one fact the inventory cannot report and this test
// cannot compute.
// count is how many shells this daemon was asked to fork, so a test can say
// whether a retry was handed a session or made a second one.
func (s *soakSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawned
}

func (s *soakSpawner) askedForLifecycle(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifeByID[sid]
}

// emit makes the pane print bytes, through the one path a real shell uses: its
// own PTY, read by the helper's pump. The text is long lines the way a build
// log arrives, and the amount is bounded by the pane's own output window — a
// session nobody has attached to has nobody to acknowledge its bytes, which is
// the honest bound on what an unread pane can produce.
func (s *soakSpawner) emit(t *testing.T, sid string, total int64) {
	t.Helper()
	s.mu.Lock()
	p := s.byID[sid]
	s.mu.Unlock()
	if p == nil {
		t.Fatalf("no shell was spawned for session %q", sid)
	}
	line := make([]byte, 0, 121)
	for i := 0; i < 120; i++ {
		line = append(line, byte('a'+i%26))
	}
	line = append(line, '\n')
	var payload []byte
	for int64(len(payload)) < total {
		payload = append(payload, line...)
	}
	// Written on its own goroutine and bounded: a pump that is not reading (a
	// full output window) is a fact about the helper, not a reason for the soak
	// to hang. The write either completes or the test says which pane stopped
	// taking output.
	done := make(chan error, 1)
	go func() {
		_, err := p.sink.Write(payload)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writing %d bytes of output for session %q: %v", len(payload), sid, err)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("session %q took none of %d bytes of output in 60s", sid, len(payload))
	}
}

// ── this machine's helper, served for real ───────────────────────────────

// soakHelper IS this machine's daemon for this file: a real endpoint socket in
// a real endpoint directory, served by the real host protocol over a real
// helpersession.Service, exactly as cmd/nocx-helper serves one.
type soakHelper struct {
	ln      net.Listener
	svc     *helpersession.Service
	answers *heldAnswers
	spawner *soakSpawner
}

// heldAnswers is this machine's daemon with one seam: it can hold the ANSWER to
// the first spawn it serves, after the service has done everything it does.
//
// The interval it holds open is the one the review round 2 findings are about:
// the shell exists, the claim is resolved, and the frame carrying the session's
// id has NOT been written yet. A withdrawal on one side of that instant and the
// other are two different outcomes, so a test must be able to stand exactly
// there.
type heldAnswers struct {
	*helpersession.Service

	mu      sync.Mutex
	hold    chan struct{}
	holding chan struct{}
	held    bool
	// withdrawnDone is signalled after an answer whose delivery was a
	// withdrawal has been acted on. A test that must prove the DECISION — that
	// a session somebody else holds was NOT ended, which no amount of polling
	// can distinguish from "the decision has not been taken yet" — waits for
	// this edge rather than for a verdict about time.
	withdrawnDone chan struct{}
}

// holdNextSpawn arms the hold and answers the signal that it has been reached,
// with the release. Only the first spawn served is held: a retry for the same
// claim is an answer of its own and runs straight through.
func (h *heldAnswers) holdNextSpawn() (<-chan struct{}, func()) {
	hold, holding := make(chan struct{}), make(chan struct{})
	h.mu.Lock()
	h.hold, h.holding, h.held = hold, holding, false
	h.mu.Unlock()
	return holding, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		select {
		case <-hold:
		default:
			close(hold)
		}
	}
}

func (h *heldAnswers) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	result, err := h.Service.Call(ctx, op, params)
	if err != nil || (op != proto.OpSpawn && op != proto.OpSpawnSSH) {
		return result, err
	}
	h.mu.Lock()
	if h.hold == nil || h.held {
		h.mu.Unlock()
		return result, err
	}
	h.held = true
	hold, holding := h.hold, h.holding
	h.mu.Unlock()
	close(holding)
	<-hold
	return result, err
}

// ResponseWritten forwards the answer's fate to the service — which is what
// decides whether an unread answer's session is ended — and says when that has
// been done for a withdrawal.
func (h *heldAnswers) ResponseWritten(ctx context.Context, op string, result any, delivery host.AnswerDelivery) {
	h.Service.ResponseWritten(ctx, op, result, delivery)
	if !delivery.Withdrawn {
		return
	}
	h.mu.Lock()
	done := h.withdrawnDone
	h.mu.Unlock()
	if done != nil {
		select {
		case done <- struct{}{}:
		default:
		}
	}
}

// awaitWithdrawal waits until the daemon has acted on an answer that reached
// nobody.
func (h *heldAnswers) awaitWithdrawal(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	done := h.withdrawnDone
	h.mu.Unlock()
	if done == nil {
		t.Fatal("no withdrawal edge was armed")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon never acted on an answer that reached nobody")
	}
}

func startSoakHelper(t *testing.T, dir, generation string, spawner *soakSpawner) *soakHelper {
	t.Helper()
	ln, err := endpoint.Listen(dir, proto.GenerationID(generation))
	if err != nil {
		t.Fatalf("serving this machine's endpoint at %s: %v", dir, err)
	}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(generation),
		Spawner:    spawner,
		SSHSpawner: spawner,
		Log:        discardLogger(t),
	})
	t.Cleanup(svc.Close)
	answers := &heldAnswers{Service: svc, withdrawnDone: make(chan struct{}, 4)}
	helper := &soakHelper{ln: ln, svc: svc, answers: answers, spawner: spawner}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	go func() {
		_ = endpoint.Serve(ctx, ln, func(conn net.Conn) {
			h := host.New(conn, conn, generation, "instance-1", discardLogger(t))
			h.Register(hostsvc.New(localgit.NewFactory()))
			// The wrapper, not the service: it forwards everything and can hold
			// one spawn's answer. It promotes the service's ResponseWritten, so
			// the answer's fate still reaches the party that acts on it.
			h.Register(answers)
			release := svc.Bind(h)
			defer release()
			_ = h.Serve(ctx)
		})
	}()
	return helper
}

// soakStand is the daemon plus the opener built the way the composition root
// builds it.
type soakStand struct {
	helper *soakHelper
	opener *localHelperOpener
}

func startSoakStand(t *testing.T) *soakStand {
	t.Helper()
	home := storagetest.IsolateWithHome(t)
	gen := fakeArtifacts{payload: syntheticPayload}.hash()
	spawner := newSoakSpawner()
	helper := startSoakHelper(t, endpoint.Dir(home), gen, spawner)

	logger := discardLogger(t)
	slogger := log.NewSlogAdapter(logger)
	reg := session.New(slogger, &reachPTYFactory{stub: pty.NewStub(slogger)})
	rc, err := ssh.NewReal(slogger, ssh.WithKnownHostsFile(home+"/known_hosts"))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	opener := &localHelperOpener{
		log:        logger,
		registry:   reg,
		dir:        endpoint.Dir(home),
		installed:  helperlocal.Installed{Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen)},
		sshTargets: rc,
		// The authenticated lifecycle kernel the composition root wires, so a
		// pane's spawn carries the launch a real pane carries and the second
		// window that goes with it.
		kernel: lifecyclepub.New(lifecycle.New(lifecycle.Options{})),
	}
	return &soakStand{helper: helper, opener: opener}
}

// inventory is what the helper says it currently holds, asked of the helper
// itself so the answer carries the same fields it releases by (the client DTO
// above this boundary keeps the window and drops the row buffer, and a soak
// that could not see the row buffer could not check the arithmetic that
// matters).
func (s *soakStand) inventory(t *testing.T) []proto.SessionEntry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	params, err := json.Marshal(proto.SessionsParams{})
	if err != nil {
		t.Fatalf("encoding the sessions request: %v", err)
	}
	raw, err := s.helper.svc.Call(ctx, proto.OpSessions, params)
	if err != nil {
		t.Fatalf("asking the helper what it holds: %v", err)
	}
	result, ok := raw.(proto.SessionsResult)
	if !ok {
		t.Fatalf("the helper answered the sessions request with %T, want proto.SessionsResult", raw)
	}
	return result.Sessions
}

// reserved is what the sessions a helper holds currently reserve, computed from
// the helper's own answer: each one's output window, its row buffer, and — when
// its spawn asked for the descriptor channel — the lifecycle window beside them.
func (s *soakStand) reserved(t *testing.T) int64 {
	t.Helper()
	return s.reservedFrom(t, s.inventory(t))
}

func (s *soakStand) reservedFrom(t *testing.T, entries []proto.SessionEntry) int64 {
	t.Helper()
	var want int64
	for _, e := range entries {
		bound := e.Launch.WindowBytes()
		want += bound + e.Launch.RowBufferBytes()
		if s.helper.spawner.askedForLifecycle(e.Session.Session) {
			want += bound
		}
	}
	return want
}

// check is the invariant, asserted after every step: the aggregate the helper
// has committed equals what the sessions it holds reserve, and it holds exactly
// `open` of them.
func (s *soakStand) check(t *testing.T, step string, open int) {
	t.Helper()
	entries := s.inventory(t)
	if len(entries) != open {
		t.Fatalf("%s: the helper holds %d sessions, want %d (inventory: %s)",
			step, len(entries), open, inventoryString(entries))
	}
	used := s.helper.svc.WindowBytesInUse()
	if want := s.reservedFrom(t, entries); used != want {
		t.Fatalf("%s: the helper has committed %d bytes but the %d session(s) it holds reserve %d.\n%s",
			step, used, len(entries), want, inventoryString(entries))
	}
	if used < 0 {
		t.Fatalf("%s: the helper has committed a negative amount, %d bytes", step, used)
	}
}

func inventoryString(entries []proto.SessionEntry) string {
	out := ""
	for _, e := range entries {
		out += fmt.Sprintf("\n  %s window=%d row=%d exit=%+v lifecycleWindow=%+v", e.Session.Session,
			e.Launch.WindowBytes(), e.Launch.RowBufferBytes(), e.Exit, e.LifecycleWindow)
	}
	if out == "" {
		out = "\n  (none)"
	}
	return out
}

// openLocal opens one local pane through the shipped opener and answers the
// session id the helper minted.
func (s *soakStand) openLocal(t *testing.T, pane string) session.ID {
	t.Helper()
	sid, err := s.tryOpenLocal(pane)
	if err != nil {
		t.Fatalf("open a local pane on this machine's helper: %v", err)
	}
	return sid
}

// tryOpenLocal is the same open, with the helper's answer kept: filling a
// helper to its aggregate is an open that FAILS, and what it failed with is the
// subject.
func (s *soakStand) tryOpenLocal(pane string) (session.ID, error) {
	return s.tryOpen(context.Background(), pane, false)
}

// tryOpen is one pane through the shipped opener, of either kind, with the
// helper's answer kept and the caller's context its own.
func (s *soakStand) tryOpen(ctx context.Context, pane string, remote bool) (session.ID, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opened, selected, err := s.opener.OpenHosted(ctx, s.configFor(pane, remote), "")
	if !selected {
		return "", fmt.Errorf("this machine's opener declined the pane")
	}
	if err != nil {
		return "", err
	}
	return opened.Session.ID(), nil
}

// openRemote opens one ssh pane through the same opener. The destination is
// resolved by the real resolver and the shell channel is the scripted one: what
// is under test is the reservation an ssh pane makes on THIS machine's helper.
func (s *soakStand) openRemote(t *testing.T, pane string) session.ID {
	t.Helper()
	sid, err := s.tryOpen(context.Background(), pane, true)
	if err != nil {
		t.Fatalf("open an ssh pane on this machine's helper: %v", err)
	}
	return sid
}

// remoteConfig is one ssh destination, resolved by the real resolver and
// carrying the credential a target is built from. The shell channel behind it
// is the scripted one; what is under test is the reservation this machine's
// helper makes for the pane.
func (s *soakStand) configFor(pane string, remote bool) session.Config {
	if remote {
		return s.remoteConfig(pane)
	}
	return session.Config{Kind: session.KindLocal, Cwd: "/", Cols: 80, Rows: 24, PaneID: pane}
}

func (s *soakStand) remoteConfig(pane string) session.Config {
	return session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			Secrets:            rememberedPassword{value: openPasswordFixturePassword},
			SecretID:           "sec:pane-soak:1",
			AuthorizedEndpoint: "host.example:22",
			DesiredMode:        "script",
			Shell:              "bash",
		},
		Cols: 80, Rows: 24, PaneID: pane,
	}
}

// closePane ends one pane's session through the verb a pane leaving the layout
// runs (internal/transport's closeSessionsForPanes calls exactly this).
func (s *soakStand) closePane(t *testing.T, sid session.ID) {
	t.Helper()
	if err := s.opener.registry.EndSession(sid); err != nil {
		t.Fatalf("closing pane %s: %v", sid, err)
	}
}

// TestACancelledOpenLeavesNothingReservedOnTheHelper is the same invariant at
// the one moment a pane can be born with nobody to own it: the shell is
// starting, the person withdraws the open (the Operations row's Cancel is
// exactly this — the renderer's rpc.cancel reaches the helper as a cancelled
// request, and closing the tab mid-connect is the same withdrawal one layer
// down), and the answer is therefore never going to be read by anyone.
//
// The shell still starts — a fork is not recallable — so the daemon has a
// session whose id reached no coordinator. Nobody can ever attach to it, no
// pane will ever close it, and the helper cannot wait for its shell to exit: an
// interactive shell at a prompt never does. Every such open would spend one
// pane's whole reservation — window, row buffer and lifecycle window — for the
// life of the daemon, which is how a helper gets to "536870912 of 536870912"
// after a long day. Both kinds of pane are the same walk here, and both must
// end the same way.
func TestACancelledOpenLeavesNothingReservedOnTheHelper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote bool
	}{
		{"local", false},
		{"ssh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stand := startSoakStand(t)
			gate := make(chan struct{})
			entered := make(chan struct{}, 1)
			stand.helper.spawner.mu.Lock()
			stand.helper.spawner.gate = gate
			stand.helper.spawner.entered = entered
			stand.helper.spawner.mu.Unlock()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type opened struct {
				selected bool
				err      error
			}
			result := make(chan opened, 1)
			go func() {
				_, selected, err := stand.opener.OpenHosted(ctx,
					stand.configFor("pane-cancelled", tc.remote), "")
				result <- opened{selected: selected, err: err}
			}()

			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("the daemon never started the pane's shell")
			}
			// The person gives up exactly while the shell is starting. The
			// withdrawal is written to the daemon before the client's call
			// returns (the cancel frame precedes the error), and the barrier
			// below is a request queued AFTER it on the same connection —
			// frames are read in order, so once its answer is here the daemon
			// has applied the cancel to the spawn it is still working on. Only
			// then is the shell allowed to finish starting, which is the order
			// the real product runs in: a request that takes its time, and a
			// person who stops waiting during it.
			cancel()
			got := <-result
			if got.err == nil {
				t.Fatalf("a withdrawn open reported success (selected=%v)", got.selected)
			}
			stand.barrier(t)
			close(gate)

			// The daemon finishes the spawn it was already running. What it
			// must NOT do is keep the result: nobody heard the session's id.
			assertNothingReserved(t, stand, "after a withdrawn open")
		})
	}
}

// TestAWithdrawalBeforeTheAnswerIsWrittenLeavesNothingReserved is the second of
// the review's findings (nocx-ouru5.1.5, review round 2): the withdrawal lands
// in the gap between the spawn's own work and the frame that carries its id, so
// the decision cannot be taken inside the handler — it is taken where the answer
// is written, and the session is released there.
//
// The gap is opened deliberately, from the daemon's side: the shell exists, the
// claim is resolved, and the answer is held. The caller is then withdrawn, the
// withdrawal is ordered against the wire, and only then is the answer allowed
// out. Nothing read the frame; nothing may keep the reservation.
func TestAWithdrawalBeforeTheAnswerIsWrittenLeavesNothingReserved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote bool
	}{
		{"local", false},
		{"ssh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stand := startSoakStand(t)
			holding, release := stand.helper.answers.holdNextSpawn()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type opened struct {
				selected bool
				err      error
			}
			result := make(chan opened, 1)
			go func() {
				_, selected, err := stand.opener.OpenHosted(ctx,
					stand.configFor("pane-withdrawn-answer", tc.remote), "")
				result <- opened{selected: selected, err: err}
			}()

			select {
			case <-holding:
			case <-time.After(30 * time.Second):
				t.Fatal("the daemon never finished making the shell")
			}
			// THE SESSION IS REAL AND ITS ANSWER IS NOT WRITTEN: this is the
			// interval, and it is a state the helper can be asked about.
			if used := stand.helper.svc.WindowBytesInUse(); used == 0 {
				t.Fatal("the held answer reserved nothing, so this test would prove nothing")
			}
			if entries := stand.inventory(t); len(entries) != 1 {
				t.Fatalf("the held answer holds %d session(s), want the one whose answer is being written", len(entries))
			}

			cancel()
			got := <-result
			if got.err == nil {
				t.Fatalf("a withdrawn open reported success (selected=%v)", got.selected)
			}
			// The withdrawal is on the wire before the answer is allowed out.
			stand.barrier(t)
			release()
			stand.helper.answers.awaitWithdrawal(t)

			assertNothingReserved(t, stand, "after a withdrawal before the answer was written")
		})
	}
}

// TestARetryHandedTheSessionKeepsItWhenTheFirstSpawnWithdraws is the first of
// the review's findings (nocx-ouru5.1.5, review round 2): a retry waiting on
// the same idempotency key is woken by the claim's resolution and handed the
// session — and the spawn that made that session, whose own answer then turns
// out to have reached nobody, must not end it. One owner, decided once.
func TestARetryHandedTheSessionKeepsItWhenTheFirstSpawnWithdraws(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote bool
	}{
		{"local", false},
		{"ssh", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stand := startSoakStand(t)
			holding, release := stand.helper.answers.holdNextSpawn()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const claim = "soak-shared-claim"
			type opened struct {
				id       session.ID
				selected bool
				err      error
			}
			first := make(chan opened, 1)
			go func() {
				openedSess, selected, err := stand.opener.OpenHosted(ctx,
					stand.configFor("pane-first-attempt", tc.remote), claim)
				var id session.ID
				if openedSess.Session != nil {
					id = openedSess.Session.ID()
				}
				first <- opened{id: id, selected: selected, err: err}
			}()

			select {
			case <-holding:
			case <-time.After(30 * time.Second):
				t.Fatal("the daemon never finished making the shell")
			}

			heldID := stand.inventory(t)[0].Session.Session

			// THE RETRY, with the same claim: the resolved claim hands it the
			// existing session, which it attaches to and adopts. This is the
			// shell the first attempt must not kill.
			retried, selected, err := stand.opener.OpenHosted(context.Background(),
				stand.configFor("pane-retry", tc.remote), claim)
			if err != nil || !selected {
				t.Fatalf("the retry waiting on the first spawn's claim: selected=%v, err=%v", selected, err)
			}
			retryID := retried.Session.ID()
			if forks := stand.helper.spawner.count(); forks != 1 {
				t.Fatalf("the retry forked a second shell (%d spawns); a resolved claim is an answer, "+
					"and this test is about the session it was handed", forks)
			}
			if string(retryID) != heldID {
				// The retry must be the SAME session the first attempt made.
				t.Fatalf("the retry was given %s, not the first attempt's session %s", retryID, heldID)
			}

			// Now the first attempt's caller gives up, and its answer is
			// written to nobody.
			cancel()
			got := <-first
			if got.err == nil {
				t.Fatalf("a withdrawn open reported success (selected=%v)", got.selected)
			}
			stand.barrier(t)
			release()
			stand.helper.answers.awaitWithdrawal(t)

			// THE RETRY'S SHELL IS STILL THERE, whole, and still somebody's.
			stand.check(t, "after the first attempt withdrew with its session handed to a retry", 1)
			entries := stand.inventory(t)
			if entries[0].Session.Session != string(retryID) {
				t.Fatalf("the helper holds %s, want the retry's session %s", entries[0].Session.Session, retryID)
			}
			if entries[0].Exit != nil {
				t.Fatal("the retry's shell exited when the first attempt withdrew")
			}
			stand.helper.spawner.emit(t, string(retryID), 4<<10)
			stand.check(t, "the retry's shell after the first attempt withdrew", 1)

			stand.closePane(t, retryID)
			stand.check(t, "the retry's pane closed", 0)
		})
	}
}

// barrier reads the daemon's inventory over the COORDINATOR'S OWN CONNECTION,
// which is what orders this test against the wire: the helper reads frames in
// order and dispatches each request as it arrives, so a request written after a
// withdrawal cannot be answered before the withdrawal has been applied.
func (s *soakStand) barrier(t *testing.T) {
	t.Helper()
	s.opener.mu.Lock()
	client := s.opener.client
	s.opener.mu.Unlock()
	if client == nil {
		t.Fatal("the opener holds no connection to this machine's helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.Sessions(ctx); err != nil {
		t.Fatalf("reading the helper's inventory over the coordinator's connection: %v", err)
	}
}

// assertNothingReserved waits for the helper to hold nothing at all, and
// reports what it does hold when the deadline passes.
func assertNothingReserved(t *testing.T, stand *soakStand, step string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		used := stand.helper.svc.WindowBytesInUse()
		entries := stand.inventory(t)
		if used == 0 && len(entries) == 0 {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%s: the helper still holds %d session(s) and %d reserved bytes with no pane that owns them, %s",
				step, len(entries), used, inventoryString(entries))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTheHelperWindowBudgetEqualsWhatItsOpenPanesReserve is the soak: panes are
// opened, long output is run through them, some are closed while others stay
// open, every tab is finally closed and the next ones opened again — local and
// ssh — and after every step the helper's committed aggregate must equal what
// its open sessions reserve. It then fills the helper to its own limit, reads
// the refusal the owner read, closes every tab, and proves the next pane starts.
func TestTheHelperWindowBudgetEqualsWhatItsOpenPanesReserve(t *testing.T) {
	stand := startSoakStand(t)
	const warmOpen = 8 // panes held at once; a 512 MiB helper admits 18 at the shipped 28 MiB

	open := map[session.ID]string{}
	var cumulative int64
	// openPane adds one pane of the named kind and counts what it reserved, so
	// the soak's own arithmetic says when it has passed the aggregate budget.
	openPane := func(t *testing.T, pane string, remote bool) session.ID {
		t.Helper()
		before := stand.helper.svc.WindowBytesInUse()
		var sid session.ID
		if remote {
			sid = stand.openRemote(t, pane)
		} else {
			sid = stand.openLocal(t, pane)
		}
		added := stand.helper.svc.WindowBytesInUse() - before
		if added <= 0 {
			t.Fatalf("%s reserved %d bytes; a pane that reserves nothing cannot exhaust a budget and cannot prove one is released", pane, added)
		}
		cumulative += added
		open[sid] = pane
		return sid
	}
	for cycle := 0; cycle < 12; cycle++ {
		for i := 0; i < warmOpen; i++ {
			pane := fmt.Sprintf("pane-%d-%d", cycle, i)
			sid := openPane(t, pane, i%2 == 1)
			// A screenful of long lines, the way a build log arrives — and once
			// per cycle a genuinely long burst, megabytes of it, through the
			// real emulator and the row bridge, because that is the work a
			// person's panes do while a budget has to stay honest.
			if i == 0 {
				stand.helper.spawner.emit(t, string(sid), 1<<20)
			} else {
				stand.helper.spawner.emit(t, string(sid), 24<<10)
			}
			stand.check(t, fmt.Sprintf("cycle %d, %s open", cycle, pane), len(open))
		}

		// Half of them go while the rest keep running — what a person's day
		// actually looks like.
		n := 0
		for sid := range open {
			if n == warmOpen/2 {
				break
			}
			stand.closePane(t, sid)
			delete(open, sid)
			n++
		}
		stand.check(t, fmt.Sprintf("cycle %d, half closed", cycle), len(open))

		// Every tab closed: nothing is open, so nothing may be reserved.
		for sid := range open {
			stand.closePane(t, sid)
			delete(open, sid)
		}
		stand.check(t, fmt.Sprintf("cycle %d, every tab closed", cycle), 0)
		if used := stand.helper.svc.WindowBytesInUse(); used != 0 {
			t.Fatalf("cycle %d: %d bytes are still reserved with no session open", cycle, used)
		}
	}
	if cumulative < 512<<20 {
		t.Fatalf("the soak reserved only %d bytes over its cycles; it has not passed the 512 MiB aggregate yet", cumulative)
	}

	// THE OWNER'S OWN SENTENCE, AND ITS RETRACTION. Panes are opened until this
	// helper refuses for its aggregate — the same refusal #689 carries — and
	// then every tab is closed and the next pane must start. That is the whole
	// of what a person asks of a budget: it can say no, and closing what you no
	// longer use buys it back.
	var refused error
	for i := 0; ; i++ {
		if i > 64 {
			t.Fatalf("the helper admitted %d panes without ever refusing; its aggregate is not a bound at all", i)
		}
		pane := fmt.Sprintf("pane-fill-%d", i)
		before := stand.helper.svc.WindowBytesInUse()
		sid, err := stand.tryOpenLocal(pane)
		if err != nil {
			refused = err
			break
		}
		cumulative += stand.helper.svc.WindowBytesInUse() - before
		open[sid] = pane
	}
	var refusal *helperclient.RefusalError
	if !errors.As(refused, &refusal) || refusal.Code != proto.ErrCodeWindowBudget {
		t.Fatalf("filling the helper ended in %v, want its window_budget refusal", refused)
	}
	if !strings.Contains(refusal.Message, "reserved for output windows and row buffers") {
		t.Fatalf("the refusal does not say what the budget is: %s", refusal.Message)
	}
	filled := stand.reserved(t)
	stand.check(t, "the helper filled to its aggregate", len(open))
	if filled < 400<<20 {
		t.Fatalf("the helper refused with only %d bytes reserved of its 512 MiB: the refusal is not the aggregate", filled)
	}

	for sid := range open {
		stand.closePane(t, sid)
	}
	open = map[session.ID]string{}
	stand.check(t, "every tab closed after filling the helper", 0)

	// AND THE NEXT PANE STARTS.
	sid := openPane(t, "pane-after-the-refusal", false)
	stand.check(t, "a pane opened after the budget was given back", len(open))
	stand.closePane(t, sid)
	delete(open, sid)
	stand.check(t, "the pane that proved the budget was given back, closed", 0)
}
