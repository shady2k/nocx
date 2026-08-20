package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAutoFixtureHome materialises a $HOME whose startup files for all
// three tiers print distinct sentinels: the bash tier sources ~/.bashrc,
// the zsh tier sources ~/.zshrc (from the original ZDOTDIR), and the posix
// tier's login shell reads ~/.profile. Exactly one sentinel appearing in a
// session's output therefore names the tier that actually ran.
func writeAutoFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	sentinels := map[string]string{
		".bashrc":  "echo BASH_TIER_RAN\n",
		".zshrc":   "echo ZSH_TIER_RAN\n",
		".profile": "echo POSIX_TIER_RAN\n",
	}
	for name, content := range sentinels {
		// #nosec G306 — test fixture files, intentionally created with restricted permissions.
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return home
}

// requireShellLink resolves shell to a path whose argv[0] basename is the
// applet name — busybox dispatches on argv[0], so `busybox ash -c` needs a
// symlink named ash. For non-busybox shells the resolved path is returned
// as-is.
func requireShellLink(t *testing.T, shell string) string {
	t.Helper()
	path := requireShell(t, shell)
	if shell != "busybox" {
		return path
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "ash")
	if err := os.Symlink(path, link); err != nil {
		t.Fatalf("symlink busybox as ash: %v", err)
	}
	return link
}

// TestAutoDispatcher_SelectsTierPerLoginShell drives the REAL ShellAuto
// command through each login shell the dispatcher must serve — bash, zsh,
// dash, busybox ash — on a real pty, and asserts that exactly the tier for
// that shell ran: the login shell parses the outer command, the dispatcher
// reads its own $0 (the login shell's argv[0], expanded by the login shell
// itself), and execs the matching payload. This is the acceptance pair for
// "a zsh host gets the zsh launcher; a host with neither bash nor zsh gets
// the minimal tier".
func TestAutoDispatcher_SelectsTierPerLoginShell(t *testing.T) {
	cases := []struct {
		name    string
		login   string
		want    string
		skipMsg string
	}{
		{name: "bash host", login: "bash", want: "BASH_TIER_RAN"},
		{name: "zsh host", login: "zsh", want: "ZSH_TIER_RAN"},
		{name: "dash host", login: "dash", want: "POSIX_TIER_RAN"},
		{name: "busybox ash host", login: "busybox", want: "POSIX_TIER_RAN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loginPath := requireShellLink(t, tc.login)
			home := writeAutoFixtureHome(t)

			cmd, reason, ok := FullBootstrapCommand(ShellAuto, LaunchOptions{
				Enhanced:  true,
				SessionID: "auto-tier-test",
			})
			if !ok {
				t.Fatalf("ShellAuto launcher refused: reason=%q", reason)
			}

			out := runLauncherOnPTY(t, loginPath, cmd,
				[]string{"HOME=" + home, "SHELL=" + loginPath, "HOSTNAME=testhost"},
				"true", "false", "exit")

			for _, sentinel := range []string{"BASH_TIER_RAN", "ZSH_TIER_RAN", "POSIX_TIER_RAN"} {
				got := strings.Contains(out, sentinel)
				if sentinel == tc.want && !got {
					t.Errorf("login shell %s: tier sentinel %s missing; output:\n%s", tc.login, sentinel, out)
				}
				if sentinel != tc.want && got {
					t.Errorf("login shell %s: wrong tier sentinel %s appeared; output:\n%s", tc.login, sentinel, out)
				}
			}
		})
	}
}

// TestAutoDispatcher_FailsOpenUnderNonPOSIXLoginShell: csh, tcsh and fish
// cannot be detected — none sets a version variable a child can see and
// none can parse a POSIX case script — so the dispatcher's csh|tcsh|fish
// arm must leave them an ordinary, alive, unintegrated login shell
// (`exec "${0#-}" -l`, ADR-0004), never a dead session and never the
// minimal tier's ENV machinery, which those shells ignore. The outer
// command is the same csh-parseable shape every tier already sends;
// tcsh's own .cshrc sentinel proves the login really happened.
func TestAutoDispatcher_FailsOpenUnderNonPOSIXLoginShell(t *testing.T) {
	tcshPath := requireShell(t, "tcsh")
	home := t.TempDir()
	// #nosec G306 — test fixture file, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(home, ".cshrc"), []byte("echo CSH_TIER_RAN\n"), 0o600); err != nil {
		t.Fatalf("write fixture .cshrc: %v", err)
	}

	// The csh/fish arm sends the session to a plain login shell and must
	// not run the minimal tier's ENV machinery — tcsh never reads ENV, so
	// a transient directory would leak. Assert the before/after delta.
	before, err := filepath.Glob(filepath.Join(os.TempDir(), "nocx-posix.*"))
	if err != nil {
		t.Fatalf("glob transient dirs: %v", err)
	}

	cmd, reason, ok := FullBootstrapCommand(ShellAuto, LaunchOptions{
		Enhanced:  true,
		SessionID: "auto-csh-test",
	})
	if !ok {
		t.Fatalf("ShellAuto launcher refused: reason=%q", reason)
	}

	out := runLauncherOnPTY(t, tcshPath, cmd,
		[]string{"HOME=" + home, "SHELL=" + tcshPath, "HOSTNAME=testhost"},
		"true", "false", "exit")

	if !strings.Contains(out, "CSH_TIER_RAN") {
		t.Errorf("tcsh login did not start (fail-open broken); output:\n%s", out)
	}
	for _, sentinel := range []string{"BASH_TIER_RAN", "ZSH_TIER_RAN", "POSIX_TIER_RAN"} {
		if strings.Contains(out, sentinel) {
			t.Errorf("tcsh host ran a detected tier (%s); output:\n%s", sentinel, out)
		}
	}
	after, err := filepath.Glob(filepath.Join(os.TempDir(), "nocx-posix.*"))
	if err != nil {
		t.Fatalf("glob transient dirs: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("tcsh host leaked a posix-tier transient dir: before=%v after=%v", before, after)
	}
}

// TestAutoCommand_CarriesTiersAsSingleQuotedArgvWords pins the csh-safety
// invariant structurally: the outer command is five single-quoted segments —
// the publish prelude (with its exec tail), the dispatcher script, and the
// three tier payloads — plus the unquoted "$0", and nothing else, so any
// login shell — including csh and fish, which cannot parse the dispatcher's
// syntax — parses the command as five ordinary quoted arguments and the
// payload words arrive intact. A single-quote count other than ten means a
// segment gained a quote and the dispatcher must switch to an escaping
// vehicle.
func TestAutoCommand_CarriesTiersAsSingleQuotedArgvWords(t *testing.T) {
	cmd, _, ok := FullBootstrapCommand(ShellAuto, LaunchOptions{
		Enhanced:  true,
		SessionID: "auto-shape-test",
	})
	if !ok {
		t.Fatal("ShellAuto launcher refused")
	}
	if got := strings.Count(cmd, "'"); got != 10 {
		t.Errorf("single-quote count = %d, want 10 (five ShellQuote'd segments); command:\n%s", got, cmd)
	}
	if !strings.Contains(cmd, ` "$0" `) {
		t.Errorf("command does not pass the login shell's $0 through; command:\n%s", cmd)
	}

	// The three payload words must be the tier arguments verbatim — the
	// dispatcher passes them through, never re-escapes them.
	bashArg, _ := remoteLauncher{}.bashArg(LaunchOptions{Enhanced: true, SessionID: "auto-shape-test"})
	zshArg, _ := remoteLauncher{}.zshArg(LaunchOptions{Enhanced: true, SessionID: "auto-shape-test"})
	posixArg, _ := remoteLauncher{}.posixArg(LaunchOptions{Enhanced: true, SessionID: "auto-shape-test"})
	for _, want := range []string{autoDispatcherScript, bashArg, zshArg, posixArg} {
		if !strings.Contains(cmd, want) {
			t.Errorf("auto command does not carry the segment verbatim (len %d); command:\n%s", len(want), cmd)
		}
	}
}

// TestAutoCommand_UnderCap: the combined command sits below the full launcher
// cap — the publish prelude plus the three payloads, no double escaping.
func TestAutoCommand_UnderCap(t *testing.T) {
	cmd, _, ok := FullBootstrapCommand(ShellAuto, LaunchOptions{
		Enhanced:  true,
		SessionID: "auto-cap-test",
	})
	if !ok {
		t.Fatal("ShellAuto launcher refused")
	}
	if len(cmd) > maxFullLauncherLen {
		t.Errorf("auto command len %d exceeds maxFullLauncherLen %d", len(cmd), maxFullLauncherLen)
	}
}

// TestFullLauncherStaysUnderArgLimit watches the MARGIN, not the ceiling,
// for EVERY tier including ShellAuto.
//
// TestAutoCommand_UnderCap above asks whether the command fits, and the
// answer stayed yes while the headroom went from 15 KB to 2 KB — so the
// first thing anyone learned about the erosion was every remote launch
// refusing at once, after a 2 KB script edit (nocx-z9s9.18). "Under the
// cap" is a moment; what matters is the interval between the command and
// the hard limit it can never approach, because MAX_ARG_STRLEN is the
// kernel's and cannot be raised.
//
// The first version of this test still failed to catch the ShellAuto
// refusal that opened nocx-z9s9.17 (measured 123,013 bytes against the
// 122,880 cap — refused, and the test green). Three blind spots: it
// measured only ShellAuto; it measured the margin to MAX_ARG_STRLEN, but
// the refusal happens at maxFullLauncherLen, and the 8 KiB between the two
// is exactly the margin the test demanded, so a command sitting 2 KB under
// the refusal cap still passed; and the 8 KiB margin itself was too small
// — the pre-strip baseline (comments shipped) leaves ShellAuto 15,919
// bytes under this fixture's cap, so a 16 KiB margin makes this test fail
// on exactly the tier that broke, while the stripped payload leaves
// ~63 KB and passes with room to spare.
//
// This version measures every tier, against the cap the launcher actually
// refuses at, with the launch options a real session carries (internal/ssh
// RemoteLifecycleLaunch shapes: the 32-hex session id, an environment id,
// the lifecycle lane/domain/epoch/port and the 64-hex capability).
//
// A failure here is not "make the number bigger": at 128 KiB there is
// nowhere left to go. It means the payload must shrink — the scripts used
// to ship ~22 KB of comments the remote host never reads, and they no
// longer do (nocx-z9s9.17).
func TestFullLauncherStaysUnderArgLimit(t *testing.T) {
	// Linux MAX_ARG_STRLEN: 32 pages, and the whole remote command is one
	// argv word. macOS is looser; this is the binding one.
	const maxArgStrLen = 128 * 1024
	// 16 KiB of room to act: the pre-strip baseline (comments shipped)
	// fails this margin on ShellAuto while every stripped tier clears it
	// by ~63 KB, so erosion trips the guard long before a refusal.
	const wantMargin = 16 * 1024

	opts := LaunchOptions{
		SessionID:     "0123456789abcdef0123456789abcdef",
		Enhanced:      true,
		Lane:          "lane-01234567",
		Domain:        "dom-01234567",
		Epoch:         42,
		LifecyclePort: 65432,
		Capability:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	for _, kind := range []ShellKind{ShellBash, ShellZsh, ShellUnknown, ShellAuto} {
		cmd, _, ok := FullBootstrapCommand(kind, opts)
		if !ok {
			t.Fatalf("%s launcher refused", kind)
		}
		if margin := maxFullLauncherLen - len(cmd); margin < wantMargin {
			t.Errorf("%s command is %d bytes, leaving %d under the %d-byte cap — want at least %d.\n"+
				"The cap cannot absorb this: MAX_ARG_STRLEN is the kernel's. Shrink the payload instead\n"+
				"— the three scripts used to ship ~22 KB of comments the far host never reads (nocx-z9s9.17).",
				kind, len(cmd), margin, maxFullLauncherLen, wantMargin)
		}
	}
	// The configured cap must itself respect the kernel bound, or it would
	// authorise a command that cannot exec. (The margin is asserted against
	// the cap above; this check is only about the cap's own ceiling.)
	if maxFullLauncherLen > maxArgStrLen {
		t.Errorf("maxFullLauncherLen %d exceeds MAX_ARG_STRLEN %d",
			maxFullLauncherLen, maxArgStrLen)
	}
}

// TestAutoCommand_RefusesOverCap lowers the full launcher cap to prove the
// refusal path: a launcher that would outgrow the remote limits must refuse,
// not emit a command the far host cannot exec.
func TestAutoCommand_RefusesOverCap(t *testing.T) {
	old := maxFullLauncherLen
	maxFullLauncherLen = 256
	t.Cleanup(func() { maxFullLauncherLen = old })

	cmd, reason, ok := FullBootstrapCommand(ShellAuto, LaunchOptions{
		Enhanced:  true,
		SessionID: "auto-overcap-test",
	})
	if ok {
		t.Fatalf("auto command accepted over the lowered cap; got %d bytes", len(cmd))
	}
	if reason != ReasonUnsupportedShell {
		t.Errorf("reason = %q, want %q", reason, ReasonUnsupportedShell)
	}
}

// TestAutoCommand_EnhancedRequiresSessionID: the pinned precondition applies
// uniformly across tiers — ShellAuto is the caller's build-time intent, so
// the same fail-closed contract holds.
func TestAutoCommand_EnhancedRequiresSessionID(t *testing.T) {
	cmd, reason, ok := FullBootstrapCommand(ShellAuto, LaunchOptions{Enhanced: true})
	if ok {
		t.Fatalf("ShellAuto enhanced with empty SessionID accepted; got %q", cmd)
	}
	if reason != ReasonUnsupportedShell {
		t.Errorf("reason = %q, want %q", reason, ReasonUnsupportedShell)
	}
}
