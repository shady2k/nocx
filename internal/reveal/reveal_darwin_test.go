//go:build darwin

package reveal

import (
	"errors"
	"strings"
	"testing"
)

// On a normal macOS machine, Reveal runs `open -R <path>` and returns nil.
// This is the paired "and on a normal machine it succeeds" for every
// "returns an error when…" below.
func TestRevealerDarwin_SucceedsOnNormalMachine(t *testing.T) {
	r := &Revealer{run: (&fakeRunner{}).run}
	if err := r.Reveal("/Users/alice/notes.txt"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// When `open` itself fails, the error propagates — never a silent success.
func TestRevealerDarwin_FailsWhenOpenFails(t *testing.T) {
	runner := &fakeRunner{runErr: errors.New("open: command not found")}
	r := &Revealer{run: runner.run}
	err := r.Reveal("/Users/alice/notes.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "open: command not found") {
		t.Fatalf("expected wrapped error, got %q", err.Error())
	}
}

// The path is passed verbatim to `open -R` — no quoting, no shell.
func TestRevealerDarwin_PassesPathVerbatim(t *testing.T) {
	runner := &fakeRunner{}
	r := &Revealer{run: runner.run}
	_ = r.Reveal("/Users/alice/has space/file.txt")
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call[0] != "open" {
		t.Fatalf("expected open, got %s", call[0])
	}
	if len(call) != 3 || call[1] != "-R" || call[2] != "/Users/alice/has space/file.txt" {
		t.Fatalf("expected [open -R /Users/alice/has space/file.txt], got %v", call)
	}
}
