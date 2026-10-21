package shellintegration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/waittest"
)

// The LOCAL zsh tier (nocx-wwz0). Every other terminal on macOS opens the
// user's login shell; nocx opened bash, for every user of the platform it ships
// to, because the local enhanced session had exactly one tier. These tests are
// the ones that report whether a zsh user gets their own shell AND keeps shell
// integration — not whether the code that was written does what it was written
// to do, so they start the shell the way the product starts it and assert what
// the user gets.
//
// SINCE nocx-ie23r.3 THE PRODUCT STARTS IT THE OTHER WAY, and this file
// follows it. A local pane is forked by this machine's helper daemon now, and
// the daemon delivers the integration through an inherited PIPE rather than
// through a transient ZDOTDIR on disk (LocalEnhancedLaunchInMemory) — the same
// delivery every helper-hosted session has always used, which is D11's "no
// code path that exists only for one of them" arriving where it was aimed.
//
// The on-disk tier is gone with its only caller, and so are the tests that
// were about the transient directory itself: where it was written, that it was
// 0700, that it carried the login-phase files, that it was erased before the
// user's rc ran, and that ZDOTDIR came back to what the user had. None of
// those states exists any more — the user's own zsh starts as their own zsh,
// with their own ZDOTDIR untouched, because nothing shadows it. What survives
// here is the assertion that was never about the mechanism: a zsh user gets
// THEIR shell, with THEIR startup files, integrated, and without the
// capability in the environment.

// Local classification preserves the account's actual shell; unsupported
// variants are conventional rather than silently replaced with Bash.
func TestLocalShellKind(t *testing.T) {
	tests := []struct {
		path string
		want ShellKind
	}{
		{"/bin/zsh", ShellZsh},
		{"/opt/homebrew/bin/zsh", ShellZsh},
		{"/usr/local/bin/zsh", ShellZsh},
		{"-zsh", ShellZsh}, // login(1)'s argv[0] convention
		{"/bin/bash", ShellBash},
		{"/run/current-system/sw/bin/bash", ShellBash},
		{"-bash", ShellBash},
		{"/usr/local/bin/fish", ShellFish},
		{"/bin/tcsh", ShellUnknown},
		{"/bin/csh", ShellUnknown},
		{"/bin/dash", ShellUnknown},
		{"/bin/sh", ShellUnknown},
		{"/usr/bin/nushell", ShellUnknown},
		{"", ShellUnknown},
	}
	for _, tt := range tests {
		if got := LocalShellKind(tt.path); got != tt.want {
			t.Errorf("LocalShellKind(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// localTestOpts is the launch a local enhanced session is built from.
//
// LifecycleFD is 4 and not 3, and that is the in-memory tier's descriptor
// order rather than an arbitrary number: the rendered script is delivered on
// fd 3 (`/dev/fd/3`), so the lifecycle channel is the second inherited
// descriptor. internal/helper/session's LocalSpawner appends them in exactly
// that order, and this test starts the shell the same way for the same reason.
func localTestOpts() LaunchOptions {
	return LaunchOptions{
		SessionID:   "sid-local",
		Enhanced:    true,
		Capability:  testCap,
		Recovery:    strings.Repeat("ef", 32),
		Lane:        testLane,
		Domain:      testDom,
		Epoch:       testEpoch,
		LifecycleFD: 4,
	}
}

// unsetZDOTDIR removes ZDOTDIR for the duration of one test and puts the
// process environment back afterwards. t.Setenv cannot express it: setting the
// variable to the empty string is a different state, and this test is about
// the user who has none.
func unsetZDOTDIR(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("ZDOTDIR")
	if err := os.Unsetenv("ZDOTDIR"); err != nil {
		t.Fatalf("unset ZDOTDIR: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("ZDOTDIR", prev)
		}
	})
}

// TestLocalZshSession_IsIntegratedOnTheUsersOwnShell is nocx-wwz0's acceptance
// criterion driven end to end through the launch the product now makes: a REAL
// zsh started by LocalEnhancedLaunchInMemory on a REAL pty with a REAL
// lifecycle channel on the inherited descriptor.
//
// What it watches a user do: open a local tab on a machine whose login shell
// is zsh and get command blocks — the handshake, the OSC 133 markers, the
// command-existence snapshot completion needs, and a lifecycle start/complete
// for a command they ran. Before nocx-wwz0 the same machine got bash 3.2.57
// and none of the user's own environment; before nocx-ie23r.3 it got all of
// that from a shell the coordinator forked, which died with the coordinator.
func TestLocalZshSession_IsIntegratedOnTheUsersOwnShell(t *testing.T) {
	zsh := requireShell(t, "zsh")

	// The user's own ~/.zshrc: it must run, and its effects must survive into
	// the session. A zsh user moved to bash loses exactly this.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("alias mine='echo USER_RC_ALIAS'\nexport USER_RC_RAN=yes\n"), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}
	// No ZDOTDIR of our own, which is the ordinary case. Nothing sets one any
	// more, and this is where that would show.
	unsetZDOTDIR(t)

	opts := localTestOpts()
	launch, err := LocalEnhancedLaunchInMemory(zsh, ShellZsh, opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunchInMemory: %v", err)
	}

	kernelFile, shellFile := lifecycleSocketpair(t)

	// #nosec G204 — launch.Command is the requireShell-resolved zsh; starting
	// a real interactive shell is the only way to observe what a user gets.
	cmd := exec.Command(launch.Command, launch.Args...)
	// The script's reader first and the lifecycle channel second — fd 3 and
	// fd 4, the order LaunchOptions.LifecycleFD names and the order the
	// helper's spawner appends them in.
	cmd.ExtraFiles = append(append([]*os.File{}, launch.ExtraFiles...), shellFile)
	// NOCX_SNAPSHOT_WAIT_MS, for the reason the script states where it reads
	// it (nocx.zsh): 250 ms is the budget a HUMAN's first prompt may spend
	// waiting for the source-time snapshot job, and on timeout the payload is
	// deliberately left for a LATER prompt rather than delaying this one. So
	// the default makes "the snapshot has been emitted" a claim about how
	// fast the machine is. The first prompt waits for the FILE TO EXIST — an
	// observable state change — and 15 s is a hang detector rather than an
	// expectation.
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
		append(launch.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000", "NOCX_SNAPSHOT_WAIT_MS=15000")...,
	)

	k := newFakeKernel(t, testCap)
	go k.serveFile(kernelFile)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		launch.Abort()
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
	}()
	// Nothing is written into the pty: the tier delivers itself during the
	// shell's own startup, through the transient ZDOTDIR (nocx-1ebrs.8).
	launch.Cleanup()

	s.waitForHandshake()

	// The user's own rc ran and its effects are live, in the shell they
	// actually use. One command, because both are properties of one shell.
	s.run(`echo "RC=[${USER_RC_RAN-no}] SH=[$ZSH_NAME]"; mine`)
	out := s.output()
	if !strings.Contains(out, "RC=[yes]") {
		t.Errorf("the user's own ~/.zshrc did not run: %q", out)
	}
	if !strings.Contains(out, "USER_RC_ALIAS") {
		t.Errorf("an alias from the user's own ~/.zshrc is not available: %q", out)
	}
	if !strings.Contains(out, "SH=[zsh]") {
		t.Errorf("the session is not running zsh: %q", out)
	}

	// Shell integration, which is the half a conventional fallback would have
	// silently cost: the prompt markers, the command-existence snapshot the
	// completion dropdown needs (nocx-qduc), and the lifecycle start/complete
	// pair for the command just run.
	if !strings.Contains(out, "\x1b]133;A") {
		t.Errorf("no OSC 133 prompt marker — the session is not integrated: %q", out)
	}
	if !strings.Contains(out, "\x1b]636;H;") || !strings.Contains(out, "\x1b]636;S;") {
		t.Errorf("no OSC 636 hello/snapshot — completion would never learn a command name: %q", out)
	}
	for _, evt := range []string{"hello", "prompt_ready", "start", "complete"} {
		if k.count(evt) == 0 {
			t.Errorf("the kernel never saw %q; accepted=%v", evt, k.events())
		}
	}

	// The capability is usable BY the shell and absent FROM its environment —
	// the property the whole text-substitution design exists for (ADR-0024
	// decision 2), asked of the real process the user's own commands are
	// children of rather than of the rendered text.
	//
	// Both verdict words are SPLIT across two quoted strings: zsh's line
	// editor redraws the typed line in pieces, so a reader sampling the pty
	// sees a PREFIX of the echo, and this command's echo carries
	// "CAP_IN_ENVIRON" a whole clause before it carries "CAP_TEXT_ONLY".
	// Sampled in that window, the leak verdict would win on a session that
	// never leaked.
	//
	// The shell-variable half is asserted with it, in the same command:
	// "absent from the environment" is satisfied vacuously by a session that
	// never received a capability at all, and this is a security assertion,
	// so it may not be able to pass for the wrong reason.
	probe := `echo "CAP_VAR""=${__nocx_cap:+yes}"; ` +
		`env | grep -q ` + testCap[:16] + ` && echo "CAP_IN""_ENVIRON" || echo "CAP_TEXT""_ONLY"`
	if _, err := ptmx.Write([]byte(probe + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var verdict string
	waittest.WaitForTimeoutDetail(t, "one of the zsh capability verdicts", 20*time.Second,
		func() string { return fmt.Sprintf("output=%q", s.output()) },
		func() bool {
			out := s.output()
			ia, ib := strings.LastIndex(out, "CAP_TEXT_ONLY"), strings.LastIndex(out, "CAP_IN_ENVIRON")
			switch {
			case ia > ib:
				verdict = "CAP_TEXT_ONLY"
			case ib > ia:
				verdict = "CAP_IN_ENVIRON"
			default:
				return false
			}
			return true
		})
	if verdict != "CAP_TEXT_ONLY" {
		t.Errorf("the capability reached the session environment, where every child of the user's shell can read it: %q", s.output())
	}
	if !strings.Contains(s.output(), "CAP_VAR=yes") {
		t.Errorf("the shell does not hold the capability in its non-exported variable, so the check above passed on a session that had none: %q", s.output())
	}
}

// TestLocalZshPane_AStartupFileThatDrainsALowDescriptorStillIntegrates is
// nocx-1ebrs.8's regression test, in the owner's own words: a startup file
// that touches a low descriptor, takes real time and prints an error is a
// LEGAL user startup file, and the pane must still integrate.
//
// WHY THIS TEST AND NOT ONE MORE LAUNCHER-STRING ASSERTION. The shipped
// defect was not a wrong string: `zsh -l -i` plus the typed `. /dev/fd/3`
// is a correct spelling of a delivery that cannot work. The script arrived
// on descriptor 3 and the line that reads it was TYPED into the pty, so
// nothing ordered the two: the typeahead waited in the tty input queue
// through the user's whole startup, and ANY startup file that read,
// drained or closed that descriptor took the integration with it. A fast,
// tidy fixture rc hid it, which is how it shipped on macOS.
//
// So the fixture here does what a user's rc may legally do — `cat <&3`
// drains a descriptor nocx happened to be holding — and the assertions are
// the two the user can see: the session integrates (the handshake and the
// OSC 133 prompt markers arrive), and nocx's plumbing is NEVER printed
// into the transcript, not once, let alone twice.
func TestLocalZshPane_AStartupFileThatDrainsALowDescriptorStillIntegrates(t *testing.T) {
	zsh := requireShell(t, "zsh")

	home := t.TempDir()
	// The shape that found this: a startup file that drains a low
	// descriptor, takes real time, and fails on an export whose NAME is not a
	// shell identifier — the `(eval):export:N` line a secret-store block
	// produces. The eval is load-bearing rather than decorative: a bare
	// `export NAME-WITH-A-HYPHEN=…` aborts the REST of a sourced zsh file, so
	// a fixture that spelled it that way would be testing zsh's error
	// handling instead of the delivery.
	//
	// No value is printed by the product or by this test; the invalid name is
	// a generic one.
	rc := strings.Join([]string{
		"# a user startup file that touches the low descriptors a terminal",
		"# might be holding: it drains the one the script arrived on and closes",
		"# the one the lifecycle channel would have used.",
		"cat <&3 >/dev/null 2>/dev/null || true",
		"sleep 3",
		`eval "export NOCX-TEST-INVALID-NAME=1"`,
		"echo USER_RC_DONE",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}
	unsetZDOTDIR(t)

	s := startLocalPane(t, zsh, ShellZsh, home, localTestOpts())

	// One command, run to completion on BOTH transports: the completion is
	// accepted on the lifecycle channel and its whole render arrives on the
	// pty, which is what makes the buffer below a fixed quantity rather than
	// whatever the pump had appended when the assertion happened to run. A
	// shell that never integrated would leave this command unaccepted.
	s.run("echo INTEGRATION_LIVE")

	out := strings.ReplaceAll(s.output(), "\r", "")
	if !strings.Contains(out, "INTEGRATION_LIVE") {
		t.Fatalf("a command typed at the first prompt never rendered: %q", out)
	}
	if !strings.Contains(out, "USER_RC_DONE") {
		t.Fatalf("the user's own .zshrc did not run to its end: %q", out)
	}
	// Integration: the handshake (waited for by startLocalZshPane) and the
	// prompt markers a command block is drawn from.
	if s.kernel.count("hello") == 0 || s.kernel.count("prompt_ready") == 0 {
		t.Fatalf("the shell never proved itself on the lifecycle channel; accepted=%v", s.kernel.events())
	}
	if !strings.Contains(out, "\x1b]133;A") {
		t.Errorf("no OSC 133 prompt marker — the session is not integrated: %q", out)
	}
	// And the startup really was the noisy one this case is about: without
	// this line the assertions above could pass on a fixture whose error
	// never fired, which is the way a regression test stops reporting.
	if !strings.Contains(out, "not valid in this context") {
		t.Fatalf("the fixture's invalid export produced no error, so this test is not exercising the case it names: %q", out)
	}
	// And the plumbing never reaches the person. The delivery this replaced
	// typed `. /dev/fd/3` into the pty and a person saw it twice: once echoed
	// by the tty's line discipline while the rc ran, then again by zle when it
	// consumed the typeahead.
	if n := strings.Count(out, ". /dev/fd/3"); n > 0 {
		t.Errorf("nocx's bootstrap line is printed in the user's transcript %d time(s): %q", n, out)
	}
	if n := strings.Count(out, "/dev/fd/"); n > 0 {
		t.Errorf("a descriptor path is printed in the user's transcript %d time(s): %q", n, out)
	}
}

type localPaneKernel interface {
	kernelHarness
	serveFile(*os.File)
}

type localPaneOptions struct {
	kernel        localPaneKernel
	waitHandshake bool
}

// startLocalPane uses the helper's own launch shape, a real PTY, and its
// inherited lifecycle socket. Native language differences belong to the
// launcher, not a second test-only bootstrap path.
func startLocalPane(t *testing.T, shell string, kind ShellKind, home string, opts LaunchOptions) *channelShell {
	t.Helper()
	return startLocalPaneMode(t, shell, kind, home, opts, localPaneOptions{waitHandshake: true})
}

func startLocalPaneMode(t *testing.T, shell string, kind ShellKind, home string, opts LaunchOptions, mode localPaneOptions) *channelShell {
	t.Helper()
	env := cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null")
	opts.EnvOverrides = append(env, opts.EnvOverrides...)
	launch, err := LocalEnhancedLaunchInMemory(shell, kind, opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunchInMemory: %v", err)
	}
	if launch.Refusal != ReasonNone {
		t.Fatalf("the shell fixture has no supported native startup adapter: %s", launch.Refusal)
	}
	kernelFile, shellFile := lifecycleSocketpair(t)
	cmd := exec.Command(launch.Command, launch.Args...) //nolint:gosec // requireShell-resolved executable and authored launcher arguments
	cmd.ExtraFiles = append(append([]*os.File{}, launch.ExtraFiles...), shellFile)
	cmd.Env = append(opts.EnvOverrides, launch.Env...)
	cmd.Env = append(cmd.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000", "NOCX_SNAPSHOT_WAIT_MS=15000")
	k := mode.kernel
	if k == nil {
		k = newFakeKernel(t, testCap)
	}
	go k.serveFile(kernelFile)
	var terminal emulator.Terminal
	if kind == ShellFish {
		// Native Fish waits for terminal negotiation before its first prompt.
		// Use the product's terminal owner, not a second query-response parser.
		terminal, err = ghostty.New(emulator.Geometry{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20})
		if err != nil {
			launch.Abort()
			t.Fatal(err)
		}
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		launch.Abort()
		if terminal != nil {
			terminal.Close()
		}
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k, terminal: terminal}
	go s.readPump()
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	launch.Cleanup()
	if mode.waitHandshake {
		s.waitForHandshake()
	}
	return s
}
