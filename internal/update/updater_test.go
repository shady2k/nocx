package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mockFetcher returns a ManifestFetcher that serves the given body and
// signature, both base64-decoded from their string representations
// (for use with raw bytes).
type mockFetcher struct {
	body []byte
	sig  []byte
}

func (f *mockFetcher) Fetch(_ context.Context) ([]byte, []byte, error) {
	return f.body, f.sig, nil
}

// makeTestKeyring generates n ed25519 keypairs and returns the public
// keys as a keyring plus one private key (the last one) for signing.
func makeTestKeyring(n int) ([]ed25519.PublicKey, ed25519.PrivateKey) {
	pubs := make([]ed25519.PublicKey, n)
	var lastPriv ed25519.PrivateKey
	for i := 0; i < n; i++ {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		pubs[i] = pub
		if i == n-1 {
			lastPriv = priv
		}
	}
	return pubs, lastPriv
}

// newTestManifest creates a signed manifest JSON with the given version
// and artifact entries, signed by priv. The signature is base64-encoded
// to match the wire format (manifest.json.sig).
func newTestManifest(version string, artifacts []Artifact, priv ed25519.PrivateKey) ([]byte, []byte) {
	m := Manifest{
		Version:   version,
		Released:  "2026-07-22T10:00:00Z",
		NotesURL:  "https://example.com/releases/v" + version,
		Artifacts: artifacts,
	}
	body, _ := json.Marshal(m)
	rawSig := ed25519.Sign(priv, body)
	sig := []byte(base64.StdEncoding.EncodeToString(rawSig))
	return body, sig
}

// makeMinimalAppImage creates an executable file with ELF + AppImage
// type-2 magic headers (the minimal plausible AppImage shape).
func makeMinimalAppImage(t *testing.T, path string) {
	t.Helper()
	header := []byte{
		0x7f, 0x45, 0x4c, 0x46, // ELF magic
		0x02,             // 64-bit
		0x01,             // little-endian
		0x01,             // ELF version
		0x00,             // SYSV
		0x41, 0x49, 0x02, // AppImage type-2
	}
	payload := make([]byte, 256)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	content := append(header, payload...)
	if err := os.WriteFile(path, content, 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

// newTestUpdater creates an Updater wired for testing against a
// temp directory's synthetic AppImage "install".
func newTestUpdater(t *testing.T, installDir string, fetcherBody, fetcherSig []byte, keyring []ed25519.PublicKey) Updater {
	t.Helper()
	installPath := filepath.Join(installDir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	return NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{body: fetcherBody, sig: fetcherSig},
		Keyring:        keyring,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})
}

// ---------------------------------------------------------------------------
// Happy path: Check → Apply → Reconcile
// ---------------------------------------------------------------------------

func TestUpdater_Check_FindsUpdate(t *testing.T) {
	dir := t.TempDir()
	pubs, priv := makeTestKeyring(1)
	arch := NewPlatform().ArtifactID()

	artifacts := []Artifact{
		{
			OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
			URL:    "https://example.com/nocx-0.2.0.AppImage",
			SHA256: "abc123",
			Size:   12345,
		},
	}
	body, sig := newTestManifest("0.2.0", artifacts, priv)

	u := newTestUpdater(t, dir, body, sig, pubs)
	info, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if info == nil {
		t.Fatal("expected update info, got nil")
	}
	if info.Version != "0.2.0" {
		t.Errorf("version: got %q, want 0.2.0", info.Version)
	}
}

// Test that Check returns nil when already current.
func TestUpdater_Check_AlreadyCurrent(t *testing.T) {
	dir := t.TempDir()
	pubs, priv := makeTestKeyring(1)
	arch := NewPlatform().ArtifactID()

	artifacts := []Artifact{
		{
			OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
			URL:    "https://example.com/nocx-0.1.0.AppImage",
			SHA256: "abc",
			Size:   123,
		},
	}
	body, sig := newTestManifest("0.1.0", artifacts, priv) // same version

	u := newTestUpdater(t, dir, body, sig, pubs)
	info, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if info != nil {
		t.Fatal("expected nil when already current, got info")
	}
}

// Test that Check returns nil when remote is older.
func TestUpdater_Check_RemoteOlder(t *testing.T) {
	dir := t.TempDir()
	pubs, priv := makeTestKeyring(1)
	arch := NewPlatform().ArtifactID()

	artifacts := []Artifact{
		{
			OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
			URL:    "https://example.com/nocx-0.0.9.AppImage",
			SHA256: "abc",
			Size:   123,
		},
	}
	body, sig := newTestManifest("0.0.9", artifacts, priv)

	u := newTestUpdater(t, dir, body, sig, pubs)
	info, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if info != nil {
		t.Fatal("expected nil for older remote, got info")
	}
}

// Test that Check fails with bad signature.
func TestUpdater_Check_BadSignature(t *testing.T) {
	dir := t.TempDir()
	pubs, _ := makeTestKeyring(1)
	_, otherPriv := makeTestKeyring(1) // different key
	arch := NewPlatform().ArtifactID()

	artifacts := []Artifact{
		{
			OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
			URL: "https://example.com/nocx.AppImage", SHA256: "abc", Size: 123,
		},
	}
	body, sig := newTestManifest("0.2.0", artifacts, otherPriv) // signed by wrong key

	u := newTestUpdater(t, dir, body, sig, pubs)
	_, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("expected error for bad signature, got nil")
	}
}

// Test that Check finds no artefact when none matches.
func TestUpdater_Check_NoMatchingArtifact(t *testing.T) {
	dir := t.TempDir()
	pubs, priv := makeTestKeyring(1)

	// Only darwin artifacts — no linux.
	artifacts := []Artifact{
		{
			OS: "darwin", Arch: "universal", Format: "zip",
			URL: "https://example.com/mac.zip", SHA256: "abc", Size: 123,
		},
	}
	body, sig := newTestManifest("0.2.0", artifacts, priv)

	u := newTestUpdater(t, dir, body, sig, pubs)
	_, err := u.Check(context.Background())
	if err == nil {
		t.Fatal("expected error for no matching artefact, got nil")
	}
}

// ---------------------------------------------------------------------------
// Journal / Reconcile tests
// ---------------------------------------------------------------------------

func TestReconcile_NoRecord_NothingInFlight(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	if err := u.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile with no journal failed: %v", err)
	}
}

func TestReconcile_NoRecord_ManagedDebris(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	// Create managed debris without a journal.
	if err := os.WriteFile(swapPath(installPath), []byte("debris"), 0o600); err != nil {
		t.Fatal(err)
	}

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	err := u.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error for managed debris with no journal, got nil")
	}
}

func TestReconcile_ExchangeDidNotHappen_OldIDMatches(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	oldID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	// Write a journal that says oldBundleID = current identity
	// (exchange didn't happen after journal was written).
	jp := journalPath(installPath)
	rec := &journalRecord{
		TxID:        "0.2.0",
		InstallPath: installPath,
		OldBundleID: oldID,
		FromVersion: "0.1.0",
		ToVersion:   "0.2.0",
	}
	if err := writeJournal(jp, rec); err != nil {
		t.Fatal(err)
	}

	// Create a swap file (like the staged bundle was renamed).
	swap := swapPath(installPath)
	_ = os.WriteFile(swap, []byte("staged"), 0o600)

	// Create extraction dir debris.
	extractDir := extractionDir(installPath, "0.2.0")
	_ = os.MkdirAll(extractDir, 0o750)

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	if err := u.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Journal should be deleted.
	if _, err := os.Stat(jp); !os.IsNotExist(err) {
		t.Error("journal not deleted after reconcile when exchange didn't happen")
	}
	// Swap should be deleted.
	if _, err := os.Stat(swap); !os.IsNotExist(err) {
		t.Error("swap not deleted after reconcile")
	}
	// Extraction dir should be deleted.
	if _, err := os.Stat(extractDir); !os.IsNotExist(err) {
		t.Error("extraction dir not deleted after reconcile")
	}
}

func TestReconcile_ExchangeHappened_PendingRestart(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	// Simulate: exchange happened. installPath now holds the new
	// AppImage (we'll write the journal with newBundleID = current ID).
	currentID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	jp := journalPath(installPath)
	rec := &journalRecord{
		TxID:           "0.2.0",
		InstallPath:    installPath,
		OldBundleID:    bundleID{Dev: 999, Ino: 999}, // arbitrary old
		NewBundleID:    currentID,
		FromVersion:    "0.1.0",
		ToVersion:      "0.2.0",
		ArtifactSHA256: "abc",
	}
	if err := writeJournal(jp, rec); err != nil {
		t.Fatal(err)
	}

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	if err := u.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Journal should still exist (pending health).
	if _, err := os.Stat(jp); os.IsNotExist(err) {
		t.Error("journal deleted — should persist for health confirmation")
	}

	// Launch counter should be incremented.
	reread, _ := readJournal(jp)
	if reread == nil || reread.LaunchAttempts != 1 {
		t.Errorf("launch attempts: got %d, want 1", reread.LaunchAttempts)
	}
}

func TestReconcile_AutoRollback_AfterThreeLaunches(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	// Create a "previous" AppImage at the swap path — this is what
	// rollback will exchange back in.
	swap := swapPath(installPath)
	makeMinimalAppImage(t, swap)
	swapID, err := statBundleID(swap)
	if err != nil {
		t.Fatal(err)
	}

	currentID, _ := statBundleID(installPath)

	jp := journalPath(installPath)
	rec := &journalRecord{
		TxID:           "0.2.0",
		InstallPath:    installPath,
		OldBundleID:    swapID, // old = the swap peer content
		NewBundleID:    currentID,
		FromVersion:    "0.1.0",
		ToVersion:      "0.2.0",
		ArtifactSHA256: "abc",
		LaunchAttempts: 2, // one more launch triggers rollback
	}
	if err = writeJournal(jp, rec); err != nil {
		t.Fatal(err)
	}

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	if err = u.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile (auto-rollback) failed: %v", err)
	}

	// Journal should be deleted.
	_, statErr := os.Stat(jp)
	if !os.IsNotExist(statErr) {
		t.Error("journal not deleted after auto-rollback")
	}

	// After rollback, installPath should hold the OLD bundle
	// (which was at swapPath, now exchanged back).
	rolledBackID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if !rolledBackID.equal(swapID) {
		t.Errorf("after rollback, install should hold swapID — got %v, want %v", rolledBackID, swapID)
	}
}

func TestReportHealthy_FinalisesUpdate(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	currentID, _ := statBundleID(installPath)

	// Simulate: exchange happened, health unconfirmed.
	jp := journalPath(installPath)
	rec := &journalRecord{
		TxID:           "0.2.0",
		InstallPath:    installPath,
		OldBundleID:    bundleID{Dev: 999, Ino: 999},
		NewBundleID:    currentID,
		FromVersion:    "0.1.0",
		ToVersion:      "0.2.0",
		ArtifactSHA256: "abc",
		LaunchAttempts: 1,
	}
	if err := writeJournal(jp, rec); err != nil {
		t.Fatal(err)
	}

	// Create a swap that holds the "old" bundle content.
	swap := swapPath(installPath)
	makeMinimalAppImage(t, swap)

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.2.0",
		InstallPath:    installPath,
	})

	if err := u.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("ReportHealthy failed: %v", err)
	}

	// Journal must be deleted.
	if _, err := os.Stat(jp); !os.IsNotExist(err) {
		t.Error("journal not deleted after health confirmation")
	}

	// Swap should be moved to backup.
	backup := backupPath(installPath)
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		t.Error("backup does not exist after health confirmation")
	}
	if _, err := os.Stat(swap); !os.IsNotExist(err) {
		t.Error("swap still exists after health confirmation")
	}
}

func TestReportHealthy_Idempotent_NoRecord(t *testing.T) {
	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{},
		Keyring:        nil,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	// Calling ReportHealthy with no record should be harmless.
	if err := u.ReportHealthy(context.Background()); err != nil {
		t.Fatalf("ReportHealthy with no record failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lock tests
// ---------------------------------------------------------------------------

func TestFlock_AcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	lk, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("acquireLock failed: %v", err)
	}
	if err := lk.release(); err != nil {
		t.Fatalf("release failed: %v", err)
	}
}

func TestFlock_TryLockBlocks(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	// Hold the lock.
	holder, err := acquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.release() }()

	// Try to get it with a short timeout — must time out.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	lk, err := tryLock(ctx, lockPath)
	if err != nil {
		t.Fatalf("tryLock returned error: %v", err)
	}
	if lk != nil {
		_ = lk.release()
		t.Fatal("tryLock acquired lock while holder still had it")
	}
}

func TestFlock_TryLockSucceedsAfterRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	holder, err := acquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = holder.release()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	lk, err := tryLock(ctx, lockPath)
	if err != nil {
		t.Fatalf("tryLock returned error: %v", err)
	}
	if lk == nil {
		t.Fatal("tryLock failed to acquire lock after release")
	}
	_ = lk.release()
}

// ---------------------------------------------------------------------------
// Apply end-to-end tests (Issue 3 coverage)
// ---------------------------------------------------------------------------

// serveAppImageFixture starts an httptest server that serves a synthetic
// AppImage fixture. Returns the server URL and the fixture's sha256 + size.
func serveAppImageFixture(t *testing.T) (url string, sha256Hex string, size int64, srv *httptest.Server) {
	t.Helper()

	// Build the AppImage fixture in memory.
	header := []byte{
		0x7f, 0x45, 0x4c, 0x46, // ELF magic
		0x02,             // 64-bit
		0x01,             // little-endian
		0x01,             // ELF version
		0x00,             // SYSV
		0x41, 0x49, 0x02, // AppImage type-2
	}
	payload := make([]byte, 512)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	fixture := append(header, payload...)

	h := sha256.Sum256(fixture)
	sha256Hex = hex.EncodeToString(h[:])
	size = int64(len(fixture))

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(fixture)
	}))

	return srv.URL, sha256Hex, size, srv
}

// TestUpdater_Apply_HappyPath tests the full Check→Apply→Reconcile→ReportHealthy
// flow end-to-end against the linux Platform using a synthetic AppImage served
// over httptest.
func TestUpdater_Apply_HappyPath(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Start a server that serves the AppImage fixture.
	fixtureURL, fixtureSHA256, fixtureSize, srv := serveAppImageFixture(t)
	defer srv.Close()

	// Generate signing keys.
	pubs, priv := makeTestKeyring(1)
	arch := NewPlatform().ArtifactID()

	// Build a signed manifest pointing at the fixture.
	artifacts := []Artifact{
		{
			OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
			URL:    fixtureURL,
			SHA256: fixtureSHA256,
			Size:   fixtureSize,
		},
	}
	manifestBody, manifestSig := newTestManifest("0.2.0", artifacts, priv)

	// Create the "installed" AppImage at installPath.
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)

	// Wire the updater. Set APPIMAGE so linux Preflight permits the update.
	t.Setenv("APPIMAGE", installPath)

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{body: manifestBody, sig: manifestSig},
		Keyring:        pubs,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	// 1. Check must return the update.
	info, err := u.Check(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if info == nil {
		t.Fatal("expected update info from Check, got nil")
	}
	if info.Version != "0.2.0" {
		t.Errorf("version: got %q, want 0.2.0", info.Version)
	}

	oldID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Apply must succeed.
	if err = u.Apply(ctx, info); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// 3. After Apply, journal must exist with newBundleID recorded.
	jp := journalPath(installPath)
	rec, err := readJournal(jp)
	if err != nil {
		t.Fatalf("read journal after Apply: %v", err)
	}
	if rec == nil {
		t.Fatal("journal missing after Apply")
	}
	if rec.NewBundleID.isZero() {
		t.Error("newBundleID not recorded after Apply")
	}

	// The install path should now hold the new identity (exchange happened).
	newID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if newID.equal(oldID) {
		t.Error("install identity unchanged after Apply — exchange did not happen")
	}

	// The swap peer should hold the old bundle.
	swap := swapPath(installPath)
	swapID, err := statBundleID(swap)
	if err != nil {
		t.Fatal(err)
	}
	if swapID.isZero() {
		t.Error("swap peer missing after Apply")
	}
	if !swapID.equal(oldID) {
		t.Error("swap peer does not hold the old bundle identity")
	}

	// 4. Reconcile must see pending health state (launch counter incremented).
	if err := u.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after Apply failed: %v", err)
	}
	reread, _ := readJournal(jp)
	if reread == nil {
		t.Fatal("journal missing after Reconcile")
	}
	if reread.LaunchAttempts < 1 {
		t.Errorf("launch attempts not incremented: got %d", reread.LaunchAttempts)
	}

	// 5. ReportHealthy must finalise (journal deleted, backup created).
	if err := u.ReportHealthy(ctx); err != nil {
		t.Fatalf("ReportHealthy failed: %v", err)
	}
	if _, err := os.Stat(jp); !os.IsNotExist(err) {
		t.Error("journal not deleted after ReportHealthy")
	}
	backup := backupPath(installPath)
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		t.Error("backup not created after ReportHealthy")
	}

	// Extraction directory should have been cleaned up.
	extractDir := extractionDir(installPath, "0.2.0")
	if _, err := os.Stat(extractDir); !os.IsNotExist(err) {
		t.Error("extraction directory not cleaned up after Apply")
	}

	t.Log("Apply happy path: Check→Apply→Reconcile→ReportHealthy all passed ✓")
}

// TestUpdater_Apply_SHA256Mismatch proves that a download whose sha256
// does not match the manifest declaration fails Apply and leaves the
// install untouched.
func TestUpdater_Apply_SHA256Mismatch(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	_, _, _, srv := serveAppImageFixture(t)
	defer srv.Close()

	pubs, priv := makeTestKeyring(1)
	arch := NewPlatform().ArtifactID()

	// Manifest declares an sha256 that does NOT match the served fixture.
	artifacts := []Artifact{
		{
			OS: arch.OS, Arch: arch.Arch, Format: arch.Format,
			URL:    srv.URL,
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000", // deliberately wrong
			Size:   523,                                                                // matches the fixture size (11 header + 512 payload)
		},
	}
	manifestBody, manifestSig := newTestManifest("0.2.0", artifacts, priv)

	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	t.Setenv("APPIMAGE", installPath)

	oldID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}

	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        &mockFetcher{body: manifestBody, sig: manifestSig},
		Keyring:        pubs,
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
	})

	info, err := u.Check(ctx)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if info == nil {
		t.Fatal("expected update info from Check")
	}

	// Apply must fail because sha256 doesn't match.
	err = u.Apply(ctx, info)
	if err == nil {
		t.Fatal("expected Apply to fail with sha256 mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error should mention sha256: %v", err)
	}

	// Install must be untouched (same identity).
	currentID, err := statBundleID(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if !currentID.equal(oldID) {
		t.Error("install changed identity despite failed Apply — should be untouched")
	}

	// No swap peer should exist (exchange never happened).
	swap := swapPath(installPath)
	if _, err := os.Stat(swap); !os.IsNotExist(err) {
		t.Error("swap peer created despite failed Apply")
	}

	t.Log("Apply sha256 mismatch: correctly refused and install untouched ✓")
}
