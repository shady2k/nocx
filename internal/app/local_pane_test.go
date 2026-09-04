package app

// A LOCAL PANE IS THE DAEMON'S, AND THIS IS WHERE THAT IS WATCHED HAPPENING
// (nocx-ie23r.3).
//
// internal/helper/session's TestOnlyTheHelperConstructsALocalPTY proves there
// is exactly ONE place a local PTY can be constructed. That is a ratchet and
// it is not an acceptance: it passes unchanged on a tree where local open is
// broken everywhere and nothing ever reaches that constructor. Uniqueness is
// not reachability.
//
// So these tests open a real pane through the SHIPPED composition root — the
// same App a person launches, the same installer, the same endpoint, the same
// handshake — and then ask the OPERATING SYSTEM who the shell's parent is. Not
// a record nocx wrote about itself, not a code path: `ps`, about a pid the
// shell reported for itself.
//
// Everything a local pane had is asserted through the new owner in the same
// way: the shell-integration activation environment by asking the shell what
// its own environment says, resize by asking it how wide it is, signals by
// stopping a job and watching it die, the exit status by ending the shell and
// reading what the product concluded.
//
// THE DAEMON IS DETACHED BY DESIGN (D1) and nothing retires a generation yet
// (D2 is unimplemented), so a test that started one and walked away would
// leave it running on the machine. newLocalPaneApp registers the cleanup that
// ends it, and it finds it the way anything finds a process on this machine —
// by asking the OS which process is running that binary.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// ── the harness ─────────────────────────────────────────────────────────────

var (
	helperBuildOnce sync.Once
	builtHelper     []byte
	helperBuildErr  error
)

// realHelperArtifacts builds cmd/nocx-helper once per test binary and serves
// it as the artifact the composition root installs.
//
// THE REAL BINARY, and that is the difference between this file and
// local_helper_install_test.go, which deliberately installs a few bytes.
// That file is about the install seam, where the payload is irrelevant. This
// one is about a shell running under a daemon, and there is no version of that
// assertion a stand-in can satisfy: the handshake would refuse it, and if it
// did not, nothing would fork a shell.
//
// The embedded source is not used for the same reason it is not used there:
// `make helpers` has not necessarily run, and a test that only passes on a
// machine that built the helpers is not a gate.
func realHelperArtifacts(t *testing.T) fakeArtifacts {
	t.Helper()
	helperBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nocxhelperbin")
		if err != nil {
			helperBuildErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		bin := filepath.Join(dir, "nocx-helper")
		out, err := exec.Command("go", "build", "-o", bin, "../../cmd/nocx-helper").CombinedOutput() //nolint:gosec // the arguments are this test's own constants
		if err != nil {
			helperBuildErr = errors.New(string(out))
			return
		}
		builtHelper, helperBuildErr = os.ReadFile(bin) // #nosec G304 — this test's own build output
	})
	if helperBuildErr != nil {
		t.Fatalf("building cmd/nocx-helper: %v", helperBuildErr)
	}
	return fakeArtifacts{payload: builtHelper}
}

// newLocalPaneApp boots the shipped composition root over an isolated home,
// with this machine's helper really installed and really reachable.
func newLocalPaneApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	// The helper is built BEFORE the home moves. `go build` resolves GOPATH
	// from HOME, so a build under the disposable one would fill it with a
	// module cache the test then cannot remove — and would pay for the
	// download on every run.
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	binary := filepath.Join(helperRoot(home, src.hash()), "nocx-helper")
	// Registered BEFORE the app starts, so a Start or an open that fails
	// half-way still has its daemon ended. t.Cleanup runs last-registered
	// first, so this one runs after the shutdown below.
	t.Cleanup(func() { endTheDaemon(t, binary) })

	a, err := newTestApp(t, append([]Option{withLocalHelperArtifacts(src)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		a.Shutdown(context.Background())
		cancel()
	})
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return a
}

// endTheDaemon ends the helper this test started, by asking the OS which
// process is running that binary. Signalled rather than killed: a helper's
// SIGTERM path is the one it ships with, and a test that only ever killed one
// would never exercise it.
func endTheDaemon(t *testing.T, binary string) {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", binary).Output() //nolint:gosec // the path is this test's own isolated home
	if err != nil {
		return // nothing is running it, which is the ordinary answer on a failed start
	}
	for _, line := range strings.Fields(string(out)) {
		pid, convErr := strconv.Atoi(line)
		if convErr != nil || pid <= 0 {
			continue
		}
		p, findErr := os.FindProcess(pid)
		if findErr != nil {
			continue
		}
		_ = p.Signal(syscall.SIGTERM)
		// WAITED FOR, not merely signalled. The disposable home is removed
		// after this cleanup returns, and a daemon still holding its endpoint
		// under that home makes the removal fail — which is a red test about
		// a tidy-up rather than about the product.
		deadline := time.Now().Add(30 * time.Second)
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// pane is one open local pane plus a reader of everything it has said.
type pane struct {
	sess session.Session
	mu   sync.Mutex
	out  strings.Builder
}

// openLocalPane opens one through the backend's own way in — the SAME opener
// the `open` JSON-RPC handler uses, with no ring and no ack, which is what
// transport.OpenSession is (nocx-dkawo.6). Going through it rather than over
// the WebSocket is what lets these tests hold the session and ask it things;
// what it does NOT do is take a shortcut past anything the product does, and
// TestALocalPaneEntersTheIntegrationAxis goes over the real socket to say so.
func openLocalPane(t *testing.T, a *App) *pane {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opened, err := a.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	p := &pane{sess: opened.Session}
	if err := opened.Session.StartOutput(context.Background(), func(data []byte) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.out.Write(data)
		return nil
	}); err != nil {
		t.Fatalf("reading the pane's output: %v", err)
	}
	t.Cleanup(func() { _ = opened.Session.Close() })
	return p
}

func (p *pane) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

// say builds a shell word whose ECHO does not match the pattern its OUTPUT
// does.
//
// A pty echoes what is typed into it, so the pane's text carries the command
// before it carries the answer, and a marker spelled the same way in both is
// matched by the echo — the assertion then reads "$NOCX_SHELL_INTEGRATION"
// back as the value of $NOCX_SHELL_INTEGRATION. Splitting the marker across
// two adjacent string literals is the smallest thing that cannot do that: the
// shell concatenates them, so the OUTPUT is the whole marker and the echoed
// COMMAND never is.
func say(marker, rest string) string {
	return `echo "` + marker[:4] + `""` + marker[4:] + rest + `"`
}

// run types a line into the pane and waits for the answer it asked for.
//
// The wait is on an OBSERVABLE — the marker the shell itself prints — and
// never on a duration, so a slow machine is slow rather than red. The deadline
// exists only so a shell that never answers fails as a failure instead of as a
// hung suite.
func (p *pane) run(t *testing.T, line string, want *regexp.Regexp) []string {
	t.Helper()
	if _, err := p.sess.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("writing %q into the pane: %v", line, err)
	}
	return p.await(t, want)
}

func (p *pane) await(t *testing.T, want *regexp.Regexp) []string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if m := want.FindStringSubmatch(p.text()); m != nil {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the pane never said anything matching %s. What it said was:\n%s", want, p.text())
	return nil
}

// ── the assertion the bead is about ─────────────────────────────────────────

var shellPID = regexp.MustCompile(`NOCXPID=(\d+)\b`)

// THE SHELL IS THE DAEMON'S CHILD, NOT THE BACKEND'S — read from the operating
// system, about a pid the shell reported for itself.
//
// This is the whole of nocx-ie23r.3 in one assertion, and it is deliberately
// made without asking nocx anything. The shell says its own pid; `ps` says who
// that pid's parent is; `ps` says what that parent is running. If the local
// pane were still forked in the coordinator — which is what it did until this
// bead — the parent would be this test binary, and every other test in this
// file would pass exactly as it does now.
func TestALocalPaneIsTheDaemonsChild(t *testing.T) {
	a := newLocalPaneApp(t)
	p := openLocalPane(t, a)

	pid, err := strconv.Atoi(p.run(t, say("NOCXPID", "=$$"), shellPID)[1])
	if err != nil || pid <= 0 {
		t.Fatalf("the shell reported an unusable pid: %v", err)
	}
	ppid := parentOf(t, pid)
	if ppid == os.Getpid() {
		t.Fatal("the shell is a child of the BACKEND: the second local PTY owner is back")
	}
	parent := commandOf(t, ppid)
	if !strings.Contains(parent, "nocx-helper") {
		t.Fatalf("the shell's parent (pid %d) is running %q, want this machine's nocx-helper daemon",
			ppid, parent)
	}
}

// parentOf asks the OS for a pid's parent. `ps` rather than /proc, because the
// shipped desktop is macOS and /proc is Linux's.
func parentOf(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // the pid is one this test's own shell reported for itself
	if err != nil {
		t.Fatalf("ps for pid %d: %v", pid, err)
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps reported %q as pid %d's parent: %v", out, pid, err)
	}
	return ppid
}

// commandOf asks the OS what a pid is running.
func commandOf(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // the pid comes from ps above, about a process this test started
	if err != nil {
		t.Fatalf("ps for pid %d: %v", pid, err)
	}
	return strings.TrimSpace(string(out))
}

// ── everything a local pane had, through the new owner ──────────────────────

var (
	gateAnswer   = regexp.MustCompile(`NOCXGATE=\[([^\]]*)\]`)
	columnAnswer = regexp.MustCompile(`NOCXCOLS=\[(\d+)\]`)
	jobStarted   = regexp.MustCompile(`NOCXJOB=(\d+)\b`)
)

// THE SHELL-INTEGRATION ACTIVATION ENVIRONMENT still reaches the shell.
//
// Asked of the SHELL rather than of /proc, and the difference is the point:
// the coordinator used to hand the activation variables to exec, so they were
// in the child's environment block. The daemon delivers them inside the
// transient rcfile the shell sources, which is where they have always been
// exported from on the remote tier. A test that read /proc/<pid>/environ would
// now be red on a product that works.
func TestALocalPaneStillGetsTheActivationEnvironment(t *testing.T) {
	requireIntegratedLoginShell(t)
	a := newLocalPaneApp(t)
	p := openLocalPane(t, a)

	if got := p.run(t, say("NOCXGATE", "=[$NOCX_SHELL_INTEGRATION]"), gateAnswer)[1]; got != "1" {
		t.Fatalf("NOCX_SHELL_INTEGRATION = %q in the pane's shell, want 1", got)
	}
}

// NEITHER BEARER REACHES THE ENVIRONMENT (ADR-0024 decision 2). The capability
// and the recovery fence ride the rcfile TEXT; a shell whose environment block
// carried either would be handing them to every descendant, including the
// user's own commands.
//
// Read out of the shell's exported environment, which is a superset of what it
// was exec'd with — so this catches both the launch putting one there and the
// rcfile exporting one.
func TestALocalPaneNeverExportsItsCapability(t *testing.T) {
	requireIntegratedLoginShell(t)
	a := newLocalPaneApp(t)
	p := openLocalPane(t, a)

	// One line per variable, ending with a marker, so the wait is on an
	// observable and the whole environment is in hand when it arrives.
	env := p.run(t, "env | tr '\\n' '@'; "+say("NOCXGATE", "=[done]"), gateAnswer)
	_ = env
	for _, entry := range strings.Split(p.text(), "@") {
		_, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || len(value) != 64 {
			continue
		}
		if isLowerHex(value) {
			t.Fatalf("the pane's environment carries a 64-hex authenticator: %q", entry)
		}
	}
}

// RESIZE reaches the daemon's PTY. The session's Resize goes over the helper
// protocol now instead of into an ioctl in this process, and the assertion is
// the one a user makes: the program running in the pane sees the new width.
func TestALocalPaneResizes(t *testing.T) {
	a := newLocalPaneApp(t)
	p := openLocalPane(t, a)
	// Settle first: a resize that arrived before the shell had read its own
	// size would be measuring the spawn, not the resize.
	p.run(t, say("NOCXGATE", "=[ready]"), gateAnswer)

	if err := p.sess.Resize(context.Background(), session.Size{Cols: 132, Rows: 40}); err != nil {
		t.Fatalf("resizing the pane: %v", err)
	}
	if got := p.run(t, say("NOCXCOLS", "=[$(tput cols 2>/dev/null || stty size | cut -d' ' -f2)]"), columnAnswer)[1]; got != "132" {
		t.Fatalf("the shell reports %s columns after a resize to 132", got)
	}
}

// SIGNALS reach the job in the pane. The run-lease stop ladder asks a session
// to name its foreground group and then signals that exact group; on a hosted
// session both questions cross the wire (internal/helper/client's signal
// seam), and until nocx-ie23r.3 neither of them could be answered at all.
//
// The assertion is the user's: a command that was running is not running any
// more, and the SHELL is — stopping a job must never take the pane with it.
func TestALocalPaneSignalsItsForegroundJob(t *testing.T) {
	a := newLocalPaneApp(t)
	p := openLocalPane(t, a)

	// A job that will not end on its own, reporting its own pid so the death
	// is observed on the OS rather than inferred from silence.
	p.run(t, "sh -c '"+say("NOCXJOB", "=$$")+"; while :; do sleep 1; done'", jobStarted)
	job, err := strconv.Atoi(jobStarted.FindStringSubmatch(p.text())[1])
	if err != nil {
		t.Fatalf("the job reported an unusable pid: %v", err)
	}

	signaller, ok := p.sess.(interface {
		ForegroundJob() (int, error)
		SignalProcessGroup(pgid int, sig syscall.Signal) error
	})
	if !ok {
		t.Fatal("a local pane cannot be asked to stop a job: the session's signal seam is unanswered")
	}
	pgid, err := signaller.ForegroundJob()
	if err != nil {
		t.Fatalf("naming the pane's foreground job: %v", err)
	}
	if err := signaller.SignalProcessGroup(pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("signalling the pane's foreground job: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for alive(job) {
		if time.Now().After(deadline) {
			t.Fatalf("the job (pid %d, group %d) is still running after SIGKILL", job, pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// And the pane survived it.
	p.run(t, say("NOCXGATE", "=[alive]"), gateAnswer)
}

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// THE EXIT STATUS is the helper's, and the product still reads it.
//
// This is the assertion internal/app's own exit_outcome_local_test.go used to
// make about a pty wrapper that forwarded WaitErr. The wrapper is gone; the
// attachment carries the helper's own status instead, and a clean `exit`
// must still be a clean exit rather than "connection lost" (nocx-o3amz).
func TestALocalPaneReportsItsExitStatus(t *testing.T) {
	a := newLocalPaneApp(t)
	p := openLocalPane(t, a)
	p.run(t, say("NOCXGATE", "=[ready]"), gateAnswer)

	if _, err := p.sess.Write([]byte("exit 7\n")); err != nil {
		t.Fatalf("ending the pane's shell: %v", err)
	}
	select {
	case <-p.sess.Done():
	case <-time.After(60 * time.Second):
		t.Fatal("the pane never ended after its shell exited")
	}
	// The status arrives on the helper's exit notification, which is a
	// different frame from the end of the data stream: Done can close first.
	var cause session.ExitCause
	var code int
	deadline := time.Now().Add(30 * time.Second)
	for {
		cause, code = p.sess.ExitOutcome()
		if cause == session.ExitExited || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cause != session.ExitExited || code != 7 {
		t.Fatalf("the pane reports %v/%d, want a clean exit with status 7", cause, code)
	}
}

// ── the refusal, and the claim ──────────────────────────────────────────────

// THERE IS NO FALLBACK (ADR-0057). With no helper installed on this machine,
// the pane is REFUSED — it is not opened by a second route, and the refusal
// names what is wrong.
//
// The wording and its closed sets are nocx-ie23r.4's. What this asserts is the
// part that is settled: the open fails rather than succeeding by another
// owner, and the sentence says which machine's helper is missing.
func TestALocalPaneIsRefusedWhenThisMachineHasNoHelper(t *testing.T) {
	storagetest.IsolateWithHome(t)
	a, err := newTestApp(t) // the default: no local helper installed
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if serr := a.Start(ctx); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer a.Shutdown(context.Background())

	_, err = a.Transport.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err == nil {
		t.Fatal("a pane opened with no local helper: something else forked a shell")
	}
	if !strings.Contains(err.Error(), "helper") {
		t.Fatalf("refusal = %q, want one that names this machine's helper", err)
	}
}

// ── shared conditions ───────────────────────────────────────────────────────

// requireIntegratedLoginShell skips the tests that are about the integrated
// tier when this account's login shell has none.
//
// It is a SKIP and not a fallback: the helper starts the user's own shell
// either way, and there is nothing to assert about an activation environment
// that was deliberately not delivered (nocx-wwz0). The condition is asked of
// the same owner the helper asks — one answer to "which shell", not two.
func requireIntegratedLoginShell(t *testing.T) {
	t.Helper()
	shell := loginShellPath()
	if shellintegration.LocalShellKind(shell) == shellintegration.ShellUnknown {
		t.Skipf("this account's login shell is %s, which has no local integrated tier", shell)
	}
}

// loginShellPath asks the SAME owner the helper asks. internal/loginshell is
// the one answer to "which shell does this user log in with" (nocx-wwz0), and
// a test that guessed would be a second one.
func loginShellPath() string { return loginshell.New().Resolve().Path }
