//go:build linux

package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Exchange
// ---------------------------------------------------------------------------

func TestExchange_SwapContents(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")

	if err := os.WriteFile(a, []byte("content-A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("content-B"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPlatform()
	if err := p.Exchange(ctx, a, b); err != nil {
		t.Fatalf("Exchange failed: %v", err)
	}

	// After swap, file "a" should have B's content and vice versa.
	gotA, err := os.ReadFile(a) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "content-B" {
		t.Errorf("file a after swap: got %q, want %q", string(gotA), "content-B")
	}

	gotB, err := os.ReadFile(b) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != "content-A" {
		t.Errorf("file b after swap: got %q, want %q", string(gotB), "content-A")
	}
}

func TestExchange_MissingPath(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	existing := filepath.Join(dir, "exists")
	missing := filepath.Join(dir, "does-not-exist")

	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPlatform()
	err := p.Exchange(ctx, existing, missing)
	if err == nil {
		t.Fatal("expected error for missing replacement path, got nil")
	}
	if !strings.Contains(err.Error(), "exchange") {
		t.Errorf("error should mention exchange: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Preflight — APPIMAGE refusal
// ---------------------------------------------------------------------------

func TestPreflight_AppImageUnset(t *testing.T) {
	// Simulate a non-AppImage run — e.g. a bare executable or dev build.
	t.Setenv("APPIMAGE", "")

	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx")

	p := NewPlatform()
	err := p.Preflight(context.Background(), installPath)
	if err == nil {
		t.Fatal("expected error when APPIMAGE is unset, got nil")
	}
	if !strings.Contains(err.Error(), "APPIMAGE") {
		t.Errorf("error should mention APPIMAGE: %v", err)
	}
}

func TestPreflight_AppImageSet(t *testing.T) {
	// Simulate running as an AppImage in a writable directory.
	dir := t.TempDir()
	appImagePath := filepath.Join(dir, "nocx.AppImage")
	installPath := appImagePath

	t.Setenv("APPIMAGE", appImagePath)

	p := NewPlatform()
	err := p.Preflight(context.Background(), installPath)
	if err != nil {
		t.Fatalf("unexpected error when APPIMAGE is set: %v", err)
	}
}

func TestPreflight_AppImageSetButReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	appImagePath := filepath.Join(dir, "nocx.AppImage")
	installPath := appImagePath

	t.Setenv("APPIMAGE", appImagePath)

	// Make the directory read-only.
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // test needs read-only dir
		t.Fatal(err)
	}

	p := NewPlatform()
	err := p.Preflight(context.Background(), installPath)
	if err == nil {
		t.Fatal("expected error for read-only directory, got nil")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Errorf("error should mention not writable: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VerifyExtracted
// ---------------------------------------------------------------------------

// makeAppImage creates a file with the correct ELF + AppImage type-2
// header magic so VerifyExtracted accepts it as plausible.
func makeAppImage(t *testing.T, path string, exec bool) {
	t.Helper()

	// Build a minimal header:
	// Bytes 0-3:  ELF magic   (0x7f, 'E', 'L', 'F')
	// Bytes 4:    ELF class   (2 = 64-bit)
	// Bytes 5:    endianness  (1 = little-endian)
	// Bytes 6:    version
	// Bytes 7:    OS/ABI      (0 = SYSV)
	// Bytes 8-10: AppImage type-2 magic ('A', 'I', 0x02)
	header := []byte{
		0x7f, 0x45, 0x4c, 0x46, // ELF
		0x02,             // 64-bit
		0x01,             // little-endian
		0x01,             // ELF version
		0x00,             // SYSV
		0x41, 0x49, 0x02, // AppImage type-2
	}

	perm := os.FileMode(0o644)
	if exec {
		perm = 0o755
	}
	if err := os.WriteFile(path, header, perm); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyExtracted_ValidAppImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nocx.AppImage")
	makeAppImage(t, path, true)

	p := NewPlatform()
	if err := p.VerifyExtracted(context.Background(), path); err != nil {
		t.Fatalf("VerifyExtracted on valid AppImage returned error: %v", err)
	}
}

func TestVerifyExtracted_NotExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nocx.AppImage")
	makeAppImage(t, path, false) // 0644, no +x

	p := NewPlatform()
	err := p.VerifyExtracted(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for non-executable file, got nil")
	}
	if !strings.Contains(err.Error(), "executable") {
		t.Errorf("error should mention executable: %v", err)
	}
}

func TestVerifyExtracted_NoELDMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-an-appimage")
	if err := os.WriteFile(path, []byte("just some text"), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}

	p := NewPlatform()
	err := p.VerifyExtracted(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for non-ELF file, got nil")
	}
	if !strings.Contains(err.Error(), "ELF magic") {
		t.Errorf("error should mention ELF magic: %v", err)
	}
}

func TestVerifyExtracted_NoAppImageMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain-elf")
	// ELF magic but no AppImage bytes at offset 8.
	header := []byte{
		0x7f, 0x45, 0x4c, 0x46, // ELF
		0x02, 0x01, 0x01, 0x00, // padding
		0x00, 0x00, 0x00, // not AppImage magic
	}
	if err := os.WriteFile(path, header, 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}

	p := NewPlatform()
	err := p.VerifyExtracted(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for missing AppImage magic, got nil")
	}
	if !strings.Contains(err.Error(), "AppImage type-2 magic") {
		t.Errorf("error should mention AppImage type-2 magic: %v", err)
	}
}

func TestVerifyExtracted_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	p := NewPlatform()
	err := p.VerifyExtracted(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// ---------------------------------------------------------------------------
// Extract
// ---------------------------------------------------------------------------

func TestExtract_CopiesWithExecBit(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, "staging")
	if err := os.Mkdir(destDir, 0o750); err != nil {
		t.Fatal(err)
	}

	srcPath := filepath.Join(dir, "source.AppImage")
	content := []byte("fake AppImage content for extract test")
	if err := os.WriteFile(srcPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPlatform()
	if err := p.Extract(context.Background(), srcPath, destDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	staged := filepath.Join(destDir, "source.AppImage")
	got, err := os.ReadFile(staged) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("staged content: got %q, want %q", string(got), string(content))
	}

	info, err := os.Stat(staged)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("staged file is not executable")
	}
}

func TestExtract_SourceNotFound(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, "staging")
	if err := os.Mkdir(destDir, 0o750); err != nil {
		t.Fatal(err)
	}

	p := NewPlatform()
	err := p.Extract(context.Background(), filepath.Join(dir, "nonexistent"), destDir)
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}

// ---------------------------------------------------------------------------
// ArtifactID
// ---------------------------------------------------------------------------

func TestArtifactID_Linux(t *testing.T) {
	p := NewPlatform()
	id := p.ArtifactID()

	if id.OS != "linux" {
		t.Errorf("OS: got %q, want linux", id.OS)
	}
	if id.Arch == "" {
		t.Error("Arch is empty")
	}
	if id.Format != "AppImage" {
		t.Errorf("Format: got %q, want AppImage", id.Format)
	}
}

func TestArtifactID_MatchesManifestEntry(t *testing.T) {
	// Quick sanity check: the ArtifactID should match a plausible
	// manifest entry after MatchArtifact.
	p := NewPlatform()
	id := p.ArtifactID()

	artifacts := []Artifact{
		{OS: "darwin", Arch: "universal", Format: "zip", URL: "https://example.com/mac.zip"},
		{OS: "linux", Arch: id.Arch, Format: "AppImage", URL: "https://example.com/linux.AppImage"},
	}

	got, err := MatchArtifact(artifacts, id)
	if err != nil {
		t.Fatalf("MatchArtifact with own ArtifactID failed: %v", err)
	}
	if got.URL != "https://example.com/linux.AppImage" {
		t.Errorf("URL: got %q, want linux.AppImage", got.URL)
	}
}
