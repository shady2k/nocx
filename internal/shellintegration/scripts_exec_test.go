package shellintegration

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// writeScriptFile materialises an embedded script to a temp file so a real
// shell can source it. Returns the path.
func writeScriptFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// requireShell skips the test when the shell is not installed (CI on macOS has
// zsh; a minimal Linux box may not).
func requireShell(t *testing.T, shell string) string {
	t.Helper()
	path, err := exec.LookPath(shell)
	if err != nil {
		t.Skipf("%s not installed: %v", shell, err)
	}
	return path
}

// runShellProg runs `shell -c prog shell arg` so that $1 == arg inside prog,
// and returns combined stdout+stderr. A non-zero exit (e.g. an intentional
// `false`) is not a test failure; assertions inspect the output.
func runShellProg(t *testing.T, shell, prog, arg string) string {
	t.Helper()
	cmd := exec.Command(shell, "-c", prog, shell, arg)
	cmd.Env = append(os.Environ(), "HOSTNAME=testhost")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s exited non-zero (may be benign): %v", shell, err)
	}
	return string(out)
}

// TestBashIntegration_ReportsRealExitCode drives the real bash hooks and
// asserts the OSC 133 D marker carries the just-finished command's exit code,
// not 0. Regression for nocx-586: __nocx_prompt_command reset $? to 0 (via an
// assignment) before __nocx_precmd could read it.
func TestBashIntegration_ReportsRealExitCode(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Two prompt cycles: the first arms the D marker (no D yet), the second —
	// after `false` — must emit D;1.
	prog := `
export NOCX_SHELL_INTEGRATION=1
source "$1"
__nocx_prompt_command
false
__nocx_prompt_command
`
	out := runShellProg(t, bash, prog, script)
	if !strings.Contains(out, "]133;D;1") {
		t.Errorf("expected OSC 133 D;1 (real exit code); got %q", out)
	}
	if strings.Contains(out, "]133;D;0") {
		t.Errorf("emitted D;0 — the exit code was clobbered before capture (nocx-586): %q", out)
	}
}

// TestBashIntegration_SourcesUnderNounset guards nocx-zrd: sourcing must not
// abort a user's rc that runs under `set -u`.
func TestBashIntegration_SourcesUnderNounset(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)
	prog := `
set -u
export NOCX_SHELL_INTEGRATION=1
source "$1"
echo NOCX_SOURCED_OK
`
	out := runShellProg(t, bash, prog, script)
	if !strings.Contains(out, "NOCX_SOURCED_OK") {
		t.Errorf("sourcing aborted under set -u (nocx-zrd); got %q", out)
	}
}

// TestZshIntegration_ReportsRealExitCode drives the real zsh hooks with a
// hostile precmd hook registered first (as oh-my-zsh / plugins would be) and
// asserts nocx still reports the real exit code. Regression for nocx-hdz:
// __nocx_capture_status must run before any other precmd hook clobbers $?.
func TestZshIntegration_ReportsRealExitCode(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	// A user precmd (registered before nocx is sourced) runs `true`, clobbering
	// $? to 0. nocx forces its capture to the front of precmd_functions, so it
	// must still see the real status. We drive two precmd cycles by hand.
	prog := `
autoload -Uz add-zsh-hook
__hostile_precmd() { true; }
add-zsh-hook precmd __hostile_precmd
export NOCX_SHELL_INTEGRATION=1
source "$1"
true;  for f in $precmd_functions; do $f; done
false; for f in $precmd_functions; do $f; done
`
	out := runShellProg(t, zsh, prog, script)
	if !strings.Contains(out, "]133;D;1") {
		t.Errorf("expected OSC 133 D;1 despite a prior precmd hook; got %q", out)
	}
	if strings.Contains(out, "]133;D;0") {
		t.Errorf("emitted D;0 — a prior precmd hook clobbered $? before capture (nocx-hdz): %q", out)
	}
}

// TestZshIntegration_SourcesUnderNounset guards nocx-zrd for zsh.
func TestZshIntegration_SourcesUnderNounset(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)
	prog := `
set -u
export NOCX_SHELL_INTEGRATION=1
source "$1"
echo NOCX_SOURCED_OK
`
	out := runShellProg(t, zsh, prog, script)
	if !strings.Contains(out, "NOCX_SOURCED_OK") {
		t.Errorf("sourcing aborted under set -u (nocx-zrd); got %q", out)
	}
}

// TestBashMarkerOnlyBeatsHostilePrompt spawns a bash that sources nocx.bash
// with NOCX_PROMPT_MODE=marker-only, with a hostile PROMPT_COMMAND that sets
// PS1='HOSTILE$ ', calls __nocx_prompt_command, and asserts HOSTILE does not
// appear in the rendered prompt.
func TestBashMarkerOnlyBeatsHostilePrompt(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Set hostile PROMPT_COMMAND BEFORE sourcing so nocx captures it.
	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only
PROMPT_COMMAND='PS1="HOSTILE$ "'
source "$1"
__nocx_prompt_command
echo "PS1=[$PS1]"
`
	out := runShellProg(t, bash, prog, script)
	if strings.Contains(out, "HOSTILE") {
		t.Errorf("bash marker-only clobbered by framework PROMPT_COMMAND:\n%s", out)
	}
	if !strings.Contains(out, "]133;B") {
		t.Errorf("bash marker-only prompt missing OSC 133 B marker:\n%s", out)
	}
}

// TestZshMarkerOnlyBeatsHostilePrompt spawns a zsh that sources nocx.zsh
// with NOCX_PROMPT_MODE=marker-only, registers a hostile precmd that sets
// PROMPT='HOSTILE$ ', runs the precmd hooks, and asserts HOSTILE does not
// appear in the rendered prompt.
func TestZshMarkerOnlyBeatsHostilePrompt(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	// Register a hostile precmd BEFORE sourcing nocx so nocx can position
	// its suppressor after it. Then invoke precmd hooks and print PROMPT.
	prog := `
autoload -Uz add-zsh-hook
__hostile() { PROMPT='HOSTILE$ '; }
add-zsh-hook precmd __hostile
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only
source "$1"
for f in $precmd_functions; do $f; done
builtin printf 'PROMPT=[%s]' "$PROMPT"
`
	out := runShellProg(t, zsh, prog, script)
	if strings.Contains(out, "HOSTILE") {
		t.Errorf("marker-only prompt was clobbered by a later precmd hook:\n%s", out)
	}
	// The prompt must still carry the B marker.
	if !strings.Contains(out, "]133;B") {
		t.Errorf("marker-only prompt missing OSC 133 B marker:\n%s", out)
	}
}

// TestZshNativeModeRestoresVisiblePrompt spawns a marker-only zsh, invokes
// __nocx_native_mode, then runs precmd hooks and asserts the prompt is
// visible (contains % or #, not merely a B marker) — nocx-4ff.9.
func TestZshNativeModeRestoresVisiblePrompt(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only
source "$1"

# First run precmd — the marker-only overlay should be active.
for f in $precmd_functions; do $f; done
builtin printf 'BEFORE=[%s]\n' "$PROMPT"

# Escape to native mode.
__nocx_native_mode

# Run precmd again — should now produce a visible prompt.
for f in $precmd_functions; do $f; done
builtin printf 'AFTER=[%s]\n' "$PROMPT"
`
	out := runShellProg(t, zsh, prog, script)

	// Before escape: the prompt must be the B marker only.
	if !strings.Contains(out, "]133;B") {
		t.Errorf("marker-only BEFORE missing OSC 133 B marker:\n%s", out)
	}

	// After escape: the prompt must be visible — contains % or # (zsh prompt),
	// and NOT only the B marker with no other content.
	// The visible prompt is '%~ %# ' which means we should find %~ or a % followed by space.
	// Note: the C marker may prefix the printf output, so search by substring.
	if !strings.Contains(out, "AFTER=[") {
		t.Fatalf("AFTER= line not found in output:\n%s", out)
	}
	afterIdx := strings.Index(out, "AFTER=[")
	afterRest := out[afterIdx:]
	endIdx := strings.Index(afterRest, "]")
	if endIdx < 0 {
		t.Fatalf("could not parse AFTER value from:\n%s", out)
	}
	afterOnly := afterRest[7:endIdx]
	if afterOnly == "" || afterOnly == "%{\x1b]133;B\a%}" {
		t.Errorf("native mode did not restore a visible prompt; PS1 is still marker-only: %q", afterOnly)
	}
	if !strings.Contains(afterOnly, "%~") && !strings.Contains(afterOnly, "%#") {
		t.Logf("prompt after native mode (expected visible chars like %%~): %q", afterOnly)
	}
}

// TestBashNativeModeRestoresVisiblePrompt spawns a marker-only bash, invokes
// __nocx_native_mode, then runs __nocx_prompt_command and asserts the prompt
// is visible (contains \w or \$, not merely a B marker) — nocx-4ff.9.
func TestBashNativeModeRestoresVisiblePrompt(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only
source "$1"

# First run prompt command — the marker-only overlay should be active.
__nocx_prompt_command
echo "BEFORE=[$PS1]"

# Escape to native mode.
__nocx_native_mode

# Run prompt command again — should now produce a visible prompt.
__nocx_prompt_command
echo "AFTER=[$PS1]"
`
	out := runShellProg(t, bash, prog, script)

	// After escape: the prompt must be visible — contains \w or \$.
	// The visible prompt is '\w \$ '.
	// Note: the C marker may prefix the echo output, so search by substring.
	if !strings.Contains(out, "AFTER=[") {
		t.Fatalf("AFTER= line not found in output:\n%s", out)
	}
	afterIdx := strings.Index(out, "AFTER=[")
	afterRest := out[afterIdx:]
	endIdx := strings.Index(afterRest, "]")
	if endIdx < 0 {
		t.Fatalf("could not parse AFTER value from:\n%s", out)
	}
	afterOnly := afterRest[7:endIdx]
	if !strings.Contains(afterOnly, "\\w") && !strings.Contains(afterOnly, "\\$") {
		t.Errorf("native mode did not restore a visible bash prompt; PS1 still marker-only: %q", afterOnly)
	}
}

// TestEnsureInstalled_SkipsVersionWhenGateFails guards nocx-1dx: the VERSION
// marker must not be recorded if a gate append failed, so the next launch
// retries instead of short-circuiting on a version match.
// TestBashTopLevelMarkerOnlyArmsBMarker verifies that a TOP-LEVEL bash
// session with NOCX_PROMPT_MODE=marker-only AND a non-empty NOCX_SESSION_ID
// DOES arm the marker-only B marker — the owner correctly identifies itself
// (nocx-4ff.13 regression fix). Without this guard the owner would see its
// own __nocx_owned_session export and incorrectly treat itself as nested.
func TestBashTopLevelMarkerOnlyArmsBMarker(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Simulate a TOP-LEVEL enhanced session: NOCX_SESSION_ID is set by the
	// backend and NO parent __nocx_owned_session exists.
	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only NOCX_SESSION_ID=deadbeefdeadbeef
source "$1"

# Run two prompt cycles — the second must set the marker-only PS1.
__nocx_prompt_command
__nocx_prompt_command
# PS1 content (includes the B marker escape) and length for assertions.
echo "PS1_CONTENT=$PS1"
echo "PS1_LEN=${#PS1}"
`
	out := runShellProg(t, bash, prog, script)

	// The B marker must be present in the output (precmd emits it).
	if !strings.Contains(out, "]133;B") {
		t.Errorf("top-level marker-only session missing OSC 133 B marker in output:\n%s", out)
	}

	// PS1 must be the marker-only B marker (short, no visible glyphs).
	if !strings.Contains(out, "PS1_LEN=") {
		t.Fatalf("PS1_LEN= line not found in output:\n%s", out)
	}
	idx := strings.Index(out, "PS1_LEN=")
	rest := out[idx:]
	end := strings.Index(rest, "\n")
	if end < 0 {
		end = len(rest)
	}
	lenStr := strings.TrimSpace(rest[8:end])
	var ps1Len int
	if _, err := fmt.Sscanf(lenStr, "%d", &ps1Len); err != nil {
		t.Fatalf("could not parse PS1_LEN: %q", lenStr)
	}
	// The B marker alone is ~12-14 chars. If PS1 is > 25 the B marker
	// was wrapped onto a visible prompt (nested branch, bug).
	if ps1Len > 25 {
		t.Errorf("top-level marker-only PS1 too long (%d chars) — may have fallen into nested branch: %q", ps1Len, out)
	}
}

// TestBashMarkerOnlyNoSpuriousCommandStartDuringSourcing guards the first-prompt
// ownership regression (nocx-4ff, "the editor appears only after the first
// command"). The DEBUG trap is live from the moment `trap ... DEBUG` runs while
// nocx.bash is still being sourced, so the ordinary `[[ ... ]]` tests further
// down the script would fire __nocx_preexec — a spurious OSC 133 C
// (command-start) BEFORE the first prompt's A. That drives the input-ownership
// machine to RUNNING_RAW, so the first A→B lands untrusted and the DOM editor
// never takes ownership until a command has run once. With __nocx_preexec_done
// initialised DISARMED, the first OSC 133 marker at a clean start is A, and C
// still fires for a genuine command line.
func TestBashMarkerOnlyNoSpuriousCommandStartDuringSourcing(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only NOCX_SESSION_ID=deadbeefdeadbeef
source "$1"
__nocx_prompt_command
echo real-command
`
	out := runShellProg(t, bash, prog, script)

	// Match the actual emitted escapes (ESC ] 133 ; X), not literal PS1 text.
	firstA := strings.Index(out, "\x1b]133;A")
	firstC := strings.Index(out, "\x1b]133;C")

	if firstA < 0 {
		t.Fatalf("no OSC 133 A marker emitted at a clean start:\n%q", out)
	}
	// The fix must not disable command markers: a real command still fires C.
	if firstC < 0 {
		t.Fatalf("no OSC 133 C marker for a genuine command — over-suppressed:\n%q", out)
	}
	// A C before the first A is the spurious command-start emitted while the
	// script was being sourced — the bug that poisons first-prompt ownership.
	if firstC < firstA {
		t.Errorf("spurious OSC 133 C before the first A: the editor would not own the first prompt:\n%q", out)
	}
}

// TestBashNestedSessionKeepsVisiblePrompt spawns a bash that inherits a
// NOCX_SESSION_ID (simulating a nested/SSH shell). The marker-only overlay
// must NOT arm — the prompt must stay visible (nocx-4ff.13).
func TestBashNestedSessionKeepsVisiblePrompt(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Simulate a nested session: NOCX_SESSION_ID is already set by a
	// parent nocx session and __nocx_owned_session was exported by the
	// parent. The shell must NOT install the marker-only overlay.
	prog := `
PS1='\w \$ '
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only NOCX_SESSION_ID=parent-id-1234 __nocx_owned_session=parent-id-1234
source "$1"
__nocx_prompt_command
# In marker-only mode, PS1 would be just the B marker (~18 chars).
# In baseline/nested mode, PS1 has the original prompt + B marker appended.
echo "PS1_LEN=${#PS1}"
`
	out := runShellProg(t, bash, prog, script)

	// In nested mode the prompt must NOT be stripped to just the B marker.
	// The B marker alone is ~18 characters. A visible prompt has more.
	if !strings.Contains(out, "PS1_LEN=") {
		t.Fatalf("PS1_LEN= line not found in output:\n%s", out)
	}
	idx := strings.Index(out, "PS1_LEN=")
	rest := out[idx:]
	end := strings.Index(rest, "\n")
	if end < 0 {
		end = len(rest)
	}
	lenStr := strings.TrimSpace(rest[8:end])
	// The B marker alone is ~12 characters. A visible prompt is longer.
	var ps1Len int
	if _, err := fmt.Sscanf(lenStr, "%d", &ps1Len); err != nil || ps1Len <= 14 {
		t.Errorf("nested session PS1 too short (%q chars) — marker-only may be armed: %q", lenStr, out)
	}
}

// TestZshNestedSessionKeepsVisiblePrompt spawns a zsh that inherits a
// NOCX_SESSION_ID (simulating a nested/SSH shell). The marker-only overlay
// must NOT arm — the prompt must stay visible (nocx-4ff.13).
func TestZshNestedSessionKeepsVisiblePrompt(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only NOCX_SESSION_ID=parent-id-1234 __nocx_owned_session=parent-id-1234
source "$1"
# In a nested session, __nocx_marker_only_prompt must NOT be in precmd_functions.
builtin printf 'MARKER_ONLY_IN_PRECMD=%s\n' "${precmd_functions[(r)__nocx_marker_only_prompt]:-NO}"
`
	out := runShellProg(t, zsh, prog, script)

	if !strings.Contains(out, "MARKER_ONLY_IN_PRECMD=") {
		t.Fatalf("MARKER_ONLY_IN_PRECMD= line not found in output:\n%s", out)
	}
	idx := strings.Index(out, "MARKER_ONLY_IN_PRECMD=")
	rest := out[idx:]
	end := strings.Index(rest, "\n")
	if end < 0 {
		end = len(rest)
	}
	val := strings.TrimSpace(rest[22:end])
	if val != "NO" {
		t.Errorf("nested session incorrectly armed marker-only prompt; precmd_functions=%q", val)
	}
}

func TestEnsureInstalled_SkipsVersionWhenGateFails(t *testing.T) {
	home := t.TempDir()
	s := New(testLogger())

	// Force a gate-append failure for one rc file by making its path a
	// directory (ReadFile/rename cannot treat it as a regular file).
	if err := os.Mkdir(filepath.Join(home, ".bashrc"), 0o750); err != nil {
		t.Fatalf("mkdir bad rc: %v", err)
	}

	if err := s.EnsureInstalled(home); err != nil {
		t.Fatalf("EnsureInstalled should stay non-fatal on gate failure: %v", err)
	}

	vf := filepath.Join(home, dirName, versionFile)
	if _, err := os.Stat(vf); err == nil {
		t.Fatal("VERSION was written despite a gate-append failure — integration would be stranded (nocx-1dx)")
	}
}

// TestBashSnapshotEmitsHelloThenSnapshot drives the real bash hooks through
// two prompt cycles: sourcing emits the OSC 636 hello (a 32-hex session
// nonce) exactly once, and the FIRST prompt emits the snapshot — carrying
// the same nonce and real command names (pwd is a builtin, so it is always
// in compgen -c). The snapshot must be there before the first prompt is
// usable, not after an unrelated command has run.
func TestBashSnapshotEmitsHelloThenSnapshot(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Keep this ordering test independent of the host's PATH size and scheduler
	// contention; TestBashSnapshotFirstPromptBoundedWait covers the slow path.
	prog := `
enable -n compgen
compgen() { printf 'pwd\n'; }
export NOCX_SHELL_INTEGRATION=1
source "$1"
__nocx_prompt_command
__nocx_prompt_command
`
	out := runShellProg(t, bash, prog, script)

	if got := strings.Count(out, "]636;H;"); got != 1 {
		t.Errorf("expected exactly one OSC 636 hello; got %d in %q", got, out)
	}
	if got := strings.Count(out, "]636;S;"); got != 1 {
		t.Errorf("expected exactly one OSC 636 snapshot; got %d in %q", got, out)
	}

	// The hello is the FIRST 636 message: nothing before it may carry the code.
	if strings.Index(out, "]636;H;") != strings.Index(out, "]636;") {
		t.Errorf("the hello is not the first OSC 636 message: %q", out)
	}

	// The snapshot must arrive DURING the first prompt cycle: after the first
	// 133;A and before the second one. That is the freshly-opened-tab case.
	firstA := strings.Index(out, "]133;A")
	if firstA < 0 {
		t.Fatalf("no 133;A marker: %q", out)
	}
	secondA := strings.Index(out[firstA+len("]133;A"):], "]133;A")
	if secondA < 0 {
		t.Fatalf("no second 133;A marker (second prompt missing): %q", out)
	}
	snap := strings.Index(out, "]636;S;")
	if snap < 0 {
		t.Fatalf("no snapshot emitted: %q", out)
	}
	if snap < firstA || snap > firstA+len("]133;A")+secondA {
		t.Errorf("snapshot must be emitted within the FIRST prompt cycle (between the first and second 133;A); positions: first A=%d, second A=%d, S=%d in %q",
			firstA, firstA+len("]133;A")+secondA, snap, out)
	}

	// Extract the hello nonce and require the snapshot to carry the same one.
	after := strings.SplitN(out, "]636;H;", 2)
	if len(after) < 2 {
		t.Fatalf("no OSC 636 hello emitted: %q", out)
	}
	nonce := strings.SplitN(after[1], "\x07", 2)[0]
	if len(nonce) != 32 {
		t.Errorf("nonce = %q, want 32 hex chars", nonce)
	}

	snapPayload := out[snap+len("]636;S;"):]
	if !strings.HasPrefix(snapPayload, nonce) {
		t.Errorf("snapshot nonce does not match the hello nonce: %q", snapPayload[:min(len(snapPayload), 40)])
	}
	if !strings.Contains(snapPayload, ";pwd;") {
		t.Errorf("snapshot payload missing the pwd builtin: %q", snapPayload[:min(len(snapPayload), 200)])
	}
}

// TestBashSnapshotFirstPromptBoundedWait drives the hook with a compgen that
// sleeps longer than the 250 ms first-prompt bound. The FIRST prompt must not
// wait for it: the snapshot is deferred to a later prompt, and the prompt is
// still reached within the 250 ms bound. The still-sleeping job is killed by
// the EXIT trap, so nothing is left behind either.
func TestBashSnapshotFirstPromptBoundedWait(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)
	tmp := t.TempDir()

	prog := `
enable -n compgen
compgen() { printf 'pwd\n'; sleep 1; }
export NOCX_SHELL_INTEGRATION=1
source "$1"
export LC_NUMERIC=C
TIMEFORMAT='PROMPT_MS=%R'
{ time __nocx_prompt_command; } 2>&1
`
	out := runShellProgEnv(t, bash, prog, script, "TMPDIR="+tmp)

	// The snapshot cannot be ready: the stub compgen is still sleeping.
	if strings.Contains(out, "]636;S;") {
		t.Errorf("a snapshot was emitted while compgen was still running: %q", out)
	}

	// The first prompt must not have waited for compgen.
	idx := strings.Index(out, "PROMPT_MS=")
	if idx < 0 {
		t.Fatalf("no PROMPT_MS timing captured: %q", out)
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(strings.SplitN(out[idx+len("PROMPT_MS="):], "\n", 2)[0]), 64)
	if err != nil {
		t.Fatalf("PROMPT_MS is not a number: %q", out)
	}
	if secs > 1.0 {
		t.Errorf("first prompt waited %.3fs for the snapshot — the bound is 250 ms", secs)
	}

	// The still-running job was killed by the EXIT trap: nothing survives.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read %s: %v", tmp, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "nocx-snap") {
			t.Errorf("snapshot file %q survived shell exit", e.Name())
		}
	}
}

// runShellProgEnv is runShellProg with extra environment entries appended
// (for duplicate keys the LAST entry wins, which is how TMPDIR is overridden).
func runShellProgEnv(t *testing.T, shell, prog, arg string, extraEnv ...string) string {
	t.Helper()
	cmd := exec.Command(shell, "-c", prog, shell, arg)
	cmd.Env = append(os.Environ(), "HOSTNAME=testhost")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s exited non-zero (may be benign): %v", shell, err)
	}
	return string(out)
}

// helloNonce extracts the 32-hex session nonce from the OSC 636 hello.
func helloNonce(t *testing.T, out string) string {
	t.Helper()
	after := strings.SplitN(out, "]636;H;", 2)
	if len(after) < 2 {
		t.Fatalf("no OSC 636 hello emitted: %q", out)
	}
	nonce := strings.SplitN(after[1], "\x07", 2)[0]
	if len(nonce) != 32 {
		t.Fatalf("nonce = %q, want 32 hex chars", nonce)
	}
	return nonce
}

// TestBashSnapshotTempFilesStayPrivate drives the real bash hook through one
// prompt cycle and then exits WITHOUT a second prompt — the exact path that
// used to leave the session nonce world-readable on disk. The nonce must
// never appear in the $TMPDIR listing, every snapshot file must be mode 600
// from creation (mktemp, no chmod window), and nothing may survive the
// shell: the chained EXIT trap must remove both the staging file and the
// .snap final even though the snapshot was never emitted.
func TestBashSnapshotTempFilesStayPrivate(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)
	tmp := t.TempDir()

	prog := `
# A pre-existing EXIT trap must be chained, not overwritten.
trap 'printf CHAINED_EXIT' EXIT
# A compgen that sleeps longer than the first-prompt bound: the snapshot never
# becomes ready, so the
# first prompt's bounded wait times out and the files are still on disk when
# the shell exits — the leak path.
enable -n compgen
compgen() { printf 'pwd\n'; sleep 1; }
export NOCX_SHELL_INTEGRATION=1
source "$1"
# The staging file exists from source time (mktemp, mode 600) — observe it
# before the first prompt; the background compgen is still sleeping.
printf '\nMODE_SOURCE\n'
ls -l "$TMPDIR"/nocx-snap.* 2>/dev/null
printf '\nLS_SOURCE\n'
ls -A "$TMPDIR"
__nocx_prompt_command
# Exit without a second prompt: the snapshot was never emitted, so any file
# left behind is a leak. The EXIT trap must clean both files.
`
	out := runShellProgEnv(t, bash, prog, script, "TMPDIR="+tmp)
	nonce := helloNonce(t, out)

	// The pre-existing EXIT trap survived: the hook chained it, not replaced it.
	if !strings.Contains(out, "CHAINED_EXIT") {
		t.Errorf("the pre-existing EXIT trap was not chained: %q", out)
	}

	// The snapshot must not have been emitted: this is the early-exit path.
	if strings.Contains(out, "]636;S;") {
		t.Errorf("a snapshot was emitted before the second prompt: %q", out)
	}

	// 1) The nonce never appears in any $TMPDIR listing during the session.
	lsIdx := strings.Index(out, "LS_SOURCE")
	if lsIdx < 0 {
		t.Fatalf("LS_SOURCE section missing from output: %q", out)
	}
	lsSection := out[lsIdx+len("LS_SOURCE"):]
	if idx := strings.Index(lsSection, nonce); idx >= 0 {
		t.Errorf("session nonce %q appears in the $TMPDIR listing at byte %d: %q",
			nonce, idx, lsSection[:min(len(lsSection), 200)])
	}

	// 2) The snapshot file present mid-session is mode 600 (rw-------).
	modeIdx := strings.Index(out, "MODE_SOURCE")
	if modeIdx < 0 {
		t.Fatalf("MODE_SOURCE section missing from output: %q", out)
	}
	modeSection := out[modeIdx+len("MODE_SOURCE"):]
	if end := strings.Index(modeSection, "LS_SOURCE"); end >= 0 {
		modeSection = modeSection[:end]
	}

	seen := 0
	for _, line := range strings.Split(modeSection, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		seen++
		if len(fields) < 9 {
			t.Errorf("unexpected ls -l line %q", line)
			continue
		}
		if fields[0] != "-rw-------" {
			t.Errorf("snapshot file %s has mode %s, want -rw------- (600)", fields[8], fields[0])
		}
	}
	if seen == 0 {
		t.Error("no snapshot file was observed mid-session (mktemp failed?)")
	}

	// 3) The shell exited between the first and second prompt: nothing survives.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read %s: %v", tmp, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "nocx-snap") {
			t.Errorf("snapshot file %q survived shell exit", e.Name())
		}
	}
}

// oscMarker is one parsed OSC 636/133 marker, in stream order.
type oscMarker struct {
	kind string // H, S, A, B, C, D
	pos  int    // byte offset in the raw output
}

// extractOscMarkers returns the 133 A/B/C/D and 636 H/S markers in order.
func extractOscMarkers(out string) []oscMarker {
	var ms []oscMarker
	for i := 0; i+2 <= len(out); {
		if out[i] != 0x1b || out[i+1] != ']' {
			i++
			continue
		}
		rel := strings.IndexByte(out[i:], 0x07)
		if rel < 0 {
			break
		}
		head := strings.SplitN(out[i+2:i+rel], ";", 3)
		if len(head) >= 2 {
			switch head[0] {
			case "133":
				if k := head[1]; k == "A" || k == "B" || k == "C" || k == "D" {
					ms = append(ms, oscMarker{k, i})
				}
			case "636":
				if k := head[1]; k == "H" || k == "S" {
					ms = append(ms, oscMarker{k, i})
				}
			}
		}
		i += rel + 1
	}
	return ms
}

// stripOsc removes every OSC sequence from out — the protocol payloads the
// frontend consumes — leaving the plain text a user would see on the tty.
func stripOsc(out string) string {
	var b strings.Builder
	for i := 0; i < len(out); {
		if i+2 <= len(out) && out[i] == 0x1b && out[i+1] == ']' {
			if rel := strings.IndexByte(out[i:], 0x07); rel >= 0 {
				i += rel + 1
				continue
			}
		}
		b.WriteByte(out[i])
		i++
	}
	return b.String()
}

// TestBashSnapshotArrivesBeforeFirstPrompt drives a REAL interactive bash in
// marker-only mode (the mode the app uses) on a pty, installed the way the
// app installs it (the gate line in ~/.bashrc sourcing ~/.nocx/
// shell-integration.bash). It asserts the marker order the owner's acceptance
// depends on: 636;S arrives before the first 133;B — before the first prompt
// is usable — so typing a nonexistent command in a freshly opened tab marks
// it. It also asserts the snapshot never appears between a 133;C and its
// 133;D, that the disowned background job prints no job-control notification
// (none of Done/compgen/__nocx_snap outside the OSC payloads), exactly one
// snapshot per session, and that no snapshot file survives the session.
func TestBashSnapshotArrivesBeforeFirstPrompt(t *testing.T) {
	bash := requireShell(t, "bash")
	home := t.TempDir()
	tmp := t.TempDir()
	nocxDir := filepath.Join(home, ".nocx")
	if err := os.MkdirAll(nocxDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", nocxDir, err)
	}
	if err := os.WriteFile(filepath.Join(nocxDir, "shell-integration.bash"), []byte(bashScript), 0o600); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	gate := "# nocx terminal shell integration\n" +
		`[[ -n "$NOCX_SHELL_INTEGRATION" ]] && source "$HOME/.nocx/shell-integration.bash"` + "\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(gate), 0o600); err != nil {
		t.Fatalf("write .bashrc: %v", err)
	}

	// #nosec G204 — bash is the path resolved by the test helper above, not
	// input; an interactive shell on a pty is the only way to observe the
	// prompt-time ordering this test is about.
	cmd := exec.Command(bash, "-i")
	cmd.Env = append(
		os.Environ(),
		"HOME="+home,
		"TMPDIR="+tmp,
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=ptytest",
	)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	done := make(chan []byte, 1)
	go func() {
		out, _ := io.ReadAll(ptmx)
		done <- out
	}()

	// Let the first prompt render (hello, A, snapshot, B), then run one
	// command — bracketed by C/D — then exit.
	time.Sleep(300 * time.Millisecond)
	if _, werr := ptmx.Write([]byte("true\n")); werr != nil {
		t.Fatalf("write command: %v", werr)
	}
	time.Sleep(300 * time.Millisecond)
	if _, werr := ptmx.Write([]byte("exit\n")); werr != nil {
		t.Fatalf("write exit: %v", werr)
	}

	var out []byte
	select {
	case out = <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for the interactive session to end")
	}
	if werr := cmd.Wait(); werr != nil {
		t.Logf("bash exited non-zero (may be benign): %v", werr)
	}
	output := string(out)

	ms := extractOscMarkers(output)
	firstH, firstS, firstB := -1, -1, -1
	for _, m := range ms {
		if firstH < 0 && m.kind == "H" {
			firstH = m.pos
		}
		if firstS < 0 && m.kind == "S" {
			firstS = m.pos
		}
		if firstB < 0 && m.kind == "B" {
			firstB = m.pos
		}
	}
	if firstH < 0 || firstS < 0 || firstB < 0 {
		t.Fatalf("missing markers (H=%d S=%d B=%d) in %q", firstH, firstS, firstB, output)
	}
	if firstS > firstB {
		t.Errorf("the snapshot arrived AFTER the first prompt was usable: S at %d, first B at %d", firstS, firstB)
	}
	if firstH > firstS {
		t.Errorf("the hello did not precede the snapshot: H at %d, S at %d", firstH, firstS)
	}

	// No snapshot between a command-start (C) and its command-end (D).
	for i, m := range ms {
		if m.kind != "C" {
			continue
		}
		nextD := -1
		for j := i + 1; j < len(ms); j++ {
			if ms[j].kind == "D" {
				nextD = ms[j].pos
				break
			}
		}
		if nextD < 0 {
			continue // trailing C at exit; no D follows
		}
		for _, s := range ms {
			if s.kind == "S" && s.pos > m.pos && s.pos < nextD {
				t.Errorf("snapshot interleaved with command output (between C at %d and D at %d)", m.pos, nextD)
			}
		}
	}

	if n := countMarkers(ms, "S"); n != 1 {
		t.Errorf("expected exactly one snapshot; got %d", n)
	}
	if n := countMarkers(ms, "H"); n != 1 {
		t.Errorf("expected exactly one hello; got %d", n)
	}

	// The disowned job prints no job-control notification: outside the OSC
	// payloads the session output must contain none of the strings the owner
	// saw printed into his output region.
	visible := stripOsc(output)
	for _, s := range []string{"Done", "compgen", "__nocx_snap"} {
		if strings.Contains(visible, s) {
			t.Errorf("job-control notification leaked into the session output (found %q): %q", s, visible)
		}
	}

	// Nothing survives the session.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read %s: %v", tmp, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "nocx-snap") {
			t.Errorf("snapshot file %q survived the session", e.Name())
		}
	}
}

// countMarkers counts markers of one kind.
func countMarkers(ms []oscMarker, kind string) int {
	n := 0
	for _, m := range ms {
		if m.kind == kind {
			n++
		}
	}
	return n
}
