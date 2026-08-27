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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
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
