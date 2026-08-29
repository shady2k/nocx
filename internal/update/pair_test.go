package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubProbe is a [CoordinatorProbe] whose answer the test dictates —
// including the answer "I cannot tell you", which is a different outcome
// from a mismatch and has to be asserted separately.
type stubProbe struct {
	build CoordinatorBuild
	err   error
	calls int
}

func (p *stubProbe) AnsweringCoordinator(context.Context) (CoordinatorBuild, error) {
	p.calls++
	if p.err != nil {
		return CoordinatorBuild{}, p.err
	}
	return p.build, nil
}

// pendingUpdate lays out the state a machine is in immediately after a
// successful Apply and a restart: the install path holds the new bundle,
// the swap peer holds the old one, and the journal is still pending
// because nothing has certified the new version yet.
//
// Returned: the install path and the journal path.
func pendingUpdate(t *testing.T, from, to string) (installPath, jp string) {
	t.Helper()
	dir := t.TempDir()
	installPath = filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	newID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	swap := swapPath(installPath)
	makeMinimalAppImage(t, swap)

	jp = journalPath(installPath)
	rec := &journalRecord{
		TxID:           to,
		InstallPath:    installPath,
		OldBundleID:    bundleID{Dev: 999, Ino: 999},
		NewBundleID:    newID,
		FromVersion:    from,
		ToVersion:      to,
		ArtifactSHA256: "abc",
		LaunchAttempts: 1,
	}
	if err := writeJournal(jp, rec); err != nil {
		t.Fatal(err)
	}
	return installPath, jp
}

// assertNotFinalised is the whole point of every refusal test: the
// rollback journal survives and no finalisation step ran. Asserting only
// that ReportHealthy returned an error would pass against an
// implementation that refused AFTER deleting the journal.
func assertNotFinalised(t *testing.T, installPath, jp string) {
	t.Helper()
	if _, err := os.Stat(jp); err != nil {
		t.Fatalf("the rollback journal must survive a refused health report: %v", err)
	}
	if _, err := os.Stat(swapPath(installPath)); err != nil {
		t.Errorf("the swap peer holds the rollback copy and must survive: %v", err)
	}
	if _, err := os.Stat(backupPath(installPath)); !os.IsNotExist(err) {
		t.Error("finalisation ran: swap was promoted to backup after a refused health report")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 1 — the sequence, end to end
// ---------------------------------------------------------------------------

// TestReportHealthy_OldCoordinatorAnswers_RollbackStaysArmed drives the
// defect: the bundle is swapped, the OLD coordinator survives it, and the
// new UI connects and tries to certify the update. The journal must
// survive and finalisation must not happen — and because it does, the
// launch counter goes on running and the third unhealthy launch rolls
// back, which is the behaviour the mixed pair used to disarm.
func TestReportHealthy_OldCoordinatorAnswers_RollbackStaysArmed(t *testing.T) {
	ctx := context.Background()
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")
	newID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	// The restarted app IS 0.2.0 — the UI half of the pair is correct.
	// The coordinator that survived the swap is still 0.1.0.
	oldDaemon := &stubProbe{build: CoordinatorBuild{Version: "0.1.0", Commit: "oldsha"}}
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
		Coordinator:    oldDaemon,
	})

	err = u.ReportHealthy(ctx)
	if err == nil {
		t.Fatal("ReportHealthy certified a mixed pair — 0.2.0 UI against a 0.1.0 coordinator")
	}
	if !errors.Is(err, ErrPairMismatch) {
		t.Errorf("error must identify the mismatch, got: %v", err)
	}
	if oldDaemon.calls != 1 {
		t.Errorf("the coordinator was asked %d times, want exactly 1", oldDaemon.calls)
	}
	assertNotFinalised(t, installPath, jp)

	// Rollback is still armed: two more launches with no health report
	// and reconciliation reverts to the old bundle.
	swapID, err := statBundleID(swapPath(installPath))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if rerr := u.Reconcile(ctx); rerr != nil {
			t.Fatalf("Reconcile %d: %v", i, rerr)
		}
	}
	if _, serr := os.Stat(jp); !os.IsNotExist(serr) {
		t.Error("journal survives after auto-rollback completed")
	}
	rolledBack, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if !rolledBack.equal(swapID) {
		t.Errorf("auto-rollback did not restore the old bundle: install=%v, want %v", rolledBack, swapID)
	}
	if rolledBack.equal(newID) {
		t.Error("install still holds the uncertified new bundle")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 2 — healthy only for the installed version
// ---------------------------------------------------------------------------

func TestReportHealthy_MatchingPair_Finalises(t *testing.T) {
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	probe := &stubProbe{build: CoordinatorBuild{Version: "0.2.0", Commit: "newsha"}}
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
		Coordinator:    probe,
	})

	if err := u.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("a matching pair must certify the update: %v", err)
	}
	if _, err := os.Stat(jp); !os.IsNotExist(err) {
		t.Error("journal not deleted after a certified health report")
	}
	if _, err := os.Stat(backupPath(installPath)); err != nil {
		t.Errorf("swap was not promoted to backup: %v", err)
	}
}

// TestReportHealthy_NewerCoordinator_IsAlsoRefused: the check is equality
// with the installed version, not "at least". A coordinator ahead of the
// bundle is as much a mixed pair as one behind it, and reading it as
// "close enough" is how a version skew becomes permanent.
func TestReportHealthy_NewerCoordinator_IsAlsoRefused(t *testing.T) {
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
		Coordinator:    &stubProbe{build: CoordinatorBuild{Version: "0.3.0"}},
	})

	if err := u.ReportHealthy(context.Background()); !errors.Is(err, ErrPairMismatch) {
		t.Fatalf("a 0.3.0 coordinator against a 0.2.0 install must be refused, got: %v", err)
	}
	assertNotFinalised(t, installPath, jp)
}

// TestReportHealthy_PreRestartBuildCannotCertifyItself covers the UI half.
// The process that called Apply is still the OLD binary running from the
// swapped-out inode, and the install path already holds the new identity
// the moment Apply returns — so without this check that process could
// finalise its own update before a single line of the new build had run.
func TestReportHealthy_PreRestartBuildCannotCertifyItself(t *testing.T) {
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	probe := &stubProbe{build: CoordinatorBuild{Version: "0.1.0"}}
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.1.0", // the build that applied the update
		InstallPath:    installPath,
		Coordinator:    probe,
	})

	err := u.ReportHealthy(context.Background())
	if !errors.Is(err, ErrPairMismatch) {
		t.Fatalf("the pre-restart build must not certify its own update, got: %v", err)
	}
	if probe.calls != 0 {
		t.Error("the coordinator should not even be asked when the reporting build is wrong")
	}
	assertNotFinalised(t, installPath, jp)
}

// ---------------------------------------------------------------------------
// Failure paths — every external call, each paired with its success above
// ---------------------------------------------------------------------------

// TestReportHealthy_NoProbe_Refuses: an updater nobody told how to
// identify the backend cannot certify a pair. Fail closed — a nil field
// read as "there is no daemon, so it must be fine" is exactly the
// unstated assumption that produced the defect.
func TestReportHealthy_NoProbe_Refuses(t *testing.T) {
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
		// Coordinator deliberately absent.
	})

	err := u.ReportHealthy(context.Background())
	if !errors.Is(err, ErrPairUnverifiable) {
		t.Fatalf("an updater with no coordinator probe must refuse, got: %v", err)
	}
	assertNotFinalised(t, installPath, jp)
}

// TestReportHealthy_ProbeFails_Refuses: the probe reaches a socket, and a
// socket can be dead. Unverifiable is not mismatched, and the message has
// to carry the underlying cause so a user can act on it.
func TestReportHealthy_ProbeFails_Refuses(t *testing.T) {
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	boom := errors.New("dial coordinator socket: connection refused")
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
		Coordinator:    &stubProbe{err: boom},
	})

	err := u.ReportHealthy(context.Background())
	if !errors.Is(err, ErrPairUnverifiable) {
		t.Fatalf("a probe that fails must refuse as unverifiable, got: %v", err)
	}
	if errors.Is(err, ErrPairMismatch) {
		t.Error("a failed probe must not be reported as a version mismatch")
	}
	if !errors.Is(err, boom) {
		t.Error("the underlying probe failure must survive in the error chain")
	}
	assertNotFinalised(t, installPath, jp)
}

// TestReportHealthy_NoTransaction_AsksNothing: with no journal there is
// nothing to certify, so the probe is never called. A health report on an
// ordinary launch must not reach for a socket.
func TestReportHealthy_NoTransaction_AsksNothing(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	probe := &stubProbe{build: CoordinatorBuild{Version: "0.2.0"}}
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
		Coordinator:    probe,
	})

	if err := u.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("ReportHealthy with no transaction in flight must be harmless: %v", err)
	}
	if probe.calls != 0 {
		t.Errorf("the coordinator was asked %d times with no update in flight", probe.calls)
	}
}
