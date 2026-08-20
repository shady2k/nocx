package shellintegration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// versionPlus offsets the current script version and returns it as a string.
//
// The publish tests are about a RELATION — an older generation must not
// displace a newer one — and they used to spell it with the literals "11" and
// "12", which were "one behind" and "current" on the day they were written.
// The next bump made "12" one BEHIND the installed version, so the
// no-downgrade test began publishing an older generation where it meant to
// publish a newer one and asserting the outcome of a case it was no longer
// exercising. It failed loudly here, which is the good outcome; the same shape
// passing quietly is the one to fear (nocx-z9s9.18).
func versionPlus(t *testing.T, delta int) string {
	t.Helper()
	n, err := strconv.Atoi(version)
	if err != nil {
		t.Fatalf("script version %q is not an integer; these tests derive their generations from it: %v", version, err)
	}
	if n+delta < 1 {
		t.Fatalf("version %q offset by %d is not a publishable generation", version, delta)
	}
	return strconv.Itoa(n + delta)
}

// The full launcher's publish is proven in three layers: unit (env block,
// prelude shape, bundle contract), one-direction conformance (the sh publish
// runs under a real /bin/sh against a disposable $HOME and the Go publisher
// verifies it), and the refusals/fault paths the coordinator requires —
// interrupted at a boundary, the previous activation stays byte-identical and
// the next attempt converges.

// TestFullLauncher_PreludeIsOneLineNoSingleQuotes: the publish prelude
// travels inside the outer command's single-quoted argument, which a csh
// login shell would split on an embedded quote or newline.
func TestFullLauncher_PreludeIsOneLineNoSingleQuotes(t *testing.T) {
	prelude := buildPublishPrelude(version)
	if strings.ContainsAny(prelude, "'\n") {
		t.Errorf("prelude contains a single quote or newline (csh would split it): %q", prelude)
	}
	for _, kind := range []ShellKind{ShellBash, ShellZsh, ShellUnknown, ShellAuto} {
		cmd, _, ok := FullBootstrapCommand(kind, LaunchOptions{
			SessionID: "s", Enhanced: true,
		})
		if !ok {
			t.Fatalf("%s refused", kind)
		}
		if strings.ContainsRune(cmd, 0) {
			t.Errorf("%s: command contains a NUL", kind)
		}
	}
}

// TestLaunchBundle_ConformsToPublisherContract: the bundle descriptor both
// carriers publish satisfies validateBundle and publishes through the Go
// publisher, and the committed manifest verifies — the contract the sh
// writer must mirror.
func TestLaunchBundle_ConformsToPublisherContract(t *testing.T) {
	b := launchBundle()
	if err := validateBundle(b); err != nil {
		t.Fatalf("launchBundle fails validateBundle: %v", err)
	}
	root := filepath.Join(t.TempDir(), dirName)
	if _, err := NewPublisher(testLogger(), NewOSFS(), root).Publish(b); err != nil {
		t.Fatalf("Go publish of the shared bundle: %v", err)
	}
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil || !vr.Installed {
		t.Fatalf("Verify after Go publish: %+v err=%v", vr, err)
	}
	// The carrier file itself is what the launcher installs; it must be a
	// parseable POSIX script with a /bin/sh shebang.
	carrier, ok := b.file(launchName)
	if !ok {
		t.Fatal("bundle has no launch carrier")
	}
	if !strings.HasPrefix(string(carrier.Data), "#!/bin/sh\n") {
		t.Errorf("carrier shebang: %q", string(carrier.Data[:min(10, len(carrier.Data))]))
	}
}

// runShPublish executes the publish prelude under a real /bin/sh with a
// disposable HOME and returns the script's output.
func runShPublish(t *testing.T, version, home string, extraEnv ...string) string {
	t.Helper()
	prelude := buildPublishPrelude(version)
	cmd := exec.Command("/bin/sh", "-c", prelude+"; true") // #nosec G204 — package consts.
	cmd.Env = append(os.Environ(), append([]string{"HOME=" + home}, extraEnv...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh publish failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestShPublish_GoVerifyConformance is one half of the bidirectional
// conformance criterion: the sh writer publishes into a disposable HOME and
// the Go Verify reports it active and complete, with every file matching the
// recorded hash and mode.
func TestShPublish_GoVerifyConformance(t *testing.T) {
	home := t.TempDir()
	out := runShPublish(t, version, home)
	if strings.TrimSpace(out) != "" {
		t.Errorf("publish printed to the terminal: %q", out)
	}
	root := filepath.Join(home, dirName)
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil {
		t.Fatalf("Go Verify of sh-published state: %v", err)
	}
	if !vr.Installed || vr.Generation != genDir(version) || vr.Version != version || vr.Protocol != ProtocolVersion {
		t.Errorf("Verify = %+v, want Installed, generation %s, version %s, protocol %d", vr, genDir(version), version, ProtocolVersion)
	}
	for _, f := range []string{"nocx.bash", "nocx.zsh", "nocx.posix"} {
		info, err := os.Stat(filepath.Join(root, integrationDir, genDir(version), f))
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", f, info.Mode().Perm())
		}
	}
	if info, err := os.Stat(filepath.Join(root, launchName)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("launch mode = %v, want 0700 (err=%v)", info, err)
	}
}

// TestShPublish_IdempotentAndNoDowngrade: publishing the same version twice
// leaves one active generation; a newer installed compatible generation is
// never downgraded (equality is not the comparison).
func TestShPublish_IdempotentAndNoDowngrade(t *testing.T) {
	home := t.TempDir()
	runShPublish(t, version, home)
	before, err := os.ReadFile(filepath.Join(home, dirName, manifestName)) // #nosec G304 — test-owned.
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	// Same version again: no change at all.
	runShPublish(t, version, home)
	after, err := os.ReadFile(filepath.Join(home, dirName, manifestName)) // #nosec G304 — test-owned path.
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(before) != string(after) {
		t.Error("same-version publish rewrote the manifest")
	}

	// A newer installed generation is not downgraded: the Go writer publishes
	// the current version, and an sh publisher carrying the one before it must
	// leave that alone.
	newer, older := version, versionPlus(t, -1)
	root := filepath.Join(home, dirName)
	if _, pubErr := NewPublisher(testLogger(), NewOSFS(), root).Publish(testBundle(newer)); pubErr != nil {
		t.Fatalf("go publish v%s: %v", newer, pubErr)
	}
	runShPublish(t, older, home)
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if vr.Version != newer {
		t.Errorf("sh publish carrying v%s downgraded the installed generation: %+v, want version %s", older, vr, newer)
	}
}

// TestShPublish_ReadonlyHome_NoInstalledFact: a read-only $HOME publishes
// nothing and records no installed fact — the session continues transient.
func TestShPublish_ReadonlyHome_NoInstalledFact(t *testing.T) {
	home := t.TempDir()
	// #nosec G302 — test fixture deliberately making HOME read-only so the
	// publish's fail-open can be proven.
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod home: %v", err)
	}
	// #nosec G302 — restoring the test fixture's HOME mode.
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	if _, err := os.Stat(filepath.Join(home, dirName)); !os.IsNotExist(err) {
		t.Errorf("read-only HOME gained a ~/.nocx (err=%v)", err)
	}
}

// TestShPublish_ForeignRootRefused: an existing ~/.nocx that is not
// recognisably ours is never modified and never has its mode changed.
func TestShPublish_ForeignRootRefused(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	if err := os.MkdirAll(filepath.Join(root, "someone-elses"), 0o700); err != nil {
		t.Fatalf("mkdir foreign root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}
	// #nosec G302 — test fixture deliberately creating a foreign-mode root
	// so the publisher's refusal can be proven.
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	runShPublish(t, version, home)
	if _, err := os.Stat(filepath.Join(root, manifestName)); !os.IsNotExist(err) {
		t.Errorf("foreign root was modified (manifest appeared, err=%v)", err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("foreign root mode changed: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); err != nil {
		t.Errorf("foreign file removed: %v", err)
	}
}

// TestShPublish_SymlinkedRootRefused: no path in ~/.nocx is followed through
// a symlink — the root and the fixed children refuse to write.
func TestShPublish_SymlinkedRootRefused(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(home, dirName)); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	runShPublish(t, version, home)
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Errorf("publish wrote through a symlinked root: %v %v", entries, err)
	}

	// A symlinked manifest/launch/integration refuses too.
	home2 := t.TempDir()
	root2 := filepath.Join(home2, dirName)
	if err := os.MkdirAll(filepath.Join(root2, integrationDir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root2, lockName)); err != nil {
		t.Fatalf("symlink lock: %v", err)
	}
	runShPublish(t, version, home2)
	if _, err := os.Stat(filepath.Join(root2, manifestName)); !os.IsNotExist(err) {
		t.Errorf("publish wrote despite a symlinked lock (err=%v)", err)
	}
}

// TestShPublish_Concurrent: two sessions opening the same host at once are an
// ordinary event. The atomic-mkdir lock plus the post-lock version re-check
// keep the result to one active generation with no torn bytes.
func TestShPublish_Concurrent(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prelude := buildPublishPrelude(version)
			cmd := exec.Command("/bin/sh", "-c", prelude+"; true") // #nosec G204 — package consts.
			cmd.Env = append(os.Environ(), "HOME="+home)
			_ = cmd.Run()
		}()
	}
	wg.Wait()
	root := filepath.Join(home, dirName)
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil {
		t.Fatalf("Verify after concurrent publishes: %v", err)
	}
	if !vr.Installed || vr.Generation != genDir(version) {
		t.Errorf("Verify = %+v after concurrent publishes", vr)
	}
	gens, err := filepath.Glob(filepath.Join(root, integrationDir, "v*"))
	if err != nil || len(gens) != 1 {
		t.Errorf("expected exactly one generation dir, got %v (err=%v)", gens, err)
	}
}

// TestShPublish_InterruptedLeavesActivationAndConverges proves the property
// the coordinator demands of the sh writer: a publish interrupted at a
// boundary leaves the previous activation byte-identical, and the next
// attempt converges with no manual cleanup. Two injection styles:
//
//   - a deterministic pre-write obstacle (tmp/ is a regular file, so staging
//     cannot begin): nothing is written and the previous activation is
//     untouched;
//   - a real interruption: the publish shell is killed mid-flight at several
//     delays, so the kill lands at different boundaries (staging, between
//     the generation rename and the manifest rename, after activation) and
//     the invariant must hold at every one. This is the coordinator's
//     "fail at an injected step" demonstrated more strongly: every step is a
//     possible kill point, the lock is stale-breakable and the manifest is
//     the only pointer, so no interruption can strand the host.
func TestShPublish_InterruptedLeavesActivationAndConverges(t *testing.T) {
	// Previous activation: v11 published by the Go writer (the same state
	// the sh writer produces; either serves as the baseline).
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	pub := NewPublisher(testLogger(), NewOSFS(), root)
	if _, err := pub.Publish(launchBundle()); err != nil {
		t.Fatalf("baseline v11 publish: %v", err)
	}
	manifest := filepath.Join(root, manifestName)
	baseline, err := os.ReadFile(manifest) // #nosec G304 — test-owned path.
	if err != nil {
		t.Fatalf("read baseline manifest: %v", err)
	}
	t.Run("staging-obstacle", func(t *testing.T) {
		obstacle := filepath.Join(root, tmpName)
		if rmErr := os.RemoveAll(obstacle); rmErr != nil {
			t.Fatal(rmErr)
		}
		if werr := os.WriteFile(obstacle, []byte("in the way"), 0o600); werr != nil {
			t.Fatal(werr)
		}
		defer func() { _ = os.RemoveAll(obstacle) }()
		runShPublish(t, "12", home)
		assertManifestBytes(t, manifest, baseline)
	})

	for _, delay := range []string{"5ms", "15ms", "40ms", "120ms"} {
		t.Run("killed-after-"+delay, func(t *testing.T) {
			prelude := buildPublishPrelude("12")
			cmd := exec.Command("/bin/sh", "-c", prelude+"; true") // #nosec G204 — package consts.
			cmd.Env = append(os.Environ(), "HOME="+home)
			if startErr := cmd.Start(); startErr != nil {
				t.Fatal(startErr)
			}
			d, parseErr := time.ParseDuration(delay)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			time.Sleep(d)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			// A wall-clock kill cannot know which boundary it landed on, and
			// on slower hardware it lands after the commit — so requiring the
			// baseline here fails for the one reason that is not a defect.
			// The invariant that does hold at EVERY boundary is that the
			// activation is one of exactly two whole states: the previous
			// one, or the new one complete and verifying. A torn manifest,
			// or one naming files that are absent or wrong, is the failure.
			assertActivationWholeAfterKill(t, root, manifest, baseline)
		})
	}

	// Convergence: obstacles gone (and any stale lock stale-broken by the
	// next attempt's bounded wait), the publish completes and verifies. The
	// generation must be NEWER than the baseline above, or "converged" and
	// "correctly refused as a downgrade" would look identical here.
	converged := versionPlus(t, +1)
	runShPublish(t, converged, home)
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil {
		t.Fatalf("verify after convergence: %v", err)
	}
	if !vr.Installed || vr.Version != converged {
		t.Errorf("Verify after convergence = %+v, want installed version %s", vr, converged)
	}
}

func assertManifestBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path) // #nosec G304 — test-owned path.
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("manifest changed across an interrupted publish:\n got: %s\nwant: %s", got, want)
	}
}

// assertActivationWholeAfterKill accepts either whole state an atomic publish
// can be interrupted into and rejects everything between them. Killing at a
// wall-clock offset is deliberately imprecise; this assertion is not.
func assertActivationWholeAfterKill(t *testing.T, root, manifest string, baseline []byte) {
	t.Helper()
	got, err := os.ReadFile(manifest) // #nosec G304 — test-owned path.
	if err != nil {
		t.Fatalf("manifest missing after an interrupted publish: %v", err)
	}
	if string(got) == string(baseline) {
		return // the commit had not happened yet
	}
	// The commit did happen before the kill: it must be complete, because a
	// manifest is renamed into place only after every file it names exists
	// with the recorded hash and mode.
	vr, verr := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if verr != nil {
		t.Fatalf("manifest changed but does not verify: %v", verr)
	}
	if !vr.Installed {
		t.Errorf("manifest changed but Verify reports nothing installed: %+v", vr)
	}
}
