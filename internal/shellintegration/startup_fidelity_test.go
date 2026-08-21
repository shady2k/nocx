package shellintegration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Startup fidelity, watched on a real pty (design §9, assertions 37-39).
//
// Everything here drives the REAL launch carrier — the script stage-1 execs
// on the far host — against a disposable $HOME and a fixture /etc, and reads
// what a user would have seen. A test that rendered the carrier and matched
// its text would prove the string exists; only running it proves the shell
// reads the file.
//
// No wait in this file is a duration. Every one is a wait for an observable
// state change (the fixture prompt, or the pty closing); the only clocks are
// failure deadlines, which fire when the thing being waited for never
// happens.

const (
	// fixtureMotdFirst is what an /etc/motd fixture starts with. It is
	// matched by COUNT, so it must not appear in anything else the
	// session prints — hence the shape.
	fixtureMotdFirst  = "NOCX-MOTD-FIRST-LINE"
	fixtureMotdSecond = "NOCX-MOTD-SECOND-LINE"
	// fixtureProfileMark is exported by the fixture system profile ONLY.
	// Observing it in the integrated shell is assertion 38's whole
	// question.
	fixtureProfileMark = "NOCX_ETC_PROFILE_REACHED"
	// fixtureUserProfileMark is exported by the fixture ~/.profile only:
	// the user half of the same login phase, and the half a test can
	// control for a shell that reads /etc/profile at a path compiled into
	// it.
	fixtureUserProfileMark = "NOCX_USER_PROFILE_REACHED"
	// fixturePrompt is the user rc's PS1: not a synchronisation point, only
	// something legible in a failure dump.
	fixturePrompt = "NOCX-PROMPT> "
	// promptReady is what every wait in this file keys off: OSC 133 B, the
	// marker that says "the prompt has been written and the shell is
	// reading". It is the product's own signal, and it is the same on all
	// three tiers — the minimal tier replaces the user's PS1 outright with
	// its markers, so a visible prompt string is not an observable there.
	promptReady = "\x1b]133;B"
)

// withEtcFixture points the rendered launch carrier and the rendered bash
// rcfile at a fixture system directory for the duration of one test. It is
// a render-time redirection, not a runtime one: everything published after
// it names the fixture, and TestStartupFidelity_ShippedPathsAreTheRealOnes
// holds the shipped bytes to /etc.
func withEtcFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := etcDir
	etcDir = dir
	t.Cleanup(func() { etcDir = prev })
	return dir
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	// #nosec G306 — test fixture file, deliberately restricted.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// startupFixtureHome materialises the $HOME a tier's integrated session
// reads: a user rc that plants the prompt sentinel, and a ~/.profile that
// plants the user-login-file sentinel. Both are present for every tier, so
// a tier that reads a file it should not is as visible as one that misses a
// file it should.
func startupFixtureHome(t *testing.T, kind ShellKind) string {
	t.Helper()
	home := t.TempDir()
	prompt := "PS1='" + fixturePrompt + "'\n"
	switch kind {
	case ShellBash:
		writeFixtureFile(t, filepath.Join(home, ".bashrc"),
			prompt+"HISTFILE=/dev/null\necho USER_RC_RAN\n")
	case ShellZsh:
		writeFixtureFile(t, filepath.Join(home, ".zshrc"),
			prompt+"HISTFILE=/dev/null\necho USER_RC_RAN\n")
	case ShellUnknown:
		// The minimal tier's user rc IS ~/.profile: the login shell reads
		// it natively. It carries both sentinels for that reason.
		writeFixtureFile(t, filepath.Join(home, ".profile"),
			prompt+"export PS1\nexport "+fixtureUserProfileMark+"=yes\necho USER_RC_RAN\n")
		return home
	default:
		t.Fatalf("no fixture home for shell kind %q", kind)
	}
	writeFixtureFile(t, filepath.Join(home, ".profile"),
		"export "+fixtureUserProfileMark+"=yes\n")
	return home
}

// startupShellFor resolves the binary the carrier dispatches on for a tier,
// failing (never skipping) for the two shells whose absence would silently
// retire a tier from the suite — the rule requireIntegrationShell exists for.
func startupShellFor(t *testing.T, kind ShellKind) string {
	t.Helper()
	switch kind {
	case ShellBash:
		requireBinBash(t)
		p, err := exec.LookPath("bash")
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		return p
	case ShellZsh:
		return requireIntegrationShell(t, "zsh")
	case ShellUnknown:
		return requireIntegrationShell(t, "dash")
	}
	t.Fatalf("no shell for kind %q", kind)
	return ""
}

// runStartupSession execs the launch carrier the way stage-1 does — under
// /bin/sh, with the bootstrap marker set, with $SHELL naming the tier — and
// drives it to its first prompt. Each line is typed only once the prompt
// that must precede it has been SEEN; the returned string is everything the
// pty produced, up to the session ending on its own.
func runStartupSession(t *testing.T, home, shellPath string, lines ...string) string {
	t.Helper()
	// #nosec G204 — the command is a package constant plus a literal
	// session id; a pty is the only way to observe prompt-time behaviour.
	c := exec.Command("/bin/sh", "-c", `exec "$HOME/.nocx/launch" `+ShellQuote("sess-fidelity"))
	c.Env = append(os.Environ(),
		"HOME="+home,
		"SHELL="+shellPath,
		"TERM=xterm",
		BootstrapEnv+"=1",
	)
	ptmx, err := pty.Start(c)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	chunks := make(chan []byte, 64)
	go func() {
		defer close(chunks)
		b := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(b)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, b[:n])
				chunks <- cp
			}
			if rerr != nil {
				return
			}
		}
	}()

	var out strings.Builder
	// A failure deadline, not a synchronisation interval: it fires only
	// when the state being waited for never arrives.
	deadline := time.After(60 * time.Second)
	from := 0
	awaitPrompt := func() bool {
		for {
			if i := strings.Index(out.String()[from:], promptReady); i >= 0 {
				from += i + len(promptReady)
				return true
			}
			select {
			case ch, ok := <-chunks:
				if !ok {
					return false
				}
				out.Write(ch)
			case <-deadline:
				t.Fatalf("timed out waiting for the shell to reach a prompt (OSC 133 B); "+
					"output so far:\n%s", out.String())
			}
		}
	}
	for _, line := range lines {
		if !awaitPrompt() {
			t.Fatalf("the session ended before it reached a prompt; output:\n%s", out.String())
		}
		if _, werr := ptmx.Write([]byte(line + "\n")); werr != nil {
			t.Fatalf("write %q: %v", line, werr)
		}
	}
	for {
		select {
		case ch, ok := <-chunks:
			if !ok {
				_ = c.Wait()
				return out.String()
			}
			out.Write(ch)
		case <-deadline:
			_ = c.Process.Kill()
			t.Fatalf("timed out waiting for the session to end; output:\n%s", out.String())
		}
	}
}

// publishCarrierInto publishes the bundle — carrier included — under a
// disposable $HOME. It must run AFTER withEtcFixture, because the carrier's
// paths are rendered at publish time.
func publishCarrierInto(t *testing.T, home string) {
	t.Helper()
	if _, err := NewPublisher(testLogger(), NewOSFS(), filepath.Join(home, dirName)).
		Publish(launchBundle()); err != nil {
		t.Fatalf("publish for carrier: %v", err)
	}
}

var startupTiers = []ShellKind{ShellBash, ShellZsh, ShellUnknown}

// Assertion 37: against a fixture with /etc/motd, an integrated session of
// each tier prints it exactly once, and prints nothing when ~/.hushlogin
// exists.
func TestStartupFidelity_MotdPrintedExactlyOncePerTier(t *testing.T) {
	for _, kind := range startupTiers {
		t.Run(string(kind), func(t *testing.T) {
			shellPath := startupShellFor(t, kind)
			etc := withEtcFixture(t)
			writeFixtureFile(t, motdPath(), fixtureMotdFirst+"\n"+fixtureMotdSecond+"\n")
			_ = etc
			home := startupFixtureHome(t, kind)
			publishCarrierInto(t, home)

			out := runStartupSession(t, home, shellPath, "exit")

			if n := strings.Count(out, fixtureMotdFirst); n != 1 {
				t.Errorf("%s: /etc/motd first line appears %d times, want exactly 1; output:\n%s",
					kind, n, out)
			}
			if n := strings.Count(out, fixtureMotdSecond); n != 1 {
				t.Errorf("%s: /etc/motd second line appears %d times, want exactly 1; output:\n%s",
					kind, n, out)
			}
		})
	}
}

func TestStartupFidelity_HushloginSuppressesTheBanner(t *testing.T) {
	for _, kind := range startupTiers {
		t.Run(string(kind), func(t *testing.T) {
			shellPath := startupShellFor(t, kind)
			withEtcFixture(t)
			writeFixtureFile(t, motdPath(), fixtureMotdFirst+"\n"+fixtureMotdSecond+"\n")
			home := startupFixtureHome(t, kind)
			writeFixtureFile(t, filepath.Join(home, hushloginFile), "")
			publishCarrierInto(t, home)

			out := runStartupSession(t, home, shellPath, "exit")

			if strings.Contains(out, fixtureMotdFirst) {
				t.Errorf("%s: the banner was printed although ~/%s exists; output:\n%s",
					kind, hushloginFile, out)
			}
		})
	}
}

// Assertion 38: for each tier, a variable exported only by the system
// profile is observable in the integrated shell, OR that tier and that
// reason appear in the declared-deviation allowlist this same test consults.
// The allowlist is fidelity.go's table, so a tier that quietly stops reading
// the file — or quietly starts — fails here until somebody changes the
// declaration too.
func TestStartupFidelity_SystemProfileObservableOrDeclared(t *testing.T) {
	for _, kind := range startupTiers {
		t.Run(string(kind), func(t *testing.T) {
			tier, ok := startupTierFor(kind)
			if !ok {
				t.Fatalf("%s has no row in the startup fidelity table", kind)
			}
			switch tier.SystemProfile {
			case profileSourcedByNocx:
				// nocx's own startup file names the path, so a fixture
				// /etc is enough to watch a real shell read a real file.
				shellPath := startupShellFor(t, kind)
				withEtcFixture(t)
				writeFixtureFile(t, systemProfilePath(),
					"export "+fixtureProfileMark+"=yes\n")
				home := startupFixtureHome(t, kind)
				publishCarrierInto(t, home)

				out := runStartupSession(t, home, shellPath,
					`echo "SYSPROFILE=[${`+fixtureProfileMark+`-}]"`, "exit")

				if !strings.Contains(out, "SYSPROFILE=[yes]") {
					t.Errorf("%s: the table says the tier sources %s, and a variable exported "+
						"only there is not visible in the integrated shell; output:\n%s",
						kind, systemProfilePath(), out)
				}
			case profileReadByLoginShell:
				// The shell reads the system profile at a path compiled
				// into it, which no fixture can redirect. What IS
				// observable is the login phase itself: the same phase
				// reads ~/.profile, which the fixture owns. Plus the
				// structural half — the tier must exec a LOGIN shell, or
				// there is no phase at all.
				arg := renderTierPayload(t, kind)
				if !strings.Contains(arg, `exec "${SHELL:-/bin/sh}" -l`) {
					t.Errorf("%s: the table says the login shell reads the system profile, but "+
						"the tier does not exec a login shell: %q", kind, arg)
				}
				shellPath := startupShellFor(t, kind)
				withEtcFixture(t)
				home := startupFixtureHome(t, kind)
				publishCarrierInto(t, home)

				out := runStartupSession(t, home, shellPath,
					`echo "LOGINPHASE=[${`+fixtureUserProfileMark+`-}]"`, "exit")

				if !strings.Contains(out, "LOGINPHASE=[yes]") {
					t.Errorf("%s: the table says the login shell reads its login files, and "+
						"~/.profile was not read; output:\n%s", kind, out)
				}
			case profileNotRead:
				if reason := declaredReasonFor(tier, devSystemProfile); strings.TrimSpace(reason) == "" {
					t.Fatalf("%s does not read %s and declares no reason: an allowlist entry "+
						"without a reason is not a declaration", kind, systemProfilePath())
				}
				// The other half of the declaration: nothing this tier
				// ships may source the file, or the table is describing a
				// product that no longer exists.
				withEtcFixture(t)
				if arg := renderTierPayload(t, kind); strings.Contains(arg, systemProfilePath()) {
					t.Errorf("%s is declared not to read %s, and its payload names it",
						kind, systemProfilePath())
				}
			default:
				t.Fatalf("%s has an unhandled system-profile mode %q", kind, tier.SystemProfile)
			}
		})
	}
}

// The zsh tier's login-shell gap, closed: the user's own ~/.zshenv,
// ~/.zprofile, ~/.zshrc and ~/.zlogin all run, in the phases a native
// `zsh -l` would run them in. Before this the transient ZDOTDIR shadowed the
// directory rather than the file, so three of the four were simply absent —
// and the source comment that declared it also claimed ~/.zlogin was absent,
// which had stopped being true when the .zshrc phase began restoring
// ZDOTDIR. Order is asserted, not just presence: a file read in the wrong
// phase sets a variable a later phase was supposed to see first.
func TestStartupFidelity_ZshRunsEveryUserLoginPhase(t *testing.T) {
	shellPath := startupShellFor(t, ShellZsh)
	withEtcFixture(t)
	home := t.TempDir()
	for _, f := range []string{".zshenv", ".zprofile", ".zlogin"} {
		writeFixtureFile(t, filepath.Join(home, f), "echo PHASE"+strings.ToUpper(f[1:])+"\n")
	}
	writeFixtureFile(t, filepath.Join(home, ".zshrc"),
		"PS1='"+fixturePrompt+"'\nHISTFILE=/dev/null\necho PHASEZSHRC\n")
	publishCarrierInto(t, home)

	out := runStartupSession(t, home, shellPath, "exit")

	want := []string{"PHASEZSHENV", "PHASEZPROFILE", "PHASEZSHRC", "PHASEZLOGIN"}
	at := make([]int, len(want))
	for i, w := range want {
		at[i] = strings.Index(out, w)
		if at[i] < 0 {
			t.Fatalf("the user's %s phase did not run; output:\n%s", w, out)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i] < at[i-1] {
			t.Errorf("%s ran before %s; a native login runs them in the order %v; output:\n%s",
				want[i], want[i-1], want, out)
		}
	}
}

// A user ~/.zshenv that chooses its own ZDOTDIR is the one file that can
// move every later phase, and a native zsh honours it. The tier must honour
// it for the USER's files and keep its own chain intact — otherwise the
// integration silently does not install and the transient directory leaks.
func TestStartupFidelity_ZshHonoursAUserChosenZdotdir(t *testing.T) {
	shellPath := startupShellFor(t, ShellZsh)
	withEtcFixture(t)
	home := t.TempDir()
	chosen := filepath.Join(home, "zdotdir")
	writeFixtureFile(t, filepath.Join(home, ".zshenv"),
		"echo PHASEZSHENV\nexport ZDOTDIR="+ShellQuote(chosen)+"\n")
	writeFixtureFile(t, filepath.Join(chosen, ".zprofile"), "echo CHOSENZPROFILE\n")
	writeFixtureFile(t, filepath.Join(chosen, ".zshrc"),
		"PS1='"+fixturePrompt+"'\nHISTFILE=/dev/null\necho CHOSENZSHRC\n")
	publishCarrierInto(t, home)

	out := runStartupSession(t, home, shellPath, `echo "ZD=[$ZDOTDIR]"`, "exit")

	for _, w := range []string{"PHASEZSHENV", "CHOSENZPROFILE", "CHOSENZSHRC"} {
		if !strings.Contains(out, w) {
			t.Errorf("%s did not run; output:\n%s", w, out)
		}
	}
	if !strings.Contains(out, "ZD=["+chosen+"]") {
		t.Errorf("ZDOTDIR was not restored to the user's own choice; output:\n%s", out)
	}
}

// The render-time fixture must never reach a user. The shipped carrier and
// the shipped bash rcfile name the real paths, and this is what says so.
func TestStartupFidelity_ShippedPathsAreTheRealOnes(t *testing.T) {
	if etcDir != "/etc" {
		t.Fatalf("etcDir = %q at rest, want /etc", etcDir)
	}
	carrier := launchCarrier()
	for _, want := range []string{"/etc/motd", hushloginFile} {
		if !strings.Contains(carrier, want) {
			t.Errorf("the shipped launch carrier does not name %q", want)
		}
	}
	rc := bashRcfile(remoteLogin, "", "", "")
	if !strings.Contains(rc, "/etc/profile") {
		t.Errorf("the shipped bash rcfile does not source /etc/profile")
	}
	// The local child was never a login: it must not acquire one.
	if strings.Contains(bashRcfile(localChild, "", "", ""), "/etc/profile") {
		t.Errorf("the LOCAL bash rcfile sources /etc/profile; a local session had no login " +
			"to reproduce")
	}
}

// declaredReasonFor is the reason a tier gives for one deviation id, or ""
// when it declares none. It reads the table rather than restating it: two
// answers to "why does this tier differ" is the defect fidelity.go exists to
// prevent, and a test is as capable of forking one as production code.
func declaredReasonFor(t startupTier, id string) string {
	for _, d := range t.Deviations {
		if d.ID == id {
			return d.Reason
		}
	}
	return ""
}

// renderTierPayload is the shell a tier actually ships, for the structural
// half of a declaration — "the payload does / does not name this path".
//
// It asks the product rather than re-deriving the expression: tierArg is the
// function the installed launch carrier itself calls for each of its three
// arms, so what this reads is the payload the far host runs, byte for byte.
// The per-tier ARG methods this used to call are gone with the remote command
// they were built for — they substituted both bearers into the rcfile TEXT and
// that text travelled in argv (ADR-0035) — and a payload rendered any other
// way here would be a second answer to "what does tier X ship".
//
// The env block is the carrier's own, which is empty: the carrier is published
// once and reused by every session, so it carries nothing per-session. Neither
// assertion below reads the env block — they ask whether the tier execs a
// login shell and whether it names the system profile, and both are properties
// of the transport and the rcfile, not of the session environment.
func renderTierPayload(t *testing.T, kind ShellKind) string {
	t.Helper()
	arg := tierArg(kind, "")
	if arg == "" {
		t.Fatalf("no payload for shell kind %q", kind)
	}
	return arg
}

// Every declared deviation is a complete declaration: a stable id from the
// closed vocabulary, what differs and why. A deviation with an empty reason
// is a note, not a declaration, and the product cannot render it. Every tier
// has a row, and ShellAuto — a build-time intent, not a tier — has none.
func TestStartupFidelity_EveryDeviationIsACompleteDeclaration(t *testing.T) {
	ids := map[string]bool{
		devUserLoginFiles: true, devSystemProfile: true, devShellNestingDepth: true,
		devBannerStaticOnly: true, devLastLoginLine: true, devEnvAfterLoginFiles: true,
	}
	for _, kind := range startupTiers {
		tier, ok := startupTierFor(kind)
		if !ok {
			t.Fatalf("%s has no row in the startup fidelity table", kind)
		}
		if len(tier.Deviations) == 0 {
			t.Errorf("%s declares no deviations at all; every tier has at least the banner's",
				kind)
		}
		seen := map[string]bool{}
		for _, d := range tier.Deviations {
			if !ids[d.ID] {
				t.Errorf("%s: deviation id %q is not in the closed vocabulary", kind, d.ID)
			}
			if seen[d.ID] {
				t.Errorf("%s: deviation %q declared twice", kind, d.ID)
			}
			seen[d.ID] = true
			if strings.TrimSpace(d.Detail) == "" || strings.TrimSpace(d.Reason) == "" {
				t.Errorf("%s: deviation %q has an empty detail or reason", kind, d.ID)
			}
		}
		// The ones that are true of every tier are true of every tier.
		for _, c := range commonDeviations {
			if !seen[c.ID] {
				t.Errorf("%s does not carry the common deviation %q", kind, c.ID)
			}
		}
	}
	if _, ok := startupTierFor(ShellAuto); ok {
		t.Error("ShellAuto is a build-time intent with no tier of its own; it must have no row")
	}
}
