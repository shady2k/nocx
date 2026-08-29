package coordinator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/update"
)

// The wiring this file exists for is D4's second half: an update is
// certified by a UI/coordinator PAIR, and the fact that makes the pair
// checkable is the one the launcher already holds.
//
// Every test here drives the real sequence — Check, Apply, restart,
// ReportHealthy — against a real updater with a real [coordinator.LaunchProbe]
// attached to a real [coordinator.Launch]. A test that called
// AnsweringCoordinator and compared its answer would prove the probe
// returns what it was given and nothing about whether an update can be
// certified by the wrong backend.

// ---------------------------------------------------------------------------
// A platform seam that does not depend on the host
// ---------------------------------------------------------------------------

// fakePlatform is a host-independent [update.Platform]. The transaction
// core is platform-agnostic and this test runs on both CI runners, so
// taking the host's real platform would give the darwin job a zip
// artefact and a dev-build preflight refusal.
type fakePlatform struct{}

func (fakePlatform) ArtifactID() update.ArtifactID {
	return update.ArtifactID{OS: "linux", Arch: "amd64", Format: "AppImage"}
}

func (fakePlatform) Preflight(context.Context, string) error { return nil }

func (fakePlatform) VerifyExtracted(context.Context, string) error { return nil }

// Extract copies the downloaded archive into destDir under its own name —
// the single-file behaviour the linux implementation has.
func (fakePlatform) Extract(_ context.Context, archivePath, destDir string) error {
	data, err := os.ReadFile(archivePath) //nolint:gosec // test-controlled path
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, filepath.Base(archivePath)), data, 0o755) //nolint:gosec // must be executable
}

// Exchange swaps two same-filesystem paths through a 3-way rename.
// os.Rename preserves inodes, so the transaction's device+inode journal
// behaves exactly as it does under the real RENAME_EXCHANGE.
func (fakePlatform) Exchange(_ context.Context, a, b string) error {
	tmp := a + ".fakeswap"
	if err := os.Rename(a, tmp); err != nil {
		return err
	}
	if err := os.Rename(b, a); err != nil {
		return err
	}
	return os.Rename(tmp, b)
}

// staticFetcher serves one signed manifest.
type staticFetcher struct{ body, sig []byte }

func (f staticFetcher) Fetch(context.Context) ([]byte, []byte, error) { return f.body, f.sig, nil }

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// journalName is the rollback journal's on-disk name (internal/update's
// journalPath). Its PRESENCE is the assertion that matters here: while it
// is there, the next launches count and reconciliation rolls back, so an
// update that was refused is still recoverable.
const journalName = ".nocx-update-journal.json"

// applyPendingUpdate drives Check and Apply for real and leaves the
// machine in the state a restart finds: the new bundle installed, the old
// one at the swap peer, the journal pending because nothing has certified
// anything yet.
//
// Returned: the install path.
func applyPendingUpdate(t *testing.T, from, to string) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	// The artefact the manifest points at — the "new" bundle's bytes.
	payload := []byte("nocx " + to + " bundle")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := fakePlatform{}.ArtifactID()
	manifest, err := json.Marshal(update.Manifest{
		Version:  to,
		Released: "2026-08-28T10:00:00Z",
		NotesURL: "https://example.com/releases/v" + to,
		Artifacts: []update.Artifact{{
			OS: id.OS, Arch: id.Arch, Format: id.Format,
			URL:    srv.URL,
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(payload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)))

	installPath := filepath.Join(dir, "nocx.AppImage")
	if err = os.WriteFile(installPath, []byte("nocx "+from+" bundle"), 0o755); err != nil { //nolint:gosec // fixture must be executable
		t.Fatal(err)
	}

	// The window that APPLIES the update is the old build. Its own pair
	// probe is irrelevant here — Apply never asks one, which is itself
	// part of the design: the question is asked at finalisation.
	applying := update.NewUpdater(update.UpdaterConfig{
		Platform:       fakePlatform{},
		Fetcher:        staticFetcher{body: manifest, sig: sig},
		Keyring:        []ed25519.PublicKey{pub},
		CurrentVersion: from,
		InstallPath:    installPath,
	})
	info, err := applying.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info == nil {
		t.Fatalf("Check found no update from %s to %s", from, to)
	}
	if err = applying.Apply(ctx, info); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, journalName)); err != nil {
		t.Fatalf("Apply left no pending journal: %v", err)
	}
	return installPath
}

// restartedWindow is the process that comes up after the update: it runs
// the NEW version and reports health through the probe the launcher
// attached.
func restartedWindow(installPath, version string, probe update.CoordinatorProbe) update.Updater {
	return update.NewUpdater(update.UpdaterConfig{
		Platform:       fakePlatform{},
		Fetcher:        staticFetcher{},
		CurrentVersion: version,
		InstallPath:    installPath,
		Coordinator:    probe,
	})
}

// attachedTo is a probe filled the way main.go fills it: from the Launch
// the launcher returned, and from nothing else.
func attachedTo(version, commit string) *coordinator.LaunchProbe {
	p := coordinator.NewLaunchProbe()
	p.Attach(coordinator.Launch{Hello: coordinator.Hello{
		Build:     coordinator.Build{Version: version, Commit: commit},
		Protocol:  coordinator.ProtocolVersion,
		WSAddress: "127.0.0.1:0",
	}})
	return p
}

func assertJournalKept(t *testing.T, installPath string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(filepath.Dir(installPath), journalName)); err != nil {
		t.Fatalf("the rollback journal was deleted by a refused health report: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Acceptance: a mixed pair cannot certify
// ---------------------------------------------------------------------------

// TestReportHealthy_MixedPair_CannotCertify is the whole point of the
// wiring. The bundle was swapped, the window restarted on the new
// version — and the coordinator that outlived the window is still the old
// build, which is precisely what a detached daemon does. Health must be
// refused and the rollback journal must survive.
func TestReportHealthy_MixedPair_CannotCertify(t *testing.T) {
	installPath := applyPendingUpdate(t, "0.1.0", "0.2.0")

	// The launcher found a running coordinator and this window attached
	// to it. It is the old one.
	stale := attachedTo("0.1.0", "oldsha")
	w := restartedWindow(installPath, "0.2.0", stale)

	err := w.ReportHealthy(context.Background())
	if !errors.Is(err, update.ErrPairMismatch) {
		t.Fatalf("a 0.2.0 window on a 0.1.0 coordinator certified the update: %v", err)
	}
	assertJournalKept(t, installPath)
}

// The paired success: the same sequence, with the coordinator this
// update installed. Without it the test above would also pass on an
// updater that refused everything.
func TestReportHealthy_MatchingPair_Certifies(t *testing.T) {
	installPath := applyPendingUpdate(t, "0.1.0", "0.2.0")

	w := restartedWindow(installPath, "0.2.0", attachedTo("0.2.0", "newsha"))

	if err := w.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("a window and coordinator both at 0.2.0 must certify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(installPath), journalName)); !os.IsNotExist(err) {
		t.Fatalf("the journal survived a certified health report (%v)", err)
	}
}

// A commit difference at the same version is NOT a mismatch, and that is
// deliberate rather than an oversight: the release manifest names a
// version and no commit, so there is no expected commit in the journal to
// compare against and a check against a fact nobody recorded would refuse
// every update for a reason it could not name.
func TestReportHealthy_SameVersionDifferentCommit_Certifies(t *testing.T) {
	installPath := applyPendingUpdate(t, "0.1.0", "0.2.0")

	w := restartedWindow(installPath, "0.2.0", attachedTo("0.2.0", "a-different-sha"))

	if err := w.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("the commit is carried, not compared: %v", err)
	}
}

// A window that never attached to a coordinator cannot say which backend
// answered it, and an unanswerable question is not the same as a
// mismatch: it is ErrPairUnverifiable, and it keeps the journal too.
//
// This is the state between constructing the updater and the launcher
// returning — the order main.go is obliged to run in — so it is a real
// state and not a hypothetical.
func TestReportHealthy_BeforeAnyCoordinatorIsAttached_IsUnverifiable(t *testing.T) {
	installPath := applyPendingUpdate(t, "0.1.0", "0.2.0")

	w := restartedWindow(installPath, "0.2.0", coordinator.NewLaunchProbe())

	err := w.ReportHealthy(context.Background())
	if !errors.Is(err, update.ErrPairUnverifiable) {
		t.Fatalf("an unattached probe must be unverifiable, got: %v", err)
	}
	assertJournalKept(t, installPath)
}

// The probe reports the handshake's answer and only that — attaching a
// second launch replaces it, which is what a window that had to replace
// an incompatible coordinator ends up doing.
func TestLaunchProbe_ReportsTheLaunchItWasAttachedTo(t *testing.T) {
	p := coordinator.NewLaunchProbe()
	if _, err := p.AnsweringCoordinator(context.Background()); err == nil {
		t.Fatal("an unattached probe answered a question it cannot answer")
	}

	p.Attach(coordinator.Launch{Hello: coordinator.Hello{
		Build: coordinator.Build{Version: "0.1.0", Commit: "old"},
	}})
	p.Attach(coordinator.Launch{
		Hello:    coordinator.Hello{Build: coordinator.Build{Version: "0.2.0", Commit: "new"}},
		Replaced: true,
	})

	got, err := p.AnsweringCoordinator(context.Background())
	if err != nil {
		t.Fatalf("an attached probe cannot fail: %v", err)
	}
	if got.Version != "0.2.0" || got.Commit != "new" {
		t.Errorf("got %+v, want the coordinator this window ended up on", got)
	}
}
