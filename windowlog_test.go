package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/update"
)

// readWindowLog is the file's current text. The handler writes each record
// before returning, so a line asserted after the call that made it is on
// disk; a read error is the test's failure, not a missing-file condition it
// should tolerate.
func readWindowLog(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // this test's own temp directory
	if err != nil {
		t.Fatalf("read the window log %s: %v", path, err)
	}
	return string(body)
}

// windowTestPlatform is the transaction's platform seam with the filesystem
// behaviour the core needs, so a real Apply runs in a temp directory on any
// host (the same shape internal/update's own tests use).
type windowTestPlatform struct{}

func (windowTestPlatform) ArtifactID() update.ArtifactID {
	return update.ArtifactID{OS: "linux", Arch: "amd64", Format: "AppImage"}
}

func (windowTestPlatform) Preflight(context.Context, string) error { return nil }

func (windowTestPlatform) VerifyExtracted(context.Context, string) error { return nil }

// InspectBundle answers like the Linux platform: these tests never leave
// debris, so nothing is inspected, and the zero report claims nothing.
func (windowTestPlatform) InspectBundle(context.Context, string) (update.BundleReport, error) {
	return update.BundleReport{}, nil
}

func (windowTestPlatform) Extract(_ context.Context, archivePath, destDir string) error {
	data, err := os.ReadFile(archivePath) //nolint:gosec // this test's own temp directory
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, filepath.Base(archivePath)), data, 0o755) //nolint:gosec // staged fixture must be executable
}

func (windowTestPlatform) Exchange(_ context.Context, a, b string) error {
	tmp := a + ".test-swap"
	if err := os.Rename(a, tmp); err != nil {
		return err
	}
	if err := os.Rename(b, a); err != nil {
		return err
	}
	return os.Rename(tmp, b)
}

// THE WINDOW'S LOG CARRIES THE UPDATE PATH (nocx-vh2hr.1.12).
//
// This is the real updater, given the window's real logger for a temp data
// directory, and run through one complete transaction. The backend's
// nocx.log cannot hold these lines: it belongs to another process.
func TestTheWindowLogCarriesTheUpdatersApplyLine(t *testing.T) {
	logger, file, path := newWindowLogger(t.TempDir())
	if file == nil {
		t.Fatal("newWindowLogger opened no file")
	}
	t.Cleanup(func() { _ = file.Close() })

	root := t.TempDir()
	install := filepath.Join(root, "nocx.AppImage")
	if err := os.WriteFile(install, []byte("the installed build"), 0o755); err != nil { //nolint:gosec // this test's temp directory
		t.Fatal(err)
	}
	payload := []byte("the downloaded next build")
	sum := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	updater := update.NewUpdater(update.UpdaterConfig{
		Platform:       windowTestPlatform{},
		CurrentVersion: "0.8.0",
		InstallPath:    install,
		StateDir:       filepath.Join(root, "state"),
		Logger:         log.NewSlogAdapter(logger),
	})
	err := updater.Apply(context.Background(), &update.UpdateInfo{
		Version: "0.8.1",
		URL:     server.URL,
		SHA256:  hex.EncodeToString(sum[:]),
		Size:    int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("Apply through the window logger: %v", err)
	}

	body := readWindowLog(t, path)
	if !strings.Contains(body, "window log file") {
		t.Fatalf("the window log does not name itself; got:\n%s", body)
	}
	if !strings.Contains(body, "update apply: journal written") {
		t.Fatalf("the apply line did not reach the window log; got:\n%s", body)
	}
	if !strings.Contains(body, "update apply: exchange complete") {
		t.Fatalf("the completed transaction is not in the window log; got:\n%s", body)
	}
}

// A STUB UPDATER whose Apply is the failure, so the test can read the exact
// error the renderer would turn into the toast.
type failingApplyUpdater struct{ err error }

func (f failingApplyUpdater) Check(context.Context) (*update.UpdateInfo, error) { return nil, nil }
func (f failingApplyUpdater) Apply(context.Context, *update.UpdateInfo) error   { return f.err }
func (f failingApplyUpdater) Reconcile(context.Context) error                   { return nil }
func (f failingApplyUpdater) ReportHealthy(context.Context) error               { return nil }
func (f failingApplyUpdater) State(context.Context) update.UpdateState          { return update.UpdateState{} }

// THE TOAST'S CAUSE REACHES THE FILE (nocx-vh2hr.1.6, nocx-vh2hr.1.12).
//
// The renderer deliberately keeps the backend's sentence off the screen
// (frontend/src/update-failure.ts), so the log is the only place the cause
// can exist. The owner's red toast had none.
func TestTheUpdateFailedToastCauseReachesTheWindowLog(t *testing.T) {
	logger, file, path := newWindowLogger(t.TempDir())
	if file == nil {
		t.Fatal("newWindowLogger opened no file")
	}
	t.Cleanup(func() { _ = file.Close() })

	cause := errors.New("atomic exchange of /Applications/nocx.app ↔ /Applications/.nocx-swap.app failed: operation not permitted")
	w := &WailsApp{
		logger:     logger,
		updater:    failingApplyUpdater{err: cause},
		updateInfo: &update.UpdateInfo{Version: "0.8.1"},
	}
	if err := w.ApplyUpdate(); !errors.Is(err, cause) {
		t.Fatalf("ApplyUpdate returned %v, want the cause it was given", err)
	}

	body := readWindowLog(t, path)
	if !strings.Contains(body, `msg="update failed"`) {
		t.Fatalf("the failure behind the toast was not logged; got:\n%s", body)
	}
	if !strings.Contains(body, "stage=apply") {
		t.Fatalf("the failure does not name the binding it came from; got:\n%s", body)
	}
	if !strings.Contains(body, "operation not permitted") {
		t.Fatalf("the failure was logged without its cause; got:\n%s", body)
	}
}

// The window log lives beside, never on top of, the backend's file: one owner
// per file (windowlog.go's file doc).
func TestTheWindowLogIsNotTheBackendsFile(t *testing.T) {
	dataDir := t.TempDir()
	if got, want := windowLogPath(dataDir), filepath.Join(dataDir, "nocx-window.log"); got != want {
		t.Fatalf("window log path = %q, want %q", got, want)
	}
	if windowLogPath(dataDir) == filepath.Join(dataDir, "nocx.log") {
		t.Fatal("the window and the backend name one log file")
	}
}
