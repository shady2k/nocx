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

// THE CEILING IS SET FROM A MEASUREMENT, and this one is libghostty-vt's cost,
// measured on the INTEGRATED helper — the runtime, the emulator port and the
// CGo adapter all linked, which is what changes the number. The table below is
// THAT measurement, taken before each helper also began carrying the third-party
// notices (nocx-ygxjv.15), on the artifacts `make helpers` produced then
// (per-target Zig, CGO_ENABLED=1, external linking, statically linked musl on
// Linux, -tags vtmusl):
//
//	linux/amd64   17,044,136   (was 6,610,992 — +10,433,144)
//	linux/arm64   16,303,928   (was 6,452,000 — +9,851,928)
//	darwin/amd64   5,614,557   (was 4,378,173 — +1,236,384)
//	darwin/arm64   5,308,802   (was 4,158,306 — +1,150,496)
//
// The Linux pair carries the whole of it and gained ~10 MB, which is the
// static ghostty archive itself: those two helpers are the ones that link it
// with the pinned musl triple, and musl's own libc is already in their figure.
// The spike's estimate for this was +12.4 MB per Linux helper
// (.internal/spikes/buildmatrix/README.md §4), so the measurement came in
// under it rather than over.
//
// 20 MiB (20,971,520 bytes) leaves 3,927,384 bytes (23%) above the largest,
// which is the same headroom the previous 8 MiB ceiling left above 6,610,992
// — and the reason it is not raised further is that what the ceiling rejects
// is still meaningful: a helper that grew by another archive's worth, or that
// re-acquired a client stack, lands on the wrong side of it.
//
// THE NOTICES ADD 61,248 BYTES, measured rather than assumed: the linux/amd64
// target was rebuilt on 2026-09-13 with the embed directory holding no document
// (internal/helper/notices), 17,064,248 bytes against 17,125,496 — the 61,163
// byte document plus the embed table and alignment. The four artifacts measure
// 17,125,496 / 16,384,376 / 5,692,501 / 5,391,490 bytes in that order on the
// current tree, so the headroom is now 3,846,024 bytes (18%): the difference
// from the table above is the notices AND whatever landed on this branch after
// that measurement, which is why the two are not arithmetic.
const maxHelperBytes int64 = 20 * 1024 * 1024

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
