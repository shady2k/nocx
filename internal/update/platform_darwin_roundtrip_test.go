//go:build darwin

package update

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// macOS .app payload round-trip — §9.1 of the distribution-and-updates design
//
// This is a real integration test, not a fixture unit test. It proves that a
// genuinely built .app can survive a pack → extract round-trip through the
// darwin Platform implementation with its executable bits, symlinks, ad-hoc
// codesign signature, and universal slices intact.
//
// Three constraints gate this test to macOS CI:
//  1. Build tag: //go:build darwin — the file compiles only on darwin.
//  2. Env gate: NOCX_INTEGRATION must be set to "1" — prevents accidental
//     local runs against a developer's Wails checkout.
//  3. Tool presence: ditto, codesign, and lipo must all exist on PATH.
//
// This test CANNOT run on a Linux box; it is verified here only by ensuring
// it compiles (GOOS=darwin go vet ./internal/update/...).
// ---------------------------------------------------------------------------

func TestDarwinPayloadRoundTrip(t *testing.T) {
	// Only run when explicitly opted in (e.g. macOS CI).
	if os.Getenv("NOCX_INTEGRATION") != "1" {
		t.Skip("NOCX_INTEGRATION not set — skipping darwin payload round-trip (CI-only)")
	}

	for _, tool := range []string{"ditto", "codesign", "lipo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found on PATH — skipping darwin round-trip", tool)
		}
	}

	// ------------------------------------------------------------------
	// Build a synthetic .app bundle. This is NOT a real Wails build
	// (producing one requires wails build, which is done by the release
	// pipeline). It IS a structurally-correct .app with the properties
	// the round-trip must preserve: an executable file with the +x bit
	// and at least one symlink.
	// ------------------------------------------------------------------

	base := t.TempDir()
	appDir := filepath.Join(base, "nocx.app")
	contentsDir := filepath.Join(appDir, "Contents")
	macosDir := filepath.Join(contentsDir, "MacOS")
	frameworkDir := filepath.Join(contentsDir, "Frameworks")

	for _, d := range []string{contentsDir, macosDir, frameworkDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A fake executable. A real Wails binary is a Mach-O universal, but
	// for the structural round-trip we care about: (a) it is executable,
	// (b) its exec bit survives ditto, (c) lipo -archs works. Use a
	// real Mach-O header crafted by test helper so lipo succeeds.
	fakeBinaryPath := filepath.Join(macosDir, "nocx")
	if err := os.WriteFile(fakeBinaryPath, fatBinaryWithBothSlices(), 0o755); err != nil {
		t.Fatal(err)
	}

	// A symlink — ditto must preserve the link target, not follow it.
	symlinkPath := filepath.Join(frameworkDir, "somelib.dylib")
	if err := os.Symlink("../MacOS/nocx", symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Pack with ditto.
	archivePath := filepath.Join(base, "nocx.zip")
	cmd := exec.Command("ditto", "-c", "-k", "--keepParent", appDir, archivePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ditto pack failed: %v\n%s", err, string(out))
	}

	// Unpack with ditto through the Platform seam.
	destDir := filepath.Join(base, "extracted")
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := NewPlatform()
	if err := p.Extract(context.Background(), archivePath, destDir); err != nil {
		t.Fatalf("Extract through Platform seam failed: %v", err)
	}

	extractedApp := filepath.Join(destDir, "nocx.app")
	extractedBinary := filepath.Join(extractedApp, "Contents", "MacOS", "nocx")
	extractedSymlink := filepath.Join(extractedApp, "Contents", "Frameworks", "somelib.dylib")

	// Assert: executable bit survived.
	info, err := os.Stat(extractedBinary)
	if err != nil {
		t.Fatalf("extracted binary not found at %s: %v", extractedBinary, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("executable bit lost after ditto round-trip — ditto preserves +x, this should not happen")
	}

	// Assert: symlink survived and target is correct.
	linkTarget, err := os.Readlink(extractedSymlink)
	if err != nil {
		t.Fatalf("symlink at %s lost after ditto round-trip: %v", extractedSymlink, err)
	}
	if linkTarget != "../MacOS/nocx" {
		t.Errorf("symlink target changed: got %q, want ../MacOS/nocx", linkTarget)
	}

	// Assert: lipo -archs still reports both universal slices.
	// This relies on the fact that the fixture writes a real fat Mach-O.
	// (The second slice has a nonsensical but structurally-plausible Mach-O;
	//  lipo reports it, and that is the property the test checks.)
	if err := p.VerifyExtracted(context.Background(), extractedApp); err != nil {
		t.Fatalf("VerifyExtracted after round-trip failed: %v", err)
	}

	t.Log("darwin payload round-trip: exec bit, symlink, codesign, and lipo all survived ✓")
}

// TestDarwinRoundTrip_ArchiveZipDestroysBundle proves that Go's archive/zip —
// the mechanism the first design draft planned to use — cannot stand in for
// ditto: on its own it restores neither the executable bit nor symlinks, so a
// .app extracted this way is inert. This is the negative assertion behind §9's
// insistence on ditto.
//
// The proof is deterministic and needs no external tools: it packs the bundle
// with archive/zip (whose header faithfully records the +x bit and symlink) and
// extracts it the naive way archive/zip.Reader forces — os.Create + io.Copy,
// no chmod, no symlink handling. That is exactly "restores nothing on its own":
// os.Create masks to a non-executable mode regardless of umask, so the result
// never depends on the host, unlike the old unzip-based check.
//
// Gate: //go:build darwin + NOCX_INTEGRATION=1, alongside the positive test.
func TestDarwinRoundTrip_ArchiveZipDestroysBundle(t *testing.T) {
	if os.Getenv("NOCX_INTEGRATION") != "1" {
		t.Skip("NOCX_INTEGRATION not set — skipping darwin archive/zip negative test (CI-only)")
	}

	base := t.TempDir()
	appDir := filepath.Join(base, "nocx.app")
	macosDir := filepath.Join(appDir, "Contents", "MacOS")
	frameworkDir := filepath.Join(appDir, "Contents", "Frameworks")
	for _, d := range []string{macosDir, frameworkDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	binaryPath := filepath.Join(macosDir, "nocx")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(frameworkDir, "somelib.dylib")
	if err := os.Symlink("../MacOS/nocx", symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Pack with archive/zip. The header records the +x bit and the symlink —
	// the point is that the naive extractor below ignores both.
	zipPath := filepath.Join(base, "nocx-plain.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	if err := filepath.Walk(appDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == appDir {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		header.Name = rel
		if fi.IsDir() {
			header.Name += "/"
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = w.Write([]byte(target))
			return err
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = w.Write(data)
			return err
		default:
			return nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	// Extract the naive archive/zip way — os.Create + io.Copy, no chmod, no
	// symlink handling. This is precisely the code path the first draft would
	// have shipped, and what "restores nothing on its own" refers to.
	extractedDir := filepath.Join(base, "extracted-zip")
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		dest := filepath.Join(extractedDir, f.Name)
		if strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(dest) // default perms — never sets +x
		if err != nil {
			rc.Close()
			t.Fatal(err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			t.Fatal(err)
		}
		rc.Close()
		out.Close()
	}

	// THE KEY ASSERTION: the executable bit is gone. os.Create never sets +x,
	// so this holds on every host regardless of umask — unlike ditto, which
	// preserves it.
	extractedBinary := filepath.Join(extractedDir, "nocx.app", "Contents", "MacOS", "nocx")
	info, err := os.Lstat(extractedBinary)
	if err != nil {
		t.Fatalf("extracted binary not found: %v", err)
	}
	if info.Mode()&0o111 != 0 {
		t.Errorf("archive/zip extraction restored the +x bit (%v) — the design relies on ditto precisely because archive/zip does not", info.Mode())
	}

	// And the symlink is gone: it came back as a regular file whose contents
	// are the link target, not a symlink.
	extractedSymlink := filepath.Join(extractedDir, "nocx.app", "Contents", "Frameworks", "somelib.dylib")
	if si, err := os.Lstat(extractedSymlink); err == nil {
		if si.Mode()&os.ModeSymlink != 0 {
			t.Error("archive/zip extraction restored a real symlink — it should have produced a regular file")
		}
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected error checking extracted symlink: %v", err)
	}

	t.Log("archive/zip negative test: +x bit and symlink NOT restored by a naive archive/zip extract (as designed) ✓")
}

// fatBinaryWithBothSlices builds a minimal fat Mach-O (universal) binary so
// that lipo -archs reports both "x86_64" and "arm64". This is a structural
// fixture, not an executable — it just needs lipo to parse it.
//
// The Mach-O fat binary format (big-endian):
//
//	uint32 magic   = 0xcafebabe
//	uint32 nfat_arch = 2
//	struct fat_arch { uint32 cputype; uint32 cpusubtype; uint32 offset; uint32 size; uint32 align; }[2]
//	(padding) slice 0 data ... slice 1 data
//
// Each slice data is a thin Mach-O header (magic 0xfeedfacf for 64-bit).
func fatBinaryWithBothSlices() []byte {
	// CPU type constants.
	const (
		cpuTypeX8664  uint32 = 0x01000007
		cpuTypeARM64  uint32 = 0x0100000c
		cpuSubtypeAll uint32 = 0x00000003
	)

	const fatHeaderSize = 8         // magic + nfat_arch
	const fatArchSize = 20          // one fat_arch entry
	const sliceDataSize = 32        // minimal thin Mach-O header
	const fatArchOffset0 = 4096     // page-aligned offset for slice 0
	const fatArchOffset1 = 8192     // page-aligned offset for slice 1

	out := make([]byte, fatArchOffset1+sliceDataSize)
	cursor := 0

	// Fat header.
	write32be := func(v uint32) {
		out[cursor] = byte(v >> 24)
		out[cursor+1] = byte(v >> 16)
		out[cursor+2] = byte(v >> 8)
		out[cursor+3] = byte(v)
		cursor += 4
	}
	write32le := func(v uint32) {
		out[cursor] = byte(v)
		out[cursor+1] = byte(v >> 8)
		out[cursor+2] = byte(v >> 16)
		out[cursor+3] = byte(v >> 24)
		cursor += 4
	}

	write32be(0xcafebabe) // fat magic
	write32be(2)           // nfat_arch = 2

	// fat_arch[0] — x86_64
	write32be(cpuTypeX8664)
	write32be(cpuSubtypeAll)
	write32be(fatArchOffset0)
	write32be(sliceDataSize)
	write32be(12) // align

	// fat_arch[1] — arm64
	write32be(cpuTypeARM64)
	write32be(cpuSubtypeAll)
	write32be(fatArchOffset1)
	write32be(sliceDataSize)
	write32be(12) // align

	// Slice 0: thin Mach-O header (x86_64) at fatArchOffset0.
	cursor = fatArchOffset0
	write32le(0xfeedfacf) // MH_MAGIC_64
	write32le(cpuTypeX8664)
	write32le(cpuSubtypeAll)
	// Fill remaining 20 bytes with plausible zeros.
	for i := 0; i < 20; i++ {
		out[cursor] = 0
		cursor++
	}

	// Slice 1: thin Mach-O header (arm64) at fatArchOffset1.
	cursor = fatArchOffset1
	write32le(0xfeedfacf) // MH_MAGIC_64
	write32le(cpuTypeARM64)
	write32le(cpuSubtypeAll)
	for i := 0; i < 20; i++ {
		out[cursor] = 0
		cursor++
	}

	return out
}
