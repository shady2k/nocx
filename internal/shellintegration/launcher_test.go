package shellintegration

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// writeBashFixtureHome materialises a fixture $HOME whose .bashrc sets a
// sentinel, prints a marker, and points HISTFILE at /dev/null — the last so
// an ordinary exit-time history write cannot disturb the no-$HOME-writes
// invariant, isolating the launcher's own behaviour.
func writeBashFixtureHome(t *testing.T, extra string) string {
	t.Helper()
	home := t.TempDir()
	rc := `export NOCX_LAUNCHER_SENTINEL=from-user-rc
HISTFILE=/dev/null
PS1='FIXTURE-PROMPT> '
echo USER_RC_RAN
` + extra
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatalf("write fixture .bashrc: %v", err)
	}
	return home
}

// requireBinBash skips the test only when the host has no bash at all. The
// launcher resolves bash through `env`, on PATH, rather than naming
// /bin/bash absolutely — an absolute path skipped this test on every NixOS
// and Guix host, which is to say it skipped the epic's primary path on the
// machine it was developed on.
func requireBinBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not present on this host: %v", err)
	}
}

// requireIntegrationShell fails the test when the shell is absent instead of
// skipping. Skipping is how the launcher's riskiest path — the zsh
// transient-ZDOTDIR lifecycle, which creates a directory on somebody else's
// machine and must erase it before any user code runs — reported green on
// every box without zsh. That is the silent success AGENTS.md's testing rules
// exist to prevent (nocx-gd84): the suite must say "this did not run", never
// pretend it did.
//
// The provisioning command is what CI runs the suite with: ubuntu-latest
// ships both dash and zsh, macOS ships zsh but not dash, so the macOS CI
// job must install dash itself (see README, "Shell integration tests").
//
//	# Debian/Ubuntu (dash and zsh)
//	sudo apt-get install -y dash zsh
//	# macOS (zsh ships with the OS)
//	brew install dash
//
// then run the suite the way CI does:
//
//	go test -race -count=1 ./internal/shellintegration/...
func requireIntegrationShell(t *testing.T, shell string) string {
	t.Helper()
	path, err := exec.LookPath(shell)
	if err == nil {
		return path
	}
	t.Fatalf("%s is required by this test and missing from PATH (%v).\n"+
		"The launcher's riskiest paths must not silently skip.\n"+
		"Provision the shells CI runs the suite with, then re-run:\n"+
		"  Debian/Ubuntu: sudo apt-get install -y dash zsh\n"+
		"  macOS:         brew install dash   (zsh ships with the OS)\n"+
		"  then:          go test -race -count=1 ./internal/shellintegration/...",
		shell, err)
	return ""
}

// writeZshFixtureHome materialises a fixture $HOME whose .zshrc sets a
// sentinel, prints a marker, and reports whether the launcher's transient
// directory still exists at the moment the user's rc runs.
func writeZshFixtureHome(t *testing.T, extra string) string {
	t.Helper()
	home := t.TempDir()
	rc := `export NOCX_LAUNCHER_SENTINEL=from-user-rc
echo USER_RC_RAN
if ls -d "${TMPDIR:-/tmp}"/nocx-zsh.* >/dev/null 2>&1; then echo TRANSIENT_PRESENT; else echo TRANSIENT_GONE; fi
` + extra
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatalf("write fixture .zshrc: %v", err)
	}
	return home
}

// runLauncherOnPTY executes `shPath -c cmd` — shPath standing in for the
// login shell sshd hands a remote command to — on a real pty, waits for the
// first prompt, types each line, then waits for the session to end. It
// returns all captured output. Write failures after the session ended on
// its own (an early exit in a fixture rc) are benign and logged.
//
// #nosec G204 — shPath is a requireShell-resolved binary and cmd is a
// launcher string built from package constants; a pty is the only way to
// observe prompt-time behaviour.
func runLauncherOnPTY(t *testing.T, shPath, cmd string, env []string, lines ...string) string {
	t.Helper()
	c := exec.Command(shPath, "-c", cmd)
	c.Env = append(os.Environ(), env...)
	ptmx, err := pty.Start(c)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	done := make(chan []byte, 1)
	go func() {
		out, _ := io.ReadAll(ptmx)
		done <- out
	}()

	// The bash launcher's first prompt can wait up to 250 ms for the
	// source-time snapshot job; give both shells room to settle.
	time.Sleep(600 * time.Millisecond)
	for _, line := range lines {
		if _, werr := ptmx.Write([]byte(line + "\n")); werr != nil {
			t.Logf("write %q failed (session may have exited early): %v", line, werr)
		}
		time.Sleep(400 * time.Millisecond)
	}

	var out []byte
	select {
	case out = <-done:
	case <-time.After(20 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("timed out waiting for the session to end")
	}
	if werr := c.Wait(); werr != nil {
		t.Logf("session exited non-zero (may be benign): %v", werr)
	}
	return string(out)
}

// The dispatch and the argv cap that used to be asserted here went with the
// command they belonged to. FullBootstrapCommand chose a tier per ShellKind,
// refused an unmapped kind, and refused anything past maxFullLauncherLen —
// all three are now the bounded carrier's, and carrier_test.go asserts them
// against a bound two orders smaller. What stays here is the far side's own
// behaviour: the tiers themselves, driven on a real pty from an installed
// generation, which is how they actually start now.

// tierCommand builds the command that brings up one tier, the way the far
// side brings it up now: the bundle is PUBLISHED into home by the product's
// own publisher, and the tier's rcfile sources the installed generation
// file. That is the launch carrier's shape, and it replaced a command that
// carried the whole bundle and both bearers in its own text.
//
// The returned env additions are what the carrier exports before it execs —
// without NOCX_GENERATION the source line names no file and the session is a
// conventional terminal, which is the fail-open direction and not what these
// tests are about.
func tierCommand(t *testing.T, kind ShellKind, home string, opts LaunchOptions) (string, []string) {
	t.Helper()
	root := filepath.Join(home, dirName)
	res, err := NewPublisher(testLogger(), NewOSFS(), root).Publish(launchBundle())
	if err != nil {
		t.Fatalf("publish the bundle into the fixture home: %v", err)
	}
	env := []string{"NOCX_GENERATION=" + res.Generation}
	switch kind {
	case ShellBash:
		arg := bashArgFor(bashRcfile(remoteLogin, launcherEnvBlock(opts), launchSourceLine("nocx.bash"),
			capabilityLiteral(bashUnsetExport, opts.Capability, opts.Recovery)))
		return "exec /usr/bin/env -u BASH_ENV bash -c " + ShellQuote(arg), env
	case ShellZsh:
		arg := zshArgFor(zshRcfile(launcherEnvBlock(opts), launchSourceLine("nocx.zsh"),
			capabilityLiteral(zshUnsetExport, opts.Capability, opts.Recovery)))
		return "exec /usr/bin/env -u BASH_ENV zsh -c " + ShellQuote(arg), env
	case ShellUnknown:
		arg := posixArgFor(posixEnvFile(launcherEnvBlock(opts), launchSourceLine("nocx.posix")))
		return "exec /usr/bin/env -u BASH_ENV /bin/sh -c " + ShellQuote(arg), env
	default:
		t.Fatalf("no tier command for shell kind %q", kind)
		return "", nil
	}
}

// TestPrintfBEscapeRoundTripsThroughBashBuiltin drives the real bash
// builtin printf — the exact decoder the launcher ships its rcfile through
// — over a payload holding every byte class the escaper must survive:
// quotes, dollars, backticks, backslashes, control bytes, octal-digit
// adjacency (`\n` followed by an octal digit must not bleed into the
// escape), and non-ASCII UTF-8.
func TestPrintfBEscapeRoundTripsThroughBashBuiltin(t *testing.T) {
	bash := requireShell(t, "bash")
	payload := "NOCX_SESSION_ID='abc'\n\"$`\\ \t\n\x00\x1b\r" +
		"1234567" + "\n" + "3" + "\n" + "7" + "\n" + "0" + "\u00e9\u4e2d"
	escaped := printfBEscape(payload)
	if strings.ContainsRune(escaped, '\'') {
		t.Errorf("escaped payload contains a literal single quote: %q", escaped)
	}
	// #nosec G204 — bash is requireShell-resolved; the escaped string is
	// generated by printfBEscape, which never emits quote characters.
	cmd := exec.Command(bash, "-c", `printf %b "`+escaped+`"`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash printf: %v", err)
	}
	if string(out) != payload {
		t.Errorf("round trip mismatch:\n got %q\nwant %q", out, payload)
	}
}

// TestShellQuoteEscapesEmbeddedQuotes: the escaper is real, not
// concatenation that happens to work on today's payloads.
func TestShellQuoteEscapesEmbeddedQuotes(t *testing.T) {
	if got := ShellQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("ShellQuote(a'b) = %q", got)
	}
	if got := ShellQuote("plain"); got != "'plain'" {
		t.Errorf("ShellQuote(plain) = %q", got)
	}
}

// TestBashLauncher_EmitsMarkersAndRunsUserRc drives the REAL bash launcher
// command end to end on a pty and asserts the acceptance surface: OSC 133 A
// and B markers arrive, the user's own ~/.bashrc ran (sentinel + marker),
// marker-only mode hides the fixture prompt, and $0 is "bash" (non-login).
func TestBashLauncher_EmitsMarkersAndRunsUserRc(t *testing.T) {
	requireShell(t, "bash")
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellBash, home, LaunchOptions{Enhanced: true, SessionID: "test-session-1"})

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...),
		`echo "SENTINEL=$NOCX_LAUNCHER_SENTINEL"; echo "ZERO=$0"`, "exit")

	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") == 0 {
		t.Errorf("no OSC 133 A marker in output: %q", out)
	}
	if countMarkers(ms, "B") == 0 {
		t.Errorf("no OSC 133 B marker in output: %q", out)
	}
	if !strings.Contains(out, "USER_RC_RAN") {
		t.Errorf("the user's ~/.bashrc did not run: %q", out)
	}
	if !strings.Contains(out, "SENTINEL=from-user-rc") {
		t.Errorf("the user's rc sentinel did not survive: %q", out)
	}
	if !strings.Contains(out, "ZERO=bash") {
		t.Errorf("$0 = %q, want bash (non-login)", out)
	}
	if !strings.Contains(out, "FIXTURE-PROMPT") {
		t.Errorf("marker-only mode suppressed the fixture prompt without a live channel (ADR-0024 decision 9): %q", out)
	}
}

// TestBashLauncher_BaselineKeepsVisiblePrompt: without Enhanced the prompt
// overlay is not armed; the fixture prompt stays visible and the B marker
// still wraps it.
func TestBashLauncher_BaselineKeepsVisiblePrompt(t *testing.T) {
	requireShell(t, "bash")
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellBash, home, LaunchOptions{})

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...), "exit")

	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") == 0 || countMarkers(ms, "B") == 0 {
		t.Errorf("baseline session missing markers: %q", out)
	}
	if !strings.Contains(out, "FIXTURE-PROMPT") {
		t.Errorf("baseline session hid the user's prompt: %q", out)
	}
}

// TestBashLauncher_NoHomeWrites compares a recursive listing of $HOME
// (names, sizes, mtimes) before and after a full bash session started by
// the launcher: nothing under $HOME may be created or modified. The
// fixture rc points HISTFILE at /dev/null so an ordinary exit-time history
// write cannot mask a launcher write.
func TestBashLauncher_NoHomeWrites(t *testing.T) {
	requireShell(t, "bash")
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellBash, home, LaunchOptions{Enhanced: true, SessionID: "test-session-2"})

	before := snapshotTree(t, home)
	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...), "echo hello", "exit")
	after := snapshotTree(t, home)

	// The launcher now publishes the bundle under ~/.nocx by design
	// (design §3.2); every OTHER byte of $HOME must be untouched. Drop the
	// published subtree from the comparison and verify it is exactly the
	// bundle the Go publisher verifies.
	excludeNocx := func(s map[string]fileState) map[string]fileState {
		out := map[string]fileState{}
		for k, v := range s {
			// The bundle subtree, and the home directory's own mtime —
			// creating ~/.nocx legitimately touches the parent dir.
			if k == "." || k == dirName || strings.HasPrefix(k, dirName+string(os.PathSeparator)) {
				continue
			}
			out[k] = v
		}
		return out
	}
	if !equalSnapshots(excludeNocx(before), excludeNocx(after)) {
		t.Errorf("$HOME changed outside the published bundle (checked a recursive listing of names, sizes and mtimes before and after):\n before: %v\n after:  %v\noutput: %q", before, after, out)
	}
	vr, err := NewPublisher(testLogger(), NewOSFS(), filepath.Join(home, dirName)).Verify()
	if err != nil {
		t.Fatalf("published state does not verify: %v", err)
	}
	if !vr.Installed {
		t.Error("the launcher's publish was not recorded as installed")
	}
}

// TestBashLauncher_BashEnvNotExecuted: the outer `bash -c` must not execute
// BASH_ENV code (spec §4.3). A hostile BASH_ENV file that writes a marker
// under $HOME must never run.
func TestBashLauncher_BashEnvNotExecuted(t *testing.T) {
	requireShell(t, "bash")
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	envScript := filepath.Join(tmp, "bashenv.sh")
	if err := os.WriteFile(envScript, []byte("echo ran > \"$HOME/bashenv-ran\"\n"), 0o600); err != nil {
		t.Fatalf("write BASH_ENV script: %v", err)
	}
	cmd, tierEnv := tierCommand(t, ShellBash, home, LaunchOptions{Enhanced: true, SessionID: "test-session-3"})

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm", "BASH_ENV=" + envScript}, tierEnv...), "exit")

	if _, err := os.Stat(filepath.Join(home, "bashenv-ran")); !os.IsNotExist(err) {
		t.Errorf("BASH_ENV code executed in the outer bash -c (marker file exists): %v", err)
	}
	if strings.Contains(out, "bashenv-ran") {
		t.Errorf("BASH_ENV marker leaked into the session output: %q", out)
	}
}

// TestBashLauncher_UserRcExecPreventsInstall: user startup wins — a
// ~/.bashrc that execs a plain shell replaces the session, and nocx's
// hooks (which live after the source) never install.
func TestBashLauncher_UserRcExecPreventsInstall(t *testing.T) {
	requireShell(t, "bash")
	requireBinBash(t)
	home := writeBashFixtureHome(t, "exec bash --norc\n")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellBash, home, LaunchOptions{Enhanced: true, SessionID: "test-session-4"})

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...), "echo alive", "exit")

	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") != 0 {
		t.Errorf("hooks installed despite the user's rc exec'ing: %q", out)
	}
	if !strings.Contains(out, "alive") {
		t.Errorf("the exec'd shell did not run: %q", out)
	}
}

// TestBashLauncher_RunsUnderDash: the pinned form's whole point — the
// remote command is parsed by the user's login shell, which may be dash.
// Parsing the same launcher with dash must work identically.
func TestBashLauncher_RunsUnderDash(t *testing.T) {
	dash := requireIntegrationShell(t, "dash")
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellBash, home, LaunchOptions{Enhanced: true, SessionID: "test-session-5"})

	out := runLauncherOnPTY(t, dash, cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...),
		`echo "SENTINEL=$NOCX_LAUNCHER_SENTINEL"`, "exit")

	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") == 0 || countMarkers(ms, "B") == 0 {
		t.Errorf("dash-parsed launcher missing markers: %q", out)
	}
	if !strings.Contains(out, "SENTINEL=from-user-rc") {
		t.Errorf("user rc did not run under dash parsing: %q", out)
	}
}

// TestZshLauncher_TransientDirFlow drives the REAL zsh launcher on a pty
// and asserts: markers arrive, the user's real .zshrc ran, the transient
// directory is gone before the first user command, and nothing of it
// survives the session.
func TestZshLauncher_TransientDirFlow(t *testing.T) {
	requireIntegrationShell(t, "zsh")
	home := writeZshFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellZsh, home, LaunchOptions{Enhanced: true, SessionID: "test-session-6"})

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...),
		`echo "SENTINEL=$NOCX_LAUNCHER_SENTINEL"; echo "ZERO=$0"`, "exit")

	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") == 0 {
		t.Errorf("no OSC 133 A marker: %q", out)
	}
	if countMarkers(ms, "B") == 0 {
		t.Errorf("no OSC 133 B marker: %q", out)
	}
	if !strings.Contains(out, "USER_RC_RAN") {
		t.Errorf("the user's .zshrc did not run: %q", out)
	}
	if !strings.Contains(out, "SENTINEL=from-user-rc") {
		t.Errorf("the user's rc sentinel did not survive: %q", out)
	}
	if !strings.Contains(out, "TRANSIENT_GONE") {
		t.Errorf("the transient dir still existed when the user's rc ran: %q", out)
	}
	if strings.Contains(out, "TRANSIENT_PRESENT") {
		t.Errorf("transient dir present at user-rc time: %q", out)
	}
	if !strings.Contains(out, "ZERO=zsh") {
		t.Errorf("$0 = %q, want zsh", out)
	}
	assertNoTransientDir(t, tmp)
}

// TestZshLauncher_CleanupAfterEarlyExit: a user .zshrc that exits early
// leaves no transient directory behind.
func TestZshLauncher_CleanupAfterEarlyExit(t *testing.T) {
	requireIntegrationShell(t, "zsh")
	home := writeZshFixtureHome(t, "exit 7\n")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellZsh, home, LaunchOptions{Enhanced: true, SessionID: "test-session-7"})

	runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...))
	assertNoTransientDir(t, tmp)
}

// TestZshLauncher_CleanupAfterSyntaxError: a user .zshrc that fails to
// parse (zsh reports and may or may not keep the shell alive) leaves no
// transient directory behind either way.
func TestZshLauncher_CleanupAfterSyntaxError(t *testing.T) {
	requireIntegrationShell(t, "zsh")
	home := writeZshFixtureHome(t, "if [[\n")
	tmp := t.TempDir()
	cmd, tierEnv := tierCommand(t, ShellZsh, home, LaunchOptions{Enhanced: true, SessionID: "test-session-8"})

	// The parse error may end the session immediately or leave the shell
	// at a prompt; send exit in either case (benign if already gone).
	runLauncherOnPTY(t, "/bin/sh", cmd,
		append([]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, tierEnv...), "exit")
	assertNoTransientDir(t, tmp)
}

// fileState is one entry of a $HOME snapshot.
type fileState struct {
	size int64
	mode os.FileMode
	mod  time.Time
}

// snapshotTree walks root and records every entry's name, size and mtime.
func snapshotTree(t *testing.T, root string) map[string]fileState {
	t.Helper()
	snap := map[string]fileState{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		snap[rel] = fileState{size: info.Size(), mode: info.Mode(), mod: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func equalSnapshots(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}

// assertNoTransientDir fails unless no launcher transient directory
// remains under tmp.
func assertNoTransientDir(t *testing.T, tmp string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(tmp, "nocx-zsh.*"))
	if err != nil {
		t.Fatalf("glob transient dirs: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("transient directories survived the session: %v", left)
	}
}

// TestZshLauncher_TransientDirRemovedDespiteAForeignFile is the case the
// cleanup used to get wrong, and the one CI found.
//
// The launcher points ZDOTDIR at a directory it created with mktemp. By the
// time the generated .zshrc runs, zsh has already sourced /etc/zshenv,
// /etc/zshrc and zshenv — and any of those may write into ZDOTDIR, because
// that is what ZDOTDIR means. Debian and Ubuntu ship an /etc/zsh/zshrc that
// runs compinit, and compinit's .zcompdump lands there.
//
// The old cleanup deleted the two files the launcher writes and then rmdir'd,
// which fails on a non-empty directory: every session on those distributions
// left a directory in TMPDIR and printed "could not remove transient dir" on
// the user's terminal. The existing flow test cannot catch it — nothing writes
// a stray file on a developer's machine, so it passes with the bug present.
// This one plants the stray file, which is the whole condition.
func TestZshLauncher_TransientDirRemovedDespiteAForeignFile(t *testing.T) {
	zsh := requireShell(t, "zsh")

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("echo USER_RC_RAN\n"), 0o600); err != nil {
		t.Fatalf("write user rc: %v", err)
	}

	// The name has to match the shape the launcher's own mktemp produces:
	// the cleanup is guarded on it precisely so an unexpected path is left
	// alone, and a test that used any other name would be asserting nothing.
	bootstrap := filepath.Join(t.TempDir(), "nocx-zsh.AbC123")
	if err := os.MkdirAll(bootstrap, 0o700); err != nil {
		t.Fatalf("mkdir bootstrap: %v", err)
	}
	rc := zshRcfile(launcherEnvBlock(LaunchOptions{Enhanced: true, SessionID: "s1"}), zshScript,
		capabilityLiteral(zshUnsetExport, "", ""))
	if err := os.WriteFile(filepath.Join(bootstrap, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatalf("write bootstrap rc: %v", err)
	}
	// Stand-in for compinit's dump: written by something that ran before the
	// generated rc, which is the only thing that matters about it.
	if err := os.WriteFile(filepath.Join(bootstrap, ".zcompdump"), []byte("# stray\n"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	out := runLauncherOnPTY(t, zsh, "exec "+zsh+" -l",
		[]string{"HOME=" + home, "ZDOTDIR=" + bootstrap, "TERM=xterm"},
		"exit")

	if _, err := os.Stat(bootstrap); !os.IsNotExist(err) {
		t.Errorf("the transient dir survived because a foreign file was in it: %s (stat err %v); output:\n%s",
			bootstrap, err, out)
	}
	if strings.Contains(out, "could not remove transient dir") {
		t.Errorf("the launcher told the user it could not clean up after itself:\n%s", out)
	}
}
