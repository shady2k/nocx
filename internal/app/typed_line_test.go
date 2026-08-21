package app

// The typed line, at the seam the parent shell actually reaches (design §4.3
// and §4.4, assertions 21 and 22).
//
// Two questions, asked of one decision. Does the wrapped line still do
// everything the user's line did — every option, and the process's own exit
// status? And does a refusal leave the user's line ALONE: their own words,
// nothing minted, no terminal taken, nothing published?

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/ssh/mux"
)

// ---------------------------------------------------------------------------
// Counting doubles. Every one of them exists to be asserted at ZERO.

type countingTerminals struct {
	mu    sync.Mutex
	opens int
	win   *harnessWindow
}

func (c *countingTerminals) OpenBootstrapWindow(session.ID) (session.BootstrapWindow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens++
	if c.win == nil {
		c.win = newHarnessWindow()
	}
	return c.win, nil
}

func (c *countingTerminals) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

type countingPublisher struct {
	mu    sync.Mutex
	calls int
}

func (c *countingPublisher) EnsureInstalledOverPipe(context.Context, io.ReadWriteCloser, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return nil
}

func (c *countingPublisher) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type countingDialer struct {
	mu    sync.Mutex
	calls int
}

func (c *countingDialer) dial(string) (TypedMaster, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return nil, errors.New("no master in this test")
}

func (c *countingDialer) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// refusingOracle answers the ssh -G question with whatever a case needs.
type refusingOracle struct{ cfg ssh.HostConfig }

func (o refusingOracle) ResolveArgv(_ context.Context, argv []string) (*ssh.HostConfig, error) {
	cfg := o.cfg
	for _, w := range argv {
		if strings.HasPrefix(w, "ControlPath=") {
			cfg.ControlPath = "/tmp/nx/m-" + strings.Repeat("a", 40)
			cfg.ControlMaster = "auto"
		}
	}
	return &cfg, nil
}

func (o refusingOracle) ResolveHost(_ context.Context, h string) (string, error) { return h, nil }

func (o refusingOracle) ResolveConfig(_ context.Context, _ string) (*ssh.HostConfig, error) {
	cfg := o.cfg
	return &cfg, nil
}

func typedTestRunner(t *testing.T, oracle ssh.ConfigResolver) (*typedRunner, *countingTerminals, *countingPublisher, *countingDialer) {
	t.Helper()
	root, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	terms := &countingTerminals{}
	pub := &countingPublisher{}
	dialer := &countingDialer{}
	return &typedRunner{
		log:      log.NewSlogAdapter(nil),
		wrapper:  ssh.NewTypedWrapper(log.NewSlogAdapter(nil), oracle, root),
		dial:     dialer.dial,
		publish:  pub,
		sessions: terms,
		probes:   defaultMasterProbes,
	}, terms, pub, dialer
}

func typedTestRequest() lifecyclepub.GrantRequest {
	return lifecyclepub.GrantRequest{
		Env: lifecycle.EnvSSH, Host: "box.example.com", User: "alice", Port: 2222,
		Opts: []string{"-i", "/home/u/.ssh/id key", "-o", "StrictHostKeyChecking=no", "-J", "bastion.example.com"},
	}
}

// ---------------------------------------------------------------------------
// Assertion 21: a pre-authentication refusal runs the user's own line with
// ZERO nocx effect. The observable is negative and is taken at four seams at
// once: the line, the mint, the terminal and the publish.

func TestTypedLine_ARefusalRunsTheUsersOwnLineAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  ssh.HostConfig
	}{
		{"a configured RemoteCommand", ssh.HostConfig{RemoteCommand: "tmux attach -t work"}},
		{"the user's own ControlMaster", ssh.HostConfig{ControlMaster: "auto", ControlPath: "/home/u/.ssh/cm"}},
		{"the user's own ControlPersist", ssh.HostConfig{ControlPersist: "10m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, terms, pub, dialer := typedTestRunner(t, refusingOracle{cfg: tc.cfg})
			sessions := newSessionRegistry()
			req := typedTestRequest()
			sessions.register(req.Lane, "aabbccddeeff00112233445566778899")

			boot, err := buildSSHChildBootstrap(log.NewSlogAdapter(nil), nil, sessions, req,
				transportKind{local: true}, runner)
			if err != nil {
				t.Fatalf("a refusal must not be an error; the user's line still has to run: %v", err)
			}
			if boot.Domain != "" || boot.Epoch != 0 {
				t.Fatalf("a refused line minted domain %q epoch %d; nothing may be minted before ownership is proven",
					boot.Domain, boot.Epoch)
			}
			if boot.Bootstrap == "" {
				t.Fatal("a refused line came back with an empty bootstrap; the parent shell evals that to NOTHING and the user's command never runs")
			}
			for _, forbidden := range []string{"ControlMaster", "ControlPath", "NOCX1", "-R "} {
				if strings.Contains(boot.Bootstrap, forbidden) {
					t.Fatalf("the refused line carries %q: %s", forbidden, boot.Bootstrap)
				}
			}
			// It is the user's OWN line: their words, their destination.
			for _, opt := range req.Opts {
				if !strings.Contains(boot.Bootstrap, opt) {
					t.Fatalf("the refused line dropped the user's %q: %s", opt, boot.Bootstrap)
				}
			}
			if !strings.Contains(boot.Bootstrap, "alice@box.example.com") {
				t.Fatalf("the refused line lost the destination: %s", boot.Bootstrap)
			}
			if n := terms.openCount(); n != 0 {
				t.Fatalf("%d bootstrap windows were opened for a refused line, want 0 — the user's terminal is theirs", n)
			}
			if n := pub.callCount(); n != 0 {
				t.Fatalf("%d publishes for a refused line, want 0", n)
			}
			if n := dialer.callCount(); n != 0 {
				t.Fatalf("%d control-socket handshakes for a refused line, want 0", n)
			}
		})
	}
}

// A line nocx does not interpose on is still a line the user typed, so the
// wrapper's refusal must be the ONLY thing standing between them and it.
func TestTypedLine_ARefusedLineParsesAndRuns(t *testing.T) {
	runner, _, _, _ := typedTestRunner(t, refusingOracle{cfg: ssh.HostConfig{RemoteCommand: "tmux attach"}})
	sessions := newSessionRegistry()
	req := typedTestRequest()
	sessions.register(req.Lane, "aabbccddeeff00112233445566778899")
	boot, err := buildSSHChildBootstrap(log.NewSlogAdapter(nil), nil, sessions, req,
		transportKind{local: true}, runner)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	binDir := t.TempDir()
	writeArgvEcho(t, binDir)
	argv := runLineArgv(t, binDir, boot.Bootstrap)
	want := []string{
		"-i", "/home/u/.ssh/id key",
		"-o", "StrictHostKeyChecking=no",
		"-J", "bastion.example.com",
		"-p", "2222",
		"alice@box.example.com",
	}
	if len(argv) != len(want) {
		t.Fatalf("the refused line ran ssh with %d arguments, want %d:\n got %q\nwant %q", len(argv), len(want), argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (whole argv %q)", i, argv[i], want[i], argv)
		}
	}
}

// ---------------------------------------------------------------------------
// Assertion 22: the process's exit status is preserved EXACTLY. The user's
// own process is what runs, so this is a property of not having wrapped it —
// and it is asserted through the seam the parent shell uses, an eval of the
// composed text.

func TestTypedLine_PreservesTheProcessExitStatusExactly(t *testing.T) {
	wrap := ssh.TypedWrap{MuxOptions: []string{
		"-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/nx/m-%C", "-o", "ControlPersist=no",
	}}
	inv := ssh.TypedInvocation{
		Host: "box.example.com", User: "alice", Port: 2222,
		Opts: []string{"-i", "/home/u/.ssh/id key"},
	}
	line := composeSSHLine(wrap, []string{"-t"}, inv, "exec /nocx/loader")

	for _, status := range []int{0, 1, 42, 255} {
		binDir := t.TempDir()
		script := "#!/bin/sh\nexit " + strings.TrimSpace(strings.Trim(strings.Join([]string{itoa(status)}, ""), " ")) + "\n"
		// #nosec G306 — a stand-in for ssh must be executable to be found on PATH.
		if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
			t.Fatalf("write ssh stand-in: %v", err)
		}
		cmd := exec.Command("bash", "-c", "PATH="+binDir+":$PATH\n"+line+"\n") // #nosec G204 — the composed line under test.
		_ = cmd.Run()
		if got := cmd.ProcessState.ExitCode(); got != status {
			t.Fatalf("the wrapped line reported %d, want the %d the process exited with", got, status)
		}
	}
}

// itoa keeps the exit-status table readable without importing strconv for one
// call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Assertion 18, at this seam: nothing is published before the handshake
// proves ownership of that specific socket.

func TestTypedLine_NothingIsPublishedBeforeOwnershipIsProven(t *testing.T) {
	runner, _, pub, dialer := typedTestRunner(t, refusingOracle{})
	d := &typedDelivery{
		runner:         runner,
		sessionID:      "aabbccddeeff00112233445566778899",
		controlPath:    filepath.Join(t.TempDir(), "m"),
		window:         newHarnessWindow(),
		publishSettled: make(chan struct{}),
	}
	win, ok := d.window.(*harnessWindow)
	if !ok {
		t.Fatalf("window is %T, want *harnessWindow", d.window)
	}
	win.attach(devNull(t))

	// The dialer never succeeds, so ownership is never proven.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.doRun(ctx)

	if n := pub.callCount(); n != 0 {
		t.Fatalf("%d publishes without a proven master, want 0", n)
	}
	if dialer.callCount() == 0 {
		t.Fatal("the delivery never asked the control socket anything; the proof it skipped is the one that gates everything")
	}
}

// devNull is the discard terminal the harness window writes its frames to,
// and it hands out the *os.File itself — ONE owner of the descriptor, whose
// single close is this cleanup.
//
// It used to hand out the raw number (`devNullFD(t) int`) and both callers
// wrapped it in a SECOND os.File. That is two owners of one descriptor, and
// the second one closes it at a time nobody chose: every *os.File carries a
// finalizer that closes its fd, so when the wrapper became unreachable at the
// end of its test, the GC closed the descriptor somewhere inside a LATER
// test — by which time the number had been handed to whatever that test
// opened next. The harness then wrote a bootstrap frame to a descriptor that
// was no longer its own, the write failed with EBADF, and the delivery did
// exactly the right thing with a genuinely broken window: it reported
// bootstrap-interrupted. THE PRODUCT WAS NEVER WRONG (nocx-m8jwn.4; CI run
// 32479199321).
//
// Measured over 500 runs of the test that caught it: 27 failures, every one
// of them carrying the EBADF; 0 when the wrapper was merely kept alive, which
// is what names the finalizer rather than anything in the delivery; 0 with
// this one-owner form. And the blast radius was never this test — a
// descriptor closed under its owner is whatever the package opened next, a
// websocket or a database file just as easily as this.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// ---------------------------------------------------------------------------
// A refused auxiliary channel refuses the delivery. It never opens a
// connection — the adapter has no code that could — so the observable is that
// the publish did not happen and the session is still the user's.

func TestTypedLine_ARefusedAuxiliaryChannelPublishesNothing(t *testing.T) {
	runner, _, pub, _ := typedTestRunner(t, refusingOracle{})
	own := ssh.NewOwnership(log.NewSlogAdapter(nil), "/nx/m", 1, ssh.MasterProbes{}, ssh.SystemClock{})
	d := &typedDelivery{
		runner:         runner,
		sessionID:      "sid",
		controlPath:    "/nx/m",
		publishSettled: make(chan struct{}),
	}
	d.publish(context.Background(), own, refusingMaster{})
	<-d.publishSettled
	if n := pub.callCount(); n != 0 {
		t.Fatalf("%d publishes after a refused subsystem, want 0 — a refusal refuses the delivery, it never opens a connection", n)
	}
}

// refusingMaster answers every auxiliary request the way a master whose
// server has run out of sessions does.
type refusingMaster struct{}

func (refusingMaster) PID() int    { return 1 }
func (refusingMaster) Exit() error { return nil }
func (refusingMaster) Close() error {
	return nil
}

func (refusingMaster) Aux(mux.SessionRequest) (io.ReadWriteCloser, error) {
	return nil, mux.ErrSessionRefused
}

// ---------------------------------------------------------------------------
// Helpers

// writeArgvEcho installs a stand-in `ssh` that prints its argv one entry per
// line, so an argument that was split or joined by quoting is visible as such.
func writeArgvEcho(t *testing.T, dir string) {
	t.Helper()
	// #nosec G306 — a stand-in for ssh must be executable to be found on PATH.
	if err := os.WriteFile(filepath.Join(dir, "ssh"),
		[]byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatalf("write ssh stand-in: %v", err)
	}
}

func runLineArgv(t *testing.T, binDir, line string) []string {
	t.Helper()
	// #nosec G204 — the composed line under test plus a PATH pointing at this
	// test's temp dir; evaluating it under a real bash IS the assertion.
	cmd := exec.Command("bash", "-c", "PATH="+binDir+":$PATH\n"+line+"\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating the composed line: %v\n%s\nline:\n%s", err, out, line)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// ---------------------------------------------------------------------------
// The canary, on the typed path. ADR-0035's subject in one assertion: the
// line the parent shell runs carries neither bearer and no bundle. Both
// values are unmistakable strings, so a test that finds them is finding
// exactly the defect, and one that does not is not passing by accident — the
// same detector finds them in a command shaped the way the retired one was.

func TestTypedLine_TheLineCarriesNeitherBearerNorTheBundle(t *testing.T) {
	const (
		canaryCapability = "a7f3c9d1e5b284060000000000000000000000000000000000000000000000ff"
		canaryRecovery   = "b8e4da02f6c395170000000000000000000000000000000000000000000000ff"
	)
	opts := shellintegration.LaunchOptions{
		SessionID: "aabbccddeeff00112233445566778899", Enhanced: true,
		Lane: "lane-0123456789abcdef", Domain: "dom-0123456789abcdef", Epoch: 7,
		LifecyclePort: 40123,
		Capability:    canaryCapability,
		Recovery:      canaryRecovery,
	}
	stage, err := shellintegration.Stage1Frame(shellintegration.ShellAuto, opts)
	if err != nil {
		t.Fatalf("stage-1: %v", err)
	}
	opts.StageDigest = shellintegration.StageDigest(stage)
	carrier, _, ok := shellintegration.NewRemoteLauncher().StartCommand(shellintegration.ShellAuto, opts)
	if !ok {
		t.Fatal("the carrier declined")
	}

	wrap := ssh.TypedWrap{MuxOptions: []string{
		"-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/nocx-mux-0/m-%C", "-o", "ControlPersist=no",
	}}
	inv := ssh.TypedInvocation{Host: "box.example.com", User: "alice", Port: 2222}
	line := composeSSHLine(wrap, []string{"-t", "-R", "127.0.0.1:40123:127.0.0.1:37777"}, inv, carrier)

	for name, secret := range map[string]string{"capability": canaryCapability, "recovery fence": canaryRecovery} {
		if strings.Contains(line, secret) {
			t.Errorf("the %s is in the line the parent shell runs; it would reach the far host's argv", name)
		}
		// Not merely absent as a whole: no half of it either, which is what
		// an encoding step would leave behind.
		if strings.Contains(line, secret[:16]) {
			t.Errorf("a fragment of the %s is in the line", name)
		}
	}
	// The detector works: the same check finds both in a command shaped the
	// way the retired launcher's was.
	asItWas := "/bin/sh -c 'NOCX_CAP=" + canaryCapability + "; NOCX_REC=" + canaryRecovery + "'"
	if !strings.Contains(asItWas, canaryCapability) || !strings.Contains(asItWas, canaryRecovery) {
		t.Error("the detector cannot find a bearer that is plainly there; the assertion above proves nothing")
	}
	// And the bundle is not in it either: the whole line is bounded by the
	// carrier's own 1 KiB plus the user's words.
	if len(line) > shellintegration.MaxCarrierLen+512 {
		t.Errorf("the line is %d bytes; the bundle is back in the command", len(line))
	}
	t.Logf("the typed line is %d bytes, of which the carrier is %d", len(line), len(carrier))
}

// ---------------------------------------------------------------------------
// Design §5.3: the input interval closes AT THE TERMINAL OUTCOME.
//
// "The interval closes at exactly one terminal outcome — BOOTSTRAP_ACCEPTED
// or BOOTSTRAP_REFUSED(reason) — and input is re-enabled on that outcome,
// never on READY", and again on the window: "it closes at exactly one
// terminal outcome […] no later than the integration deadline of §7".
//
// Only the deferred Close stood for that edge, and a defer runs after
// awaitMasterEnd — a DIFFERENT interval, which ends when the user's own ssh
// master does. So a session that had just come up integrated went on refusing
// every keystroke for the whole life of that master, and said so on screen:
// "this connection has stopped accepting input". e2e/nocxify-journey.spec.ts
// found it; no Go test could, because the one that drives a typed session
// end to end types straight into the pty and never touches the session write
// queue where the quarantine lives.
//
// The observable is ORDERING, so the master here never ends: the window must
// be closed while the delivery is still inside awaitMasterEnd.
func TestTypedLine_TheInputQuarantineEndsWithTheOutcomeNotWithTheMaster(t *testing.T) {
	runner, _, _, _ := typedTestRunner(t, refusingOracle{})
	win := &orderingWindow{closed: make(chan struct{})}
	master := &livingMaster{}
	runner.dial = func(string) (TypedMaster, error) { return master, nil }
	// Probes that never report a loss: this master outlives the bootstrap,
	// which is the ordinary case and the one the defect hid in.
	runner.probes = func(string, int, TypedMaster) ssh.MasterProbes {
		return ssh.MasterProbes{
			SocketPresent: func(string) bool { return true },
			ProcessAlive:  func(int) bool { return true },
			Terminate:     func() error { return nil },
		}
	}

	d := &typedDelivery{
		runner:         runner,
		sessionID:      "aabbccddeeff00112233445566778899",
		controlPath:    filepath.Join(t.TempDir(), "m"),
		window:         win,
		publishSettled: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.doRun(ctx)
	}()

	select {
	case <-win.closed:
	case <-done:
		t.Fatal("the delivery returned before the window was closed; this case can no longer see the ordering it is about")
	case <-time.After(30 * time.Second):
		t.Fatal("the bootstrap window was never closed: the user's keystrokes are refused for as long as their own ssh master lives")
	}

	// And the paired half: the quarantine was actually opened, so the test
	// above is not passing against a delivery that never quarantined
	// anything.
	if !win.quarantined() {
		t.Error("input was never quarantined; §5.3's interval has no opening edge either")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the delivery did not return after its context was cancelled")
	}
}

// orderingWindow is a bootstrap window that refuses to answer, so the
// delivery reaches its terminal outcome at once, and that records WHEN it was
// closed rather than only whether.
type orderingWindow struct {
	mu     sync.Mutex
	quar   bool
	closed chan struct{}
	once   sync.Once
}

// ReadLine answers EOF immediately: there is no far side in this test, so the
// bootstrap refuses on its first token instead of spending a deadline.
func (w *orderingWindow) ReadLine(context.Context, time.Duration) (string, error) {
	return "", io.EOF
}

func (w *orderingWindow) Write(p []byte) (int, error) { return len(p), nil }

func (w *orderingWindow) QuarantineInput() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.quar = true
}

func (w *orderingWindow) quarantined() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.quar
}

func (w *orderingWindow) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

// livingMaster is a master that is still there: its auxiliary channels are
// refused (this test is not about the publish) and it never exits.
type livingMaster struct{}

func (*livingMaster) PID() int    { return os.Getpid() }
func (*livingMaster) Exit() error { return nil }
func (*livingMaster) Close() error {
	return nil
}

func (*livingMaster) Aux(mux.SessionRequest) (io.ReadWriteCloser, error) {
	return nil, mux.ErrSessionRefused
}
