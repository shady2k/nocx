package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/version"
)

// ---------------------------------------------------------------------------
// The launch count across a build change (nocx-vh2hr.1.20)
// ---------------------------------------------------------------------------

// twoNightlyBuilds is the pair a nightly update joins: the build that applies
// the update and the signed candidate it installs. They share a version, as
// consecutive nightlies do, so only the build identity can tell them apart.
func twoNightlyBuilds() (applier, installed version.BuildIdentity) {
	applier = version.BuildIdentity{
		Commit:  strings.Repeat("a", 40),
		BuiltAt: "2026-11-05T13:08:00Z",
	}
	installed = version.BuildIdentity{
		Commit:  strings.Repeat("b", 40),
		BuiltAt: "2026-11-06T07:49:00Z",
	}
	return applier, installed
}

// setProcessBuild makes this process report build as version.Identity(), the
// link-time stamps a real binary carries. It is what makes one test able to be
// the applier and then the restarted candidate.
func setProcessBuild(t *testing.T, build version.BuildIdentity) {
	t.Helper()
	previous := version.Identity()
	version.Commit, version.Date = build.Commit, build.BuiltAt
	t.Cleanup(func() { version.Commit, version.Date = previous.Commit, previous.BuiltAt })
}

// appliedUpdate is the world after the real Check→Apply: the candidate's bytes
// are installed, the previous bundle sits at the swap peer, and the journal is
// pending health confirmation.
type appliedUpdate struct {
	installPath string
	journal     string
	stateDir    string
	previous    string // sha256 of the bundle that was installed before Apply
	installed   string // sha256 of the candidate's bytes
}

// applyNightlyAcrossBuilds drives the seam the window drives: the applier's
// stamps are the running binary's, the manifest offers installed as a later
// nightly of the same version.
func applyNightlyAcrossBuilds(t *testing.T, applier, installed version.BuildIdentity) appliedUpdate {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	stateDir := filepath.Join(dir, "state")
	previous, err := digestOf(installPath)
	if err != nil {
		t.Fatal(err)
	}

	fixtureURL, fixtureSHA, fixtureSize, artifact := serveAppImageFixture(t)
	t.Cleanup(artifact.Close)
	private := channelTestKey(t)
	body, err := json.Marshal(Manifest{
		Version: "0.8.1",
		Build:   installed,
		Artifacts: []Artifact{{
			OS: "linux", Arch: "amd64", Format: "AppImage",
			URL: fixtureURL, SHA256: fixtureSHA, Size: fixtureSize,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	relay := signedChannelRelay(t, body, private)
	fetch := NewGitHubManifestFetcher(nil)
	fetch.NightlyBaseURL = relay.URL
	fetch.SetChannel(ChannelNightly)

	setProcessBuild(t, applier)
	applying := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        fetch,
		Keyring:        []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])},
		CurrentVersion: "0.8.1",
		InstallPath:    installPath,
		StateDir:       stateDir,
	})
	info, err := applying.Check(ctx)
	if err != nil || info == nil {
		t.Fatalf("Check = %v, %v; want the later nightly offered", info, err)
	}
	if err = applying.Apply(ctx, info); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	installedBytes, err := digestOf(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if installedBytes == previous {
		t.Fatal("the exchange did not replace the installed bytes")
	}
	return appliedUpdate{
		installPath: installPath,
		journal:     journalPath(stateDir),
		stateDir:    stateDir,
		previous:    previous,
		installed:   installedBytes,
	}
}

func digestOf(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// journalDocument is the journal as it sits on disk, so a test can write a
// record a build OTHER than this one would have left.
func journalDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // journal path is inside t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func writeJournalDocument(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // journal path is inside t.TempDir.
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // path is inside t.TempDir.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// TestReconcile_AnOldBuildsSpentLaunchesAreNotTheCandidates is the nightly of
// 2026-11-06 (run 42183378724): the build being replaced reached its startup
// reconcile twice in ONE process, because GetUpdateState reconciled before
// reading the state (nocx-vh2hr.1.19 removed that from the new code, and only
// from the new code). It quit for the restart with launchAttempts=2 in the
// journal, and the candidate's first start made it three — a good update
// exchanged back for the previous build, with no crash and no health report.
//
// The old build cannot be changed; the candidate has to act on none of what a
// process running the old bundle wrote. This test lays down the journal that
// binary leaves, starts the candidate once, and requires no rollback and a
// finalisation.
func TestReconcile_AnOldBuildsSpentLaunchesAreNotTheCandidates(t *testing.T) {
	ctx := context.Background()
	applier, installed := twoNightlyBuilds()
	w := applyNightlyAcrossBuilds(t, applier, installed)

	// THE OLD BINARY'S FILE. Its Apply wrote a record with no attempt owner
	// (the field did not exist), and two state reads in the applying process
	// spent one launch each before it quit for the restart.
	doc := journalDocument(t, w.journal)
	delete(doc, "launchAttemptsOwnedBy")
	doc["launchAttempts"] = 2
	writeJournalDocument(t, w.journal, doc)

	// THE CANDIDATE'S FIRST START. Same version, different build: exactly the
	// pair a nightly update joins.
	setProcessBuild(t, installed)
	candidate := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		CurrentVersion: "0.8.1",
		InstallPath:    w.installPath,
		StateDir:       w.stateDir,
		Coordinator:    &stubProbe{build: CoordinatorBuild{Version: "0.8.1", Commit: installed.Commit}},
	})

	// Reading the state is not a launch, and a read must leave the record
	// exactly as it found it — the half nocx-vh2hr.1.19 fixed in the caller,
	// asserted here as the premise the count rests on.
	before := mustRead(t, w.journal)
	for read := 0; read < 3; read++ {
		candidate.State(ctx)
	}
	if string(mustRead(t, w.journal)) != string(before) {
		t.Fatal("reading the update state spent or rewrote something in the rollback journal")
	}

	if err := candidate.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile on the candidate's first start: %v", err)
	}
	rec, err := readJournal(w.journal)
	if err != nil || rec == nil {
		t.Fatalf("journal after the candidate's first start: %+v, %v; the transaction was rolled back", rec, err)
	}
	if rec.LaunchAttempts != 1 {
		t.Fatalf("the candidate's first start counted %d launches, want 1: the replaced build's two state reads were acted on", rec.LaunchAttempts)
	}
	if rec.LaunchAttemptsOwnedBy != installed {
		t.Errorf("the count is attributed to %+v, want the installed build %+v", rec.LaunchAttemptsOwnedBy, installed)
	}
	if _, err := os.Stat(swapPath(w.installPath)); err != nil {
		t.Fatalf("the rollback copy must survive a first start: %v", err)
	}
	if got := mustDigest(t, w.installPath); got != w.installed {
		t.Fatalf("the installed bytes changed on a first start: %s, want the candidate's %s", got, w.installed)
	}

	// THE REPLACED BUILD STILL HAS TO BE ABLE TO READ IT. Its reader refuses an
	// unknown schema version, and nothing about this change may give it one.
	if doc := journalDocument(t, w.journal); doc["schemaVersion"] != float64(journalSchemaVersion) {
		t.Fatalf("journal schema version = %v, want %d: an older binary refuses any other", doc["schemaVersion"], journalSchemaVersion)
	}

	if err := candidate.ReportHealthy(ctx); err != nil {
		t.Fatalf("ReportHealthy on a first start that spent nothing: %v", err)
	}
	if _, err := os.Stat(w.journal); !os.IsNotExist(err) {
		t.Fatal("the good update did not finalise")
	}
	if _, err := os.Stat(backupPath(w.installPath)); err != nil {
		t.Fatalf("finalisation did not retain the previous build as backup: %v", err)
	}
}

func mustDigest(t *testing.T, path string) string {
	t.Helper()
	sum, err := digestOf(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// TestReconcile_ThreeStartsOfTheInstalledBuildStillRollBack keeps what the
// counter is for: a candidate that really reaches startup three times and
// never certifies is exchanged back. Discarding another build's count must not
// discard this build's own.
func TestReconcile_ThreeStartsOfTheInstalledBuildStillRollBack(t *testing.T) {
	ctx := context.Background()
	applier, installed := twoNightlyBuilds()
	w := applyNightlyAcrossBuilds(t, applier, installed)

	setProcessBuild(t, installed)
	candidate := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		CurrentVersion: "0.8.1",
		InstallPath:    w.installPath,
		StateDir:       w.stateDir,
	})

	for launch := 1; launch <= 2; launch++ {
		if err := candidate.Reconcile(ctx); err != nil {
			t.Fatalf("start %d: %v", launch, err)
		}
		rec, err := readJournal(w.journal)
		if err != nil || rec == nil {
			t.Fatalf("start %d rolled back early: %+v, %v", launch, rec, err)
		}
		if rec.LaunchAttempts != launch {
			t.Fatalf("start %d: launchAttempts = %d", launch, rec.LaunchAttempts)
		}
	}
	if err := candidate.Reconcile(ctx); err != nil {
		t.Fatalf("third start: %v", err)
	}
	if _, err := os.Stat(w.journal); !os.IsNotExist(err) {
		t.Fatal("the third start without a health report did not roll the update back")
	}
	if got := mustDigest(t, w.installPath); got != w.previous {
		t.Fatalf("rollback restored %s, want the previous build's %s", got, w.previous)
	}
}

// TestReconcile_AProcessRunningAnotherBuildSpendsNothing states the rule the
// count now rests on: a launch attempt is a start of the build that was
// INSTALLED. The applying process is still alive when Apply returns, running
// from the swapped-out bundle, and a second copy of an older build can
// reconcile the same journal; neither is a start of the installed build, so
// neither spends an attempt — and, with two more healthy starts available to
// the real candidate, nothing rolls back on their word.
func TestReconcile_AProcessRunningAnotherBuildSpendsNothing(t *testing.T) {
	ctx := context.Background()
	applier, installed := twoNightlyBuilds()
	w := applyNightlyAcrossBuilds(t, applier, installed)

	setProcessBuild(t, applier)
	preRestart := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		CurrentVersion: "0.8.1",
		InstallPath:    w.installPath,
		StateDir:       w.stateDir,
	})
	before := mustRead(t, w.journal)
	for launch := 0; launch < 3; launch++ {
		if err := preRestart.Reconcile(ctx); err != nil {
			t.Fatalf("a process that is not the installed build refused to reconcile: %v", err)
		}
	}
	if string(mustRead(t, w.journal)) != string(before) {
		t.Fatal("a process running another build changed the rollback journal")
	}
}
