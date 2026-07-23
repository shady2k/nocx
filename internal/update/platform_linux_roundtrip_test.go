//go:build linux

package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Linux AppImage structural round-trip — ADR-0006 / bead nocx-3dk
//
// This test proves the exec bit and exact-byte integrity survive a synthetic
// Extract → VerifyExtracted → Exchange round-trip through the linux Platform,
// using a minimal AppImage-shaped fixture built in the test.
//
// The real-artefact proof (a genuine CI-produced AppImage from nocx-mbu
// passing through the same seam) runs in CI; this structural test is the
// local half that verifies the seam logic on any Linux box.
// ---------------------------------------------------------------------------

// makeAppImageFixture creates a minimal AppImage-shaped file with:
//   - Correct ELF header magic (bytes 0–3)
//   - Correct AppImage type-2 magic at offset 8
//   - A random payload body so we can assert byte-level integrity
//   - The executable bit set
//
// Returns the path to the created file and its exact content bytes.
func makeAppImageFixture(t *testing.T, dir, name string) (string, []byte) {
	t.Helper()

	// 256 bytes of unique random payload (the "app").
	payload := make([]byte, 256)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	header := []byte{
		0x7f, 0x45, 0x4c, 0x46, // ELF magic
		0x02,             // 64-bit
		0x01,             // little-endian
		0x01,             // ELF version
		0x00,             // SYSV
		0x41, 0x49, 0x02, // AppImage type-2 magic
	}

	content := append(header, payload...)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	return path, content
}

func TestLinuxAppImageStructuralRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()

	// Step 1 — create a synthetic AppImage fixture.
	srcPath, srcContent := makeAppImageFixture(t, base, "nocx-0.2.0-x86_64.AppImage")

	// Step 2 — Extract into a staging directory.
	stagingDir := filepath.Join(base, "staging")
	if err := os.Mkdir(stagingDir, 0o755); err != nil { //nolint:gosec // test-controlled dir
		t.Fatal(err)
	}

	p := NewPlatform()
	if err := p.Extract(ctx, srcPath, stagingDir); err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	// Step 3 — the staged file must exist and have the correct name.
	stagedPath := filepath.Join(stagingDir, filepath.Base(srcPath))
	if _, err := os.Stat(stagedPath); err != nil {
		t.Fatalf("staged AppImage not found at %s: %v", stagedPath, err)
	}

	// Step 4 — VerifyExtracted must accept the staged file.
	if err := p.VerifyExtracted(ctx, stagedPath); err != nil {
		t.Fatalf("VerifyExtracted rejected valid staged AppImage: %v", err)
	}

	// Step 5 — byte-level integrity: the staged file must be identical
	// to the original.
	stagedContent, err := os.ReadFile(stagedPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stagedContent, srcContent) {
		t.Errorf("staged AppImage bytes differ from original — integrity lost during Extract")
		t.Errorf("  original len: %d, staged len: %d", len(srcContent), len(stagedContent))
		if len(srcContent) == len(stagedContent) {
			for i := 0; i < len(srcContent); i++ {
				if srcContent[i] != stagedContent[i] {
					t.Errorf("  first diff at byte %d: 0x%02x vs 0x%02x", i, srcContent[i], stagedContent[i])
					break
				}
			}
		}
	}

	// Step 6 — exec bit must be set on the staged file.
	info, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("executable bit lost during Extract — staged AppImage is not +x")
	}

	// Step 7 — Exchange: swap the staged file with a "currently installed" one.
	installPath := filepath.Join(base, "nocx-current.AppImage")
	installContent := []byte("previous-version-content")
	if err = os.WriteFile(installPath, installContent, 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}

	if err = p.Exchange(ctx, installPath, stagedPath); err != nil {
		t.Fatalf("Exchange failed: %v", err)
	}

	// Step 8 — after Exchange:
	//   installPath must now hold the new AppImage (the staged content).
	//   stagedPath must now hold the old content.
	gotNew, err := os.ReadFile(installPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotNew, srcContent) {
		t.Error("after Exchange, installPath does not contain the new AppImage")
	}

	gotOld, err := os.ReadFile(stagedPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotOld, installContent) {
		t.Error("after Exchange, replacementPath does not contain the previous AppImage")
	}

	// Step 9 — the exchanged-in file must still pass VerifyExtracted.
	// (installPath now holds the new AppImage.)
	if err := p.VerifyExtracted(ctx, installPath); err != nil {
		t.Fatalf("VerifyExtracted rejected the exchanged AppImage: %v", err)
	}

	t.Logf("linux AppImage structural round-trip: Extract → VerifyExtracted → Exchange all passed ✓ (fixture size: %d bytes)", len(srcContent))
}

func TestLinuxAppImageRoundTrip_VariousPayloadSizes(t *testing.T) {
	// Structural integrity should hold for a range of AppImage sizes,
	// from tiny to a plausible real-world size.
	ctx := context.Background()

	sizes := []int{32, 512, 4096, 65536}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			base := t.TempDir()

			payload := make([]byte, size)
			if _, err := rand.Read(payload); err != nil {
				t.Fatal(err)
			}

			header := []byte{
				0x7f, 0x45, 0x4c, 0x46,
				0x02, 0x01, 0x01, 0x00,
				0x41, 0x49, 0x02,
			}

			full := append(header, payload...)
			srcPath := filepath.Join(base, "fixture.AppImage")
			if err := os.WriteFile(srcPath, full, 0o755); err != nil { //nolint:gosec // test fixture must be executable
				t.Fatal(err)
			}

			stagingDir := filepath.Join(base, "staging")
			if err := os.Mkdir(stagingDir, 0o755); err != nil { //nolint:gosec // test-controlled dir
				t.Fatal(err)
			}

			p := NewPlatform()
			if err := p.Extract(ctx, srcPath, stagingDir); err != nil {
				t.Fatalf("Extract failed: %v", err)
			}

			stagedPath := filepath.Join(stagingDir, "fixture.AppImage")
			staged, err := os.ReadFile(stagedPath) //nolint:gosec // test-controlled path
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(staged, full) {
				t.Fatalf("byte integrity lost for size %d", size)
			}

			if err := p.VerifyExtracted(ctx, stagedPath); err != nil {
				t.Fatalf("VerifyExtracted failed for size %d: %v", size, err)
			}
		})
	}
}
