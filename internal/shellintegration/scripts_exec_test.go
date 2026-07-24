package shellintegration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
