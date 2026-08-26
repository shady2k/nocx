package shellintegration

// The bootstrap progress channel (nocx-yww2), driven through real shells.
//
// The defect these tests report is the one the owner is looking at: nocx's
// rcfile reads the user's own startup file FIRST and by design, so a user rc
// that hands the shell to another terminal's integration ends our process
// image before the install line is ever reached. Measured on the shipped app,
// the bare shell underneath that wrapper inherits the environment but not our
// descriptors, and the wrapper holds our end of the socketpair open — so there
// is no EOF either, and the only signal left is ten seconds of silence that
// looks identical to a shell that never started.
//
// A negative fact cannot be proved by waiting, so it is not: each test asserts
// something POSITIVE and the pair of them discriminates. The hijack tests wait
// for the wrapper's own marker on the pty — after which the set of bytes ever
// written to the progress descriptor is fixed, because the process that would
// have written the second fact no longer exists — and assert the first fact
// arrived. The paired tests on an ordinary rc assert the second fact arrives
// and the handshake completes. The product's sentence for the difference is
// asserted where it is produced, over the real socket, in internal/transport.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/bootstrapprogress"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/testwait"
)

// progressLog records the stages the reader reports, in order.
type progressLog struct {
	mu     sync.Mutex
	stages []bootstrapprogress.Stage
}

func (p *progressLog) note(s bootstrapprogress.Stage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stages = append(p.stages, s)
}

func (p *progressLog) all() []bootstrapprogress.Stage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bootstrapprogress.Stage(nil), p.stages...)
}

func (p *progressLog) saw(want bootstrapprogress.Stage) bool {
	for _, s := range p.all() {
		if s == want {
			return true
		}
	}
	return false
}

// waitForStage blocks until the stage is reported. It waits on a state change
// rather than on a duration; the deadline exists only so a failure reports
// something better than a hung test.
func (p *progressLog) waitForStage(t *testing.T, want bootstrapprogress.Stage) {
	t.Helper()
	testwait.WaitForTimeout(t, fmt.Sprintf("bootstrap stage %q", want), 20*time.Second, func() bool {
		return p.saw(want)
	})
}

// foreignTerminalWrapper writes the shape a second shell integration installs
// into the user's rc: a wrapper that replaces our shell process, KEEPS the
// descriptors it inherited (so nothing ever sees an EOF) and starts its own
// bare interactive shell without them. It announces itself on the pty first,
// which is the observable event the assertions are ordered against.
func foreignTerminalWrapper(t *testing.T, bareShell, noRCFlags string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "foreign-term")
	body := "#!/bin/sh\n" +
		"printf 'FOREIGN_WRAPPER_UP\\n'\n" +
		// No exec: the wrapper stays alive holding fds 3 and 4, exactly as the
		// measured one does. The inner shell gets neither (3>&- 4>&-), so it
		// could not speak the lifecycle protocol even if it had our hooks.
		bareShell + " " + noRCFlags + " -i 3>&- 4>&-\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatalf("write wrapper: %v", err)
	}
	return path
}

// progressSession is one real shell started the way the composition root
// starts it: the tier's own launch, the lifecycle socketpair on fd 3 and the
// bootstrap progress pipe on fd 4.
type progressSession struct {
	shell    *channelShell
	progress *progressLog
	kernel   *fakeKernel
}

func startProgressSession(t *testing.T, shellPath, home string) *progressSession {
	t.Helper()

	opts := localTestOpts()
	opts.BootstrapFD = 4
	launch, err := LocalEnhancedLaunch(shellPath, LocalShellKind(shellPath), opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch: %v", err)
	}

	kernelFile, shellFile := lifecycleSocketpair(t)

	plog := &progressLog{}
	reader, progressChild, err := bootstrapprogress.New(log.NewSlogAdapter(nil), plog.note)
	if err != nil {
		t.Fatalf("bootstrapprogress.New: %v", err)
	}

	// #nosec G204 — launch.Command is the requireShell-resolved shell; a real
	// interactive shell on a real pty is the only way to observe what a user
	// gets out of the rcfile.
	cmd := exec.Command(launch.Command, launch.Args...)
	// fd 3 then fd 4, the order internal/app hands them to pty.WithExtraFiles
	// and the order LaunchOptions.BootstrapFD assumes.
	cmd.ExtraFiles = []*os.File{shellFile, progressChild}
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
		append(launch.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000")...,
	)

	k := newFakeKernel(t, testCap)
	go k.serveFile(kernelFile)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		launch.Cleanup()
		t.Fatalf("pty start: %v", err)
	}
	_ = progressChild.Close()
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = reader.Close()
		launch.Cleanup()
	})
	return &progressSession{shell: s, progress: plog, kernel: k}
}

// A user rc that hands the shell to another terminal's integration is
// DETECTED: nocx's rcfile says it began, and never says the user's startup
// returned, because it no longer exists to say it. That pair is the whole
// diagnosis — before it, this session produced no signal at all until the
// handshake bound expired ten seconds later, and that bound says only that ten
// seconds passed.
func TestBashBootstrapProgress_AUserRcThatTakesTheShellStopsAtStartupEntered(t *testing.T) {
	bash := requireShell(t, "bash")
	home := t.TempDir()
	wrapper := foreignTerminalWrapper(t, bash, "--norc --noprofile")
	// Line 1 of the user's rc, which is where Kiro CLI, Amazon Q and Fig all
	// install: the shell is handed over before anything of ours runs.
	if err := os.WriteFile(filepath.Join(home, ".bashrc"),
		[]byte("exec "+ShellQuote(wrapper)+"\n"), 0o600); err != nil {
		t.Fatalf("write user .bashrc: %v", err)
	}

	s := startProgressSession(t, bash, home)

	// The wrapper is up, so the exec has happened and no further byte can ever
	// reach the progress descriptor from our rcfile.
	var verdict string
	testwait.WaitForTimeout(t, "the foreign wrapper verdict", 20*time.Second, func() bool {
		out := s.shell.output()
		ia, ib := strings.LastIndex(out, "FOREIGN_WRAPPER_UP"), strings.LastIndex(out, "__NOCX_NEVER__")
		if ia == ib {
			return false
		}
		if ia > ib {
			verdict = "FOREIGN_WRAPPER_UP"
		} else {
			verdict = "__NOCX_NEVER__"
		}
		return true
	})
	if verdict != "FOREIGN_WRAPPER_UP" {
		t.Fatalf("the fixture's wrapper never took the shell: %q", s.shell.output())
	}
	s.progress.waitForStage(t, bootstrapprogress.StageStartupEntered)
	if s.progress.saw(bootstrapprogress.StageUserRCReturned) {
		t.Errorf("the user's rc exec'd away and nocx still recorded it as returning: %v", s.progress.all())
	}
	// And the session really is the failure being diagnosed: nothing ever
	// authenticated, so a product that only watched the channel would have
	// nothing but silence to report.
	if s.kernel.count("hello") != 0 {
		t.Errorf("the hijacked session completed a handshake; the fixture does not reproduce the defect")
	}
}

// The paired case, and the one that has to keep working: an ordinary user rc
// returns, both facts arrive in order, and the session integrates. Without it
// the test above would pass just as well against a channel that never carries
// the second fact at all.
//
// It carries the capability assertion too, because it is a property of the
// same shell: the user's rc runs BEFORE __nocx_cap is assigned, so the code
// most likely to be hostile by accident cannot read the capability — and the
// fact that says the rc returned is written before that assignment, so buying
// the diagnosis costs the availability window not one byte.
func TestBashBootstrapProgress_AnOrdinaryRcReturnsAndTheCapabilityIsInvisibleToIt(t *testing.T) {
	bash := requireShell(t, "bash")
	home := t.TempDir()
	seen := filepath.Join(t.TempDir(), "cap-as-the-user-rc-saw-it")
	if err := os.WriteFile(filepath.Join(home, ".bashrc"),
		[]byte("printf '[%s]\\n' \"${__nocx_cap-UNSET}\" > "+ShellQuote(seen)+"\n"), 0o600); err != nil {
		t.Fatalf("write user .bashrc: %v", err)
	}

	s := startProgressSession(t, bash, home)

	s.progress.waitForStage(t, bootstrapprogress.StageStartupEntered)
	s.progress.waitForStage(t, bootstrapprogress.StageUserRCReturned)
	if got := s.progress.all(); len(got) != 2 || got[0] != bootstrapprogress.StageStartupEntered {
		t.Errorf("stages = %v, want startup-entered then user-rc-returned exactly once each", got)
	}
	// The shell that reports those facts is a working, integrated one.
	s.shell.waitForHandshake()

	body, err := os.ReadFile(seen) //nolint:gosec // a path this test minted
	if err != nil {
		t.Fatalf("the user's rc never ran: %v", err)
	}
	if strings.TrimSpace(string(body)) != "[UNSET]" {
		t.Errorf("the user's own rc could read the capability: %q", strings.TrimSpace(string(body)))
	}
}

// The zsh tier, which is not optional: the owner's machine carries the foreign
// integration's block in BOTH rc files, and macOS has defaulted to zsh since
// Catalina. A diagnosis that existed only for bash would miss the platform the
// product ships to.
func TestZshBootstrapProgress_AUserRcThatTakesTheShellStopsAtStartupEntered(t *testing.T) {
	zsh := requireShell(t, "zsh")
	unsetZDOTDIR(t)
	home := t.TempDir()
	wrapper := foreignTerminalWrapper(t, zsh, "-f")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("exec "+ShellQuote(wrapper)+"\n"), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}

	s := startProgressSession(t, zsh, home)

	var verdict string
	testwait.WaitForTimeout(t, "the foreign wrapper verdict", 20*time.Second, func() bool {
		out := s.shell.output()
		ia, ib := strings.LastIndex(out, "FOREIGN_WRAPPER_UP"), strings.LastIndex(out, "__NOCX_NEVER__")
		if ia == ib {
			return false
		}
		if ia > ib {
			verdict = "FOREIGN_WRAPPER_UP"
		} else {
			verdict = "__NOCX_NEVER__"
		}
		return true
	})
	if verdict != "FOREIGN_WRAPPER_UP" {
		t.Fatalf("the fixture's wrapper never took the shell: %q", s.shell.output())
	}
	s.progress.waitForStage(t, bootstrapprogress.StageStartupEntered)
	if s.progress.saw(bootstrapprogress.StageUserRCReturned) {
		t.Errorf("the user's rc exec'd away and nocx still recorded it as returning: %v", s.progress.all())
	}
	if s.kernel.count("hello") != 0 {
		t.Errorf("the hijacked session completed a handshake; the fixture does not reproduce the defect")
	}
}

// zsh's paired success, with the same capability assertion. The two tiers
// render from different templates, so neither answer carries over.
func TestZshBootstrapProgress_AnOrdinaryRcReturnsAndTheCapabilityIsInvisibleToIt(t *testing.T) {
	zsh := requireShell(t, "zsh")
	unsetZDOTDIR(t)
	home := t.TempDir()
	seen := filepath.Join(t.TempDir(), "cap-as-the-user-rc-saw-it")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("printf '[%s]\\n' \"${__nocx_cap-UNSET}\" > "+ShellQuote(seen)+"\n"), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}

	s := startProgressSession(t, zsh, home)

	s.progress.waitForStage(t, bootstrapprogress.StageStartupEntered)
	s.progress.waitForStage(t, bootstrapprogress.StageUserRCReturned)
	if got := s.progress.all(); len(got) != 2 || got[0] != bootstrapprogress.StageStartupEntered {
		t.Errorf("stages = %v, want startup-entered then user-rc-returned exactly once each", got)
	}
	s.shell.waitForHandshake()

	body, err := os.ReadFile(seen) //nolint:gosec // a path this test minted
	if err != nil {
		t.Fatalf("the user's rc never ran: %v", err)
	}
	if strings.TrimSpace(string(body)) != "[UNSET]" {
		t.Errorf("the user's own rc could read the capability: %q", strings.TrimSpace(string(body)))
	}
}

// The ordering that makes the whole construction safe, asserted on the
// rendered text of both tiers rather than inferred from a shell's behaviour:
// the fact that says the user's rc returned is written BEFORE the capability
// exists. Move the write one line down and every test above still passes while
// the availability window silently widens to include the user's own rc — which
// is the one thing this channel was not allowed to cost.
func TestRenderedRcfiles_ReportTheUserRcReturnedBeforeTheCapabilityExists(t *testing.T) {
	opts := localTestOpts()
	opts.BootstrapFD = 4

	bash, err := LocalBashRcfile(opts)
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	zsh, err := LocalZshRcfile(opts)
	if err != nil {
		t.Fatalf("LocalZshRcfile: %v", err)
	}
	for name, rc := range map[string]string{"bash": bash, "zsh": zsh} {
		fact := strings.Index(rc, "user-rc-returned")
		capAt := strings.Index(rc, "__nocx_cap='")
		if fact < 0 {
			t.Errorf("%s: the rendered rcfile reports no user-rc-returned fact", name)
			continue
		}
		if capAt < 0 {
			t.Errorf("%s: the rendered rcfile carries no capability", name)
			continue
		}
		if fact > capAt {
			t.Errorf("%s: the user-rc-returned fact is written after the capability is assigned, "+
				"which widens the window in which the user's own rc can read it", name)
		}
		if !strings.Contains(rc, "NOCX_BOOTSTRAP_FD=4") {
			t.Errorf("%s: the rendered rcfile never learns the progress descriptor", name)
		}
		// And the first fact precedes the user's startup, or "our rcfile
		// began" would be indistinguishable from "the user's rc returned".
		entered := strings.Index(rc, "startup-entered")
		if entered < 0 || entered > fact {
			t.Errorf("%s: startup-entered is not written before user-rc-returned", name)
		}
	}
}

// A session with no progress descriptor renders an rcfile that reports
// nothing and, above all, breaks nothing: every remote tier is in exactly that
// state, because there is no second descriptor to hand a far shell.
func TestRenderedRcfiles_WithoutAProgressDescriptorSayNothing(t *testing.T) {
	opts := localTestOpts() // BootstrapFD left at zero
	bash, err := LocalBashRcfile(opts)
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	zsh, err := LocalZshRcfile(opts)
	if err != nil {
		t.Fatalf("LocalZshRcfile: %v", err)
	}
	for name, rc := range map[string]string{"bash": bash, "zsh": zsh} {
		// The guard still READS the variable — that is how it discovers there
		// is nothing to report — so what must be absent is the assignment and
		// the export the launcher would have rendered for a real descriptor.
		if strings.Contains(rc, "NOCX_BOOTSTRAP_FD=") {
			t.Errorf("%s: a session with no progress pipe still exports a descriptor number", name)
		}
		if strings.Contains(rc, "export NOCX_SHELL_INTEGRATION") &&
			strings.Contains(rc, " NOCX_BOOTSTRAP_FD\n") {
			t.Errorf("%s: a session with no progress pipe still names the descriptor in its export list", name)
		}
	}
}

// A shell whose progress descriptor is not open must still come up. The guard
// is a real failure path — the remote tiers take it on every launch — and a
// redirection to a closed descriptor is precisely the kind of failure that
// ends a shell under an inherited errexit and prints on the user's terminal.
//
// The premise — fd 4 is not open in the shell — is ASSERTED here rather than
// assumed, because assuming it is what made this test report the machine's
// state instead of the product's behaviour (nocx-dsie): the fixture's own
// socketpair used to leak into the shell, and on a process where the kernel's
// end landed on fd 4 the "closed" descriptor was open, was the lifecycle
// socket, and the redirection this test says costs nothing corrupted the
// handshake. The shell answers the question itself: `>&4` only DUPS, so the
// probe cannot write a byte into whatever is there if the premise is broken.
func TestBootstrapProgress_AClosedDescriptorCostsNoTerminal(t *testing.T) {
	for _, family := range []string{"bash", "zsh"} {
		t.Run(family, func(t *testing.T) {
			shellPath := requireShell(t, family)
			home := t.TempDir()
			rcName := ".bashrc"
			if family == "zsh" {
				rcName = ".zshrc"
				unsetZDOTDIR(t)
			}
			// An rc that turns errexit on, which is what makes a failed
			// redirection fatal rather than merely noisy, and that reports
			// what the shell can see on fd 4.
			rc := "set -e\n" +
				"if { : >&4; } 2>/dev/null; then printf 'NOCX_FD4_OPEN\\n'; else printf 'NOCX_FD4_CLOSED\\n'; fi\n"
			if err := os.WriteFile(filepath.Join(home, rcName), []byte(rc), 0o600); err != nil {
				t.Fatalf("write user rc: %v", err)
			}

			opts := localTestOpts()
			opts.BootstrapFD = 4 // announced, and deliberately never handed over
			launch, err := LocalEnhancedLaunch(shellPath, LocalShellKind(shellPath), opts)
			if err != nil {
				t.Fatalf("LocalEnhancedLaunch: %v", err)
			}
			kernelFile, shellFile := lifecycleSocketpair(t)

			// #nosec G204 — shellPath is requireShell-resolved.
			cmd := exec.Command(launch.Command, launch.Args...)
			cmd.ExtraFiles = []*os.File{shellFile} // fd 3 only: fd 4 does not exist
			cmd.Env = append(
				cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
				append(launch.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000")...,
			)
			k := newFakeKernel(t, testCap)
			go k.serveFile(kernelFile)
			ptmx, err := pty.Start(cmd)
			if err != nil {
				launch.Cleanup()
				t.Fatalf("pty start: %v", err)
			}
			s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
			go s.readPump()
			t.Cleanup(func() {
				_ = ptmx.Close()
				_ = cmd.Process.Kill()
				launch.Cleanup()
			})

			// The premise first, on the shell's own word: a descriptor that
			// IS open would make everything below vacuous.
			var verdict string
			testwait.WaitForTimeout(t, "the fd 4 descriptor verdict", 20*time.Second, func() bool {
				out := s.output()
				ia, ib := strings.LastIndex(out, "NOCX_FD4_CLOSED"), strings.LastIndex(out, "NOCX_FD4_OPEN")
				if ia == ib {
					return false
				}
				if ia > ib {
					verdict = "NOCX_FD4_CLOSED"
				} else {
					verdict = "NOCX_FD4_OPEN"
				}
				return true
			})
			if verdict != "NOCX_FD4_CLOSED" {
				t.Fatalf("fd 4 is open in the shell, so this test never exercised a closed descriptor: "+
					"the fixture leaked one; output=%q", s.output())
			}

			// The session integrates anyway — the strongest statement that the
			// missing descriptor cost nothing — and the shell's own terminal
			// carries no complaint about it.
			s.waitForHandshake()
			if strings.Contains(strings.ToLower(s.output()), "bad file descriptor") {
				t.Errorf("a missing progress descriptor printed on the user's terminal: %q", s.output())
			}
		})
	}
}

// forEachBashProgress is the reminder that the oldest bash this must work on
// is macOS's 3.2.57, which has neither {var} redirections nor read -N. The
// tests above run against the machine's default bash; this one runs the
// rendered rcfile's own progress block against every bash installed.
func TestBootstrapProgressBlock_RunsOnEveryBashOnThisMachine(t *testing.T) {
	forEachBash(t, func(t *testing.T, bash string) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		defer func() { _ = r.Close() }()
		// The two blocks the template installs, lifted verbatim in shape: the
		// guard, the first fact, the second fact and the close.
		script := `__nocx_bp_fd="${NOCX_BOOTSTRAP_FD:-}"
case "${__nocx_bp_fd}" in
    ''|*[!0-9]*) __nocx_bp_fd='' ;;
    *) { builtin printf 'startup-entered\n' >&"${__nocx_bp_fd}"; } 2>/dev/null || : ;;
esac
if [ -n "${__nocx_bp_fd}" ]; then
    { builtin printf 'user-rc-returned\n' >&"${__nocx_bp_fd}"; } 2>/dev/null || :
    { eval "exec ${__nocx_bp_fd}>&-"; } 2>/dev/null || :
fi
`
		// #nosec G204 — bash is a path forEachBash resolved on this machine.
		cmd := exec.Command(bash, "-c", script)
		cmd.ExtraFiles = []*os.File{w, w} // fds 3 and 4; only 4 is written
		cmd.Env = cleanEnv("NOCX_BOOTSTRAP_FD=4")
		out, err := cmd.CombinedOutput()
		_ = w.Close()
		if err != nil {
			t.Fatalf("%s: %v (%s)", bash, err, out)
		}
		if len(out) != 0 {
			t.Errorf("%s: the progress block printed on the terminal: %q", bash, out)
		}
		buf := make([]byte, 128)
		n, _ := r.Read(buf)
		if got := string(buf[:n]); got != "startup-entered\nuser-rc-returned\n" {
			t.Errorf("%s: facts = %q, want both in order", bash, got)
		}
	})
}
