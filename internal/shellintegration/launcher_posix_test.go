package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPosixLauncher_EmitsMarkersFromPS1 drives the REAL posix launcher
// command end to end on a pty, with dash standing in for both the login
// shell that parses the remote command and the $SHELL it execs — the
// minimal tier's acceptance: a shell that is neither bash nor zsh reaches a
// marker-emitting session. It asserts the DAB stream with the real exit
// status on the THIRD prompt, that no C marker is ever emitted (C is
// unreachable through portable prompt hooks; faking it is forbidden), that
// the user's ~/.profile ran (a login shell really started), and that the
// launcher's transient ENV file erased itself — nothing survives the
// session.
//
// dash is required, not optional: this test is the minimal tier's only
// end-to-end proof, so a machine without dash must report "did not run",
// never green (nocx-gd84, same treatment as the bash/zsh launcher tests).
func TestPosixLauncher_EmitsMarkersFromPS1(t *testing.T) {
	dashPath := requireIntegrationShell(t, "dash")

	home := t.TempDir()
	// #nosec G306 — test fixture file, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("echo USER_RC_RAN\n"), 0o600); err != nil {
		t.Fatalf("write fixture .profile: %v", err)
	}

	cmd, reason, ok := FullBootstrapCommand(ShellUnknown, LaunchOptions{})
	if !ok {
		t.Fatalf("ShellUnknown launcher refused: reason=%q", reason)
	}

	out := runLauncherOnPTY(t, dashPath, cmd,
		[]string{"HOME=" + home, "SHELL=" + dashPath, "HOSTNAME=testhost"},
		"true", "false", "exit")

	if !strings.Contains(out, "USER_RC_RAN") {
		t.Errorf("login shell did not read the fixture ~/.profile; output:\n%s", out)
	}

	ms := extractOscMarkers(out)
	var kinds strings.Builder
	for _, m := range ms {
		kinds.WriteString(m.kind)
	}
	// Three prompts (after `true`, after `false`, and the pre-command
	// prompt); each emits D before A before B, and no C anywhere.
	if kinds.String() != "DABDABDAB" {
		t.Errorf("marker stream = %q, want DAB x3 (no C); output:\n%s", kinds.String(), out)
	}

	// D payloads: 0 (the source's own status — a POSIX prompt expansion
	// forks, so the first prompt's D cannot be suppressed the way bash/zsh
	// suppress theirs; every frontend consumer ignores a D while no command
	// is running), then 0 after `true`, 1 after `false`.
	wantStatuses := []string{"0", "0", "1"}
	if got := extractDStatuses(out); !equalStrings(got, wantStatuses) {
		t.Errorf("D statuses = %v, want %v; output:\n%s", got, wantStatuses, out)
	}

	if osc7 := extractOsc7(out); len(osc7) != 3 {
		t.Errorf("OSC 7 payload count = %d, want 3; output:\n%s", len(osc7), out)
	}

	// The ENV file erases its transient directory when sourced; the
	// launcher's one write is gone by session end.
	leftovers, err := filepath.Glob(filepath.Join(os.TempDir(), "nocx-posix.*"))
	if err != nil {
		t.Fatalf("glob transient dirs: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("launcher transient dir left behind: %v", leftovers)
	}
}

// TestPosixLauncher_MarkerOnlyKeepsSameStream: the minimal tier has no
// ownership protocol and no C marker, so Enhanced changes nothing about the
// emitted stream — but the pinned precondition "SessionID is never empty
// when Enhanced" still applies (it is the caller's contract, enforced
// uniformly across tiers). This is the enforcement half; the acceptance half
// is the pty test above with an empty LaunchOptions.
func TestPosixLauncher_EnhancedRequiresSessionID(t *testing.T) {
	cmd, reason, ok := FullBootstrapCommand(ShellUnknown, LaunchOptions{Enhanced: true})
	if ok {
		t.Fatalf("ShellUnknown enhanced with empty SessionID accepted; got %q", cmd)
	}
	if reason != ReasonUnsupportedShell {
		t.Errorf("reason = %q, want %q", reason, ReasonUnsupportedShell)
	}
}
