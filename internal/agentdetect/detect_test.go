package agentdetect

// Detection asks the person's login shell where its PATH is, once, and then
// answers the same question for every agent the assistant can run: is it
// there, what version does it print, and does it say it is ready. These tests
// fake the LOGIN SHELL only — the PATH it reports is a real directory holding
// real executables — so the resolution and the probes are the production ones.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentrecord"
)

// fakeShell answers the one question the login shell is asked, without a
// shell: this machine's PATH for the test.
type fakeShell struct{ path string }

func (s fakeShell) Path(context.Context) (string, error) { return s.path, nil }

// countingShell counts the passes that actually asked the login shell, so the
// cache is observed rather than assumed.
type countingShell struct {
	mu    sync.Mutex
	path  string
	calls int
}

func (s *countingShell) Path(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.path, nil
}

func (s *countingShell) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// failingShell is a login shell that could not answer.
type failingShell struct{ err error }

func (s failingShell) Path(context.Context) (string, error) { return "", s.err }

func writeExecutable(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
}

func detectionByID(t *testing.T, rows []agentrecord.Detection, id string) agentrecord.Detection {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no detection row for %q in %+v", id, rows)
	return agentrecord.Detection{}
}

// A login-shell PATH holding a pi that prints its version and answers
// `auth check` ready, a codex that prints only a version, and no claude at
// all. pi is found with the version it printed and the readiness its own
// probe reported; codex is found and installed, which is all it states;
// claude is not installed; and every agent of the record appears exactly once.
func TestDetectFindsPiOnTheLoginShellPathAndClaudeNowhere(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "pi"), `#!/bin/sh
case "$1" in
  --version) printf 'pi 1.5.2\n' ;;
  auth) exit 0 ;;
esac
`)
	writeExecutable(t, filepath.Join(dir, "codex"), `#!/bin/sh
printf 'codex-cli 0.167.1\n'
`)
	detector := New(fakeShell{path: dir})

	rows, err := detector.Detect(context.Background(), false)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	want := agentrecord.SupportedAgents()
	if len(rows) != len(want) {
		t.Fatalf("detected %d agents, the record holds %d", len(rows), len(want))
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if seen[row.ID] {
			t.Fatalf("agent %q appears more than once: %+v", row.ID, rows)
		}
		seen[row.ID] = true
	}
	for _, agent := range want {
		if !seen[agent.ID] {
			t.Fatalf("agent %q is missing from %+v", agent.ID, rows)
		}
	}

	pi := detectionByID(t, rows, "pi")
	if !pi.Found || pi.Version != "pi 1.5.2" || pi.Readiness != agentrecord.ReadinessReady {
		t.Fatalf("pi = %+v, want found with its version and ready", pi)
	}
	codex := detectionByID(t, rows, "codex")
	if !codex.Found || codex.Version != "codex-cli 0.167.1" || codex.Readiness != agentrecord.ReadinessInstalled {
		t.Fatalf("codex = %+v, want found, its version, and installed (it states no readiness)", codex)
	}
	claude := detectionByID(t, rows, "claude")
	if claude.Found || claude.Version != "" || claude.Readiness != agentrecord.ReadinessNotInstalled {
		t.Fatalf("claude = %+v, want not installed", claude)
	}
}

// A probe that never exits is bounded and answers unknown. It must not be read
// as ready, which is the exit status a probe that finished would have given.
func TestDetectReportsAHangingProbeAsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "pi"), `#!/bin/sh
case "$1" in
  --version) printf 'pi 1.5.2\n' ;;
  auth) exec /bin/sleep 30 ;;
esac
`)
	detector := New(fakeShell{path: dir})
	detector.probeTimeout = 100 * time.Millisecond

	rows, err := detector.Detect(context.Background(), false)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	pi := detectionByID(t, rows, "pi")
	if !pi.Found || pi.Version != "pi 1.5.2" {
		t.Fatalf("pi = %+v, want found with the version its other probe printed", pi)
	}
	if pi.Readiness != agentrecord.ReadinessUnknown {
		t.Fatalf("hanging probe reported %q, want unknown", pi.Readiness)
	}
}

// The login shell is asked once per pass, and a cached answer is served until
// a caller asks for a refresh.
func TestDetectAsksTheLoginShellOnceUntilRefresh(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "pi"), "#!/bin/sh\nexit 0\n")
	shell := &countingShell{path: dir}
	detector := New(shell)
	ctx := context.Background()

	if _, err := detector.Detect(ctx, false); err != nil {
		t.Fatalf("first Detect: %v", err)
	}
	if _, err := detector.Detect(ctx, false); err != nil {
		t.Fatalf("second Detect: %v", err)
	}
	if got := shell.asked(); got != 1 {
		t.Fatalf("the login shell was asked %d times for two cached reads, want 1", got)
	}
	if _, err := detector.Detect(ctx, true); err != nil {
		t.Fatalf("refresh Detect: %v", err)
	}
	if got := shell.asked(); got != 2 {
		t.Fatalf("the login shell was asked %d times after a refresh, want 2", got)
	}
}

// A login shell that cannot answer is an error, not a machine with no agents:
// "not installed" would be a claim about the person's computer made from a
// failed look.
func TestDetectRefusesToAnswerWhenTheLoginShellFails(t *testing.T) {
	detector := New(failingShell{err: errors.New("the login shell did not answer")})
	rows, err := detector.Detect(context.Background(), false)
	if err == nil {
		t.Fatalf("Detect answered %+v for a login shell that failed", rows)
	}
	if rows != nil {
		t.Fatalf("Detect answered rows %+v alongside its error", rows)
	}
}

// TestLoginShellPathSucceedsOnAnOrdinaryMachine runs the production path — the
// real login shell through internal/shellenv — because every test above fakes
// the shell, and a resolver that only works against fakes is the defect the
// paired-success rule exists for.
func TestLoginShellPathSucceedsOnAnOrdinaryMachine(t *testing.T) {
	path, err := LoginShell{}.Path(context.Background())
	if err != nil {
		t.Fatalf("LoginShell.Path: %v", err)
	}
	if path == "" {
		t.Fatal("the login shell resolved an empty PATH")
	}
}
