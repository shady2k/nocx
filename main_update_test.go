package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/update"
)

func TestWindowUpdaterUsesAppImageTarget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux AppImage placement")
	}
	for _, appImage := range []string{"", "/home/person/nocx.AppImage"} {
		t.Run(appImage, func(t *testing.T) {
			t.Setenv("APPIMAGE", appImage)
			_, logger := logtest.New(t)
			executable := "/tmp/.mount_nocx/usr/bin/nocx"
			_, target, err := newWindowUpdater(executable, t.TempDir(), update.UpdaterConfig{Platform: update.NewPlatform(), Coordinator: coordinator.NewLaunchProbe(), Logger: logger})
			if err != nil {
				t.Fatal(err)
			}
			want := appImage
			if want == "" {
				want = executable
			}
			if target != want {
				t.Fatalf("target %q; want %q", target, want)
			}
		})
	}
}

func TestWindowUpdaterInstallsAndRestartsSelectedTarget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux AppImage transaction")
	}
	storagetest.Isolate(t)
	paths, err := storage.NewAppPaths()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	targetDir := filepath.Join(root, "installed")
	runningDir := filepath.Join(root, "mounted")
	for _, dir := range []string{targetDir, runningDir} {
		if mkdirErr := os.Mkdir(dir, 0o750); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
	}
	target := filepath.Join(targetDir, "nocx.AppImage")
	running := filepath.Join(runningDir, "nocx")
	old := []byte("old installed image")
	for _, path := range []string{target, running} {
		if writeErr := os.WriteFile(path, old, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if chmodErr := os.Chmod(runningDir, 0o555); chmodErr != nil { //nolint:gosec // Exercise a read-only running-copy directory.
		t.Fatal(chmodErr)
	}
	t.Cleanup(func() { _ = os.Chmod(runningDir, 0o750) }) //nolint:gosec // Restore traversal of this private test directory for cleanup.
	t.Setenv("APPIMAGE", target)
	payload := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 'A', 'I', 2}, bytes.Repeat([]byte("new image"), 32)...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	sum := sha256.Sum256(payload)
	_, logger := logtest.New(t)
	updater, selected, err := newWindowUpdater(running, paths.DataDir(), update.UpdaterConfig{Platform: update.NewPlatform(), CurrentVersion: "0.1.0", Coordinator: coordinator.NewLaunchProbe(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if reconcileErr := updater.Reconcile(context.Background()); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	if applyErr := updater.Apply(context.Background(), &update.UpdateInfo{Version: "0.2.0", URL: server.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}); applyErr != nil {
		t.Fatal(applyErr)
	}
	got, err := os.ReadFile(target) //nolint:gosec // target is inside t.TempDir.
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("installed bytes %q, error %v", got, err)
	}
	got, err = os.ReadFile(running) //nolint:gosec // running is inside t.TempDir.
	if err != nil || !bytes.Equal(got, old) {
		t.Fatalf("running copy changed: %q, %v", got, err)
	}
	for _, name := range []string{".nocx-update.lock", ".nocx-update-journal.json"} {
		if _, err := os.Stat(filepath.Join(paths.DataDir(), name)); err != nil {
			t.Fatalf("state %s: %v", name, err)
		}
	}
	for _, dir := range []string{targetDir, runningDir} {
		for _, name := range []string{".nocx-update.lock", ".nocx-update-journal.json"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatalf("state leaked into %s: %v", dir, err)
			}
		}
	}
	var restarted string
	w := &WailsApp{updateInstallPath: selected, restartWindowFn: func(path string) error { restarted = path; return nil }, quit: &quitSeam{logger: logtest.Slog(t), quit: func() {}}}
	if err := w.RestartWindow(); err != nil {
		t.Fatal(err)
	}
	if restarted != target {
		t.Fatalf("restarted %q; want %q", restarted, target)
	}
}

// TestWindowUpdaterRefusesWithoutTheLaunchProbe is the regression the dead-code
// ratchet caught on the branch that added this test (nocx-1ebrs.66.1.1): the
// startup path stopped constructing the probe, nothing failed, and only a
// reachability check saw it. A window updater without a probe certifies nothing,
// so every update would swap and roll back. It must refuse at construction.
func TestWindowUpdaterRefusesWithoutTheLaunchProbe(t *testing.T) {
	_, logger := logtest.New(t)
	_, _, err := newWindowUpdater("", t.TempDir(), update.UpdaterConfig{Platform: update.NewPlatform(), Logger: logger})
	if err == nil {
		t.Fatal("a window updater without the launch probe must be refused: nothing could certify an update without it")
	}
	if !strings.Contains(err.Error(), "launch probe") {
		t.Fatalf("the refusal must name the probe, got %q", err)
	}
}
