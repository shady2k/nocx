package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The full bootstrap launcher's acceptance surface, watched end to end on a
// real pty against a disposable $HOME: the launcher publishes the bundle,
// the integrated shell comes up integrated, and the Go publisher verifies
// the published state. The readiness passport is DELETED (nocx-u7uh.11):
// the environment identity now rides the authenticated lifecycle channel,
// and the scripts emit no OSC 636 P — asserted below.

func TestBashFullLauncher_PublishesAndPassportNamesGeneration(t *testing.T) {
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, _, ok := FullBootstrapCommand(ShellBash, LaunchOptions{
		SessionID: "sess-bash", Enhanced: true,
	})
	if !ok {
		t.Fatal("bash launcher refused")
	}

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		[]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, "echo hello", "exit")

	if strings.Contains(out, "636;P") {
		t.Errorf("no readiness passport may be emitted (nocx-u7uh.11); output:\n%s", out)
	}
	if !strings.Contains(out, "USER_RC_RAN") {
		t.Errorf("user rc did not run; output:\n%s", out)
	}
	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") == 0 || countMarkers(ms, "B") == 0 {
		t.Errorf("no A/B markers: the remote shell did not come up integrated; output:\n%s", out)
	}

	// The launcher's own publish is the Go-verifiable installed fact.
	vr, err := NewPublisher(testLogger(), NewOSFS(), filepath.Join(home, dirName)).Verify()
	if err != nil || !vr.Installed || vr.Generation != genDir(version) {
		t.Errorf("Verify after full launcher = %+v err=%v, want installed %s", vr, err, genDir(version))
	}
}

// TestZshFullLauncher_PublishesAndPassportNamesGeneration is the zsh tier's
// half: the transient-ZDOTDIR lifecycle plus the publish.
func TestZshFullLauncher_PublishesAndPassportNamesGeneration(t *testing.T) {
	requireIntegrationShell(t, "zsh")
	home := writeZshFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, _, ok := FullBootstrapCommand(ShellZsh, LaunchOptions{
		SessionID: "sess-zsh", Enhanced: true,
	})
	if !ok {
		t.Fatal("zsh launcher refused")
	}

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		[]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, "echo hi", "exit")

	if strings.Contains(out, "636;P") {
		t.Errorf("no readiness passport may be emitted (nocx-u7uh.11); output:\n%s", out)
	}
	vr, err := NewPublisher(testLogger(), NewOSFS(), filepath.Join(home, dirName)).Verify()
	if err != nil || !vr.Installed || vr.Generation != genDir(version) {
		t.Errorf("Verify after full launcher = %+v err=%v, want installed %s", vr, err, genDir(version))
	}
}

// TestPosixFullLauncher_PublishesAndPassportNamesGeneration is the minimal
// tier's half: dash parses the command and the publish lands.
func TestPosixFullLauncher_PublishesAndPassportNamesGeneration(t *testing.T) {
	dashPath := requireIntegrationShell(t, "dash")
	home := t.TempDir()
	// #nosec G306 — test fixture file, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("echo USER_RC_RAN\n"), 0o600); err != nil {
		t.Fatalf("write fixture .profile: %v", err)
	}
	tmp := t.TempDir()
	cmd, _, ok := FullBootstrapCommand(ShellUnknown, LaunchOptions{
		SessionID: "sess-posix", Enhanced: true,
	})
	if !ok {
		t.Fatal("posix launcher refused")
	}

	out := runLauncherOnPTY(t, dashPath, cmd,
		[]string{"HOME=" + home, "SHELL=" + dashPath, "TMPDIR=" + tmp, "TERM=xterm"},
		"true", "exit")

	if strings.Contains(out, "636;P") {
		t.Errorf("no readiness passport may be emitted (nocx-u7uh.11); output:\n%s", out)
	}
	vr, err := NewPublisher(testLogger(), NewOSFS(), filepath.Join(home, dirName)).Verify()
	if err != nil || !vr.Installed || vr.Generation != genDir(version) {
		t.Errorf("Verify after full launcher = %+v err=%v, want installed %s", vr, err, genDir(version))
	}
}

// TestFullLauncher_ReadonlyHome_FallsBackToVisibleNativePrompt: a read-only
// $HOME publishes nothing and records no installed fact. ADR-0024 decision 4
// deletes the old "transient-integrated" middle tier (integration without an
// installed generation): the argv tiers now SOURCE the installed generation
// files, which a failed publish never created, so the session is a plain
// conventional terminal — a visible native prompt, no markers.
func TestFullLauncher_ReadonlyHome_FallsBackToVisibleNativePrompt(t *testing.T) {
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, _, ok := FullBootstrapCommand(ShellBash, LaunchOptions{
		SessionID: "sess-ro", Enhanced: true,
	})
	if !ok {
		t.Fatal("bash launcher refused")
	}

	// #nosec G302 — test fixture deliberately making HOME read-only so the
	// publish's fail-open can be proven.
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod home: %v", err)
	}
	// #nosec G302 — restoring the test fixture's HOME mode.
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		[]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, "echo hello", "exit")

	if strings.Contains(out, "636;P") {
		t.Errorf("no readiness passport may be emitted (nocx-u7uh.11):\n%s", out)
	}
	ms := extractOscMarkers(out)
	if countMarkers(ms, "A") != 0 || countMarkers(ms, "C") != 0 {
		t.Errorf("markers emitted after a failed publish; the session must be conventional:\n%s", out)
	}
	// The user's fixture prompt must be visible — the conventional terminal
	// is the fail-open, and it must be a prompt the user can see.
	if !strings.Contains(out, "FIXTURE-PROMPT") {
		t.Errorf("no visible native prompt after a failed publish:\n%s", out)
	}
	// The source-line failure must not print a shell error on the terminal.
	if strings.Contains(out, "No such file or directory") {
		t.Errorf("failed publish leaked a source error onto the terminal:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, dirName)); !os.IsNotExist(err) {
		t.Errorf("read-only HOME gained a ~/.nocx (err=%v)", err)
	}
}

// TestFullLauncher_ForeignRoot_RefusedAndConventional: an existing ~/.nocx
// that is not recognisably ours is never modified, and — with the
// transient-integrated tier deleted (ADR-0024 decision 4) — the session is a
// plain conventional terminal: visible native prompt, no markers.
func TestFullLauncher_ForeignRoot_RefusedAndConventional(t *testing.T) {
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	root := filepath.Join(home, dirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-ours.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	cmd, _, ok := FullBootstrapCommand(ShellBash, LaunchOptions{
		SessionID: "sess-f", Enhanced: true,
	})
	if !ok {
		t.Fatal("bash launcher refused")
	}

	out := runLauncherOnPTY(t, "/bin/sh", cmd,
		[]string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}, "exit")

	if strings.Contains(out, "636;P") {
		t.Errorf("no readiness passport may be emitted (nocx-u7uh.11):\n%s", out)
	}
	if strings.Contains(out, "No such file or directory") {
		t.Errorf("refused publish leaked a source error onto the terminal:\n%s", out)
	}
	if !strings.Contains(out, "FIXTURE-PROMPT") {
		t.Errorf("no visible native prompt over a refused publish:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, manifestName)); !os.IsNotExist(err) {
		t.Errorf("foreign root was modified (manifest appeared, err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "not-ours.txt")); err != nil {
		t.Errorf("foreign file touched: %v", err)
	}
}

// TestFullLauncher_SecondConnection_StillIntegrates is the case every proof
// above misses and every real host is in: a $HOME that already carries the
// bundle (nocx-tr2n).
//
// The rcfile the full launcher installs sources
// integration/$NOCX_GENERATION/nocx.bash with stderr suppressed, and the
// prelude only named a generation on the path where it published one. On the
// second connection the version is already installed, the prelude skipped,
// NOCX_GENERATION came out empty, the source resolved to a directory that
// does not exist and said nothing — an ordinary terminal with the
// integration environment set and no integration in it. That is what the
// owner saw over ssh: markers on the first connection to a host, never
// again.
//
// The fixture runs the SAME launcher twice against one $HOME. The first run
// is the installing one; the assertions are all on the second.
func TestFullLauncher_SecondConnection_StillIntegrates(t *testing.T) {
	requireBinBash(t)
	home := writeBashFixtureHome(t, "")
	tmp := t.TempDir()
	cmd, _, ok := FullBootstrapCommand(ShellBash, LaunchOptions{
		SessionID: "sess-bash", Enhanced: true,
	})
	if !ok {
		t.Fatal("bash launcher refused")
	}
	env := []string{"HOME=" + home, "TMPDIR=" + tmp, "TERM=xterm"}

	first := runLauncherOnPTY(t, "/bin/sh", cmd, env, "echo hello", "exit")
	if ms := extractOscMarkers(first); countMarkers(ms, "A") == 0 {
		t.Fatalf("the installing connection was not integrated, so the second proves nothing; output:\n%s", first)
	}

	second := runLauncherOnPTY(t, "/bin/sh", cmd, env, "echo hello", "exit")
	ms := extractOscMarkers(second)
	if countMarkers(ms, "A") == 0 || countMarkers(ms, "B") == 0 {
		t.Errorf("a connection to a host that already has the bundle came up unintegrated "+
			"(no A/B markers): NOCX_GENERATION was not named, so the rcfile sourced nothing; output:\n%s", second)
	}
	if !strings.Contains(second, "USER_RC_RAN") {
		t.Errorf("user rc did not run on the second connection; output:\n%s", second)
	}
}
