package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
)

func TestLocalPty_ImplementsInterface(t *testing.T) {
	var _ Pty = (*LocalPty)(nil)
}

func TestLocalPty_SpawnAndWrite(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	n, err := lp.Write([]byte("echo hello\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n == 0 {
		t.Fatal("Write returned 0 bytes")
	}
}

func TestLocalPty_ReadReturnsOutput(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	_, err := lp.Write([]byte("echo hello\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 4096)
	var output strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, readErr := lp.Read(buf)
		if readErr != nil && readErr != io.EOF {
			t.Fatalf("Read: %v", readErr)
		}
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "hello") {
				return
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	t.Fatalf("expected output to contain 'hello', got: %q", output.String())
}

func TestLocalPty_Resize(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	err := lp.Resize(context.Background(), 132, 43, 0, 0)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
}

// A resize that arrives after the close is REFUSED, and the refusal names the
// closed descriptor.
//
// This is the deterministic half of nocx-vqziz. The race itself needs a resize
// and the read pump to interleave on a loaded machine, which is why CI found it
// and the dev host did not; what can be asserted every run is the ordering rule
// that removes it — Resize takes the same lock Close does and never reaches
// pty.Setsize once the file is gone. Returning nil here would be worse than the
// race: a caller told its resize succeeded, on a grid nobody is running at.
func TestLocalPty_ResizeAfterCloseIsRefused(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	if err := lp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := lp.Resize(context.Background(), 132, 43, 0, 0)
	if err == nil {
		t.Fatal("Resize after Close returned nil; a resize nobody applied must not report success")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Resize after Close = %v, want an error wrapping os.ErrClosed", err)
	}
}

func TestLocalPty_CloseTwice(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	if err := lp.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lp.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func mustSpawn(t testing.TB, cols, rows uint16) *LocalPty {
	t.Helper()
	// These tests exercise PTY/job-control behavior with bash commands, not
	// the developer's login shell or its personal startup configuration.
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Command: "/bin/bash",
		Args:    []string{"--noprofile", "--norc", "-i"},
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return lp
}

// A macOS .app launched from Finder inherits no locale at all. The child shell
// then computes a non-UTF-8 stdout encoding and every Python/Rich/prompt_toolkit
// TUI silently downgrades non-ASCII output to '?' — which looks exactly like a
// font bug in the renderer. Fill the gap, but never override a deliberate choice.
func TestWithUTF8Locale(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string // expected LANG entry, "" = must not be added
	}{
		{
			name: "adds LANG when the environment carries no locale at all (Finder launch)",
			env:  []string{"PATH=/usr/bin", "TERM=xterm-256color"},
			want: "LANG=en_US.UTF-8",
		},
		{
			name: "keeps an inherited LANG untouched",
			env:  []string{"LANG=ru_RU.UTF-8"},
			want: "",
		},
		{
			name: "respects a deliberate non-UTF-8 LANG",
			env:  []string{"LANG=C"},
			want: "",
		},
		{
			name: "LC_ALL alone is enough — do not add LANG",
			env:  []string{"LC_ALL=en_GB.UTF-8"},
			want: "",
		},
		{
			name: "LC_CTYPE alone is enough — do not add LANG",
			env:  []string{"LC_CTYPE=en_US.UTF-8"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withUTF8Locale(tt.env)

			var added []string
			for _, kv := range got {
				if !contains(tt.env, kv) {
					added = append(added, kv)
				}
			}

			if tt.want == "" {
				if len(added) != 0 {
					t.Fatalf("expected no additions, got %v", added)
				}
				return
			}
			if len(added) != 1 || added[0] != tt.want {
				t.Fatalf("expected exactly %q to be added, got %v", tt.want, added)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestScrubLauncherSession(t *testing.T) {
	// nocx is developed from inside a coding agent, so its own process carries
	// that agent's session markers. Handing them to a user's shell made
	// `claude` in a tab think it was a child session and turn transcript
	// saving off — a terminal must not leak its launcher's identity.
	env := []string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_SESSION_ID=abc",
		"CLAUDE_PID=123",
		// Coding agents export these for their own tools; leaking them into a
		// PTY makes TUIs render black-and-white.
		"TERM=dumb",
		// A bash that starts with extdebug here tries to load bashdb before
		// anything of ours runs (nocx-1ebrs.23).
		"BASHOPTS=checkwinsize:extdebug",
		"NO_COLOR=1",
		// The daemon's own agent tool endpoint. A pane belongs to the
		// coordinator that opened it, and that coordinator names the endpoint
		// on the spawn; whatever this process inherited names a different one
		// (nocx-e7khb).
		shellintegration.ToolSocketEnvVar + "=/run/nocx/daemon-start-tool.sock",
		// The activation marker says "the shell under this terminal is
		// integrated", and only the launch that integrated it may say so
		// (nocx-1ebrs.8). A value this daemon inherited is a claim about
		// somebody else's shell.
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"HOME=/Users/someone",
		// Not a session marker: stripping a credential would break the very
		// tool this fix exists for.
		"CLAUDE_API_KEY=secret",
	}

	got := scrubLauncherSession(env)

	// The tool socket is named through the CONSTANT and not a literal: the
	// scrub list is written in literals because every other entry is another
	// tool's spelling, and this one is ours. Naming it here is what stops the
	// two drifting — a rename in shellintegration that missed the list would
	// leave the scrub silently matching nothing, and this test red.
	for _, unwanted := range []string{"CLAUDECODE=", "CLAUDE_CODE_CHILD_SESSION=", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_PID=", "TERM=", "NO_COLOR=", "BASHOPTS=", "NOCX_SHELL_INTEGRATION=", "NOCX_PROMPT_MODE=", shellintegration.ToolSocketEnvVar + "="} {
		for _, kv := range got {
			if strings.HasPrefix(kv, unwanted) {
				t.Errorf("launcher session marker survived: %q", kv)
			}
		}
	}

	for _, wanted := range []string{"PATH=/usr/bin", "HOME=/Users/someone", "CLAUDE_API_KEY=secret"} {
		found := false
		for _, kv := range got {
			if kv == wanted {
				found = true
			}
		}
		if !found {
			t.Errorf("scrub removed something it should have kept: %q", wanted)
		}
	}
}

// The decision about WHICH shell a local session runs moved to
// internal/loginshell (nocx-wwz0): it had three copies — this one, one in
// internal/git/local, and a hardcoded "bash" in the composition root that
// outranked both — and on macOS the third one won, so every user of the
// platform this product ships to was greeted by a shell they had not chosen.
// The table that used to be here lives beside its owner now; what stays here
// is the assertion that this package still REPORTS the answer it acts on.

// And the paired assertion AGENTS.md asks for: on an ordinary machine the
// decision is not merely correct, it reaches the log. A resolver nobody can
// read the output of is the arrangement this bead exists to remove.
func TestNewLocal_LogsTheShellItResolved(t *testing.T) {
	var buf bytes.Buffer
	lp, err := NewLocal(
		log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil))),
		Config{Cols: 80, Rows: 24},
	)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = lp.Close() }()

	line := buf.String()
	if !strings.Contains(line, "local pty shell resolved") {
		t.Fatalf("no resolved-shell line in the log:\n%s", line)
	}
	// The path and where it came from, both: "bash" without "SHELL said so"
	// leaves the next reader doing exactly the inference this removes.
	if !strings.Contains(line, "shell=") || !strings.Contains(line, "source=") {
		t.Errorf("the line names neither the shell nor its source:\n%s", line)
	}
}

// TestWaitReadableReturnsWhenWokenWithoutHoldingTheDrainLock is the
// regression test for nocx-6q1uh.18 on a real PTY: a goroutine parked in
// WaitReadable on a silent program used to hold the master file's own read
// lock (internal/poll's SyscallConn().Read semantics) for as long as it
// stayed parked, which is exactly the lock RawReadChunk needed next —
// so a drain called from a second goroutine, on the same idle program,
// blocked forever. WaitReadable no longer touches the master file at all
// (its own doc explains the dup-fd-plus-self-pipe mechanism), so this
// proves both halves of that fix at once: RawReadChunk succeeds while
// a wait is parked, and WakeReadiness is what unparks that wait afterward.
//
// No sleep synchronises the two goroutines, deliberately (AGENTS.md, "a
// test may not depend on timing"): the drain below must succeed whether or
// not the WaitReadable goroutine has already reached its own poll(2) call,
// precisely because nothing is shared between them any more; and
// WakeReadiness's byte sits in the wake pipe until read regardless of
// whether the poll(2) call has started yet, so ordering cannot make it
// arrive too early to be seen.
func TestWaitReadableReturnsWhenWokenWithoutHoldingTheDrainLock(t *testing.T) {
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 30"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = lp.Close() })

	waitDone := make(chan error, 1)
	go func() { waitDone <- lp.WaitReadable(context.Background()) }()

	// The observable assertion that the deadlock is gone: on the broken
	// code this call never returns.
	buf := make([]byte, 4096)
	n, barrier, err := lp.RawReadChunk(buf)
	if err != nil {
		t.Fatalf("RawReadChunk while a readiness wait may be parked: %v", err)
	}
	if n != 0 || !barrier {
		t.Fatalf("a silent program read = (%d, barrier=%t), want (0, true)", n, barrier)
	}

	select {
	case err := <-waitDone:
		t.Fatalf("WaitReadable returned (err=%v) before the fd was readable and before anything woke it", err)
	default:
		// Nothing has made the fd readable and nothing has woken it yet, so
		// it is still parked — whether already inside the poll(2) call or
		// about to enter it, WakeReadiness below reaches it either way.
	}

	lp.WakeReadiness()

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitReadable returned %v after WakeReadiness, want nil: ctx was never cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WakeReadiness did not unpark a WaitReadable parked on a silent program")
	}
}

// TestCloseEndsAReadinessWaitOnAProgramThatOutlivesTheHangup is the pty half
// of nocx-mrfe5: a helper Service.Close that hung for ten minutes on macOS
// with the readiness goroutine parked in WaitReadable's poll(2).
//
// Close used to wake a parked wait with a spurious nil and then close the
// very fds the wait polls, so a caller doing what runReadiness does — look
// again after a nil — re-entered poll(2) on descriptors that no longer
// belonged to it. On Linux that answers POLLNVAL at once, forever: such a
// loop spins and never learns the pty is gone. On Darwin, whose poll(2) is
// built on kqueue, closing a polled descriptor drops its registration and a
// wait that entered before the close is never woken by anything (inferred
// from the CI dump; not reproducible here). Either way the caller is told
// nothing, and it is the program ignoring SIGHUP that removes the other
// thing that could have ended it: a slave that hangs up.
//
// The contract asserted here is platform-free: a wait that is parked when
// Close runs ends with os.ErrClosed rather than a nil, and so does every call
// after it.
//
// # Why neither the wait nor the premise is a count (nocx-atpw5.1.43)
//
// This test used to spin WaitReadable in a goroutine up to 1000 times, then
// call Close. On ci-mac all 1000 answered nil within 0.01 s and it failed. A
// count is not the event, and a readiness answer needs one of two things:
// something readable, or Close's latch. This program writes nothing, so 1000
// nils can only mean the premise was false — the child had not installed its
// SIGHUP trap yet, Close's SIGHUP killed it, and the master answered
// EOF-readable at once. Measured on Linux: Close immediately after spawn kills
// that child in 6 of 200 runs, because `trap` runs a moment after the process
// starts. With a readable master the count is then burned while Close's latch
// does not exist yet: reproduced on Linux, 85 of 100 runs ended with 1000 nils
// once Close was held back by a millisecond.
//
// The child therefore writes a marker AFTER the trap and BEFORE the exec, and
// the test READS it: seeing it proves the program ignores the hangup before
// Close can send one. Collecting it goes through the same readiness/drain
// pair production uses and ends on EAGAIN, so from that point the program is
// alive, ignores SIGHUP and writes nothing — a readiness wait parks. The
// single wait then asserts on the moment Close takes that premise away, and
// the program's survival is checked afterwards, so a pass cannot come from
// the master becoming readable for some other reason.
func TestCloseEndsAReadinessWaitOnAProgramThatOutlivesTheHangup(t *testing.T) {
	const marker = "trap-installed"
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Command: "/bin/sh",
		// The marker is written after the trap and before the exec, so
		// reading it proves the program already ignores SIGHUP.
		Args: []string{"-c", "trap '' HUP; printf " + marker + "; exec sleep 60"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	pid := lp.Pid()
	t.Cleanup(func() {
		_ = lp.Close()
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})

	// The premise, established rather than assumed: the program ignores the
	// hangup, and it is silent — the master is drained to EAGAIN here.
	drainUntil(t, lp, marker)

	waitDone := make(chan error, 1)
	go func() { waitDone <- lp.WaitReadable(context.Background()) }()

	// Asked at the moment Close takes the premise away, never by sleeping:
	// with nothing to read and nothing to wake it, a wait cannot have
	// answered. If it has, the premise is false and this test would prove
	// nothing about Close.
	select {
	case err := <-waitDone:
		t.Fatalf("WaitReadable answered %v with nothing to read and nothing to wake it: the premise below is false", err)
	default:
	}

	if err := lp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-waitDone:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("a WaitReadable parked when Close ran = %v, want os.ErrClosed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never woke a WaitReadable parked on a program that outlives the hangup")
	}

	// The program outlived the hangup, so what ended the wait was Close's
	// latch and nothing else: the one other thing that could have ended it, a
	// slave that hangs up, did not happen.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the program did not outlive the hangup (kill 0 = %v): the wait above ended for another reason", err)
	}

	// And the refusal is not a one-off for the wait that happened to be in
	// flight: every call after Close answers the same, so a caller that looks
	// again is never told nil.
	for i := 1; i <= 3; i++ {
		if err := lp.WaitReadable(context.Background()); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("WaitReadable call %d after Close = %v, want os.ErrClosed", i, err)
		}
	}
	// And a wake after Close is still harmless.
	lp.WakeReadiness()
}

// drainUntil waits — through the production readiness/drain pair, so no sleep
// synchronises anything — until the master's output holds needle, and leaves
// the master drained to EAGAIN: the caller knows the program wrote what it
// wrote and has written nothing since. waitForOutput is the sibling waiting
// on a marker, but it reads with a blocking Read and stops at the marker,
// which cannot say the fd is quiet.
//
// The deadline is a failure watchdog only; what is asserted on is the
// program's own output.
func drainUntil(t *testing.T, lp *LocalPty, needle string) string {
	t.Helper()
	type collected struct {
		out string
		err error
	}
	done := make(chan collected, 1)
	go func() {
		var out strings.Builder
		buf := make([]byte, 4096)
		for {
			for {
				n, barrier, err := lp.RawReadChunk(buf)
				if err != nil {
					done <- collected{out.String(), err}
					return
				}
				out.Write(buf[:n])
				if barrier || n == 0 {
					break
				}
			}
			if strings.Contains(out.String(), needle) {
				done <- collected{out.String(), nil}
				return
			}
			if err := lp.WaitReadable(context.Background()); err != nil {
				done <- collected{out.String(), err}
				return
			}
		}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("collecting output until %q: %v (got %q)", needle, got.err, got.out)
		}
		return got.out
	case <-time.After(10 * time.Second):
		t.Fatalf("the program never wrote %q", needle)
		return ""
	}
}
