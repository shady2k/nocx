package update

// Managed debris with no journal, resolved rather than refused
// (nocx-vh2hr.1.14).
//
// A successful update leaves .nocx-backup.app behind on purpose, and a crash
// can leave .nocx-swap.app behind with no journal at all: ReportHealthy logs a
// failed swap->backup rename and deletes the journal anyway. The old rule
// refused the messages those two files produce at startup and before every
// apply, so a person whose first update failed could never update again and
// was told only that "the update could not finish".
//
// The rule these tests hold: debris nocx can PROVE is its own is resolved by
// reconcile itself — removed when it is not the running install and carries
// nothing that install lacks — and only a bundle that is not nocx's is
// refused, with the path to remove it and the step to take.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// debrisUpdater wires an updater whose install is a minimal AppImage and whose
// platform answers the facts each case needs.
func debrisUpdater(t *testing.T, dir string, p *fakePlatform) (Updater, string) {
	t.Helper()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	return NewUpdater(UpdaterConfig{
		Platform:       p,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
		StateDir:       filepath.Join(dir, "state"),
	}), installPath
}

// resolveDebris runs the startup reconcile and fails the test on any error.
func resolveDebris(t *testing.T, u Updater) {
	t.Helper()
	if err := u.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile refused nocx's own debris: %v", err)
	}
}

func mustBeGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s was not removed (stat error %v)", path, err)
	}
}

func mustRemain(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s should still exist: %v", path, err)
	}
}

func TestReconcile_NoRecord_SwapOnlyIsResolved(t *testing.T) {
	dir := t.TempDir()
	p := newFakePlatform()
	u, installPath := debrisUpdater(t, dir, p)

	swap := swapPath(installPath)
	makeMinimalAppImage(t, swap)

	resolveDebris(t, u)

	mustBeGone(t, swap)
	mustRemain(t, installPath)
}

func TestReconcile_NoRecord_BackupOnlyIsResolved(t *testing.T) {
	dir := t.TempDir()
	p := newFakePlatform()
	u, installPath := debrisUpdater(t, dir, p)

	backup := backupPath(installPath)
	makeMinimalAppImage(t, backup)

	resolveDebris(t, u)

	mustBeGone(t, backup)
	mustRemain(t, installPath)
}

func TestReconcile_NoRecord_BothAreResolved(t *testing.T) {
	dir := t.TempDir()
	p := newFakePlatform()
	u, installPath := debrisUpdater(t, dir, p)

	swap, backup := swapPath(installPath), backupPath(installPath)
	makeMinimalAppImage(t, swap)
	makeMinimalAppImage(t, backup)

	resolveDebris(t, u)

	mustBeGone(t, swap)
	mustBeGone(t, backup)
	mustRemain(t, installPath)
}

// TestReconcile_NoRecord_BackupNewerThanInstallIsKept: a copy of a NEWER nocx
// than the one running carries something the install lacks, so reconcile may
// not delete it — it is not debris, it is the only copy of that release.
func TestReconcile_NoRecord_BackupNewerThanInstallIsKept(t *testing.T) {
	dir := t.TempDir()
	p := newFakePlatform()
	u, installPath := debrisUpdater(t, dir, p)

	backup := backupPath(installPath)
	makeMinimalAppImage(t, backup)
	p.bundles = map[string]BundleReport{
		backup: {Nocx: true, Version: "0.3.0"},
	}

	resolveDebris(t, u)

	mustRemain(t, backup)
}

// TestReconcile_NoRecord_ForeignBundleIsRefused: the name is ours, the bundle
// at it is not. Deleting it would destroy somebody else's application.
func TestReconcile_NoRecord_ForeignBundleIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := newFakePlatform()
	u, installPath := debrisUpdater(t, dir, p)

	backup := backupPath(installPath)
	makeMinimalAppImage(t, backup)
	p.bundles = map[string]BundleReport{backup: {Nocx: false}}

	err := u.Reconcile(context.Background())
	if err == nil {
		t.Fatal("reconcile accepted a foreign bundle at a managed name")
	}
	if !strings.Contains(err.Error(), backup) {
		t.Errorf("the refusal does not name the blocking path %s: %v", backup, err)
	}
	if !strings.Contains(err.Error(), "not nocx") {
		t.Errorf("the refusal does not say the bundle is not nocx's: %v", err)
	}
	if !strings.Contains(err.Error(), "move") && !strings.Contains(err.Error(), "remove") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	mustRemain(t, backup)
}

// TestReconcile_NoRecord_NeverRemovesTheRunningInstall: device+inode is the
// only identity that survives a copy, and it is what says "this is the app".
func TestReconcile_NoRecord_NeverRemovesTheRunningInstall(t *testing.T) {
	dir := t.TempDir()
	p := newFakePlatform()
	u, installPath := debrisUpdater(t, dir, p)

	// A second name for the running install: same device and inode.
	backup := backupPath(installPath)
	if err := os.Link(installPath, backup); err != nil {
		t.Fatalf("hard link the running install: %v", err)
	}

	resolveDebris(t, u)

	mustRemain(t, installPath)
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("reconcile removed a name of the running install: %v", err)
	}
}

// TestUpdater_Apply_ResolvesStaleDebrisBeforeStarting is the path the person
// actually takes: install, apply. The stale backup an earlier update left must
// be settled by Apply's own reconcile, not refuse the apply.
func TestUpdater_Apply_ResolvesStaleDebrisBeforeStarting(t *testing.T) {
	useStampedTestBuild(t)
	dir := t.TempDir()
	ctx := context.Background()

	fixtureURL, fixtureSHA256, fixtureSize, srv := serveAppImageFixture(t)
	defer srv.Close()

	pubs, priv := makeTestKeyring(1)
	arch := newFakePlatform().ArtifactID()
	manifestBody, manifestSig := newTestManifest("0.2.0", []Artifact{{
		OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
		URL: fixtureURL, SHA256: fixtureSHA256, Size: fixtureSize,
	}}, priv)

	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	backup := backupPath(installPath)
	makeMinimalAppImage(t, backup)

	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{body: manifestBody, sig: manifestSig},
		Keyring:        pubs,
		CurrentVersion: "0.1.0", InstallPath: installPath, StateDir: filepath.Join(dir, "state"),
	})

	info, err := u.Check(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if applyErr := u.Apply(ctx, info); applyErr != nil {
		t.Fatalf("Apply refused to run past the debris an earlier update left: %v", applyErr)
	}

	// The stale copy is settled, and the exchange has happened around it.
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Errorf("the stale backup survived Apply (stat error %v)", statErr)
	}
	newID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}
	swapID, err := statBundleID(swapPath(installPath))
	if err != nil {
		t.Fatal(err)
	}
	if swapID.isZero() || swapID.equal(newID) {
		t.Error("the exchange did not happen: the swap peer does not hold the previous bundle")
	}
}
