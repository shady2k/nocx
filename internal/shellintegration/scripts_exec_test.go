package shellintegration

import (
	"encoding/json"
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
// PS1='HOSTILE$ ', and asserts the B-marker overlay wins — but ONLY once the
// authenticated channel is live. ADR-0024 decision 9: suppressing the native
// prompt without a live domain is the phishing primitive, so a shell whose
// handshake never completed keeps the framework's visible prompt. The live
// state is injected here (the accept path itself is exercised end to end by
// the channel tests); the no-channel arm below pins the new fail-open half.
func TestBashMarkerOnlyBeatsHostilePrompt(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Set hostile PROMPT_COMMAND BEFORE sourcing so nocx captures it.
	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only
PROMPT_COMMAND='PS1="HOSTILE$ "'
source "$1"
exec 9>/dev/null
__nocx_lc_fd=9
__nocx_lc_lane_esc=L
__nocx_lc_dom_esc=D
__nocx_lc_epoch=1
__nocx_cap=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
__nocx_lc_active=1
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

// TestBashMarkerOnlyKeepsNativePromptWithoutChannel pins decision 9's other
// half: a marker-only shell whose channel never became live must NOT
// suppress the prompt. Before the channel, this test asserted the B marker
// won unconditionally — the exact failure ADR-0024 decision 9 forbids.
func TestBashMarkerOnlyKeepsNativePromptWithoutChannel(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)
	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only
PROMPT_COMMAND='PS1="HOSTILE$ "'
source "$1"
__nocx_prompt_command
echo "PS1=[$PS1]"
`
	out := runShellProg(t, bash, prog, script)
	if !strings.Contains(out, "HOSTILE") {
		t.Errorf("bash suppressed its prompt without a live channel; the framework's prompt must stand (ADR-0024 decision 9):\n%s", out)
	}
}

// TestZshMarkerOnlyBeatsHostilePrompt spawns a zsh that sources nocx.zsh
// with NOCX_PROMPT_MODE=marker-only, registers a hostile precmd that sets
// PROMPT='HOSTILE$ ', runs the precmd hooks, and asserts HOSTILE does not
// appear in the rendered prompt once the channel is live (injected here;
// the accept path is covered by the channel tests).
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
exec 9>/dev/null
__nocx_lc_fd=9
__nocx_lc_lane_esc=L
__nocx_lc_dom_esc=D
__nocx_lc_epoch=1
__nocx_cap=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
__nocx_lc_active=1
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

// TestZshMarkerOnlyKeepsNativePromptWithoutChannel is the zsh half of the
// decision-9 fail-open: no live channel, no prompt suppression.
func TestZshMarkerOnlyKeepsNativePromptWithoutChannel(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)
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
	if !strings.Contains(out, "HOSTILE") {
		t.Errorf("zsh suppressed its prompt without a live channel (ADR-0024 decision 9):\n%s", out)
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
__nocx_lc_active=1

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
__nocx_lc_active=1

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
	// backend and NO parent __nocx_owned_session exists. The marker-only
	// prompt is suppressed only when the channel is live (ADR-0024 decision
	// 9), so the channel state is injected after sourcing, writing to
	// /dev/null (the accept path itself is exercised end to end by the
	// channel tests).
	prog := `
export NOCX_SHELL_INTEGRATION=1 NOCX_PROMPT_MODE=marker-only NOCX_SESSION_ID=deadbeefdeadbeef
source "$1"
exec 9>/dev/null
__nocx_lc_fd=9
__nocx_lc_lane_esc=L
__nocx_lc_dom_esc=D
__nocx_lc_epoch=1
__nocx_cap=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
__nocx_lc_active=1

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

	// compgen is stubbed FAST, the mirror of what
	// TestBashSnapshotFirstPromptBoundedWait does by stubbing it slow.
	//
	// What is asserted below is an ORDER — the snapshot lands inside the first
	// prompt cycle — and that order only exists while the source-time job beats
	// the 250 ms grace period the script grants it. Past that the product
	// deliberately defers the snapshot to a later prompt, so on a machine where
	// the real compgen is slow this test was measuring the runner rather than
	// the hook, and it duly failed on CI while passing on every developer's
	// laptop (nocx-0ije). Stubbing removes the machine from the question. The
	// payload assertion still wants pwd, so the stub emits it.
	prog := `
enable -n compgen
compgen() { builtin printf '%s\n' cd echo pwd true; }
export NOCX_SHELL_INTEGRATION=1
source "$1"
__nocx_prompt_command
__nocx_prompt_command
`
	// The shipped 250 ms is a budget for a human's prompt, not for this
	// assertion. What is under test is that the source-time job produces a
	// snapshot and the first prompt emits it — and on a loaded runner compgen
	// simply takes longer than a UX deadline, so the default turned a
	// mechanism test into a machine-speed test (nocx-0ije). The budget is
	// stated here instead, generously, because nothing in this test is about
	// how long a user should wait.
	out := runShellProgEnv(t, bash, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")

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

// TestBashSnapshotSurvivesTheSecondSource drives the arrangement the launcher
// actually produces, which the test above does not: the user's ~/.bashrc
// sources the installer-era copy, and then the launcher rcfile unsets
// __nocx_loaded and sources the session's authenticated copy OVER it. That
// second pass is not an edge case — nocx.bash's own comment on
// __nocx_snapshot_wait_ms calls it "EVERY local enhanced session, because the
// app writes that gate line itself".
//
// The second pass used to mint a fresh nonce and announce a second hello.
// command-snapshot.ts keeps the FIRST hello on purpose — accepting a re-hello
// is exactly the re-anchoring its forgery defence exists to prevent — so the
// snapshot, emitted later from a prompt under the SECOND nonce, failed the
// match and was discarded. The store stayed `unavailable` for the life of the
// session and command completion never learned a single command name. Both
// frames were well-formed; they simply disagreed, which is why every check
// that looked at one frame at a time stayed green (nocx-cbtc).
//
// So the assertion is on the PAIR: one hello, one snapshot, same nonce.
func TestBashSnapshotSurvivesTheSecondSource(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// The REWIND, then the re-source — mirroring bashRcfileTemplate, not a
	// simplification of it. The rcfile restores the user's original DEBUG and
	// EXIT traps and their PROMPT_COMMAND before unsetting __nocx_loaded,
	// precisely so the fresh install chains to the user's originals instead of
	// to our own wrappers. Leaving that out is not a smaller version of the
	// same thing: __nocx_prompt_command then chains to itself, bash recurses
	// through the prompt until the stack is gone, and the shell segfaults — so
	// a test without the rewind measures a shell nobody runs.
	//
	// Kept in step with the launcher by the assertion below, which fails if
	// the rcfile stops doing this at all.
	if !strings.Contains(bashRcfileTemplate, "unset __nocx_loaded") {
		t.Fatal("bashRcfileTemplate no longer re-sources the script; this test reproduces an arrangement that no longer exists")
	}
	// compgen is stubbed fast for the same reason as the test above: what is
	// under test is the nonce's identity across a re-source, never the
	// machine's speed.
	prog := `
enable -n compgen
compgen() { builtin printf '%s\n' cd echo pwd true; }
export NOCX_SHELL_INTEGRATION=1
source "$1"
if [[ -n "${__nocx_old_debug:-}" ]]; then trap "${__nocx_old_debug}" DEBUG; else trap - DEBUG; fi
if [[ -n "${__nocx_old_exit:-}" ]]; then trap "${__nocx_old_exit}" EXIT; else trap - EXIT; fi
if [[ "${PROMPT_COMMAND-}" == "__nocx_prompt_command" ]]; then PROMPT_COMMAND="${__nocx_old_pc-}"; fi
unset __nocx_loaded __nocx_prompt_wrapped __nocx_owned_session \
      __nocx_arm_marker_only __nocx_preexec_done __nocx_in_prompt_command \
      __nocx_first_prompt
source "$1"
__nocx_prompt_command
__nocx_prompt_command
`
	out := runShellProgEnv(t, bash, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")

	if got := strings.Count(out, "]636;H;"); got != 1 {
		t.Errorf("a re-source announced a second session: want exactly 1 OSC 636 hello, got %d in %q", got, out)
	}
	if got := strings.Count(out, "]636;S;"); got != 1 {
		t.Errorf("want exactly 1 OSC 636 snapshot, got %d in %q", got, out)
	}

	// The nonce the snapshot carries is the one the hello established. This is
	// the assertion the defect actually failed, and the counts above can all
	// pass while it does not.
	hello := helloNonce(t, out)
	after := strings.SplitN(out, "]636;S;", 2)
	if len(after) < 2 {
		t.Fatalf("no OSC 636 snapshot emitted: %q", out)
	}
	snapNonce := strings.SplitN(after[1], ";", 2)[0]
	if snapNonce != hello {
		t.Errorf("snapshot nonce = %q, hello established %q — the renderer discards a mismatch, so command completion never works in this session", snapNonce, hello)
	}

	// And the payload is real: pwd is a builtin, so it is in the
	// session-local enumeration on every machine.
	if !strings.Contains(after[1], "pwd") {
		t.Errorf("snapshot carries no command names: %q", out)
	}
}

// TestBashSnapshotAnnouncesInAChildThatInheritedTheNonce is the other half of
// the once-per-shell latch, and it guards the failure mode the FIRST attempt
// at that latch introduced.
//
// Latching on "is __nocx_snapshot_nonce set" cannot tell a re-source from an
// INHERITED value, and the two need opposite answers. A nonce that reached
// the environment — a user rc under `set -a` auto-exports every assignment,
// which is the hazard the script's own `export -n` exists for — then silenced
// a legitimately new shell for good: no hello, so no snapshot is ever
// accepted, and completion is dead for that session with nothing said.
// Measured at the time: zero hello frames and zero snapshots in the child.
//
// That is also fail-CLOSED, the wrong direction for this file. The latch is
// the shell's own pid, which no child can inherit, so this test and
// TestBashSnapshotSurvivesTheSecondSource pin the two directions together —
// neither can be satisfied by dropping the other.
func TestBashSnapshotAnnouncesInAChildThatInheritedTheNonce(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// The parent exports a nonce; this shell is the child that inherits it.
	prog := `
enable -n compgen
compgen() { builtin printf '%s\n' cd echo pwd true; }
export NOCX_SHELL_INTEGRATION=1
export __nocx_snapshot_nonce=deadbeefdeadbeefdeadbeefdeadbeef
source "$1"
__nocx_prompt_command
__nocx_prompt_command
`
	out := runShellProgEnv(t, bash, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")

	if got := strings.Count(out, "]636;H;"); got != 1 {
		t.Errorf("a shell that inherited a nonce must still announce itself: want 1 OSC 636 hello, got %d in %q", got, out)
	}
	// And the nonce it announces is its OWN, not the inherited one — an
	// inherited nonce is another session's, and reusing it would let one
	// session's snapshot be accepted for another.
	hello := helloNonce(t, out)
	if hello == "deadbeefdeadbeefdeadbeefdeadbeef" {
		t.Error("the child announced the INHERITED nonce; it must mint its own")
	}
	if got := strings.Count(out, "]636;S;"); got != 1 {
		t.Errorf("want 1 OSC 636 snapshot, got %d in %q", got, out)
	}
	after := strings.SplitN(out, "]636;S;", 2)
	if len(after) < 2 || strings.SplitN(after[1], ";", 2)[0] != hello {
		t.Errorf("snapshot nonce does not match the hello's in %q", out)
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
	cmd.Env = append(os.Environ(), "HOSTNAME=testhost", "LC_ALL=C")
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
		// The mode string is ten characters of type and permission bits, and
		// `ls -l` may append ONE more: "@" when the file carries extended
		// attributes, "+" for an ACL, "." for a security context. macOS tags
		// every file mktemp creates with com.apple.provenance, so on darwin
		// this column reads "-rw-------@" and a string compare against the
		// bare mode fails on the developer's own machine — for a marker that
		// is not a permission and grants nobody anything. What this assertion
		// is about is the permission bits, so compare those.
		if mode := fields[0]; len(mode) < 10 || mode[:10] != "-rw-------" {
			t.Errorf("snapshot file %s has mode %s, want -rw------- (600)", fields[8], mode)
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
	kind string // H, S, P, A, B, C, D
	pos  int    // byte offset in the raw output
}

// extractOscMarkers returns the 133 A/B/C/D and 636 H/S/P markers in order.
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
				if k := head[1]; k == "H" || k == "S" || k == "P" {
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
	// The stub goes in FIRST, before the gate line sources the integration, so
	// the script's source-time snapshot job picks up the function rather than
	// the builtin. Same reason as the sibling test: this case asserts that the
	// snapshot reaches the terminal before the first prompt is usable, which is
	// an ordering guarantee that only holds while compgen finishes inside the
	// script's 250 ms grace period. A real compgen on a loaded runner does not,
	// and the product is right to defer — so the assertion has to be freed from
	// the machine to be about the hook at all (nocx-0ije).
	gate := "compgen() { builtin printf '%s\\n' cd echo pwd true; }\n" +
		"enable -n compgen\n" +
		"# nocx terminal shell integration\n" +
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
		// What this test asserts is an ORDER — the snapshot reaches the
		// terminal before the first prompt is usable — not a speed. The
		// shipped 250 ms is how long a human's prompt may be held, and on a
		// loaded machine compgen outlasts it, at which point the product
		// correctly degrades to a later prompt and the order under test
		// stops existing. Stating a budget here keeps the assertion about
		// ordering (nocx-0ije).
		"NOCX_SNAPSHOT_WAIT_MS=5000",
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

// TestBashSnapshotEncodesHostileNames pins the payload encoder byte for byte.
//
// The encoder is the snapshot's escaping layer: `;` separates names and `\`
// starts an escape, so a name carrying either must not be able to forge a
// boundary or a second field. It had no test of its own — every existing
// assertion looked for `;pwd;` in a payload of ordinary names, which the
// identity function would also satisfy.
//
// That gap became urgent when the encoder gained a fast path for names needing
// no escaping (nocx-z9s9.16 — it was ~85ms of the ~104ms snapshot pipeline, in
// front of a 250ms grace that has no second chance). A fast path is only safe
// while it is a strict subset of the slow one, and "I read it carefully" is not
// how that gets established. Both paths are exercised here: the first two cases
// take the fast path, every other case must fall through to the loop.
func TestBashSnapshotEncodesHostileNames(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// Sourcing emits the hello on stdout; send it away and mark the payload so
	// the assertion reads exactly what the encoder produced.
	prog := `
export NOCX_SHELL_INTEGRATION=1
source "$1" >/dev/null 2>&1
{
  printf 'plain\n'
  printf 'with space\n'
  printf 'semi;colon\n'
  printf 'back\\slash\n'
  printf 'ctl\001x\n'
  printf 'del\177x\n'
  printf 'c1\202x\n'
  printf 'utf\303\251\n'
} | __nocx_snapshot_build > /tmp/nocx-enc-payload
printf 'START'
cat /tmp/nocx-enc-payload
printf 'END'
`
	out := runShellProg(t, bash, prog, script)
	start := strings.Index(out, "START")
	end := strings.LastIndex(out, "END")
	if start < 0 || end < start {
		t.Fatalf("could not delimit the payload in %q", out)
	}
	got := out[start+len("START") : end]

	// Each name is followed by ';'. Printable ASCII passes through unchanged;
	// a literal backslash doubles; a literal ';' becomes \x3b so it can never
	// be read as a separator; C0, DEL and C1 (127..159) are hex-escaped; and
	// bytes >= 0xa0 pass through raw, because the terminal decodes UTF-8 and
	// escaping it would double the payload for no safety.
	want := "plain;" +
		"with space;" +
		`semi\x3bcolon;` +
		`back\\slash;` +
		`ctl\x01x;` +
		`del\x7fx;` +
		`c1\x82x;` +
		"utf\xc3\xa9;"
	if got != want {
		t.Errorf("encoded payload mismatch\n got: %q\nwant: %q", got, want)
	}
}

// ── The zsh tier of the command-existence snapshot (nocx-qduc) ────────────
//
// macOS's default login shell is zsh and macOS is this MVP's platform, so a
// zsh with no snapshot is a session where tab completion never learns a
// single command name — the dropdown says "Command names are still loading"
// for the life of the tab (suggest/controller.ts). The frontend cannot know
// a shell's aliases, functions, builtins and PATH; it asks, and only the
// shell can answer (command-snapshot.ts).
//
// The protocol is the bash tier's, unchanged — one hello per session before
// the first prompt, one snapshot carrying that nonce, the same hex escaping
// and the same caps — so these tests are deliberately the bash tests with a
// zsh driver. Where the two shells differ is mechanism, and the difference
// is what each test names.

// zshPrecmdCycle is one prompt boundary for a non-interactive zsh: zle is
// not running, so the precmd chain is driven by hand exactly as the existing
// zsh exec tests drive it.
const zshPrecmdCycle = "for f in $precmd_functions; do $f; done\n"

// TestZshSnapshotEmitsHelloThenSnapshot is the zsh twin of
// TestBashSnapshotEmitsHelloThenSnapshot: sourcing announces the session
// once, and the FIRST prompt carries the snapshot under that same nonce.
//
// The bash twin stubs `compgen` to take the machine out of the question.
// zsh's enumeration is not a command but parameter expansion over the
// shell's own tables (${(k)commands} and friends), so there is nothing to
// stub — the budget is stated instead, generously, because nothing here is
// about how long a user should wait.
func TestZshSnapshotEmitsHelloThenSnapshot(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1
source "$1"
` + zshPrecmdCycle + zshPrecmdCycle
	out := runShellProgEnv(t, zsh, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")

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

	nonce := helloNonce(t, out)
	snapPayload := out[snap+len("]636;S;"):]
	if !strings.HasPrefix(snapPayload, nonce) {
		t.Errorf("snapshot nonce does not match the hello nonce: %q", snapPayload[:min(len(snapPayload), 40)])
	}
	// pwd is a zsh builtin, so it is in every enumeration whatever the PATH.
	if !strings.Contains(snapPayload, ";pwd;") {
		t.Errorf("snapshot payload missing the pwd builtin: %q", snapPayload[:min(len(snapPayload), 200)])
	}
}

// TestZshSnapshotCarriesTheSessionLocalTablesAndNotThePath pins the split
// of carrier design §8 in BOTH directions, on the tier where the tables are
// four different parameters.
//
// One direction: a snapshot is only worth having if it reports what THIS
// shell can run, and enumerating three of the session-local tables would
// still produce a well-formed snapshot, a matching nonce and a green count
// while the editor marked the user's own alias as a command that does not
// exist.
//
// The other direction, and the one this package's own tests could not report
// before: a PATH executable must NOT be here. It is identical for every
// session to this host, so it is enumerated once by the backend and shared;
// enumerating it here as well would be the per-session scan §8 exists to
// remove, running a second time beside the shared one, and no count or nonce
// check would notice.
func TestZshSnapshotCarriesTheSessionLocalTablesAndNotThePath(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	// A PATH executable of our own, so the absence below is checked against
	// a name that cannot come from anywhere else.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "nocxprobebin"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // an executable probe is the point
		t.Fatalf("write probe binary: %v", err)
	}

	prog := `
autoload -Uz add-zsh-hook
alias nocxprobealias='echo hi'
nocxprobefunc() { :; }
export NOCX_SHELL_INTEGRATION=1
source "$1"
` + zshPrecmdCycle
	out := runShellProgEnv(t, zsh, prog, script,
		"NOCX_SNAPSHOT_WAIT_MS=15000", "PATH="+bin+":"+os.Getenv("PATH"))

	snap := strings.Index(out, "]636;S;")
	if snap < 0 {
		t.Fatalf("no snapshot emitted: %q", out)
	}
	payload := out[snap:]
	for _, want := range []string{
		";pwd;",            // builtin
		";nocxprobealias;", // alias
		";nocxprobefunc;",  // function
		";if;",             // reserved word
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("snapshot does not report %q — the editor would mark it as a command that does not exist", strings.Trim(want, ";"))
		}
	}
	if strings.Contains(payload, ";nocxprobebin;") {
		t.Errorf("snapshot carries a PATH executable: that half is the backend's shared scan, and enumerating it here is the per-session scan §8 removes")
	}
}

// TestBashSnapshotCarriesTheSessionLocalTablesAndNotThePath is the bash twin
// of the assertion above. It matters more here than in zsh: `compgen -c`
// merged all five tables into one answer, so the split is invisible unless
// something asserts the PATH half is gone.
func TestBashSnapshotCarriesTheSessionLocalTablesAndNotThePath(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "nocxprobebin"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // an executable probe is the point
		t.Fatalf("write probe binary: %v", err)
	}

	prog := `
alias nocxprobealias='echo hi'
nocxprobefunc() { :; }
export NOCX_SHELL_INTEGRATION=1
shopt -s expand_aliases
source "$1"
__nocx_prompt_command
`
	out := runShellProgEnv(t, bash, prog, script,
		"NOCX_SNAPSHOT_WAIT_MS=15000", "PATH="+bin+":"+os.Getenv("PATH"))

	snap := strings.Index(out, "]636;S;")
	if snap < 0 {
		t.Fatalf("no snapshot emitted: %q", out)
	}
	payload := out[snap:]
	for _, want := range []string{
		";pwd;",           // builtin
		";nocxprobefunc;", // function
		";if;",            // reserved word
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("snapshot does not report %q — the editor would mark it as a command that does not exist", strings.Trim(want, ";"))
		}
	}
	if strings.Contains(payload, ";nocxprobebin;") {
		t.Errorf("snapshot carries a PATH executable: that half is the backend's shared scan, and enumerating it here is the per-session scan §8 removes")
	}
}

// TestZshSnapshotSurvivesTheSecondSource is the zsh twin of nocx-cbtc: the
// launcher's .zshrc unsets __nocx_loaded and sources the session's
// authenticated copy over the installer-era one the user's ~/.zshrc already
// sourced, which is EVERY enhanced zsh session. A second pass that minted a
// fresh nonce would announce a second hello, and command-snapshot.ts keeps
// the FIRST one on purpose — so the snapshot would carry a nonce the store
// discards and completion would be dead with both frames well-formed.
func TestZshSnapshotSurvivesTheSecondSource(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	// Kept in step with the launcher: if the rcfile stops re-sourcing, this
	// test reproduces an arrangement that no longer exists.
	if !strings.Contains(zshRcfileTemplate, "unset __nocx_loaded") {
		t.Fatal("zshRcfileTemplate no longer re-sources the script; this test reproduces an arrangement that no longer exists")
	}

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1
source "$1"
unset __nocx_loaded __nocx_prompt_wrapped __nocx_owned_session
source "$1"
` + zshPrecmdCycle + zshPrecmdCycle
	out := runShellProgEnv(t, zsh, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")

	if got := strings.Count(out, "]636;H;"); got != 1 {
		t.Errorf("a re-source announced a second session: want exactly 1 OSC 636 hello, got %d in %q", got, out)
	}
	if got := strings.Count(out, "]636;S;"); got != 1 {
		t.Errorf("want exactly 1 OSC 636 snapshot, got %d in %q", got, out)
	}
	hello := helloNonce(t, out)
	after := strings.SplitN(out, "]636;S;", 2)
	if len(after) < 2 {
		t.Fatalf("no OSC 636 snapshot emitted: %q", out)
	}
	if snapNonce := strings.SplitN(after[1], ";", 2)[0]; snapNonce != hello {
		t.Errorf("snapshot nonce = %q, hello established %q — the renderer discards a mismatch, so command completion never works in this session", snapNonce, hello)
	}
	if !strings.Contains(after[1], "pwd") {
		t.Errorf("snapshot carries no command names: %q", out)
	}
}

// TestZshSnapshotAnnouncesInAChildThatInheritedTheNonce is the other half of
// the once-per-shell latch, and it pins the opposite direction from the test
// above: "is the variable set" cannot tell a re-source from an INHERITED
// value, and a nonce that reached the environment (a user rc under `set -a`
// auto-exports every assignment — the hazard zsh's `typeset +x` exists for)
// would otherwise silence a legitimately new shell for good. That is a
// fail-CLOSED degrade, the wrong direction for this file.
func TestZshSnapshotAnnouncesInAChildThatInheritedTheNonce(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1
export __nocx_snapshot_nonce=deadbeefdeadbeefdeadbeefdeadbeef
source "$1"
` + zshPrecmdCycle + zshPrecmdCycle
	out := runShellProgEnv(t, zsh, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")

	if got := strings.Count(out, "]636;H;"); got != 1 {
		t.Errorf("a shell that inherited a nonce must still announce itself: want 1 OSC 636 hello, got %d in %q", got, out)
	}
	hello := helloNonce(t, out)
	if hello == "deadbeefdeadbeefdeadbeefdeadbeef" {
		t.Error("the child announced the INHERITED nonce; it must mint its own")
	}
	if got := strings.Count(out, "]636;S;"); got != 1 {
		t.Errorf("want 1 OSC 636 snapshot, got %d in %q", got, out)
	}
	after := strings.SplitN(out, "]636;S;", 2)
	if len(after) < 2 || strings.SplitN(after[1], ";", 2)[0] != hello {
		t.Errorf("snapshot nonce does not match the hello's in %q", out)
	}
}

// TestZshSnapshotNonceNeverLeavesTheShell pins the forgery defence itself:
// the nonce is the ONLY thing separating the session's own snapshot from one
// a command's output printed, so it may not be exported (an /proc/<pid>/
// environ read, and every child process), and it may not appear in a
// filename (a world-readable directory listing).
func TestZshSnapshotNonceNeverLeavesTheShell(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	// `set -a` is the hazard: every assignment the sourced script makes would
	// otherwise be auto-exported, and zsh has no `export -n`.
	//
	// The environment is searched INSIDE the shell and only the verdict is
	// printed. Dumping it would put the developer's own environment — which
	// on a real machine is full of tokens — into a test log for the sake of
	// one substring check.
	prog := `
autoload -Uz add-zsh-hook
set -a
export NOCX_SHELL_INTEGRATION=1
source "$1"
set +a
if env | grep -q -- "$__nocx_snapshot_nonce"; then
    printf '\nNONCE_EXPORTED\n'
else
    printf '\nNONCE_PRIVATE\n'
fi
`
	out := runShellProgEnv(t, zsh, prog, script, "NOCX_SNAPSHOT_WAIT_MS=15000")
	helloNonce(t, out) // the hello is the only place the nonce may appear

	if !strings.Contains(out, "NONCE_PRIVATE") {
		t.Errorf("the session nonce is EXPORTED: any child process and any /proc/<pid>/environ read can forge a snapshot for this session; got %q", stripOsc(out))
	}
}

// TestZshSnapshotDefersToALaterPromptWhenNotReady is the failure path, and
// it is the one the bash twin (TestBashSnapshotFirstPromptBoundedWait) buys
// with a sleeping compgen. The state under test is "the payload has not
// landed yet", and on zsh the only deterministic way to hold a shell in it
// is to name a final file that never appears — the enumeration is parameter
// expansion, not a command that can be stubbed slow.
//
// Two halves, and the second is the product's actual promise: the first
// prompt must not be held open forever, AND the snapshot must still arrive
// at a later prompt rather than being lost with the missed window.
func TestZshSnapshotDefersToALaterPromptWhenNotReady(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1
source "$1"
__nocx_snap_real="$__nocx_snap_file"
__nocx_snap_file="${TMPDIR:-/tmp}/nocx-snap-never-appears.snap"
` + zshPrecmdCycle + `printf '\nFIRST_PROMPT_RETURNED\n'
__nocx_snap_file="$__nocx_snap_real"
# Wait on the producer's published file, not on an assumed machine speed,
# then drive the first prompt boundary that can consume it.
for __nocx_test_i in {1..750}; do
    for f in $precmd_functions; do $f; done
    (( __nocx_snapshot_done )) && break
    zselect -t 2 2>/dev/null
done
`

	started := time.Now()
	out := runShellProgEnv(t, zsh, prog, script, "TMPDIR="+t.TempDir(), "NOCX_SNAPSHOT_WAIT_MS=250")
	elapsed := time.Since(started)

	first := strings.Index(out, "FIRST_PROMPT_RETURNED")
	if first < 0 {
		t.Fatalf("the first prompt never returned: %q", out)
	}
	if idx := strings.Index(out, "]636;S;"); idx >= 0 && idx < first {
		t.Errorf("a snapshot was emitted while the payload had not landed: %q", out)
	}
	// The wait is BOUNDED: an unbounded one would hang here rather than fail,
	// so the assertion is that the shell finished at all, plus a ceiling with
	// enough headroom that it measures boundedness and never machine speed.
	if elapsed > 30*time.Second {
		t.Errorf("the shell took %s with a 250 ms snapshot bound — the first prompt is not bounded", elapsed)
	}
	// And the snapshot is not lost with the window it missed.
	if got := strings.Count(out, "]636;S;"); got != 1 {
		t.Errorf("want exactly 1 OSC 636 snapshot at the later prompt, got %d in %q", got, out)
	}
	if hello := helloNonce(t, out); !strings.Contains(out, "]636;S;"+hello+";") {
		t.Errorf("the deferred snapshot does not carry the hello's nonce: %q", out)
	}
}

// TestZshSnapshotTempFilesStayPrivate is the zsh twin of the bash test with
// the same name, and it covers the leak path: a session that exits before
// any prompt emitted the snapshot. The staging file exists from source time,
// so the nonce must never be in its NAME (a directory listing is
// world-readable), the file must be mode 600 from creation (mktemp, not
// create-then-chmod), and nothing may survive the shell.
//
// The chaining mechanism is the difference from bash: bash saves and re-runs
// the previous EXIT trap, while zsh has an exit HOOK ARRAY (add-zsh-hook
// zshexit), so a user's own exit handler is appended to rather than saved —
// there is nothing to clobber, and the test asserts that it still runs.
func TestZshSnapshotTempFilesStayPrivate(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)
	tmp := t.TempDir()

	prog := `
autoload -Uz add-zsh-hook
__user_exit() { printf 'CHAINED_EXIT' }
add-zsh-hook zshexit __user_exit
export NOCX_SHELL_INTEGRATION=1
source "$1"
printf '\nLS_SOURCE\n'
ls -l "$TMPDIR"
printf '\nLS_END\n'
`
	out := runShellProgEnv(t, zsh, prog, script, "TMPDIR="+tmp, "NOCX_SNAPSHOT_WAIT_MS=15000")
	nonce := helloNonce(t, out)

	if !strings.Contains(out, "CHAINED_EXIT") {
		t.Errorf("the user's own zshexit hook did not run: %q", out)
	}
	if strings.Contains(out, "]636;S;") {
		t.Errorf("a snapshot was emitted without a prompt cycle: %q", out)
	}

	start := strings.Index(out, "LS_SOURCE")
	end := strings.LastIndex(out, "LS_END")
	if start < 0 || end < start {
		t.Fatalf("LS_SOURCE section missing from output: %q", out)
	}
	listing := out[start+len("LS_SOURCE") : end]
	if strings.Contains(listing, nonce) {
		t.Errorf("the session nonce appears in the $TMPDIR listing: %q", listing)
	}
	seen := 0
	for _, line := range strings.Split(listing, "\n") {
		if !strings.Contains(line, "nocx-snap") {
			continue
		}
		seen++
		fields := strings.Fields(line)
		if len(fields) < 9 {
			t.Errorf("unexpected ls -l line %q", line)
			continue
		}
		// macOS tags every mktemp file with com.apple.provenance, so `ls -l`
		// appends an "@" to the mode column; compare the permission bits.
		if mode := fields[0]; len(mode) < 10 || mode[:10] != "-rw-------" {
			t.Errorf("snapshot file %s has mode %s, want -rw------- (600)", fields[8], mode)
		}
	}
	if seen == 0 {
		t.Error("no snapshot file was observed mid-session (mktemp failed?)")
	}

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

// TestZshSnapshotSurvivesAnUnusableTempDir is the paired failure path for
// the staging file: a machine whose $TMPDIR cannot be written to gets no
// snapshot, and that is all it gets — the shell still starts, the prompt
// still works, and the hello still anchors the session. A fail-closed degrade
// here would be a shell that errors into the user's terminal on every prompt.
func TestZshSnapshotSurvivesAnUnusableTempDir(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1
source "$1"
` + zshPrecmdCycle + `printf '\nPROMPT_OK\n'
`
	out := runShellProgEnv(t, zsh, prog, script,
		"TMPDIR="+filepath.Join(t.TempDir(), "does-not-exist"), "NOCX_SNAPSHOT_WAIT_MS=250")

	if !strings.Contains(out, "PROMPT_OK") {
		t.Errorf("the prompt cycle did not complete when mktemp failed: %q", out)
	}
	if strings.Contains(out, "]636;S;") {
		t.Errorf("a snapshot was emitted with no staging file: %q", out)
	}
	if got := strings.Count(out, "]636;H;"); got != 1 {
		t.Errorf("the session must still announce itself: want 1 hello, got %d in %q", got, out)
	}
	// Nothing the user did not ask for reached their terminal.
	if visible := stripOsc(out); strings.Contains(visible, "mktemp") || strings.Contains(visible, "No such file") {
		t.Errorf("the failed staging leaked a diagnostic into the terminal: %q", visible)
	}
}

// TestZshSnapshotEncodesHostileNames pins the zsh payload encoder against
// the bash one, byte for byte and case for case: `;` separates names and `\`
// starts an escape, so a name carrying either must not be able to forge a
// boundary or a second field. Both encoder paths are exercised — the first
// two cases take the fast path, every other case must fall through to the
// per-character loop.
//
// The expectation is the bash twin's literal, not a zsh-flavoured one. Two
// escapings of one wire format is exactly the second implementation AD-8
// forbids; if they can differ at all, they will differ on the byte nobody
// tried.
func TestZshSnapshotEncodesHostileNames(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
export NOCX_SHELL_INTEGRATION=1
source "$1" >/dev/null 2>&1
printf 'START'
__nocx_snapshot_build \
  'plain' \
  'with space' \
  'semi;colon' \
  'back\slash' \
  $'ctl\001x' \
  $'del\177x' \
  $'c1\M-\C-Bx' \
  $'utf\xc3\xa9'
printf 'END'
`
	out := runShellProg(t, zsh, prog, script)
	start := strings.Index(out, "START")
	end := strings.LastIndex(out, "END")
	if start < 0 || end < start {
		t.Fatalf("could not delimit the payload in %q", out)
	}
	got := out[start+len("START") : end]

	want := "plain;" +
		"with space;" +
		`semi\x3bcolon;` +
		`back\\slash;` +
		`ctl\x01x;` +
		`del\x7fx;` +
		`c1\x82x;` +
		"utf\xc3\xa9;"
	if got != want {
		t.Errorf("encoded payload mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestZshSnapshotRespectsTheFrontendsCaps pins the two bounds, and the
// failure they prevent is not truncation — it is silence. The store rejects a
// payload carrying more than MAX_SNAPSHOT_NAMES names or more than
// MAX_SNAPSHOT_CHARS characters OUTRIGHT (command-snapshot.ts decodeNames
// returns null, and null leaves the previous snapshot in place — there is
// none), so a cap that does not fire does not ship a slightly-too-big
// snapshot, it ships no snapshot at all, on exactly the machines with the
// largest PATHs.
//
// Neither bound is reachable from an ordinary enumeration on the machine
// running this test (~2500 names, ~22 KB), which is why they are driven
// directly.
func TestZshSnapshotRespectsTheFrontendsCaps(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
export NOCX_SHELL_INTEGRATION=1
source "$1" >/dev/null 2>&1
printf 'NAMES_START'
__nocx_snapshot_build {1..9000}
printf 'NAMES_END'
__nocx_pad=''
__nocx_long="${(l:1000::x:)__nocx_pad}"
__nocx_big=()
repeat 100 do __nocx_big+=("$__nocx_long"); done
printf 'CHARS_START'
__nocx_snapshot_build "${__nocx_big[@]}"
printf 'CHARS_END'
`
	out := runShellProg(t, zsh, prog, script)

	names := between(t, out, "NAMES_START", "NAMES_END")
	if got := strings.Count(names, ";"); got != 4096 {
		t.Errorf("name cap: payload carries %d names, want the 4096 §8 puts on the session-local half", got)
	}

	chars := between(t, out, "CHARS_START", "CHARS_END")
	if len(chars) > 65536 {
		t.Errorf("character cap: payload is %d characters, over the 65536 the frontend accepts", len(chars))
	}
	// And it stops at a NAME boundary, never mid-name: a truncated tail would
	// be a command name that does not exist, reported as one that does. Every
	// name in this fixture is exactly 1000 characters, so a short segment is
	// a cut one.
	for _, part := range strings.Split(chars, ";") {
		if part != "" && len(part) != 1000 {
			t.Errorf("character cap truncated mid-name: a %d-character segment in a payload of 1000-character names", len(part))
			break
		}
	}
	// The cap must also not be so eager that it drops nearly everything.
	if len(chars) < 60000 {
		t.Errorf("character cap: payload is only %d characters; the bound is 65536", len(chars))
	}
}

// between returns the text between two literal delimiters, failing the test
// when either is missing.
func between(t *testing.T, out, start, end string) string {
	t.Helper()
	i := strings.Index(out, start)
	j := strings.LastIndex(out, end)
	if i < 0 || j < i {
		t.Fatalf("could not delimit %s..%s in %q", start, end, out)
	}
	return out[i+len(start) : j]
}

// TestZshSnapshotArrivesBeforeFirstPrompt is the paired "and on an ordinary
// machine it succeeds" assertion, and the only one here that runs the REAL
// enumeration on a REAL interactive zsh over a pty, installed the way the
// app installs it (the gate line in .zshrc). It asserts the order the
// owner's acceptance depends on: 636;S arrives before the first 133;B —
// before the first prompt is usable — so typing a nonexistent command in a
// freshly opened tab marks it.
func TestZshSnapshotArrivesBeforeFirstPrompt(t *testing.T) {
	zsh := requireShell(t, "zsh")
	zdot := t.TempDir()
	tmp := t.TempDir()
	nocxDir := filepath.Join(zdot, ".nocx")
	if err := os.MkdirAll(nocxDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", nocxDir, err)
	}
	if err := os.WriteFile(filepath.Join(nocxDir, "shell-integration.zsh"), []byte(zshScript), 0o600); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	gate := "# nocx terminal shell integration\n" +
		`[[ -n "$NOCX_SHELL_INTEGRATION" ]] && source "$HOME/.nocx/shell-integration.zsh"` + "\n"
	if err := os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte(gate), 0o600); err != nil {
		t.Fatalf("write .zshrc: %v", err)
	}

	// #nosec G204 — zsh is the path resolved by the test helper above, not
	// input; an interactive shell on a pty is the only way to observe the
	// prompt-time ordering this test is about.
	cmd := exec.Command(zsh, "-i")
	cmd.Env = append(
		os.Environ(),
		"HOME="+zdot,
		"ZDOTDIR="+zdot,
		"TMPDIR="+tmp,
		"HISTFILE=/dev/null",
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=ptytest",
		// An ORDER is under test, not a speed: the shipped 250 ms is how long
		// a human's prompt may be held, and a loaded runner outlasts it, at
		// which point the product correctly defers and the order stops
		// existing (nocx-0ije).
		"NOCX_SNAPSHOT_WAIT_MS=5000",
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

	time.Sleep(500 * time.Millisecond)
	if _, werr := ptmx.Write([]byte("true\n")); werr != nil {
		t.Fatalf("write command: %v", werr)
	}
	time.Sleep(500 * time.Millisecond)
	if _, werr := ptmx.Write([]byte("exit\n")); werr != nil {
		t.Fatalf("write exit: %v", werr)
	}

	var out []byte
	select {
	case out = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for the interactive session to end")
	}
	if werr := cmd.Wait(); werr != nil {
		t.Logf("zsh exited non-zero (may be benign): %v", werr)
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

	// No snapshot between a command-start (C) and its command-end (D): the
	// payload is only ever written while the shell is the sole writer to the
	// tty, so it can never interleave with a command's output.
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
			continue
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

	// The background enumeration prints no job-control notification: zsh
	// announces a finished background job at the next prompt, which would put
	// the job's own implementation into the user's output region.
	visible := stripOsc(output)
	for _, s := range []string{"done", "__nocx_snap", "suspended"} {
		if strings.Contains(visible, s) {
			t.Errorf("job-control notification leaked into the session output (found %q): %q", s, visible)
		}
	}

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

// TestBashEmitsNoPassport drives the real bash hooks with an environment id
// set (the launcher-era NOCX_ENVIRONMENT_ID): the readiness passport is
// DELETED (nocx-u7uh.11) — the environment identity now rides the
// authenticated lifecycle channel — so no OSC 636 P and no nocx_env= tagged
// marker may reach the wire, whatever the environment carries. The A/B/C/D
// markers stay untagged, exactly the pre-passport shape.
func TestBashEmitsNoPassport(t *testing.T) {
	bash := requireShell(t, "bash")
	script := writeScriptFile(t, "nocx.bash", bashScript)

	prog := `
export NOCX_SHELL_INTEGRATION=1
export NOCX_ENVIRONMENT_ID=env-abc-123
source "$1"
printf 'PS1=%s\n' "$PS1"
__nocx_prompt_command
true
__nocx_prompt_command
`
	out := runShellProg(t, bash, prog, script)

	if strings.Contains(out, "]636;P") {
		t.Errorf("the readiness passport must not be emitted (nocx-u7uh.11); output:\n%s", out)
	}
	if strings.Contains(out, "nocx_env=") {
		t.Errorf("no marker may carry a nocx_env= tag (nocx-u7uh.11); output:\n%s", out)
	}
	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") == 0 || countMarkers(ms, "C") == 0 {
		t.Errorf("the untagged A/C markers must still be emitted; markers: %+v", ms)
	}
}

// TestZshEmitsNoPassport is the zsh half of the deletion.
func TestZshEmitsNoPassport(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	prog := `
autoload -Uz add-zsh-hook
export NOCX_SHELL_INTEGRATION=1
export NOCX_ENVIRONMENT_ID=env-abc-123
source "$1"
printf 'PS1=%s\n' "$PS1"
__nocx_preexec
true;  for f in $precmd_functions; do $f; done
`
	out := runShellProg(t, zsh, prog, script)

	if strings.Contains(out, "]636;P") {
		t.Errorf("the readiness passport must not be emitted (nocx-u7uh.11); output:\n%s", out)
	}
	if strings.Contains(out, "nocx_env=") {
		t.Errorf("no marker may carry a nocx_env= tag (nocx-u7uh.11); output:\n%s", out)
	}
}

// TestZshNestedJsonUnescape decodes the grant's bootstrap field exactly as
// the wire carries it (nocx-u7uh.28): the frame is built by Go's JSON
// encoder — the same bytes lifecyclecodec writes for the domain_grant — and
// the payload deliberately carries backslashes, quotes, newlines, tabs, an
// OSC escape and non-ASCII. A broken decoder corrupts the child rcfile,
// which makes the child a conventional shell — the safe direction, but
// invisible in the pty test unless the corruption is exact. The decoded
func TestZshNestedJsonUnescape(t *testing.T) {
	zsh := requireShell(t, "zsh")
	script := writeScriptFile(t, "nocx.zsh", zshScript)

	// \x01 exercises the \uXXXX wire form (Go escapes it as \u0001); the
	// other bytes cover the backslash, quote, newline, tab and OSC cases.
	payload := "# rc\nprintf '\\e]133;B\\a' \"q\\\"q\" \\\n\tline\né done\x01x\n"
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	frame := []byte(`{"v":1,"lane":"L","dom":"D","epoch":1,"seq":9,"cap":"abc","evt":"domain_grant","request":"r-1","env":"sudo","bootstrap":` + string(b) + `}`)
	framePath := filepath.Join(t.TempDir(), "frame")
	if err := os.WriteFile(framePath, frame, 0o600); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	// Sourcing emits the OSC 636 hello on stdout (nocx-qduc); send it away so
	// the comparison below reads exactly what the decoder produced, the same
	// way the bash encoder test does.
	prog := `
export NOCX_SHELL_INTEGRATION=1
source "$1" >/dev/null 2>&1
frame=$(cat "$NOCX_TEST_FRAME_PATH")
bootstrap="${frame##*\"bootstrap\":\"}"
bootstrap="${bootstrap%?}"
bootstrap="${bootstrap%\"}"
__nocx_lc_json_unescape "$bootstrap"
builtin printf '%s' "$__nocx_lc_json_unescaped"
`
	out := runShellProgEnv(t, zsh, prog, script, "NOCX_TEST_FRAME_PATH="+framePath)
	if out != payload {
		t.Errorf("unescaped bootstrap mismatch:\n got %q\nwant %q", out, payload)
	}
}
