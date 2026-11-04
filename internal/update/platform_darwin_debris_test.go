//go:build darwin

package update

// Ownership of a managed name on macOS: the bundle at .nocx-swap.app or
// .nocx-backup.app is nocx's only when its own Info.plist says so
// (nocx-vh2hr.1.14). This runs in the macOS CI job; a Linux VM cannot run it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/desktopidentity"
)

// writeSyntheticBundle lays out the minimum of a .app that InspectBundle
// reads: Contents/Info.plist.
func writeSyntheticBundle(t *testing.T, path, identifier, version string) {
	t.Helper()
	contents := filepath.Join(path, "Contents")
	if err := os.MkdirAll(contents, 0o750); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>` + identifier + `</string>
	<key>CFBundleShortVersionString</key>
	<string>` + version + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
}

func TestInspectBundle_NocxBundleIsOwnedAndCarriesItsVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nocx-backup.app")
	writeSyntheticBundle(t, path, desktopidentity.ID, "0.3.0")

	report, err := NewPlatform().InspectBundle(context.Background(), path)
	if err != nil {
		t.Fatalf("InspectBundle: %v", err)
	}
	if !report.Nocx {
		t.Errorf("the app's own bundle identifier %q was not accepted", desktopidentity.ID)
	}
	if report.Version != "0.3.0" {
		t.Errorf("version: got %q, want 0.3.0", report.Version)
	}
}

func TestInspectBundle_ForeignBundleIsNotOwned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nocx-swap.app")
	writeSyntheticBundle(t, path, "com.example.someone-else", "1.0")

	report, err := NewPlatform().InspectBundle(context.Background(), path)
	if err != nil {
		t.Fatalf("InspectBundle: %v", err)
	}
	if report.Nocx {
		t.Error("another application's bundle was accepted as nocx's own")
	}
}

func TestInspectBundle_UnreadableBundleIsNotOwned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nocx-backup.app")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}

	report, err := NewPlatform().InspectBundle(context.Background(), path)
	if err != nil {
		t.Fatalf("InspectBundle: %v", err)
	}
	if report.Nocx {
		t.Error("a bundle with no readable Info.plist was accepted as nocx's own")
	}
}
