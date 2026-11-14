package agentdetect

// Detection asks the person's login shell where its PATH is, once, and then
// answers the same question for every agent the assistant can run: is it
// there, what version does it print, and does it say it is ready. These tests
// fake the LOGIN SHELL only — the PATH it reports is a real directory holding
// real executables — so the resolution and the probes are the production ones.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/shellenv"
	"github.com/shady2k/nocx/internal/waittest"
)

// fakeShell answers the one question the login shell is asked, without a
// shell: this machine's PATH for the test.
type fakeShell struct{ path string }

func (s fakeShell) Environment(context.Context) ([]string, string, error) {
	return []string{"PATH=" + s.path}, "", nil
}

// countingShell counts the passes that actually asked the login shell, so the
// cache is observed rather than assumed.
type countingShell struct {
	mu    sync.Mutex
	path  string
	calls int
}

func (s *countingShell) Environment(context.Context) ([]string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return []string{"PATH=" + s.path}, "", nil
}

func (s *countingShell) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// failingShell is a login shell that could not answer.
type failingShell struct{ err error }

func (s failingShell) Environment(context.Context) ([]string, string, error) { return nil, "", s.err }

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
	env, _, err := LoginShell{}.Environment(context.Background())
	path, _ := shellenv.Path(env)
	if err != nil {
		t.Fatalf("LoginShell.Path: %v", err)
	}
	if path == "" {
		t.Fatal("the login shell resolved an empty PATH")
	}
}

func TestProbeCancellationStopsDescendant(t *testing.T) {
	for _, detached := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprint("deadline=", deadline, "/detached=", detached), func(t *testing.T) {
				dir := t.TempDir()
				pidFile := filepath.Join(dir, "pid")
				command := filepath.Join(dir, "probe")
				blocker := filepath.Join(dir, "blocker")
				ready := filepath.Join(dir, "ready")
				if err := syscall.Mkfifo(blocker, 0o600); err != nil {
					t.Fatal(err)
				}
				redirect := ""
				if detached {
					redirect = " >/dev/null 2>&1"
				}
				writeExecutable(t, command, "#!/bin/sh\n(trap '' INT TERM; printf ready > \"$3\"; read line < \"$2\")"+redirect+" &\nchild=$!\nprintf '%s' \"$child\" > \"$1\"\nwait \"$child\"\n")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				d := New(fakeShell{})
				d.probeTimeout = time.Hour
				if deadline {
					d.probeTimeout = 300 * time.Millisecond
				}
				done := make(chan error, 1)
				go func() {
					_, _, err := d.run(ctx, command, []string{pidFile, blocker, ready}, nil, "", !detached)
					done <- err
				}()
				var pid int
				waittest.WaitFor(t, "descendant started", func() bool {
					if _, err := os.Stat(ready); err != nil {
						return false
					}
					data, err := os.ReadFile(pidFile) //nolint:gosec // private fixture pid path
					if err != nil {
						return false
					}
					pid, err = strconv.Atoi(string(data))
					return err == nil
				})
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				if !deadline {
					cancel()
				}
				if err := <-done; err == nil {
					t.Fatal("cancelled probe succeeded")
				}
				waittest.WaitFor(t, "descendant stopped", func() bool {
					// Linux container PID 1 may retain an orphan zombie. It is no
					// longer executing or holding the probe's pipes.
					data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
					if err == nil && strings.Contains(string(data), ") Z ") {
						return true
					}
					return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
				})
			})
		}
	}
}

func TestProbeVersionOutputIsBounded(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "probe")
	writeExecutable(t, command, "#!/bin/sh\nprintf 'version 1\\n'\n/usr/bin/head -c 1048576 /dev/zero\n")
	d := New(fakeShell{})
	for _, capture := range []bool{true, false} {
		out, exit, err := d.run(context.Background(), command, nil, nil, "", capture)
		if err != nil || exit != 0 {
			t.Fatalf("probe: exit %d: %v", exit, err)
		}
		if capture && (len(out) > 4096 || !strings.HasPrefix(out, "version 1\n")) {
			t.Fatalf("retained output length %d, want version prefix capped at 4096", len(out))
		}
		if !capture && out != "" {
			t.Fatalf("readiness retained %d bytes, want none", len(out))
		}
	}
}

type resolvedShell struct {
	env []string
	cwd string
}

func (s resolvedShell) Environment(context.Context) ([]string, string, error) {
	return s.env, s.cwd, nil
}

func TestDetectUsesFullResolvedEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NOCX_REMOVED", "backend-only")
	writeExecutable(t, filepath.Join(dir, "pi"), `#!/bin/sh
case "$1" in
 --version) printf 'pi env\n' ;;
 auth) [ "$NOCX_CREDENTIAL" = 'resolved credential' ] && [ "${NOCX_REMOVED+x}" != x ] ;;
esac
`)
	d := New(resolvedShell{env: []string{"PATH=" + dir, "NOCX_CREDENTIAL=resolved credential"}, cwd: dir})
	rows, err := d.Detect(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectionByID(t, rows, "pi").Readiness; got != agentrecord.ReadinessReady {
		t.Fatalf("readiness %s, want ready with resolved environment only", got)
	}
}

func TestDetectUsesResolvedWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "pi"), `#!/bin/sh
case "$1" in
 --version) printf 'pi cwd\n' ;;
 auth) [ "$PWD" = "$HOME" ] ;;
esac
`)
	d := New(resolvedShell{env: []string{"PATH=bin", "HOME=" + home}, cwd: home})
	rows, err := d.Detect(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	pi := detectionByID(t, rows, "pi")
	if !pi.Found || pi.Readiness != agentrecord.ReadinessReady {
		t.Fatalf("relative lookup/probe cwd differs: %+v", pi)
	}
}

type gatedShell struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *gatedShell) Environment(context.Context) ([]string, string, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return []string{"PATH=/nonexistent"}, "", nil
}

type joiningContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *joiningContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestConcurrentRefreshJoinsAndWaitIsCancellable(t *testing.T) {
	shell := &gatedShell{entered: make(chan struct{}), release: make(chan struct{})}
	d := New(shell)
	first := make(chan error, 1)
	go func() { _, err := d.Detect(context.Background(), true); first <- err }()
	<-shell.entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter := &joiningContext{Context: ctx, observed: make(chan struct{})}
	cancelled := make(chan error, 1)
	go func() { _, err := d.Detect(waiter, true); cancelled <- err }()
	select {
	case <-waiter.observed:
	case <-time.After(5 * time.Second):
		close(shell.release)
		t.Fatal("waiter never joined in-flight pass")
	}
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error: %v", err)
	}
	joined := &joiningContext{Context: context.Background(), observed: make(chan struct{})}
	second := make(chan error, 1)
	go func() { _, err := d.Detect(joined, true); second <- err }()
	<-joined.observed
	close(shell.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if got := shell.calls.Load(); got != 1 {
		t.Fatalf("concurrent refresh ran %d login shells", got)
	}
}
