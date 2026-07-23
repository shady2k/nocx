package update

// The payload round trip is the load-bearing packaging proof (distribution
// design §9.1). The .zip is the updater's payload, so if a real nocx.app does
// not survive `ditto -c -k --keepParent` → `ditto -x -k` with its executable
// bit, symlinks, both universal slices and Wails' ad-hoc code signature intact,
// the updater would ship a bundle that cannot launch — and Go's archive/zip,
// which an earlier design draft proposed, restores none of that (§7.4 step 9).
//
// This is a macOS integration test against a *real* CI-produced artefact, not a
// unit test with a fixture: point NOCX_ROUNDTRIP_APP at a built nocx.app and the
// tests run; release.yml sets it right after the build. Absent that artefact or
// the macOS packaging tools, the tests skip. The file is darwin-only (its name
// ends in _darwin_test.go), so a Linux `go test ./...` never sees it.

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const roundtripAppEnv = "NOCX_ROUNDTRIP_APP"

// roundtripApp returns the .app under test, skipping when the artefact or the
// macOS tooling this test drives is unavailable.
func roundtripApp(t *testing.T) string {
	t.Helper()
	app := os.Getenv(roundtripAppEnv)
	if app == "" {
		t.Skipf("%s is unset; this integration test needs a built .app (release.yml provides one)", roundtripAppEnv)
	}
	for _, tool := range []string{"ditto", "lipo", "codesign"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH; the payload round trip needs macOS packaging tools", tool)
		}
	}
	fi, err := os.Stat(app)
	if err != nil || !fi.IsDir() {
		t.Fatalf("%s=%q is not a bundle directory: %v", roundtripAppEnv, app, err)
	}
	return app
}

// mainExecutable returns the single file under Contents/MacOS — the binary macOS
// launches for the bundle — deriving the name rather than assuming "nocx".
func mainExecutable(t *testing.T, app string) string {
	t.Helper()
	dir := filepath.Join(app, "Contents", "MacOS")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var bins []string
	for _, e := range entries {
		if !e.IsDir() {
			bins = append(bins, e.Name())
		}
	}
	if len(bins) != 1 {
		t.Fatalf("expected exactly one executable in %s, found %v", dir, bins)
	}
	return filepath.Join(dir, bins[0])
}

func runIn(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed tool names and test-controlled paths
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func run(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	return runIn(t, "", name, args...)
}

func archsOf(t *testing.T, bin string) []string {
	t.Helper()
	out, err := run(t, "lipo", "-archs", bin)
	if err != nil {
		t.Fatalf("lipo -archs %s: %v (%s)", bin, err, out)
	}
	return strings.Fields(out)
}

func isUniversal(archs []string) bool {
	var x86, arm bool
	for _, a := range archs {
		switch a {
		case "x86_64":
			x86 = true
		case "arm64":
			arm = true
		}
	}
	return x86 && arm
}

func isExecutable(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode()&0o111 != 0
}

// symlinksUnder maps each symlink's path (relative to root) to its target, so a
// round trip can be checked for having preserved every link exactly.
func symlinksUnder(t *testing.T, root string) map[string]string {
	t.Helper()
	links := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		links[rel] = target
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return links
}

func sameLinks(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// dittoZip packages app exactly as the release pipeline does (§5).
func dittoZip(t *testing.T, app, zipPath string) {
	t.Helper()
	if out, err := run(t, "ditto", "-c", "-k", "--keepParent", "--noqtn", app, zipPath); err != nil {
		t.Fatalf("ditto -c -k: %v (%s)", err, out)
	}
}

func TestPayloadRoundTrip_DittoPreservesBundle(t *testing.T) {
	app := roundtripApp(t)
	srcBin := mainExecutable(t, app)

	// Baseline: the artefact must be what the round trip claims to preserve.
	if !isUniversal(archsOf(t, srcBin)) {
		t.Fatalf("source binary is not universal: %v", archsOf(t, srcBin))
	}
	if out, err := run(t, "codesign", "--verify", "--deep", "--strict", app); err != nil {
		t.Fatalf("source bundle fails codesign before the round trip: %v (%s)", err, out)
	}
	if !isExecutable(t, srcBin) {
		t.Fatal("source binary is not executable")
	}

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "payload.zip")
	dittoZip(t, app, zipPath)

	extract := filepath.Join(dir, "extract")
	// Exactly the extraction the updater performs at §7.4 step 9.
	if out, err := run(t, "ditto", "-x", "-k", zipPath, extract); err != nil {
		t.Fatalf("ditto -x -k: %v (%s)", err, out)
	}

	outApp := filepath.Join(extract, filepath.Base(app))
	if fi, err := os.Stat(outApp); err != nil || !fi.IsDir() {
		t.Fatalf("--keepParent should place %s at the archive root: %v", filepath.Base(app), err)
	}
	outBin := mainExecutable(t, outApp)

	if !isUniversal(archsOf(t, outBin)) {
		t.Errorf("round trip dropped a universal slice: %v", archsOf(t, outBin))
	}
	if out, err := run(t, "codesign", "--verify", "--deep", "--strict", outApp); err != nil {
		t.Errorf("round trip broke the ad-hoc signature: %v (%s)", err, out)
	}
	if !isExecutable(t, outBin) {
		t.Error("round trip dropped the executable bit")
	}
	if src, got := symlinksUnder(t, app), symlinksUnder(t, outApp); !sameLinks(src, got) {
		t.Errorf("round trip changed symlinks: source %v, extracted %v", src, got)
	}

	// It must actually run. --version short-circuits before Wails opens a window
	// (a75.1), so this launches the extracted bundle headlessly and confirms it.
	out, err := run(t, outBin, "--version")
	if err != nil {
		t.Fatalf("extracted binary did not run: %v (%s)", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("extracted binary printed no version")
	}
}

// naiveUnzip extracts with archive/zip the way the rejected first draft would
// have: it copies bytes and ignores the modes ditto stored, so every file lands
// as a plain 0644 regular file with no symlinks. This is not a helper the
// product uses — it exists to prove what does NOT work.
func naiveUnzip(t *testing.T, zipPath, dest string) {
	t.Helper()
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		target := filepath.Join(dest, f.Name) //nolint:gosec // test-controlled archive we just created
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec // deliberately naive: fixed mode is the bug under test
		if err != nil {
			_ = rc.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(out, rc) //nolint:gosec // bounded, test-controlled archive
		_ = rc.Close()
		_ = out.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
	}
}

// A plain archiver is not interchangeable with ditto. Extracting the very same
// ditto-made archive with archive/zip yields a regular, non-executable file
// where the binary was — the exact reason the updater must shell out to
// /usr/bin/ditto (§7.4 step 9, §9). A regression to a Go extractor cannot pass
// this test.
func TestPayloadRoundTrip_NaiveArchiveZipYieldsNonExecutable(t *testing.T) {
	app := roundtripApp(t)
	srcBin := mainExecutable(t, app)
	binRel, err := filepath.Rel(filepath.Dir(app), srcBin)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "payload.zip")
	dittoZip(t, app, zipPath)

	extract := filepath.Join(dir, "naive")
	naiveUnzip(t, zipPath, extract)

	naiveBin := filepath.Join(extract, binRel)
	fi, err := os.Lstat(naiveBin)
	if err != nil {
		t.Fatalf("archive/zip did not produce %s at all: %v", binRel, err)
	}
	if fi.Mode()&0o111 != 0 {
		t.Fatalf("archive/zip produced an executable %s; the property that makes "+
			"/usr/bin/ditto mandatory is not real on this artefact", binRel)
	}
}

// The bead's acceptance names the create side too: an archive made with plain
// `zip` instead of ditto must not round-trip cleanly. `zip` follows symlinks and
// drops metadata that codesign seals over. Where the artefact carries neither a
// symlink nor a signature that plain zip disturbs, there is nothing to corrupt,
// and that is reported as skipped rather than faked — §13 verifies the
// friction-free claim on real hardware.
func TestPayloadRoundTrip_PlainZipCreateBreaksBundle(t *testing.T) {
	app := roundtripApp(t)
	if _, err := exec.LookPath("zip"); err != nil {
		t.Skip("zip not on PATH")
	}

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "plain.zip")
	// cwd = the bundle's parent so the archive stores "nocx.app/…"; -r recurse,
	// -q quiet. Default `zip` follows symlinks and omits ditto's bundle metadata.
	if out, err := runIn(t, filepath.Dir(app), "zip", "-r", "-q", zipPath, filepath.Base(app)); err != nil {
		t.Fatalf("zip -r: %v (%s)", err, out)
	}
	extract := filepath.Join(dir, "extract")
	if out, err := run(t, "ditto", "-x", "-k", zipPath, extract); err != nil {
		t.Fatalf("ditto -x -k of the plain-zip archive: %v (%s)", err, out)
	}
	outApp := filepath.Join(extract, filepath.Base(app))

	srcLinks := symlinksUnder(t, app)
	if len(srcLinks) > 0 && !sameLinks(srcLinks, symlinksUnder(t, outApp)) {
		return // plain zip lost symlinks — demonstrated.
	}
	if _, err := run(t, "codesign", "--verify", "--deep", "--strict", outApp); err != nil {
		return // plain zip broke the signature — demonstrated.
	}
	t.Skipf("artefact has %d symlinks and its signature survived plain zip; "+
		"the create-side discriminator is not exercised here (see design §13)", len(srcLinks))
}
