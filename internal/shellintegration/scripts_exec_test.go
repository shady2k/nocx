package shellintegration

import (
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

// TestEnsureInstalled_SkipsVersionWhenGateFails guards nocx-1dx: the VERSION
// marker must not be recorded if a gate append failed, so the next launch
// retries instead of short-circuiting on a version match.
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
