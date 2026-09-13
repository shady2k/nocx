//go:build helperacceptance

package artifacts

import (
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These checks run only in the release helpers job, after its initial
// `make helpers`. That job owns the artifact directory and invokes this
// package with `-tags helperacceptance`; ordinary go test never writes build
// output.

var helperArtifactTargets = []string{
	"nocx-helper-linux-amd64.gz",
	"nocx-helper-linux-arm64.gz",
	"nocx-helper-darwin-amd64.gz",
	"nocx-helper-darwin-arm64.gz",
}

// THE CEILING IS SET FROM A MEASUREMENT, and this one is the CGo switch's
// cost rather than the library's. Measured 2026-09-13 with
// third_party/libghostty-vt/scripts/measure-helper-size.sh, on the artifacts
// `make helpers` now produces (per-target Zig, CGO_ENABLED=1, external
// linking, statically linked musl on Linux):
//
//	linux/amd64   6,610,992
//	linux/arm64   6,452,000
//	darwin/amd64  4,378,173
//	darwin/arm64  4,158,306
//
// The Linux pair moved from ~4.2 MB to ~6.6 MB because a static musl binary
// carries its libc; that is the price of the property Makefile's helpers
// comment protects, and it was paid deliberately rather than measured away.
// 8 MiB leaves 1,777,616 bytes (21%) above the largest, which is the same
// proportion the previous 5 MiB ceiling left above 4,212,352 — and it still
// rejects a reintroduced client stack.
//
// IT IS NOT THE INTEGRATION BUDGET. The helper links libghostty-vt in
// nocx-ygxjv.2, and the spike measured roughly +12.4 MB per Linux helper for a
// probe that links it (.internal/spikes/buildmatrix/README.md §4). That bump
// must be derived from a REAL helper — through the same script and from the
// sizes the test below logs — rather than applied in advance, because a
// ceiling raised ahead of the measurement measures nothing.
const maxHelperBytes int64 = 8 * 1024 * 1024

func TestMakeHelpersIsIdempotent(t *testing.T) {
	first := artifactSizes(t)
	runMakeHelpers(t)
	second := artifactSizes(t)

	for _, name := range helperArtifactTargets {
		if first[name] != second[name] {
			t.Fatalf("make helpers is not idempotent for %s: first decompressed size %d, second %d", name, first[name], second[name])
		}
	}
}

func TestHelperArtifactsStayBelowSizeCeiling(t *testing.T) {
	// THE MEASUREMENT HOOK. The release helpers job runs with -v on a failure
	// and always prints a summary line, so the numbers a budget decision needs
	// are in the job's own output rather than in somebody's terminal: the
	// ceiling, the largest artifact, and each target's size.
	sizes := artifactSizes(t)
	largest := int64(0)
	for _, name := range helperArtifactTargets {
		size := sizes[name]
		if size > largest {
			largest = size
		}
		t.Logf("helper artifact %s: %d bytes decompressed", name, size)
	}
	t.Logf("largest helper artifact %d bytes; ceiling %d bytes; headroom %d bytes",
		largest, maxHelperBytes, maxHelperBytes-largest)
	for name, size := range sizes {
		if size > maxHelperBytes {
			t.Fatalf("helper artifact %s exceeds size ceiling: decompressed size %d, ceiling %d", name, size, maxHelperBytes)
		}
	}
}

func runMakeHelpers(t *testing.T) {
	t.Helper()
	root := moduleRoot(t)
	cmd := exec.Command("make", "helpers")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make helpers: %v\n%s", err, output)
	}
}

func artifactSizes(t *testing.T) map[string]int64 {
	t.Helper()
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", "helper", "deploy", "artifacts", "bin")
	entries := append([]string(nil), helperArtifactTargets...)
	sort.Strings(entries)
	sizes := make(map[string]int64, len(entries))
	for _, name := range entries {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("open helper artifact %s: %v", name, err)
		}
		zr, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			t.Fatalf("open gzip helper artifact %s: %v", name, err)
		}
		size, err := io.Copy(io.Discard, zr)
		closeErr := zr.Close()
		fileErr := file.Close()
		if err != nil {
			t.Fatalf("read helper artifact %s: %v", name, err)
		}
		if closeErr != nil || fileErr != nil {
			t.Fatalf("close helper artifact %s: gzip=%v file=%v", name, closeErr, fileErr)
		}
		sizes[name] = size
	}
	return sizes
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == "/dev/null" {
		t.Fatalf("go env GOMOD returned %q", gomod)
	}
	return filepath.Dir(gomod)
}
