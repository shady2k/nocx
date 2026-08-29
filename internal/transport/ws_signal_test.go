package transport

// session.signal over a REAL local pty (nocx-23rph): a person's UI can
// address a signal to the command running in a session, without owning the
// keyboard.
//
// WHY A REAL PTY AND NOT A STUB. The whole method is one question —
// "does the signal reach the foreground process group?" — and a stub pty
// has no process group at all, so a stubbed test could only ever observe
// the refusal. Both halves of the contract's enum need a real shell: at a
// prompt the foreground group is the shell's own and there is nothing to
// signal (nothing-running), and with a job in front of it there is
// (delivered). The pair is what makes either one evidence.
//
// NOTHING HERE WAITS OUT A DURATION. The synchronisation point is always an
// observable, and each one is chosen for what it PROVES rather than for when
// it happens: the job's readiness marker says the process the signal will
// reach is already exec'd and in the foreground (see the interrupt test —
// a marker printed by an intermediate shell proves strictly less than that,
// and the difference is nocx-sf4kx), and the shell's own next output says
// the job is gone.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/unix"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
)

// signalWireResult is session.signal's answer as the renderer reads it: the
// contract's two fields and nothing else.
type signalWireResult struct {
	Signal  string `json:"signal"`
	Outcome string `json:"outcome"`
}

// signalServer is a server over REAL local shells with a socket tap, which
// is what lets one test both submit a command on the data plane and call a
// control method (gorilla allows exactly one concurrent reader).
func signalServer(t *testing.T) (*websocket.Conn, *socketTap) {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithReal(log.NewSlogAdapter(nil)))
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(t.Context()) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, newSocketTap(conn)
}

// openSessionTapped opens a real local session on a tapped socket. openSession
// cannot be used here: it reads the response itself, and the tap owns the
// reader.
func openSessionTapped(t *testing.T, conn *websocket.Conn, tap *socketTap) string {
	t.Helper()
	raw := tapCall(t, conn, tap, 1, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	})
	var env struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal open: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("open: %+v", env.Error)
	}
	if env.Result.SessionID == "" {
		t.Fatal("open returned an empty sessionId")
	}
	return env.Result.SessionID
}

// tapSignal drives one session.signal on a tapped socket and fails on a
// refusal — the tests that expect one read the envelope themselves.
func tapSignal(t *testing.T, conn *websocket.Conn, tap *socketTap, id int, params map[string]any) signalWireResult {
	t.Helper()
	raw := tapCall(t, conn, tap, id, "session.signal", params)
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
	return env.Result
}

// TestSessionSignal_InterruptReachesTheRunningCommand is the bead's sentence
// in one test: a command is running, nobody's keyboard is involved, and the
// signal stops it.
func TestSessionSignal_InterruptReachesTheRunningCommand(t *testing.T) {
	conn, tap := signalServer(t)
	sid := openSessionTapped(t, conn, tap)

	// THE MARKER IS NOT SPELLED IN THE COMMAND LINE AT ALL. It is the
	// content of a file, and the job is a `tail -f` that echoes it back —
	// which is what makes observing it mean the one thing this test needs
	// it to mean: the process the signal is about to reach already exists.
	//
	// `sh -c 'printf %s%s FOREGROUND -READY; sleep 300'` could not say that
	// (nocx-sf4kx). Its marker is printed by the INTERMEDIATE SHELL, before
	// that shell has forked the command the signal is meant to kill, and a
	// SIGINT arriving in the window between the printf and the fork reaches
	// a process group whose only member is that shell. dash catches SIGINT
	// and drops it there, then forks `sleep 300` and waits on it forever:
	// kill(2) succeeded, session.signal honestly answered "delivered", and
	// nothing died. Measured on a loaded machine — 2 runs in 120 at loadavg
	// 49, /proc showing dash and sleep both alive, no signal pending, and
	// the pty's foreground group still the job's.
	//
	// `tail -f` closes that window by construction rather than by timing:
	// the marker cannot reach the data plane until the process bash placed
	// in the foreground group has completed its execve, opened the file and
	// written it. When the wait below returns, that process exists, is the
	// only member of the group, and carries tail's own default disposition
	// for SIGINT — there is nothing left that could swallow it, at any
	// speed. It also cannot be satisfied by the terminal's ECHO of the
	// command line, and for a stronger reason than the two-piece printf
	// gave: the line names a path, and the marker is never in it.
	dir := t.TempDir()
	marker := filepath.Join(dir, "foreground-ready")
	if err := os.WriteFile(marker, []byte("FOREGROUND-READY\n"), 0o600); err != nil {
		t.Fatalf("write the readiness marker: %v", err)
	}
	submitCommand(t, conn, sid, "tail -f '"+marker+"'")
	tapDataFor(t, tap, sid, "FOREGROUND-READY", 20*time.Second)

	got := tapSignal(t, conn, tap, 2, map[string]any{"sessionId": sid, "signal": "interrupt"})
	if got.Signal != "interrupt" || got.Outcome != "delivered" {
		t.Fatalf("session.signal = %+v, want interrupt/delivered", got)
	}

	// And the execution is actually gone: the shell prints a fresh prompt
	// only once it has the terminal back. Asked of the product, not of the
	// clock — the marker below is echoed by the shell reading a line again.
	submitCommand(t, conn, sid, "printf %s%s INTERRUPTED -OK")
	tapDataFor(t, tap, sid, "INTERRUPTED-OK", 20*time.Second)
}

// TestSessionSignal_AtAPromptIsRefusedHonestly: the refusal a person can
// act on. The shell's own process group is the foreground group at a
// prompt, and the shell must never be signalled here — it is not part of
// the job it is waiting on.
func TestSessionSignal_AtAPromptIsRefusedHonestly(t *testing.T) {
	conn, tap := signalServer(t)
	sid := openSessionTapped(t, conn, tap)
	// The session is at a prompt when the shell has echoed something back;
	// asked of the product rather than slept for.
	submitCommand(t, conn, sid, "printf %s%s PROMPT -READY")
	tapDataFor(t, tap, sid, "PROMPT-READY", 20*time.Second)

	for _, signal := range []string{"interrupt", "stop"} {
		got := tapSignal(t, conn, tap, 3, map[string]any{"sessionId": sid, "signal": signal})
		if got.Outcome != "nothing-running" {
			t.Errorf("session.signal(%s) at a prompt = %q, want nothing-running", signal, got.Outcome)
		}
		if got.Signal != signal {
			t.Errorf("session.signal(%s) echoed %q", signal, got.Signal)
		}
	}
}

// TestSessionSignal_StopEscalatesUntilTheExecutionIsGone: Stop promises the
// execution is gone when it answers, and it keeps that promise against a
// command that IGNORES the first two rungs. That is the whole reason it
// goes through the run lease's existing ladder rather than sending one
// signal and hoping.
func TestSessionSignal_StopEscalatesUntilTheExecutionIsGone(t *testing.T) {
	conn, tap := signalServer(t)
	sid := openSessionTapped(t, conn, tap)

	// INT and TERM are trapped and ignored; only KILL can end it. The marker
	// is printed after the traps are installed, so observing it means the
	// traps are in place.
	submitCommand(t, conn, sid,
		"sh -c 'trap \"\" INT TERM; printf %s%s STUBBORN -READY; while true; do sleep 1; done'")
	tapDataFor(t, tap, sid, "STUBBORN-READY", 20*time.Second)

	got := tapSignal(t, conn, tap, 4, map[string]any{"sessionId": sid, "signal": "stop"})
	if got.Signal != "stop" || got.Outcome != "delivered" {
		t.Fatalf("session.signal = %+v, want stop/delivered", got)
	}

	// The shell has the terminal back — which it cannot have while the job
	// is still in the foreground.
	submitCommand(t, conn, sid, "printf %s%s STOPPED -OK")
	tapDataFor(t, tap, sid, "STOPPED-OK", 30*time.Second)
}

// TestSessionSignal_RefusesParamsItCannotHonour: the params are validated
// before the handler runs, so a bad signal name or a session this
// connection does not hold never reaches a process group.
// ── the protected group: one policy, two mechanisms (nocx-7l4ex.10/.11) ──

// fakeForegroundSession is a session whose kernel answer this test chooses.
// It exists because the three answers SignalForeground can now give are
// exactly what selects the mechanism, and only one of them may reach the
// terminal-interrupt fallback.
type fakeForegroundSession struct {
	err   error
	calls []syscall.Signal
}

func (f *fakeForegroundSession) SignalForeground(sig syscall.Signal) error {
	f.calls = append(f.calls, sig)
	return f.err
}

// fakeProtected is the lifecycle half of the fallback, so the policy can be
// driven through every branch without a pty or a shell.
type fakeProtected struct {
	attempt   lifecycle.AttemptID
	accept    bool // does the input queue take the byte
	ends      bool // does the exact attempt leave open within the bound
	writes    int
	asked     int
	waitedFor lifecycle.AttemptID
}

func (f *fakeProtected) Attempt() (lifecycle.AttemptID, bool) {
	f.asked++
	return f.attempt, f.attempt != ""
}

func (f *fakeProtected) Interrupt(lifecycle.AttemptID) bool {
	if !f.accept {
		return false
	}
	f.writes++
	return true
}

func (f *fakeProtected) Ended(a lifecycle.AttemptID, _ time.Duration) bool {
	f.waitedFor = a
	return f.ends
}

// TestForegroundSignal_TheKernelAnswerChoosesTheMechanism is the policy in
// one table (nocx-7l4ex.10).
//
// The mechanism is never chosen by a mode flag or by which caller asked: it
// is read off what SignalForeground established, and the three answers it can
// give are three on purpose. The rows that matter most are the last two — a
// kill that FAILED is no evidence about topology, so it may neither claim
// delivery nor reach for the terminal, and a protected group with nothing
// started in it is an ordinary prompt.
func TestForegroundSignal_TheKernelAnswerChoosesTheMechanism(t *testing.T) {
	const attempt = lifecycle.AttemptID("att-protected")
	for _, tc := range []struct {
		name      string
		err       error
		fb        *fakeProtected
		intent    string
		want      foregroundOutcome
		wantSigs  int
		wantBytes int
	}{
		{
			name: "an independent group takes the signal itself",
			err:  nil, fb: &fakeProtected{attempt: attempt, accept: true},
			intent: signalInterrupt, want: foregroundDelivered, wantSigs: 1, wantBytes: 0,
		},
		{
			name: "a protected group holding a started execution takes the terminal interrupt",
			err:  pty.ErrProtectedForeground, fb: &fakeProtected{attempt: attempt, accept: true},
			intent: signalInterrupt, want: foregroundDelivered, wantSigs: 1, wantBytes: 1,
		},
		{
			name: "a protected group with nothing started is the ordinary prompt",
			err:  pty.ErrProtectedForeground, fb: &fakeProtected{accept: true},
			intent: signalInterrupt, want: foregroundNothingRunning, wantSigs: 1, wantBytes: 0,
		},
		{
			name: "a refused input queue is not a delivery",
			err:  pty.ErrProtectedForeground, fb: &fakeProtected{attempt: attempt, accept: false},
			intent: signalInterrupt, want: foregroundUnreconciled, wantSigs: 1, wantBytes: 0,
		},
		{
			name: "no group left to signal",
			err:  pty.ErrNoForeground, fb: &fakeProtected{attempt: attempt, accept: true},
			intent: signalInterrupt, want: foregroundNothingRunning, wantSigs: 1, wantBytes: 0,
		},
		{
			// The row the old collapse got wrong: EPERM is not the guard,
			// and reading it as one wrote a byte into a session on the
			// strength of a failure that said nothing about what is running.
			name:   "a kill that failed neither claims delivery nor reaches for the terminal",
			err:    fmt.Errorf("pty: signal foreground group 42: %w", unix.EPERM),
			fb:     &fakeProtected{attempt: attempt, accept: true},
			intent: signalInterrupt, want: foregroundUnreconciled, wantSigs: 1, wantBytes: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeForegroundSession{err: tc.err}
			got := interruptForeground(log.NewSlogAdapter(nil), "sid", sess, tc.fb)
			if got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
			if len(sess.calls) != tc.wantSigs {
				t.Errorf("kill(2) calls = %d, want %d", len(sess.calls), tc.wantSigs)
			}
			if tc.fb.writes != tc.wantBytes {
				t.Errorf("terminal interrupts written = %d, want %d", tc.fb.writes, tc.wantBytes)
			}
		})
	}
}

// TestForegroundSignal_StopOverAProtectedGroupWaitsForTheExactAttempt: Stop
// cannot escalate into the launcher shell's group, so the promise it keeps
// there is a different one and it must not pretend otherwise. It waits for
// THAT attempt — not for a foreground group, which over a protected group is
// the shell's whether the program lives or dies — and says so when it cannot.
func TestForegroundSignal_StopOverAProtectedGroupWaitsForTheExactAttempt(t *testing.T) {
	const attempt = lifecycle.AttemptID("att-protected")
	for _, tc := range []struct {
		name string
		ends bool
		want foregroundOutcome
	}{
		{name: "the attempt closes, so the execution is gone", ends: true, want: foregroundDelivered},
		{name: "the attempt stays open, so nothing may claim it stopped", ends: false, want: foregroundUnreconciled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeForegroundSession{err: pty.ErrProtectedForeground}
			fb := &fakeProtected{attempt: attempt, accept: true, ends: tc.ends}
			got := stopForeground(log.NewSlogAdapter(nil), "sid", sess, 10*time.Millisecond, fb)
			if got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
			// Exactly one rung, and it never became TERM or KILL: the shell
			// is in that group.
			if len(sess.calls) != 1 || sess.calls[0] != syscall.SIGINT {
				t.Fatalf("signals sent = %v, want one SIGINT and no escalation", sess.calls)
			}
			if fb.waitedFor != attempt {
				t.Fatalf("waited for attempt %q, want the exact one that was named (%q)", fb.waitedFor, attempt)
			}
		})
	}
}

// TestForegroundSignal_NoLifecycleIsTheHonestRefusal: the run lease passes no
// fallback at all, because it holds no authenticated attempt. A nil fallback
// must be the prompt's answer, never a panic and never a byte written on a
// guess.
func TestForegroundSignal_NoLifecycleIsTheHonestRefusal(t *testing.T) {
	sess := &fakeForegroundSession{err: pty.ErrProtectedForeground}
	if got := interruptForeground(log.NewSlogAdapter(nil), "sid", sess, nil); got != foregroundNothingRunning {
		t.Fatalf("interrupt with no fallback = %q, want nothing-running", got)
	}
	if got := stopForeground(log.NewSlogAdapter(nil), "sid", sess, time.Millisecond, nil); got != foregroundNothingRunning {
		t.Fatalf("stop with no fallback = %q, want nothing-running", got)
	}
}

// TestSessionSignal_ProtectedAttemptResolutionRefusesWhatItCannotName covers
// the three refusals of sessionProtectedForeground.Attempt (nocx-7l4ex.10).
//
// The projection is the ONLY thing that can say a program is inside a
// protected group, so what it will not say is as load-bearing as what it
// will. A submitted-but-unstarted attempt is the important one: the app opens
// an attempt BEFORE the bytes that could cause it are written
// (lifecycle-protocol §7), so reading "open" as "running" would put 0x03 into
// a prompt whenever a submit stalled.
func TestSessionSignal_ProtectedAttemptResolutionRefusesWhatItCannotName(t *testing.T) {
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := session.ID(e.openSession(t, 1))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}

	// live brings one lane to Running through the authenticated channel and
	// returns whether the attempt was STARTED by the shell (a start event) or
	// merely submitted by the app.
	live := func(t *testing.T, lane lifecycle.LaneID, seq *uint64, started bool) {
		t.Helper()
		e.ws.RegisterLifecycleLane(lane, sid)
		h, err := pub.RequestDomain(lane, nil, "T")
		if err != nil {
			t.Fatalf("RequestDomain: %v", err)
		}
		*seq++
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, *seq, lifecycleHelloEvt()))
		ackEstablishmentFrom(t, pub, lane, h, e.conn)
		if started {
			*seq++
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, *seq, lifecycleStartEvt(nil, "key-owning-tui")))
			return
		}
		decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
			map[string]string{"domain": string(h.Domain), "command": "key-owning-tui", "cwd": "/tmp", "host": "", "source": "user"}, 40))
	}

	resolver := sessionProtectedForeground{ctx: t.Context(), s: e.ws, sid: sid}

	var seqA uint64
	live(t, "lane-submitted", &seqA, false)
	if got, ok := resolver.Attempt(); ok {
		t.Fatalf("a submitted-but-unstarted attempt was named as running: %q", got)
	}

	var seqB uint64
	live(t, "lane-started", &seqB, true)
	first, ok := resolver.Attempt()
	if !ok {
		t.Fatal("an authenticated started attempt was not named")
	}

	// A second started lane on the same session: there is no right answer,
	// and the first one Go's map happens to yield is not it.
	var seqC uint64
	live(t, "lane-started-2", &seqC, true)
	if got, ok := resolver.Attempt(); ok {
		t.Fatalf("two started lanes resolved to %q; want a refusal (the other was %q)", got, first)
	}
}

func TestSessionSignal_RefusesParamsItCannotHonour(t *testing.T) {
	conn, tap := signalServer(t)
	sid := openSessionTapped(t, conn, tap)

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"an unknown signal name", map[string]any{"sessionId": sid, "signal": "sighup"}},
		{"no signal at all", map[string]any{"sessionId": sid}},
		{"no session at all", map[string]any{"signal": "interrupt"}},
		{"a session id of the wrong shape", map[string]any{"sessionId": "not-a-session", "signal": "interrupt"}},
		{"a session this connection does not hold", map[string]any{
			"sessionId": "ffffffffffffffffffffffffffffffff", "signal": "interrupt",
		}},
	}
	for i, tc := range cases {
		raw := tapCall(t, conn, tap, 10+i, "session.signal", tc.params)
		var env struct {
			Result *json.RawMessage `json:"result"`
			Error  *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if env.Error == nil {
			t.Errorf("%s: answered a result (%s), want a refusal", tc.name, deref(env.Result))
			continue
		}
		if env.Error.Code != -32602 {
			t.Errorf("%s: code %d, want -32602", tc.name, env.Error.Code)
		}
	}
}

func deref(raw *json.RawMessage) string {
	if raw == nil {
		return "<none>"
	}
	return string(*raw)
}

// protectedSignalServer is signalServer plus a lifecycle publisher: REAL
// local shells, so the kernel's answer about the foreground group is the real
// one, and a real projection, so the fallback's evidence is the real one too.
func protectedSignalServer(t *testing.T) (*websocket.Conn, *socketTap, *WSServer, *lifecyclepub.Publisher) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	ws := NewWSServer(logger, newRegWithReal(logger),
		WithLifecyclePublisher(pub),
		WithRunLease(RunLeaseConfig{SignalGrace: 2 * time.Second}))
	pub.SetEmitter(ws)
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(t.Context()) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	return conn, newSocketTap(conn), ws, pub
}

// tapAckEstablishment is ackEstablishmentFrom for a tapped socket: the tap
// owns the reader, so the establishment fact arrives through it.
func tapAckEstablishment(t *testing.T, tap *socketTap, pub *lifecyclepub.Publisher, lane lifecycle.LaneID, h lifecycle.DomainHandle) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case raw, ok := <-tap.msgs:
			if !ok {
				t.Fatal("socket closed before the establishment fact arrived")
			}
			var env struct {
				Method string            `json:"method"`
				Params lifecyclepub.Fact `json:"params"`
			}
			if json.Unmarshal(raw, &env) != nil || env.Method != "lifecycle.changed" {
				continue
			}
			f := env.Params
			if f.Lane != string(lane) || f.Domain != string(h.Domain) || f.Epoch != h.Epoch || f.Generation == "" {
				continue
			}
			if err := pub.AcknowledgeEstablishment(lane, h.Domain, h.Epoch, f.Generation); err != nil {
				t.Fatalf("AcknowledgeEstablishment: %v", err)
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for the establishment fact")
}

// TestSessionSignal_StopsAProgramInsideTheLauncherShellsGroup is the bead's
// sentence as a user can check it (nocx-7l4ex.11): a program owns the screen
// from INSIDE the shell's own process group, and the person's Stop ends it.
//
// This is the incident's topology, produced the way ADR-0024 already names it
// — `set +m` turns job control off, and bash then runs the command in its own
// group rather than making one for it. The kernel's answer is therefore
// identical to the one it gives at an idle prompt, so the ordinary ladder
// correctly refuses (it would be killing the shell) and everything below
// rests on the authenticated projection instead.
//
// Every part of the evidence is asked of the product: the readiness marker is
// a file's content echoed by the running program, not a string in the command
// line; the attempt is opened by an authenticated `start`, not by the app; and
// the proof that the program is gone is the shell answering a later command,
// which it cannot do while a foreground job holds the terminal.
func TestSessionSignal_StopsAProgramInsideTheLauncherShellsGroup(t *testing.T) {
	conn, tap, ws, pub := protectedSignalServer(t)
	sid := openSessionTapped(t, conn, tap)

	const lane = lifecycle.LaneID("lane-protected")
	ws.RegisterLifecycleLane(lane, session.ID(sid))
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	tapAckEstablishment(t, tap, pub, lane, h)

	dir := t.TempDir()
	marker := filepath.Join(dir, "protected-ready")
	if err := os.WriteFile(marker, []byte("PROTECTED-READY\n"), 0o600); err != nil {
		t.Fatalf("write the readiness marker: %v", err)
	}
	// Job control off FIRST, and observed before the job is launched — a
	// `set +m` that had not taken effect yet would silently give the job its
	// own group and test the ordinary ladder instead of this one.
	submitCommand(t, conn, sid, "set +m; printf %s%s JOBCONTROL -OFF")
	tapDataFor(t, tap, sid, "JOBCONTROL-OFF", 20*time.Second)
	submitCommand(t, conn, sid, "tail -f '"+marker+"'")
	tapDataFor(t, tap, sid, "PROTECTED-READY", 20*time.Second)

	// The shell tells the kernel it started something. This is the only
	// thing that may say a program is inside the protected group.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(nil, "tail -f")))

	got := tapSignal(t, conn, tap, 7, map[string]any{"sessionId": sid, "signal": "interrupt"})
	if got.Signal != "interrupt" || got.Outcome != string(foregroundDelivered) {
		t.Fatalf("session.signal = %+v, want interrupt/delivered", got)
	}

	// And the program is actually gone: the shell reads a line again, which
	// it cannot do while `tail -f` holds the terminal.
	submitCommand(t, conn, sid, "printf %s%s PROTECTED -STOPPED")
	tapDataFor(t, tap, sid, "PROTECTED-STOPPED", 20*time.Second)
}

// TestSessionSignal_UnreconciledOverTheWire: the fourth outcome, off the real
// socket, because a word the renderer branches on has to be a word the server
// actually sends (AGENTS.md testing rule 5).
//
// The shape is the one the fallback cannot resolve: a protected group, an
// authenticated started attempt, and a program that does NOT go away — so
// Stop, which may not escalate into the shell's group, has to say it could
// not keep its promise rather than claim the execution is gone.
func TestSessionSignal_UnreconciledOverTheWire(t *testing.T) {
	conn, tap, ws, pub := protectedSignalServer(t)
	sid := openSessionTapped(t, conn, tap)

	const lane = lifecycle.LaneID("lane-stubborn")
	ws.RegisterLifecycleLane(lane, session.ID(sid))
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	tapAckEstablishment(t, tap, pub, lane, h)

	// INT is trapped and ignored, so the terminal interrupt arrives and
	// changes nothing — and there is no rung left, because the group is the
	// shell's.
	submitCommand(t, conn, sid, "set +m; printf %s%s JOBCONTROL -OFF")
	tapDataFor(t, tap, sid, "JOBCONTROL-OFF", 20*time.Second)
	submitCommand(t, conn, sid,
		"sh -c 'trap \"\" INT; printf %s%s STUBBORN -READY; while true; do sleep 1; done'")
	tapDataFor(t, tap, sid, "STUBBORN-READY", 20*time.Second)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(nil, "stubborn")))

	raw := tapCall(t, conn, tap, 8, "session.signal", map[string]any{"sessionId": sid, "signal": "stop"})
	validateJSON(t, loadSchema(t, "session.signal.schema.json"), resultOf(t, raw),
		"session.signal unreconciled, over the wire")
	var env struct {
		Result signalWireResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal session.signal: %v", err)
	}
	if env.Result.Outcome != string(foregroundUnreconciled) {
		t.Fatalf("stop over an unstoppable protected group = %q, want unreconciled", env.Result.Outcome)
	}
}

// TestSessionSignal_AppSubmitBecomesStartedWhenTheShellAttaches is the other
// half of the resolver's Started rule, and it exists because requiring Started
// would be useless if the ordinary path never set it: an app attempt is created
// at submit (origin app, Started false) and the shell's own start ATTACHES to
// it, flipping Started while the id, command text and origin stay app-owned
// (lifecycle-protocol §7, kernel.go's attach branch). So the rule refuses a
// submit that nothing has begun, and admits the same attempt one envelope later.
func TestSessionSignal_AppSubmitBecomesStartedWhenTheShellAttaches(t *testing.T) {
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := session.ID(e.openSession(t, 1))
	if err := pub.BindTransport("T", noopPort{}); err != nil {
		t.Fatal(err)
	}
	const lane = lifecycle.LaneID("lane-attach")
	e.ws.RegisterLifecycleLane(lane, sid)
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	ackEstablishmentFrom(t, pub, lane, h, e.conn)

	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		map[string]string{"domain": string(h.Domain), "command": "set +m; sh job.sh", "cwd": "/tmp", "host": "", "source": "user"}, 40))
	resolver := sessionProtectedForeground{ctx: t.Context(), s: e.ws, sid: sid}
	if named, ok := resolver.Attempt(); ok {
		t.Fatalf("a submitted attempt was named before anything started it: %q", named)
	}

	// The shell begins the line, naming it in its OWN namespace — which is
	// what the real integration sends, and what the e2e journey depends on.
	shellID := lifecycle.AttemptID("s-" + string(h.Domain) + "-1")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(&shellID, "set +m; sh job.sh")))

	named, ok := resolver.Attempt()
	if !ok {
		t.Fatal("the attempt was not named after the shell's start attached to it")
	}
	if string(named) != got.ID {
		t.Fatalf("named %q, want the app-minted id %q — the app id stays authoritative", named, got.ID)
	}
}
