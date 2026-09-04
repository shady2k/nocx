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

// TestLocalShellKind pins the classification the tier choice is made on. It is
// a mirror of autoDispatcherScript's case arms, and the arms that matter most
// are the ones with no local tier: a fish or a tcsh user must be STARTED, not
// substituted for bash, which is exactly what nocx-wwz0 was.
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
		{"/usr/local/bin/fish", ShellUnknown},
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
	// The bootstrap the helper writes into the pty once the shell is up: zsh
	// has no --rcfile, so the script is SOURCED from the descriptor rather
	// than delivered as one. It is written here for the same reason the
	// daemon writes it — the shell is already running its own startup, and
	// this is the line that adds ours to it.
	if _, werr := ptmx.Write(launch.Bootstrap); werr != nil {
		t.Fatalf("write the launch bootstrap: %v", werr)
	}
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
