package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/version"
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
	useStampedTestBuild(t)
	dir := t.TempDir()
	installPath = filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	newID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	swap := swapPath(installPath)
	makeMinimalAppImage(t, swap)

	jp = testJournalPath(t, installPath)
	rec := &journalRecord{
		TxID:           to,
		InstallPath:    installPath,
		OldBundleID:    bundleID{Dev: 999, Ino: 999},
		NewBundleID:    newID,
		FromVersion:    from,
		ToVersion:      to,
		ToBuild:        version.Identity(),
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

// The ADR-0101 held-pair safeguard remains for already attached windows.
// New launchers take over across Version differences (ADR-0104), but a
// health report from a pre-existing pair must still preserve rollback until
// the installed coordinator runs. This is updater acceptance, not permission
// for the current launcher to reuse an older Version.
func TestReportHealthy_OldCompatibleCoordinator_HoldsTheUpdateWithoutRollback(t *testing.T) {
	ctx := context.Background()
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	kept := &stubProbe{build: CoordinatorBuild{Version: "0.1.0", Commit: "oldsha", Compatible: true}}
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: kept,
	})

	for launch := 1; launch <= 5; launch++ {
		if rerr := u.Reconcile(ctx); rerr != nil {
			t.Fatalf("Reconcile %d: %v", launch, rerr)
		}
		err := u.ReportHealthy(ctx)
		if !errors.Is(err, ErrPairHeld) {
			t.Fatalf("launch %d: a healthy window on a kept older coordinator must report the pair as held, got: %v", launch, err)
		}
		if _, serr := os.Stat(jp); serr != nil {
			t.Fatalf("launch %d: the pending journal vanished without the update finishing: %v", launch, serr)
		}
	}

	// The hold is a fact the window can tell a person about, and it names
	// the coordinator that holds it.
	state := u.State(ctx)
	if !state.Pending || !state.HeldByOlderCoordinator {
		t.Fatalf("State = %+v, want a pending update held by an older coordinator", state)
	}
	if state.Version != "0.2.0" {
		t.Errorf("State.Version = %q, want 0.2.0", state.Version)
	}

	// And nothing was finalised: the swap peer still holds the rollback copy.
	if _, err := os.Stat(swapPath(installPath)); err != nil {
		t.Errorf("the swap peer holds the rollback copy and must survive a held pair: %v", err)
	}
}

// TestReportHealthy_NoHealthReport_RollsBackAfterThreeLaunches keeps the
// other half of the policy: the held state suspends rollback because the
// reporting UI proved itself healthy. A build whose renderer cannot report
// at all — the genuinely unhealthy new build this gate exists for — still
// rolls back on the third launch, held record or none.
func TestReportHealthy_NoHealthReport_RollsBackAfterThreeLaunches(t *testing.T) {
	ctx := context.Background()
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")
	swapID, err := statBundleID(swapPath(installPath))
	if err != nil {
		t.Fatal(err)
	}

	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), // A coordinator probe exists and would answer; the build never
		// gets far enough to report health through it.
		Coordinator: &stubProbe{build: CoordinatorBuild{Version: "0.2.0"}},
	})

	// pendingUpdate starts at LaunchAttempts=1. The next launch reaches
	// attempt two and survives; the following one reaches three and rolls
	// back. No health report occurs between them.
	if rerr := u.Reconcile(ctx); rerr != nil {
		t.Fatalf("second Reconcile: %v", rerr)
	}
	if _, serr := os.Stat(jp); serr != nil {
		t.Fatalf("second launch rolled back early: %v", serr)
	}
	if rerr := u.Reconcile(ctx); rerr != nil {
		t.Fatalf("third Reconcile: %v", rerr)
	}
	if _, serr := os.Stat(jp); !os.IsNotExist(serr) {
		t.Fatal("the third launch without a health report did not roll the update back")
	}
	rolledBack, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if !rolledBack.equal(swapID) {
		t.Errorf("auto-rollback did not restore the old bundle: install=%v, want %v", rolledBack, swapID)
	}
	if state := u.State(ctx); state.Pending {
		t.Errorf("State after rollback = %+v, want nothing in flight", state)
	}
}

// An incompatible attached pair remains a mismatch (ADR-0104): preserved
// legacy held-pair handling never certifies a peer with the wrong protocol.
func TestReportHealthy_AnIncompatibleCoordinator_IsStillAMismatch(t *testing.T) {
	ctx := context.Background()
	installPath, jp := pendingUpdate(t, "0.1.0", "0.2.0")

	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: &stubProbe{build: CoordinatorBuild{Version: "0.1.0", Compatible: false}},
	})

	err := u.ReportHealthy(ctx)
	if !errors.Is(err, ErrPairMismatch) {
		t.Fatalf("an incompatible coordinator answering must stay a mismatch, got: %v", err)
	}
	if errors.Is(err, ErrPairHeld) {
		t.Error("a mismatch must not double as a held pair")
	}
	assertNotFinalised(t, installPath, jp)

	// And the counter is not reset by it: three launches still roll back.
	// The pending fixture starts at attempt one. A mismatch does not
	// reset the counter, so the next launch survives at two and the
	// following launch reaches three and rolls back.
	if rerr := u.Reconcile(ctx); rerr != nil {
		t.Fatalf("second Reconcile: %v", rerr)
	}
	_ = u.ReportHealthy(ctx)
	if _, serr := os.Stat(jp); serr != nil {
		t.Fatalf("an incompatible report must not suspend rollback before attempt three: %v", serr)
	}
	if rerr := u.Reconcile(ctx); rerr != nil {
		t.Fatalf("third Reconcile: %v", rerr)
	}
	if _, serr := os.Stat(jp); !os.IsNotExist(serr) {
		t.Error("an incompatible pair must not suspend the rollback")
	}
}

// TestStateWithNothingInFlight: the ordinary launch — no journal, no
// transaction — must answer an empty state, not a guess.
func TestStateWithNothingInFlight(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"),
	})

	if state := u.State(context.Background()); state.Pending {
		t.Fatalf("State with no transaction = %+v, want nothing in flight", state)
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
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: probe,
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
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: &stubProbe{build: CoordinatorBuild{Version: "0.3.0"}},
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
		CurrentVersion: "0.1.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: probe,
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
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"),
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
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: &stubProbe{err: boom},
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
		CurrentVersion: "0.2.0", InstallPath: installPath, StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: probe,
	})

	if err := u.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("ReportHealthy with no transaction in flight must be harmless: %v", err)
	}
	if probe.calls != 0 {
		t.Errorf("the coordinator was asked %d times with no update in flight", probe.calls)
	}
}

// A nightly transaction must be certified by the window it installed, not an
// older window whose semver happens to match. ADR-0104 still permits that new
// window to keep the compatible same-version coordinator.
func TestReportHealthy_SameVersionInstalledIdentity(t *testing.T) {
	oldStamp := version.Identity()
	t.Cleanup(func() { version.Commit, version.Date = oldStamp.Commit, oldStamp.BuiltAt })
	older := version.BuildIdentity{Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BuiltAt: "2026-10-27T10:00:00Z"}
	installed := version.BuildIdentity{Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", BuiltAt: "2026-10-28T10:00:00Z"}
	for _, tc := range []struct {
		name         string
		reporter     version.BuildIdentity
		wantMismatch bool
	}{
		{"older pair refused", older, true},
		{"installed commit with older build time refused", version.BuildIdentity{Commit: installed.Commit, BuiltAt: older.BuiltAt}, true},
		{"older commit with installed build time refused", version.BuildIdentity{Commit: older.Commit, BuiltAt: installed.BuiltAt}, true},
		{"installed window keeps compatible older coordinator", installed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version.Commit, version.Date = older.Commit, older.BuiltAt
			installPath := filepath.Join(t.TempDir(), "nocx.AppImage")
			makeMinimalAppImage(t, installPath)
			fixtureURL, sha, size, artifact := serveAppImageFixture(t)
			defer artifact.Close()
			private := channelTestKey(t)
			body, err := json.Marshal(Manifest{
				Version: "0.8.0", Build: installed,
				Artifacts: []Artifact{{OS: "linux", Arch: "amd64", Format: "AppImage", URL: fixtureURL, SHA256: sha, Size: size}},
			})
			if err != nil {
				t.Fatal(err)
			}
			relay := signedChannelRelay(t, body, private)
			fetch := NewGitHubManifestFetcher(nil)
			fetch.NightlyBaseURL = relay.URL
			fetch.SetChannel(ChannelNightly)
			probe := &stubProbe{build: CoordinatorBuild{Version: "0.8.0", Commit: older.Commit, Compatible: true}}
			ctx, logger := logtest.New(t)
			cfg := UpdaterConfig{
				Platform: newFakePlatform(), Fetcher: fetch,
				Keyring:        []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])},
				CurrentVersion: "0.8.0", InstallPath: installPath,
				StateDir: filepath.Join(filepath.Dir(installPath), "state"), Coordinator: probe, Logger: logger,
			}
			applying := NewUpdater(cfg)
			info, err := applying.Check(ctx)
			if err != nil || info == nil {
				t.Fatalf("Check = %v, %v; want a later nightly", info, err)
			}
			if err = applying.Apply(ctx, info); err != nil {
				t.Fatal(err)
			}
			jp := journalPath(cfg.StateDir)
			rec, err := readJournal(jp)
			if err != nil || rec == nil {
				t.Fatalf("journal after Apply: %+v, %v", rec, err)
			}
			if rec.ToBuild != installed {
				t.Fatalf("journal target = %+v, want signed identity %+v", rec.ToBuild, installed)
			}

			// The constructor captures the running binary's link-time identity.
			// Neither swapping the file nor another window changes that fact.
			version.Commit, version.Date = tc.reporter.Commit, tc.reporter.BuiltAt
			reporting := NewUpdater(cfg)
			if err = reporting.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(jp) //nolint:gosec // journal path is inside t.TempDir.
			if err != nil {
				t.Fatal(err)
			}
			err = reporting.ReportHealthy(ctx)
			if tc.wantMismatch {
				if !errors.Is(err, ErrPairMismatch) {
					t.Fatalf("same-version older window must not certify the installed build, got %v", err)
				}
				assertNotFinalised(t, installPath, jp)
				after, readErr := os.ReadFile(jp) //nolint:gosec // journal path is inside t.TempDir.
				if readErr != nil || string(before) != string(after) {
					t.Fatalf("a stale report changed the rollback counter or hold: %s, %v", after, readErr)
				}
				if probe.calls != 0 {
					t.Fatal("wrong reporting identity must be refused before asking the coordinator")
				}
				return
			}
			if err != nil {
				t.Fatalf("ADR-0104: installed window with kept same-version coordinator must certify: %v", err)
			}
			if probe.calls != 1 {
				t.Fatalf("coordinator calls = %d, want 1", probe.calls)
			}
			if _, err := os.Stat(jp); !os.IsNotExist(err) {
				t.Fatalf("certified journal survived: %v", err)
			}
			if _, err := os.Stat(backupPath(installPath)); err != nil {
				t.Fatalf("rollback copy not promoted to backup: %v", err)
			}
		})
	}
}

func TestReportHealthy_MissingInstalledIdentityCannotCertify(t *testing.T) {
	for _, target := range []version.BuildIdentity{
		{},
		{Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{BuiltAt: "2026-10-28T10:00:00Z"},
		{Commit: "none", BuiltAt: "unknown"},
	} {
		t.Run(target.Commit+target.BuiltAt, func(t *testing.T) {
			installPath, jp := pendingUpdate(t, "0.7.0", "0.8.0")
			// Write the persisted shape rather than a new Go field, so the
			// no-identity case includes journals written by older binaries.
			raw, err := os.ReadFile(jp) //nolint:gosec // journal path is inside t.TempDir.
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err = json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if target == (version.BuildIdentity{}) {
				delete(record, "toBuild") // a journal written before build identity was recorded
			} else {
				record["toBuild"] = target
			}
			raw, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(jp, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, logger := logtest.New(t)
			u := NewUpdater(UpdaterConfig{
				Platform: newFakePlatform(), CurrentVersion: "0.8.0", InstallPath: installPath, StateDir: filepath.Dir(jp),
				Coordinator: &stubProbe{build: CoordinatorBuild{Version: "0.8.0", Compatible: true}}, Logger: logger,
			})
			if err := u.ReportHealthy(ctx); !errors.Is(err, ErrPairUnverifiable) {
				t.Fatalf("an absent or incomplete installed identity must not certify: %v", err)
			}
			assertNotFinalised(t, installPath, jp)
			// A legacy journal can still reconcile and roll back. Refusing
			// identity proof must not strand the installed transaction.
			for launch := 0; launch < 2; launch++ {
				if err := u.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(jp); !os.IsNotExist(err) {
				t.Fatalf("unverifiable build did not roll back: %v", err)
			}
		})
	}
}
