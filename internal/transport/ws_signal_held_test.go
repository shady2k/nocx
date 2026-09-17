package transport

// session.signal when the Stop arrives BEFORE the shell's start
// (nocx-zas0d): the gesture is accepted and held FOR THAT ATTEMPT, and the
// byte goes in the moment the shell authenticates the start — never before.
//
// WHY NOT JUST SEND IT. A signal written between the submit and the start is
// exactly the window upstream bash executes the SUFFIX of an accepted line in:
// measured today (nocx-xn63t.6.11/.6.12), SIGINT landing while bash's parser
// consumes a line resumes it at the index readline had reached, so a typed
// `echo RACE…` ran as `cho RACE…`. Holding the gesture and delivering it after
// the start is not politeness, it is the only ordering that is correct.
//
// WHY A RECORDING PTY. The whole claim here is about ONE BYTE and WHEN it was
// written, and no shell can be asked "how many interrupts did you receive"
// without answering in its own language — a trap firing twice is
// indistinguishable from a trap firing once in anything a shell prints. So the
// real local pty and the real session write queue are kept, and a recorder sits
// under them: the pty factory is already the transport's own seam (ws_test.go's
// real and stub factories), and every byte the session writes to a shell passes
// through it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// heldStopGrace is the cooperative bound the stand gives the ladder. Small on
// purpose: the two exits that wait for a program to cooperate (stop over a
// protected group, and the escalation between rungs) would otherwise put
// seconds of nothing into every case below, and nothing here is about how long
// the bound is.
const heldStopGrace = 100 * time.Millisecond

// ── the recording pty ─────────────────────────────────────────────────────

// ptyWriteRecorder spawns REAL local ptys and records every byte written to
// one. The session's write queue is between the caller and this Write, so what
// it counts is what reached the shell — in order, one job at a time (session's
// writeLoop) — and nothing else in this stand writes 0x03.
type ptyWriteRecorder struct {
	log   log.Logger
	mu    sync.Mutex
	bytes []byte
}

func (f *ptyWriteRecorder) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	p, err := pty.NewLocal(f.log, cfg)
	if err != nil {
		return nil, err
	}
	return &recordedPTY{Pty: p, recorder: f}, nil
}

func (f *ptyWriteRecorder) record(p []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes = append(f.bytes, p...)
}

// interrupts counts the terminal interrupt bytes written to any pty of this
// stand.
func (f *ptyWriteRecorder) interrupts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return bytes.Count(f.bytes, []byte{0x03})
}

type recordedPTY struct {
	pty.Pty
	recorder *ptyWriteRecorder
}

func (p *recordedPTY) Write(b []byte) (int, error) {
	p.recorder.record(b)
	return p.Pty.Write(b)
}

// ── the stand ─────────────────────────────────────────────────────────────

// heldStopStand is the nocx-7l4ex.10 protected-group stand (REAL local shells,
// REAL lifecycle publisher, its facts over the real socket) plus the recorder
// above. Job control is off in establish's own first command, which is what
// puts a running command inside the launcher shell's process group — the
// topology in which the lifecycle projection, and not the kernel's answer
// about the foreground group, is what a Stop has to rest on.
type heldStopStand struct {
	conn     *websocket.Conn
	tap      *socketTap
	ws       *WSServer
	pub      *lifecyclepub.Publisher
	recorder *ptyWriteRecorder

	sid    string
	lane   lifecycle.LaneID
	handle lifecycle.DomainHandle
	seq    uint64
}

func newHeldStopStand(t *testing.T) *heldStopStand {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	factory := &ptyWriteRecorder{log: logger}
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	ws := NewWSServer(logger, session.New(logger, factory),
		WithLifecyclePublisher(pub),
		WithRunLease(RunLeaseConfig{SignalGrace: heldStopGrace}))
	pub.SetEmitter(ws)
	// Owner: the test process; closing event: the Stop at the end of the test.
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &heldStopStand{conn: conn, tap: newSocketTap(conn), ws: ws, pub: pub, recorder: factory}
}

// establish opens a real session, takes job control off and brings a domain
// live on one lane.
func (s *heldStopStand) establish(t *testing.T) {
	t.Helper()
	s.sid = openSessionTapped(t, s.conn, s.tap)
	// Asked of the product, never slept for: a `set +m` that had not taken
	// effect yet would let the shell give the command its own group, and every
	// case below would silently test the ordinary ladder instead.
	submitCommand(t, s.conn, s.sid, "set +m; printf %s%s JOBCONTROL -OFF")
	tapDataFor(t, s.tap, s.sid, "JOBCONTROL-OFF", 20*time.Second)

	s.lane = lifecycle.LaneID("lane-held-stop")
	s.ws.RegisterLifecycleLane(s.lane, session.ID(s.sid))
	h, err := s.pub.RequestDomain(s.lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	s.handle = h
	s.seq = 1
	mustLifecycleIngest(t, s.pub, "T", lifecycleEnv(s.lane, h, s.seq, lifecycleHelloEvt()))
	tapAckEstablishment(t, s.tap, s.pub, s.lane, h)
}

// ingest delivers one authenticated envelope from the shell side, as the
// lifecycle adapter would.
func (s *heldStopStand) ingest(t *testing.T, evt lifecycle.Event) {
	t.Helper()
	s.seq++
	mustLifecycleIngest(t, s.pub, "T", lifecycleEnv(s.lane, s.handle, s.seq, evt))
}

// submit opens the app-owned attempt exactly as the renderer does: the RPC
// lands BEFORE the bytes that could cause the shell's own start (ADR-0024 §5),
// which is the whole reason a Stop can arrive over an attempt nothing has
// started.
func (s *heldStopStand) submit(t *testing.T, id int, command string) lifecycleSubmitAttemptResult {
	t.Helper()
	res := decodeSubmitAttemptResult(t, tapCall(t, s.conn, s.tap, id, "lifecycle.submitAttempt", map[string]string{
		"domain": string(s.handle.Domain), "command": command, "cwd": "/tmp", "host": "", "source": "user",
	}))
	if res.ID == "" {
		t.Fatal("lifecycle.submitAttempt opened no attempt")
	}
	return res
}

// stop drives one Stop through the real socket and returns the outcome the
// renderer would read.
func (s *heldStopStand) stop(t *testing.T, id int) string {
	t.Helper()
	raw := tapCall(t, s.conn, s.tap, id, "session.signal",
		map[string]any{"sessionId": s.sid, "signal": "stop"})
	var env struct {
		Result signalWireResult `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal session.signal: %v\nraw: %s", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("session.signal: %+v", env.Error)
	}
	return env.Result.Outcome
}

// shellIDFor mints the id the real integration names in its start envelope
// (nocx.bash: `s-<dom>-<n>`). The shell never learns the app-minted id
// (protocol §8), so its own is the only name it can report, and a start that
// names one is what production sends.
func (s *heldStopStand) shellIDFor(n int) lifecycle.AttemptID {
	return lifecycle.AttemptID(fmt.Sprintf("s-%s-%d", s.handle.Domain, n))
}

// ── 1. the bead's sentence ────────────────────────────────────────────────

// TestSessionSignal_StopBeforeTheStartIsHeldUntilItStarts: a Stop pressed in
// the window between the submit and the running fact is ACCEPTED, and the
// interrupt goes in the moment the shell says it started — one byte, then, and
// none before.
func TestSessionSignal_StopBeforeTheStartIsHeldUntilItStarts(t *testing.T) {
	s := newHeldStopStand(t)
	s.establish(t)
	s.submit(t, 5, "sleep 30")

	// The bytes reach the pty here, as the renderer writes them a round trip
	// after its submit — the shell is about to parse the line and nothing has
	// authenticated a start yet.
	submitCommand(t, s.conn, s.sid, "sleep 30")

	raw := tapCall(t, s.conn, s.tap, 6, "session.signal",
		map[string]any{"sessionId": s.sid, "signal": "stop"})
	validateJSON(t, loadSchema(t, "session.signal.schema.json"), resultOf(t, raw),
		"session.signal held, over the wire")
	var env struct {
		Result signalWireResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal session.signal: %v", err)
	}
	if env.Result.Outcome != string(foregroundHeld) {
		t.Fatalf("a Stop before the start answered %q, want %q", env.Result.Outcome, foregroundHeld)
	}
	// NOTHING was written. A byte here lands while bash is still consuming the
	// line, which is the suffix-execution window this is held to avoid.
	if got := s.recorder.interrupts(); got != 0 {
		t.Fatalf("interrupts written before the start = %d, want 0", got)
	}

	// The shell authenticates the start, naming the attempt in its own
	// namespace.
	s.ingest(t, lifecycleStartEvt(new(s.shellIDFor(1)), "sleep 30"))

	waittest.WaitForDetail(t, "the held Stop's interrupt to reach the pty",
		func() string {
			return fmt.Sprintf("interrupts=%d (want 1)", s.recorder.interrupts())
		},
		func() bool { return s.recorder.interrupts() == 1 })

	// AND EXACTLY ONE. This is a NEGATIVE bound, not a synchronisation point:
	// a second interrupt can only come from a second delivery, and the window
	// is here to give one the chance to arrive before the count is read.
	time.Sleep(200 * time.Millisecond)
	if got := s.recorder.interrupts(); got != 1 {
		t.Fatalf("interrupts delivered for one held Stop = %d, want exactly 1", got)
	}

	// The interrupted `sleep 30` is gone: the shell has the terminal back and
	// answers a later line, which it cannot do while a foreground job holds it.
	submitCommand(t, s.conn, s.sid, "printf %s%s HELD -STOPPED")
	tapDataFor(t, s.tap, s.sid, "HELD-STOPPED", 20*time.Second)
}

// ── 2. the discard ────────────────────────────────────────────────────────

// TestSessionSignal_AHeldStopIsDiscardedWhenTheAttemptNeverStarts: the line
// was discarded before bash's DEBUG trap ever fired (case 2 of
// nocx-xn63t.6.1, reproduced 40/40), so no start is coming. The held Stop is
// dropped — and, the part a person can be hurt by, it can never reach a LATER
// command.
func TestSessionSignal_AHeldStopIsDiscardedWhenTheAttemptNeverStarts(t *testing.T) {
	s := newHeldStopStand(t)
	s.establish(t)
	s.submit(t, 5, "sleep 30")
	if got := s.stop(t, 6); got != string(foregroundHeld) {
		t.Fatalf("a Stop before the start answered %q, want %q", got, foregroundHeld)
	}

	// A prompt_ready arriving over an open, unstarted attempt is the
	// PROMPT_COMMAND/DEBUG race; the SECOND one with no start between them is
	// the shell reporting, twice, that nothing is running.
	s.ingest(t, lifecyclePromptEvt())
	s.ingest(t, lifecyclePromptEvt())

	// A fresh command, and the start the shell really sends for it.
	s.submit(t, 7, "printf %s%s NEXT -OK")
	s.ingest(t, lifecycleStartEvt(new(s.shellIDFor(2)), "printf NEXT-OK"))

	// The discarded Stop must not have reached it: the byte count would be 1
	// if the hold had survived the closure it belonged to.
	time.Sleep(200 * time.Millisecond)
	if got := s.recorder.interrupts(); got != 0 {
		t.Fatalf("a Stop held for a discarded attempt reached a later command: %d interrupts", got)
	}
}

// TestHeldStop_AnAttemptThatLeavesOpenDropsTheHold is the bookkeeping half of
// the discard, asserted where it lives: the obligation is gone, not merely
// unable to fire. Without it a Stop accepted in the window would sit in the
// transport's registry for the life of the session.
func TestHeldStop_AnAttemptThatLeavesOpenDropsTheHold(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	const attempt = lifecycle.AttemptID("att-closed")
	// Armed through the same locked step StopTarget uses, because that is the
	// only way the obligation is ever recorded: the read that decided it and
	// the record must not be separable.
	ws.heldMu.Lock()
	ws.armHeldStopLocked(attempt, session.ID("sid-held"))
	ws.heldMu.Unlock()
	if _, ok := ws.claimHeldStop(attempt); !ok {
		t.Fatal("the hold was not recorded")
	}
	ws.heldMu.Lock()
	ws.armHeldStopLocked(attempt, session.ID("sid-held"))
	ws.heldMu.Unlock()
	ws.PublishAttemptClosed(attempt)
	if _, ok := ws.claimHeldStop(attempt); ok {
		t.Fatal("an attempt that left open left its held Stop behind")
	}
}

// ── the obligation is DROPPED on every terminal path ──────────────────────
//
// The discard is bookkeeping, and bookkeeping is exactly what a byte count
// cannot see: "nothing was written" is true of a hold that was dropped, of one
// that is still waiting, and of one nobody ever armed. So each path that can
// end an attempt without its start asserts the OBLIGATION ITSELF is gone —
// through the same take the delivery uses — and that the path really did close
// the attempt, so the assertion cannot pass on a no-op.
//
// No pty here on purpose: nothing in this group writes a byte, and a stub
// registry keeps the seven paths one screen of code. The byte's own half lives
// in the recording-pty cases above.

// heldArm is one armed obligation plus everything needed to assert about it.
type heldArm struct {
	stand   *armedLoopStand
	attempt lifecycle.AttemptID
}

// armedLoopStand brings up the light stand: a stub registry, the REAL
// publisher, a real connection, and one lane with a live domain and nothing
// running — an app attempt is opened the way the renderer opens one, and the
// hold is armed through the same read the request path uses.
type armedLoopStand struct {
	ws     *WSServer
	pub    *lifecyclepub.Publisher
	conn   *websocket.Conn
	sid    session.ID
	lane   lifecycle.LaneID
	handle lifecycle.DomainHandle
	seq    uint64
}

func newArmedLoopStand(t *testing.T) *armedLoopStand {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	ws := NewWSServer(logger, newRegWithStub(logger), WithLifecyclePublisher(pub))
	pub.SetEmitter(ws)
	// Owner: the test process; closing event: the Stop at the end of the test.
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	s := &armedLoopStand{ws: ws, pub: pub, conn: conn, sid: session.ID(openSessionOnConn(t, ws, conn, 1))}
	s.lane = lifecycle.LaneID("lane-armed-loop")
	ws.RegisterLifecycleLane(s.lane, s.sid)
	h, err := pub.RequestDomain(s.lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	s.handle = h
	s.seq = 1
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(s.lane, h, s.seq, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, s.lane, h, conn)
	return s
}

func (s *armedLoopStand) ingest(t *testing.T, evt lifecycle.Event) {
	t.Helper()
	s.seq++
	mustLifecycleIngest(t, s.pub, "T", lifecycleEnv(s.lane, s.handle, s.seq, evt))
}

// arm opens an app attempt and arms the hold for it through the real read: the
// request path's StopTarget, which is the only thing that ever arms one.
func (s *armedLoopStand) arm(t *testing.T) heldArm {
	t.Helper()
	res := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, s.conn, "lifecycle.submitAttempt", map[string]string{
		"domain": string(s.handle.Domain), "command": "sleep 30", "cwd": "/tmp", "host": "", "source": "user",
	}, 40))
	fb := sessionProtectedForeground{ctx: t.Context(), s: s.ws, sid: s.sid, mayHold: true}
	target, ok := fb.StopTarget()
	if !ok || target.Started || string(target.Attempt) != res.ID {
		t.Fatalf("StopTarget = (%q, started=%v, ok=%v), want the open unstarted attempt %q",
			target.Attempt, target.Started, ok, res.ID)
	}
	if !s.ws.heldStopArmed(lifecycle.AttemptID(res.ID)) {
		t.Fatal("the read that answered the Stop did not arm the hold for it")
	}
	return heldArm{stand: s, attempt: lifecycle.AttemptID(res.ID)}
}

// assertDropped is the assertion this whole group exists for, for the paths
// that end the attempt IN THE KERNEL: the attempt really did leave `open` (so
// the case cannot pass vacuously) and the obligation is gone — through the same
// take the delivery itself uses.
func (a heldArm) assertDropped(t *testing.T, path string) {
	t.Helper()
	if current, ok := a.stand.pub.Attempt(a.attempt); ok && current.State == lifecycle.AttemptOpen {
		t.Fatalf("%s did not close the attempt — this case would pass vacuously", path)
	}
	a.assertHoldGone(t, path)
}

// assertHoldGone is the half every path must satisfy. A session ending does not
// reach into the kernel — the transport's teardown is what ends it — so the
// attempt may still read open there while the only thing that could ever
// discharge the obligation has gone.
func (a heldArm) assertHoldGone(t *testing.T, path string) {
	t.Helper()
	if _, ok := a.stand.ws.claimHeldStop(a.attempt); ok {
		t.Fatalf("the held Stop survived %s", path)
	}
}

func TestHeldStop_EveryTerminalPathWithoutAStartDropsTheHold(t *testing.T) {
	for _, tc := range []struct {
		name string
		// drive runs the one mutation that ends the attempt without a start.
		drive func(t *testing.T, s *armedLoopStand, arm heldArm)
		// holdOnly marks the one path that does not close the attempt in the
		// kernel (see assertHoldGone).
		holdOnly bool
	}{
		{
			// The prompt_ready with no start: the line was discarded before the
			// DEBUG trap fired, and the shell reports its prompt a second time.
			name: "a second prompt_ready with no start between them",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				s.ingest(t, lifecyclePromptEvt())
				s.ingest(t, lifecyclePromptEvt())
			},
		},
		{
			name: "the domain closes under the open attempt",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				s.ingest(t, lifecycle.Event{Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{}})
			},
		},
		{
			// Framing corruption past the lane's budgets revokes it, and a
			// revoked domain's open attempts go unknown.
			name: "the lane's desync budget revokes it",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				// Two reports: the first opens the desync episode and records
				// the garbage, the second accumulates it and is the one the
				// budget is checked on (kernel.go's notifyGapLocked).
				for i := 0; i < 2; i++ {
					if err := s.pub.NotifyGap("T", s.handle.Domain, 1<<20, 1<<20); err != nil {
						t.Fatalf("NotifyGap #%d: %v", i+1, err)
					}
				}
			},
		},
		{
			// A fresh submit closes a PRIMED attempt (the shell reached a
			// prompt twice with nothing attaching): the publisher's own
			// SubmitAttempt is the second mutation that can do it.
			name: "a fresh submit closes the primed attempt",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				s.ingest(t, lifecyclePromptEvt())
				if _, err := s.pub.SubmitAttempt(s.handle.Domain, "echo next", "/tmp", "", ""); err != nil {
					t.Fatalf("SubmitAttempt: %v", err)
				}
			},
		},
		{
			name: "the attempt is explicitly abandoned",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				if err := s.pub.AbandonAttempt(arm.attempt); err != nil {
					t.Fatalf("AbandonAttempt: %v", err)
				}
			},
		},
		{
			name: "the transport is lost",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				if err := s.pub.TransportLost("T"); err != nil {
					t.Fatalf("TransportLost: %v", err)
				}
			},
		},
		{
			name: "the session ends",
			drive: func(t *testing.T, s *armedLoopStand, arm heldArm) {
				jsonrpcCallWithID(t, s.conn, "close", map[string]string{"sessionId": string(s.sid)}, 41)
			},
			// The kernel is not told: the session's teardown is the transport's
			// own, and the obligation goes with it.
			holdOnly: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stand := newArmedLoopStand(t)
			arm := stand.arm(t)
			tc.drive(t, stand, arm)
			if tc.holdOnly {
				arm.assertHoldGone(t, tc.name)
				return
			}
			arm.assertDropped(t, tc.name)
		})
	}
}

// ── 3. two Stops, one interrupt ───────────────────────────────────────────

// TestSessionSignal_ASecondStopBeforeTheStartIsTheSameObligation: pressing
// Stop twice in the window does not queue two interrupts. Both gestures are
// accepted — a refusal would be a lie about a Stop that is already armed — and
// one byte goes in at the start.
func TestSessionSignal_ASecondStopBeforeTheStartIsTheSameObligation(t *testing.T) {
	s := newHeldStopStand(t)
	s.establish(t)
	s.submit(t, 5, "sleep 30")

	for _, id := range []int{6, 7} {
		if got := s.stop(t, id); got != string(foregroundHeld) {
			t.Fatalf("Stop #%d before the start answered %q, want %q", id-5, got, foregroundHeld)
		}
	}
	if got := s.recorder.interrupts(); got != 0 {
		t.Fatalf("interrupts written before the start = %d, want 0", got)
	}

	s.ingest(t, lifecycleStartEvt(new(s.shellIDFor(1)), "sleep 30"))
	waittest.WaitFor(t, "the one interrupt the two Stops share",
		func() bool { return s.recorder.interrupts() == 1 })
	time.Sleep(200 * time.Millisecond)
	if got := s.recorder.interrupts(); got != 1 {
		t.Fatalf("two Stops before the start produced %d interrupts, want exactly 1", got)
	}
}

// ── 4. after the start, nothing changed ───────────────────────────────────

// TestSessionSignal_StopAfterTheStartIsStillDeliveredImmediately: once the
// shell has authenticated the start, a Stop is the ordinary one — the byte
// goes in on the request's own lane, with no waiting on anything else.
func TestSessionSignal_StopAfterTheStartIsStillDeliveredImmediately(t *testing.T) {
	s := newHeldStopStand(t)
	s.establish(t)
	attempt := s.submit(t, 5, "sleep 30")
	s.ingest(t, lifecycleStartEvt(new(s.shellIDFor(1)), "sleep 30"))

	// Sent without waiting for its own answer: the byte is written inside the
	// call, and the completion that closes Stop's promise is delivered after.
	sendControl(t, s.conn, "session.signal", map[string]any{"sessionId": s.sid, "signal": "stop"}, 9)
	waittest.WaitFor(t, "the stop's interrupt to reach the pty",
		func() bool { return s.recorder.interrupts() == 1 })
	// The completion names the APP's id, which is the attempt's identity: the
	// shell's own id (named in its start above) is an alias the kernel keeps
	// internally and never resolves a completion by.
	s.ingest(t, lifecycleCompleteEvt(lifecycle.AttemptID(attempt.ID), 130, lifecycleFence(0x0C)))

	raw := tapWaitForID(t, s.tap, 9)
	var env struct {
		Result signalWireResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal session.signal: %v\nraw: %s", err, raw)
	}
	if env.Result.Outcome != string(foregroundDelivered) {
		t.Fatalf("a Stop after the start answered %q, want %q", env.Result.Outcome, foregroundDelivered)
	}
	if got := s.recorder.interrupts(); got != 1 {
		t.Fatalf("interrupts for one Stop after the start = %d, want exactly 1", got)
	}
}

// tapWaitForID waits for the response with this JSON-RPC id on the tap. The
// tap owns the reader, so a request whose answer arrives after the test has
// acted cannot go through tapCall.
func tapWaitForID(t *testing.T, tap *socketTap, id int) json.RawMessage {
	t.Helper()
	want := fmt.Sprintf("%d", id)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-tap.msgs:
			if !ok {
				t.Fatalf("socket closed before the response to id %d%s", id, socketClosedWhy)
			}
			var env struct {
				ID *json.RawMessage `json:"id"`
			}
			if json.Unmarshal(msg, &env) != nil || env.ID == nil || string(*env.ID) != want {
				continue
			}
			return msg
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("no response to id %d", id)
	return nil
}
