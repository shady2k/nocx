package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
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
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Cols: cols,
		Rows: rows,
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
		"NO_COLOR=1",
		"HOME=/Users/someone",
		// Not a session marker: stripping a credential would break the very
		// tool this fix exists for.
		"CLAUDE_API_KEY=secret",
	}

	got := scrubLauncherSession(env)

	for _, unwanted := range []string{"CLAUDECODE=", "CLAUDE_CODE_CHILD_SESSION=", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_PID=", "TERM=", "NO_COLOR="} {
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
// stayed parked, which is exactly the lock RawReadUntilAgain needed next —
// so a drain called from a second goroutine, on the same idle program,
// blocked forever. WaitReadable no longer touches the master file at all
// (its own doc explains the dup-fd-plus-self-pipe mechanism), so this
// proves both halves of that fix at once: RawReadUntilAgain succeeds while
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
	var got []byte
	buf := make([]byte, 4096)
	eof, err := lp.RawReadUntilAgain(buf, func(p []byte) { got = append(got, p...) })
	if err != nil {
		t.Fatalf("RawReadUntilAgain while a readiness wait may be parked: %v", err)
	}
	if eof {
		t.Fatalf("RawReadUntilAgain reported eof against a program that has not exited")
	}
	if len(got) != 0 {
		t.Fatalf("a silent program (`sleep 30`) produced %d bytes, want 0", len(got))
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
