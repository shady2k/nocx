package shellintegration

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The LOCAL zsh tier (nocx-wwz0). Every other terminal on macOS opens the
// user's login shell; nocx opened bash, for every user of the platform it ships
// to, because the local enhanced session had exactly one tier. These tests are
// the ones that report whether a zsh user gets their own shell AND keeps shell
// integration — not whether the code that was written does what it was written
// to do, so they start the shell the way internal/app starts it and assert what
// the user gets.

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

// TestLocalZshRcfile_SecretRidesTextNotEnv is the bash tier's assertion made
// for zsh, and it has to be made separately: the two tiers render from
// different templates, and a capability that reached the environment would be
// in /proc/<pid>/environ of every child of the user's shell (ADR-0024
// decision 2).
func TestLocalZshRcfile_SecretRidesTextNotEnv(t *testing.T) {
	opts := LaunchOptions{
		SessionID:   "sid-local-zsh",
		Enhanced:    true,
		Capability:  strings.Repeat("ab", 32),
		Recovery:    strings.Repeat("cd", 32),
		Lane:        "lane-z",
		Domain:      "dom-z",
		Epoch:       9,
		LifecycleFD: 3,
	}
	rc, err := LocalZshRcfile(opts)
	if err != nil {
		t.Fatalf("LocalZshRcfile: %v", err)
	}
	if !strings.Contains(rc, "__nocx_cap='"+opts.Capability+"'") {
		t.Fatalf("rcfile must carry the capability as substituted text")
	}
	if !strings.Contains(rc, "__nocx_lc_recovery='"+opts.Recovery+"'") {
		t.Fatalf("rcfile must carry the recovery fence as substituted text")
	}
	env := launcherEnvBlock(opts)
	if strings.Contains(env, opts.Capability) || strings.Contains(env, opts.Recovery) {
		t.Fatalf("a secret leaked into the exported environment block")
	}
	// The rcfile is the one that reaches a shell, so the addressing has to be
	// in IT, not merely in a block a different tier substitutes.
	for _, want := range []string{
		"NOCX_LIFECYCLE_LANE='lane-z'",
		"NOCX_LIFECYCLE_DOMAIN='dom-z'",
		"NOCX_LIFECYCLE_EPOCH=9",
		"NOCX_LIFECYCLE_FD=3",
		"NOCX_SESSION_ID='sid-local-zsh'",
	} {
		if !strings.Contains(rc, want) {
			t.Fatalf("rendered .zshrc must carry %s", want)
		}
	}
	// The local tier EMBEDS the script rather than sourcing the installed
	// generation: this session's authenticators must win over an installer-era
	// install the user's own ~/.zshrc may load.
	if !strings.Contains(rc, "__nocx_lc_send") {
		t.Fatalf("rendered .zshrc does not embed nocx.zsh")
	}
}

// TestLocalZshRcfile_RequiresEnhancedSession pins the precondition, the same
// one the bash tier has: a conventional session has no session id to anchor the
// ownership protocol and no channel to authenticate, so refusing beats
// rendering a .zshrc that claims a channel which cannot exist.
func TestLocalZshRcfile_RequiresEnhancedSession(t *testing.T) {
	if _, err := LocalZshRcfile(LaunchOptions{Enhanced: false}); err == nil {
		t.Error("a conventional session must not render a lifecycle .zshrc")
	}
	if _, err := LocalZshRcfile(LaunchOptions{Enhanced: true}); err == nil {
		t.Error("an enhanced session with no session id must not render a lifecycle .zshrc")
	}
}

// TestWriteLocalZDOTDIR_MatchesSelfDeleteGuardAndStaysPrivate is the failure
// this tier would otherwise ship: the .zshrc carries the per-epoch capability,
// and the shell deletes the directory by matching `*/nocx-zsh.??????`. A name
// the guard does not match is never removed, so every session would leave the
// capability in TMPDIR — and a directory another user can read makes the
// capability theirs.
func TestWriteLocalZDOTDIR_MatchesSelfDeleteGuardAndStaysPrivate(t *testing.T) {
	dir, err := writeLocalZDOTDIRIn("# test zshrc\n", t.TempDir())
	if err != nil {
		t.Fatalf("writeLocalZDOTDIRIn: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	if !regexp.MustCompile(`^nocx-zsh\.[0-9a-f]{6}$`).MatchString(filepath.Base(dir)) {
		t.Fatalf("zdotdir %q must match the template's */nocx-zsh.?????? self-delete guard", filepath.Base(dir))
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("zdotdir mode = %o, want 0700", st.Mode().Perm())
	}
	rc, err := os.Stat(filepath.Join(dir, ".zshrc"))
	if err != nil {
		t.Fatalf("stat .zshrc: %v", err)
	}
	if rc.Mode().Perm() != 0o600 {
		t.Errorf(".zshrc mode = %o, want 0600 (it carries the capability)", rc.Mode().Perm())
	}
	// zsh reads $ZDOTDIR/.zshrc and nothing else here; a directory with the
	// file under any other name is a session with no integration at all.
	body, err := os.ReadFile(filepath.Join(dir, ".zshrc")) //nolint:gosec // test-owned temp path
	if err != nil || string(body) != "# test zshrc\n" {
		t.Errorf("the rendered rcfile is not at $ZDOTDIR/.zshrc: %v %q", err, body)
	}
}

// TestWriteLocalZDOTDIR_LeavesNothingBehindWhenItCannotWrite is the failure
// path AGENTS.md rule 3 asks for, stated as an interval with both ends: from
// before the directory exists until the launch owns it, a caller that gets an
// error must be able to assume NOTHING was left on disk — otherwise a machine
// with a full or read-only TMPDIR accumulates 0700 directories nobody removes.
func TestWriteLocalZDOTDIR_LeavesNothingBehindWhenItCannotWrite(t *testing.T) {
	// A TMPDIR that exists but refuses creation is the shape of a read-only
	// or exhausted temp filesystem.
	readOnly := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0500 does not refuse root, so there is no failure to observe")
	}

	if _, err := writeLocalZDOTDIRIn("# unreachable\n", readOnly); err == nil {
		t.Fatal("writeLocalZDOTDIRIn must fail when the parent directory refuses creation")
	}
	entries, err := os.ReadDir(readOnly)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed write left %d entries behind: %v", len(entries), entries)
	}
}

// TestLocalZshEnv_DistinguishesUnsetFromEmpty is the whole reason there are TWO
// carrier variables instead of one. The template restores an unset ZDOTDIR by
// unsetting it and a set-but-empty one by exporting the empty string, and an
// empty NOCX_ZDOTDIR_ORIG cannot tell those apart — so a user who had no
// ZDOTDIR would come out of a nocx session with an exported empty one, which
// zsh reads as "look for startup files in the current directory".
func TestLocalZshEnv_DistinguishesUnsetFromEmpty(t *testing.T) {
	unset := localZshEnv("/tmp/nocx-zsh.abcdef", "", false)
	if !contains(unset, "NOCX_ZDOTDIR_WAS_SET=0") {
		t.Errorf("an unset ZDOTDIR must be carried as WAS_SET=0: %v", unset)
	}
	set := localZshEnv("/tmp/nocx-zsh.abcdef", "", true)
	if !contains(set, "NOCX_ZDOTDIR_WAS_SET=1") || !contains(set, "NOCX_ZDOTDIR_ORIG=") {
		t.Errorf("a set-but-empty ZDOTDIR must be carried as WAS_SET=1 with an empty original: %v", set)
	}
	real := localZshEnv("/tmp/nocx-zsh.abcdef", "/home/u/.config/zsh", true)
	if !contains(real, "NOCX_ZDOTDIR_ORIG=/home/u/.config/zsh") {
		t.Errorf("the original ZDOTDIR must be carried verbatim: %v", real)
	}
	for _, env := range [][]string{unset, set, real} {
		if !contains(env, "ZDOTDIR=/tmp/nocx-zsh.abcdef") {
			t.Errorf("ZDOTDIR must point at the transient directory: %v", env)
		}
	}
}

// unsetZDOTDIR removes ZDOTDIR for the duration of one test and puts the
// process environment back afterwards. t.Setenv cannot express it: setting the
// variable to the empty string is the OTHER case these tests distinguish.
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

func contains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestLocalEnhancedLaunch_BashIsUnchanged is acceptance criterion 4 as an
// assertion: a bash user must get exactly what they got before this bead —
// a non-login interactive bash reading one transient rcfile — and the only
// difference is that the binary is now the account's bash by path rather than
// whichever bash PATH happened to find.
func TestLocalEnhancedLaunch_BashIsUnchanged(t *testing.T) {
	launch, err := LocalEnhancedLaunch("/bin/bash", ShellBash, localTestOpts())
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch(bash): %v", err)
	}
	defer launch.Cleanup()

	if launch.Command != "/bin/bash" {
		t.Errorf("command = %q, want the login shell's own path", launch.Command)
	}
	if len(launch.Args) != 3 || launch.Args[0] != "--rcfile" || launch.Args[2] != "-i" {
		t.Fatalf("args = %v, want [--rcfile <path> -i]", launch.Args)
	}
	if launch.Env != nil {
		t.Errorf("the bash tier must add no environment; got %v", launch.Env)
	}
	if _, err := os.Stat(launch.Args[1]); err != nil {
		t.Errorf("the rcfile the launch names does not exist: %v", err)
	}
	launch.Cleanup()
	if _, err := os.Stat(launch.Args[1]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Cleanup must remove the rcfile; stat says %v", err)
	}
}

// TestLocalEnhancedLaunch_ZshCarriesTheTransientZDOTDIR pins the zsh launch
// shape: the login shell by path, `-l -i`, and ZDOTDIR pointed at a directory
// that actually holds the rendered .zshrc.
func TestLocalEnhancedLaunch_ZshCarriesTheTransientZDOTDIR(t *testing.T) {
	t.Setenv("ZDOTDIR", "/home/u/zdot")
	launch, err := LocalEnhancedLaunch("/bin/zsh", ShellZsh, localTestOpts())
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch(zsh): %v", err)
	}
	defer launch.Cleanup()

	if launch.Command != "/bin/zsh" {
		t.Errorf("command = %q, want the login shell's own path", launch.Command)
	}
	if len(launch.Args) != 2 || launch.Args[0] != "-l" || launch.Args[1] != "-i" {
		t.Errorf("args = %v, want [-l -i]: -l for the login startup files a GUI launch has no PATH without, "+
			"-i because zsh reads $ZDOTDIR/.zshrc only when interactive", launch.Args)
	}
	if !contains(launch.Env, "NOCX_ZDOTDIR_ORIG=/home/u/zdot") || !contains(launch.Env, "NOCX_ZDOTDIR_WAS_SET=1") {
		t.Errorf("the user's own ZDOTDIR must be carried for restoration: %v", launch.Env)
	}
	var dir string
	for _, kv := range launch.Env {
		if rest, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
			dir = rest
		}
	}
	if dir == "" {
		t.Fatalf("the launch sets no ZDOTDIR: %v", launch.Env)
	}
	if _, err := os.Stat(filepath.Join(dir, ".zshrc")); err != nil {
		t.Errorf("ZDOTDIR names a directory with no .zshrc in it: %v", err)
	}
	launch.Cleanup()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Cleanup must remove the transient directory; stat says %v", err)
	}
}

// TestLocalEnhancedLaunch_RefusesAShellWithNoTier pins the contract the caller
// depends on to keep the degrade honest: a fish or tcsh login shell has no
// local tier, and the launcher says so rather than handing back a bash the user
// never asked for. The caller starts the user's own shell conventionally and
// reports ReasonUnsupportedShell.
func TestLocalEnhancedLaunch_RefusesAShellWithNoTier(t *testing.T) {
	for _, shell := range []string{"/usr/local/bin/fish", "/bin/tcsh", "/bin/sh"} {
		launch, err := LocalEnhancedLaunch(shell, LocalShellKind(shell), localTestOpts())
		if err == nil {
			t.Errorf("%s: expected a refusal, got %+v", shell, launch)
		}
		if launch.Command != "" {
			t.Errorf("%s: a refusal must name no command, got %q", shell, launch.Command)
		}
	}
}

func localTestOpts() LaunchOptions {
	return LaunchOptions{
		SessionID:   "sid-local",
		Enhanced:    true,
		Capability:  testCap,
		Recovery:    strings.Repeat("ef", 32),
		Lane:        testLane,
		Domain:      testDom,
		Epoch:       testEpoch,
		LifecycleFD: 3,
	}
}

// TestLocalZshSession_IsIntegratedOnTheUsersOwnShell is the bead's acceptance
// criterion driven end to end, through the exact call the composition root
// makes: a REAL zsh started by LocalEnhancedLaunch on a REAL pty with a REAL
// lifecycle channel on the inherited descriptor.
//
// What it watches a user do that they could not before: open a local tab on a
// machine whose login shell is zsh and get command blocks — the handshake, the
// OSC 133 markers, the command-existence snapshot completion needs, and a
// lifecycle start/complete for a command they ran. Before this bead the same
// machine got bash 3.2.57 and none of the user's own environment.
//
// It also closes the two intervals the transient ZDOTDIR opens, because both
// ends of each are what makes them invariants rather than moments: the
// directory exists from the write until the shell's own rc phase and NOT
// after, and ZDOTDIR carries our directory from the spawn until the top of the
// .zshrc and the user's own value from there on.
func TestLocalZshSession_IsIntegratedOnTheUsersOwnShell(t *testing.T) {
	zsh := requireShell(t, "zsh")

	// The user's own ~/.zshrc: it must run, and its effects must survive into
	// the session. A zsh user moved to bash loses exactly this.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("alias mine='echo USER_RC_ALIAS'\nexport USER_RC_RAN=yes\n"), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}
	// No ZDOTDIR of our own, which is the ordinary case and the one where an
	// unset-versus-empty mistake is visible.
	unsetZDOTDIR(t)

	opts := localTestOpts()
	launch, err := LocalEnhancedLaunch(zsh, LocalShellKind(zsh), opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch: %v", err)
	}
	transient := ""
	for _, kv := range launch.Env {
		if rest, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
			transient = rest
		}
	}
	if _, serr := os.Stat(filepath.Join(transient, ".zshrc")); serr != nil {
		t.Fatalf("the transient ZDOTDIR is not ready before the spawn: %v", serr)
	}

	kernelFile, shellFile := lifecycleSocketpair(t)

	// #nosec G204 — launch.Command is the requireShell-resolved zsh; starting
	// a real interactive shell is the only way to observe what a user gets.
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.ExtraFiles = []*os.File{shellFile} // fd 3, exactly as pty.WithExtraFiles
	// NOCX_SNAPSHOT_WAIT_MS, for the reason the script states where it reads
	// it (nocx.zsh): 250 ms is the budget a HUMAN's first prompt may spend
	// waiting for the source-time snapshot job, and on timeout the payload is
	// deliberately left for a LATER prompt rather than delaying this one. So
	// the default makes "the snapshot has been emitted" a claim about how
	// fast the machine is — this test runs exactly one command and then
	// asserts on 636;S, which on a loaded runner had not been emitted yet.
	// It failed three times in one CI day while passing here in 0.10 s.
	//
	// Raising the budget does not paper over a race, it removes one: the
	// first prompt now waits for the FILE TO EXIST — an observable state
	// change — and 15 s is a hang detector rather than an expectation. Ten
	// tests in scripts_exec_test.go already do exactly this; the launcher
	// tier simply never inherited it.
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
		append(launch.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000", "NOCX_SNAPSHOT_WAIT_MS=15000")...,
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
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		launch.Cleanup()
	}()

	s.waitForHandshake()

	// 1. The transient directory is gone — erased by the .zshrc before any
	//    user code ran, which is the closing end of the "capability on disk"
	//    interval. Waiting on the handshake above is what makes this a state
	//    assertion rather than a race against a duration.
	if _, err := os.Stat(transient); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the transient ZDOTDIR survived the shell's startup (%v) — every session would leave the capability in TMPDIR", err)
	}

	// 2. The user's own rc ran and its effects are live, and ZDOTDIR is back
	//    to unset. One command, because both are properties of the same shell.
	s.run(`echo "RC=[${USER_RC_RAN-no}] ZD=[${ZDOTDIR-UNSET}] SH=[$ZSH_NAME]"; mine`)
	out := s.output()
	if !strings.Contains(out, "RC=[yes]") {
		t.Errorf("the user's own ~/.zshrc did not run: %q", out)
	}
	if !strings.Contains(out, "USER_RC_ALIAS") {
		t.Errorf("an alias from the user's own ~/.zshrc is not available: %q", out)
	}
	if !strings.Contains(out, "ZD=[UNSET]") {
		t.Errorf("ZDOTDIR was not restored to its original unset state: %q", out)
	}
	if !strings.Contains(out, "SH=[zsh]") {
		t.Errorf("the session is not running zsh: %q", out)
	}

	// 3. Shell integration, which is the half a conventional fallback would
	//    have silently cost: the prompt markers, the command-existence
	//    snapshot the completion dropdown needs (nocx-qduc), and the
	//    lifecycle start/complete pair for the command just run.
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

	// 4. The capability is usable BY the shell and absent FROM its
	//    environment — the property the whole text-substitution design exists
	//    for (ADR-0024 decision 2), asked of the real process the user's own
	//    commands are children of rather than of the rendered text.
	//
	//    Both verdict words are SPLIT across two quoted strings, the idiom
	//    the two tests below already use and for the same reason: zsh's line
	//    editor redraws the typed line in pieces, so a reader sampling the
	//    pty sees a PREFIX of the echo, and this command's echo carries
	//    "CAP_IN_ENVIRON" a whole clause before it carries "CAP_TEXT_ONLY".
	//    Whole words here let the echo answer for the shell: sampled in that
	//    window, the leak verdict wins on a session that never leaked. It
	//    won on `ci-backend` (run 32470670241) and reproduces 10 times in 10
	//    on a bare zsh outside this suite. The old comment reasoned that the
	//    verdict words carry none of the CAPABILITY, which is true and is
	//    not the hazard — the hazard is the verdict words themselves.
	//
	//    The shell-variable half is asserted with it, in the same command:
	//    "absent from the environment" is satisfied vacuously by a session
	//    that never received a capability at all, and this is a security
	//    assertion, so it may not be able to pass for the wrong reason.
	probe := `echo "CAP_VAR""=${__nocx_cap:+yes}"; ` +
		`env | grep -q ` + testCap[:16] + ` && echo "CAP_IN""_ENVIRON" || echo "CAP_TEXT""_ONLY"`
	if _, err := ptmx.Write([]byte(probe + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	verdict := waitForEither(t, s, "CAP_TEXT_ONLY", "CAP_IN_ENVIRON")
	if verdict != "CAP_TEXT_ONLY" {
		t.Errorf("the capability reached the session environment, where every child of the user's shell can read it: %q", s.output())
	}
	if !strings.Contains(s.output(), "CAP_VAR=yes") {
		t.Errorf("the shell does not hold the capability in its non-exported variable, so the check above passed on a session that had none: %q", s.output())
	}
}

// waitForEither blocks until one of two verdict words appears on the pty and
// reports which. Anchoring on the shell's own answer rather than on a duration
// is what keeps this assertion from passing because the command had not run
// yet — the shape AGENTS.md's "wait on an observable state change" asks for.
//
// NEITHER word may appear in the command that is typed to produce it. The pty
// carries the echo of that line, zsh's editor redraws it in pieces, and a
// caller sampling mid-redraw reads a prefix — so a typed line containing both
// words answers its own question, in whichever order it spells them. Callers
// split the word across two quoted strings ("CAP_IN""_ENVIRON") or keep it out
// of the typed text entirely.
func waitForEither(t *testing.T, s *channelShell, a, b string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out := s.output()
		ia, ib := strings.LastIndex(out, a), strings.LastIndex(out, b)
		switch {
		case ia > ib:
			return a
		case ib > ia:
			return b
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("neither %q nor %q appeared on the pty; output=%q", a, b, s.output())
	return ""
}

// TestLocalZshSession_RestoresAUsersOwnZDOTDIR is the other half of the
// unset-versus-set interval, and it needs its own shell because a shell can
// only have started one way. A user who keeps their zsh configuration outside
// $HOME must come out of the session with that ZDOTDIR still pointing there —
// and must have had THAT directory's .zshrc sourced, not $HOME's.
func TestLocalZshSession_RestoresAUsersOwnZDOTDIR(t *testing.T) {
	zsh := requireShell(t, "zsh")

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export WHICH_RC=home\n"), 0o600); err != nil {
		t.Fatalf("write home .zshrc: %v", err)
	}
	userZdot := t.TempDir()
	if err := os.WriteFile(filepath.Join(userZdot, ".zshrc"), []byte("export WHICH_RC=zdotdir\n"), 0o600); err != nil {
		t.Fatalf("write zdotdir .zshrc: %v", err)
	}
	t.Setenv("ZDOTDIR", userZdot)

	launch, err := LocalEnhancedLaunch(zsh, ShellZsh, localTestOpts())
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch: %v", err)
	}

	kernelFile, shellFile := lifecycleSocketpair(t)

	// #nosec G204 — launch.Command is the requireShell-resolved zsh.
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.ExtraFiles = []*os.File{shellFile}
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null", "ZDOTDIR="+userZdot),
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
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		launch.Cleanup()
	}()

	s.waitForHandshake()
	s.run(fmt.Sprintf(`echo "ZD=[${ZDOTDIR-UNSET}] RC=[$WHICH_RC] MATCH=[%s]"`, userZdot))
	out := s.output()
	if !strings.Contains(out, "ZD=["+userZdot+"]") {
		t.Errorf("the user's own ZDOTDIR was not restored: %q", out)
	}
	if !strings.Contains(out, "RC=[zdotdir]") {
		t.Errorf("the .zshrc under the user's own ZDOTDIR was not the one sourced: %q", out)
	}
}

// TestLocalZshSession_SurvivesAUserRcThatFails is the failure path for the one
// piece of user code this tier runs: a broken ~/.zshrc must not cost the user
// a terminal. The declared equivalence set says user startup wins — including
// when it is wrong — so the shell must still come up and still be usable, and
// the transient directory must still be gone.
func TestLocalZshSession_SurvivesAUserRcThatFails(t *testing.T) {
	zsh := requireShell(t, "zsh")

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("this-command-does-not-exist\nfalse\n"), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}
	unsetZDOTDIR(t)

	launch, err := LocalEnhancedLaunch(zsh, ShellZsh, localTestOpts())
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch: %v", err)
	}
	transient := ""
	for _, kv := range launch.Env {
		if rest, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
			transient = rest
		}
	}

	kernelFile, shellFile := lifecycleSocketpair(t)

	// #nosec G204 — launch.Command is the requireShell-resolved zsh.
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.ExtraFiles = []*os.File{shellFile}
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
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		launch.Cleanup()
	}()

	// A usable shell is the assertion: it RUNS what it is told, whatever the
	// user's rc did on the way past. The marker is split across two quoted
	// strings so the pty's echo of the typed line cannot contain it — matching
	// the echo would let this pass ~25 ms in, before the shell had sourced
	// anything, which is exactly how it passed on a fast host and failed in the
	// Linux container.
	if _, err := ptmx.Write([]byte(`echo "BROKEN""_RC_STILL_USABLE"` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := waitForEither(t, s, "BROKEN_RC_STILL_USABLE", "__NOCX_NEVER__"); got != "BROKEN_RC_STILL_USABLE" {
		t.Fatalf("a broken user ~/.zshrc cost the user a terminal: %q", s.output())
	}
	// And the transient directory is gone — the shell reached a prompt, so the
	// rc phase that erases it has run. Anchoring on the command's own output
	// rather than on a duration is what makes that a fact rather than a hope.
	if _, err := os.Stat(transient); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the transient ZDOTDIR survived a failing user rc (%v)", err)
	}
}

// TestLocalZshSession_KeepsAFrameworkAcceptLineWrapper is the defect a real
// machine reported the moment this tier first ran on one: pressing Enter
// printed "No such widget `_zsh_highlight_widget_orig-…-accept-line'" and the
// command did not run. Every zsh user with fast-syntax-highlighting,
// zsh-syntax-highlighting or zsh-autosuggestions — which is most of them —
// would have been handed a terminal that cannot execute a command.
//
// The mechanism is a name confusion in the nested-launch interception:
// `zle -lL accept-line` reports the FUNCTION that implements the widget, and
// the chain called that name as if it were a WIDGET. It works whenever the
// framework happens to register a widget of the same name, which is why it
// survived review; it fails for every framework that does not.
//
// The fixture wraps accept-line the way those plugins do — a wrapper function
// that is not itself a widget — and the assertions are the user's: the command
// runs, the framework's wrapper still ran, and nothing complained.
func TestLocalZshSession_KeepsAFrameworkAcceptLineWrapper(t *testing.T) {
	zsh := requireShell(t, "zsh")

	home := t.TempDir()
	userRC := "__fixture_accept_line() { print -u2 FIXTURE_WRAPPER_RAN; zle .accept-line }\n" +
		"zle -N accept-line __fixture_accept_line\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(userRC), 0o600); err != nil {
		t.Fatalf("write user .zshrc: %v", err)
	}
	unsetZDOTDIR(t)

	launch, err := LocalEnhancedLaunch(zsh, ShellZsh, localTestOpts())
	if err != nil {
		t.Fatalf("LocalEnhancedLaunch: %v", err)
	}

	kernelFile, shellFile := lifecycleSocketpair(t)

	// #nosec G204 — launch.Command is the requireShell-resolved zsh.
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.ExtraFiles = []*os.File{shellFile}
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
		append(launch.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000")...,
	)

	k := newFakeKernel(t, testCap)
	go k.serveFile(kernelFile)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		launch.Cleanup()
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		launch.Cleanup()
	}()

	s.waitForHandshake()
	// The marker is split across two quoted strings, so the pty's echo of the
	// typed line cannot contain it — only the shell's OUTPUT does. Without
	// that, the assertion passes on the echo of a command that never ran.
	if _, err := ptmx.Write([]byte(`echo "ENTER""_WORKS"` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := waitForEither(t, s, "ENTER_WORKS", "No such widget"); got != "ENTER_WORKS" {
		t.Fatalf("pressing Enter did not run the command: %q", s.output())
	}
	if strings.Contains(s.output(), "No such widget") {
		t.Errorf("the accept-line chain named something that is not a widget: %q", s.output())
	}
	if !strings.Contains(s.output(), "FIXTURE_WRAPPER_RAN") {
		t.Errorf("the framework's own accept-line wrapper was bypassed — nocx took a surface it does not own: %q", s.output())
	}
}
