//go:build linux

package reveal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withFakeXdgOpen places a fake xdg-open on PATH and returns the fake's
// directory. The fake records its argv to a marker file. Tests that
// inject a fake RUNNER still need the real LookPath in Reveal to
// succeed, so a fake binary must exist on PATH regardless of which
// runner the test uses.
func withFakeXdgOpen(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "called.args")
	script := "#!/bin/sh\necho \"$@\" > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "xdg-open"), []byte(script), 0o755); err != nil { //nolint:gosec // a fixture executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

// On a normal Linux machine, Reveal opens the containing directory with
// xdg-open. The file itself cannot be "revealed" (no cross-desktop
// standard), so the parent directory is the best the platform offers.
// The fake runner stands in for a working xdg-open: the assertion is
// that Reveal calls it with the containing directory, which is what a
// normal machine's file-manager opener receives.
func TestRevealerLinux_SucceedsOnNormalMachine(t *testing.T) {
	withFakeXdgOpen(t)
	r := &Revealer{run: (&fakeRunner{}).run}
	if err := r.Reveal("/home/alice/notes.txt"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// Linux opens the PARENT directory, not the file.
func TestRevealerLinux_OpensContainingDirectory(t *testing.T) {
	withFakeXdgOpen(t)
	runner := &fakeRunner{}
	r := &Revealer{run: runner.run}
	_ = r.Reveal("/home/alice/docs/notes.txt")
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call[0] != "xdg-open" {
		t.Fatalf("expected xdg-open, got %s", call[0])
	}
	if len(call) != 2 || call[1] != "/home/alice/docs" {
		t.Fatalf("expected [xdg-open /home/alice/docs], got %v", call)
	}
}

// When xdg-open is not installed, Reveal refuses by name — the error
// matches ErrNoFileManager, never an opaque exec failure. PATH is
// narrowed to an empty directory so the lookup cannot find a real
// xdg-open regardless of the host.
func TestRevealerLinux_FailsWhenXdgOpenAbsent(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	r, err := New()
	if err != nil {
		t.Skip("unsupported platform")
	}
	err = r.Reveal("/home/alice/notes.txt")
	if !errors.Is(err, ErrNoFileManager) {
		t.Fatalf("expected ErrNoFileManager, got %v", err)
	}
}

// The runner's own failure (xdg-open present but the open itself fails,
// e.g. no display) propagates with the command name and output — the
// named refusal for a machine that HAS the tool but cannot use it.
func TestRevealerLinux_FailsWhenOpenFails(t *testing.T) {
	withFakeXdgOpen(t)
	runner := &fakeRunner{runErr: errors.New("no display available")}
	r := &Revealer{run: runner.run}
	err := r.Reveal("/home/alice/notes.txt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "xdg-open: no display available" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A path with no parent (root) opens root itself.
func TestRevealerLinux_RootPathOpensRoot(t *testing.T) {
	withFakeXdgOpen(t)
	runner := &fakeRunner{}
	r := &Revealer{run: runner.run}
	_ = r.Reveal("/")
	call := runner.calls[0]
	if call[1] != "/" {
		t.Fatalf("expected xdg-open /, got xdg-open %s", call[1])
	}
}

// The real runner path (realRunner, not the fake): with a fake xdg-open
// on PATH, Reveal succeeds and passes the containing directory. This is
// the deterministic paired positive — it does not depend on a desktop
// being installed on the test host.
func TestRevealerLinux_RealRunnerInvokesXdgOpen(t *testing.T) {
	dir := withFakeXdgOpen(t)
	r, newErr := New()
	if newErr != nil {
		t.Skip("unsupported platform")
	}

	filePath := filepath.Join(dir, "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if revealErr := r.Reveal(filePath); revealErr != nil {
		t.Fatalf("Reveal failed with real runner and fake xdg-open: %v", revealErr)
	}

	got, err := os.ReadFile(filepath.Join(dir, "called.args")) //nolint:gosec // a test-only path under t.TempDir()
	if err != nil {
		t.Fatalf("xdg-open was not called (marker not written): %v", err)
	}
	want := filepath.Join(dir, "sub")
	if string(got) != want+"\n" {
		t.Fatalf("xdg-open called with %q, want %q (the containing directory)", string(got), want+"\n")
	}
}
