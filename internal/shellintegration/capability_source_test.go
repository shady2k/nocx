package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tiers read their bearers from a descriptor, and the startup files stay
// capability-free (design §5.2 point 7).
//
// This is the assertion the epic exists for, seen from the far end: the
// rcfile an installed generation carries is written long before the session
// and CANNOT contain a per-session value — so it is capability-free by
// construction, not by care — and the value arrives on a descriptor the shell
// reads once, after the user's own startup has run and returned.
//
// The runs are real shells on a real terminal, waiting on markers rather than
// on durations.

// capProbe is what stands in for the generation script: it reports every fact
// the assertions need about where the bearers ended up.
const capProbe = `printf 'CAP=[%s] FENCE=[%s]\n' "$__nocx_cap" "$__nocx_lc_recovery"
printf 'CAPFD=[%s]\n' "${NOCX_CAP_FD-unset}"
if { true <&7; } 2>/dev/null; then printf 'FD7=[open]\n'; else printf 'FD7=[closed]\n'; fi
printf 'PROBE_DONE\n'
`

// capFixture writes the two files a tier run needs: the rcfile under test and
// the unlinked-descriptor stand-in holding the pair.
func capFixture(t *testing.T, rc string) (rcPath, dataPath string) {
	t.Helper()
	dir := t.TempDir()
	rcPath = filepath.Join(dir, "rc")
	dataPath = filepath.Join(dir, "data")
	// #nosec G306 — test fixture.
	if err := os.WriteFile(rcPath, []byte(rc), 0o600); err != nil {
		t.Fatalf("write rc: %v", err)
	}
	// #nosec G306 — test fixture: the real one is an unlinked temp file.
	if err := os.WriteFile(dataPath, []byte(canaryCap+"\n"+canaryFence+"\n"), 0o600); err != nil {
		t.Fatalf("write descriptor contents: %v", err)
	}
	return rcPath, dataPath
}

func TestTier_BashReadsTheCapabilityFromTheDescriptorOnceAndClosesIt(t *testing.T) {
	requireBinBash(t)
	home := t.TempDir()
	// The user's own startup file runs FIRST and must not be able to see
	// either bearer — the position of the read is the promise.
	// #nosec G306 — test fixture.
	if err := os.WriteFile(filepath.Join(home, ".bashrc"),
		[]byte("printf 'USER_RC_SEES=[%s]\\n' \"${__nocx_cap-unset}\"\n"), 0o600); err != nil {
		t.Fatalf("write .bashrc: %v", err)
	}
	rcPath, dataPath := capFixture(t,
		bashRcfile(remoteLogin, "", capProbe, capabilityFromDescriptor(bashUnsetExport)))

	s := startLoader(t,
		"exec 7<"+dataPath+"; "+CapabilityFDEnv+"=7 exec bash --rcfile "+rcPath+" -i",
		[]string{"HOME=" + home, "TERM=xterm", "TMPDIR=" + t.TempDir()}, stdoutOnTerminal)
	s.waitFor("PROBE_DONE")
	out := s.output()

	if !strings.Contains(out, "USER_RC_SEES=[unset]") {
		t.Errorf("the user's rc could see a bearer; output:\n%s", out)
	}
	if !strings.Contains(out, "CAP=["+canaryCap+"] FENCE=["+canaryFence+"]") {
		t.Errorf("the tier did not read both values from the descriptor; output:\n%s", out)
	}
	if !strings.Contains(out, "CAPFD=[unset]") {
		t.Errorf("the descriptor number survived into the shell; output:\n%s", out)
	}
	if !strings.Contains(out, "FD7=[closed]") {
		t.Errorf("the descriptor was left open for every descendant; output:\n%s", out)
	}
	// The rcfile itself is capability-free: it is an installed generation
	// file, and the assertion is on its TEXT.
	if strings.Contains(rcPathContents(t, rcPath), canaryCap) {
		t.Error("the rcfile text carries the capability")
	}
}

func TestTier_ZshReadsTheCapabilityFromTheDescriptorOnceAndClosesIt(t *testing.T) {
	zshPath := requireIntegrationShell(t, "zsh")
	home := t.TempDir()
	// #nosec G306 — test fixture.
	if err := os.WriteFile(filepath.Join(home, ".zshrc"),
		[]byte("printf 'USER_RC_SEES=[%s]\\n' \"${__nocx_cap-unset}\"\n"), 0o600); err != nil {
		t.Fatalf("write .zshrc: %v", err)
	}
	zdotdir := t.TempDir()
	rc := zshRcfile("", capProbe, capabilityFromDescriptor(zshUnsetExport))
	// #nosec G306 — test fixture.
	if err := os.WriteFile(filepath.Join(zdotdir, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatalf("write transient .zshrc: %v", err)
	}
	_, dataPath := capFixture(t, rc)

	s := startLoader(t,
		"exec 7<"+dataPath+"; "+CapabilityFDEnv+"=7 ZDOTDIR="+zdotdir+" exec "+zshPath+" -l",
		[]string{"HOME=" + home, "TERM=xterm", "TMPDIR=" + t.TempDir()}, stdoutOnTerminal)
	s.waitFor("PROBE_DONE")
	out := s.output()

	if !strings.Contains(out, "USER_RC_SEES=[unset]") {
		t.Errorf("the user's rc could see a bearer; output:\n%s", out)
	}
	if !strings.Contains(out, "CAP=["+canaryCap+"] FENCE=["+canaryFence+"]") {
		t.Errorf("the tier did not read both values from the descriptor; output:\n%s", out)
	}
	if !strings.Contains(out, "CAPFD=[unset]") {
		t.Errorf("the descriptor number survived into the shell; output:\n%s", out)
	}
	if !strings.Contains(out, "FD7=[closed]") {
		t.Errorf("the descriptor was left open for every descendant; output:\n%s", out)
	}
}

// TestLaunchCarrier_CarriesNoBearerInAnyTier: the installed carrier is
// published once and read by every session, so a per-session value in it
// would be a value shared between sessions. The assertion is on the shipped
// bytes, for all three tiers at once.
func TestLaunchCarrier_CarriesNoBearerInAnyTier(t *testing.T) {
	text := launchCarrier()
	for _, forbidden := range []string{"@CAP@", "@RECOVERY@", canaryCap, canaryFence} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the launch carrier contains %q", forbidden)
		}
	}
	// And it reads the descriptor in each tier that has a bearer to read.
	if n := strings.Count(text, CapabilityFDEnv); n < 2 {
		t.Errorf("the launch carrier names %s %d times; bash and zsh both read it", CapabilityFDEnv, n)
	}
	// The minimal tier consumes no capability at all — nocx.posix never
	// names one — so it gets no read rather than a read of nothing.
	if strings.Contains(posixEnvFile("", "x"), CapabilityFDEnv) {
		t.Error("the minimal tier reads a descriptor for a capability its script never uses")
	}
}

// TestLaunchCarrier_NamesTheTerminalOutcomeOnlyUnderABootstrap: the carrier
// is the only component that knows whether the generation it is about to exec
// still proves out, so it names the outcome — but a launch that did not come
// from a bootstrap must not put protocol tokens on a user's terminal.
func TestLaunchCarrier_NamesTheTerminalOutcomeOnlyUnderABootstrap(t *testing.T) {
	text := launchCarrier()
	if !strings.Contains(text, OutcomePrefix) {
		t.Fatal("the launch carrier names no outcome at all")
	}
	if !strings.Contains(text, BootstrapEnv) {
		t.Fatal("the outcome is not gated on the bootstrap marker")
	}
	// Gated and then cleared, so the marker never reaches the user's shell.
	if !strings.Contains(text, "unset "+BootstrapEnv) {
		t.Error("the bootstrap marker is not unset before the tier is exec'd")
	}
}

func rcPathContents(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) // #nosec G304 — test-owned path.
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
