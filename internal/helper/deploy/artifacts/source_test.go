//go:build helperacceptance

package artifacts

import (
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// These checks run only in the release helpers job, after its initial
// `make helpers` AND its `make helper-local`. That job owns both artifact
// directories and invokes this package with `-tags helperacceptance`; ordinary
// go test never writes build output.

var helperArtifactTargets = []string{
	"nocx-helper-linux-amd64.gz",
	"nocx-helper-linux-arm64.gz",
	"nocx-helper-darwin-amd64.gz",
	"nocx-helper-darwin-arm64.gz",
}

// localHelperArtifactDir is the second directory: this machine's own helper,
// which is the same binary with an ssh client linked in (nocx_local_ssh,
// nocx-50w7p.7). Every check below asks the same question of it that it asks of
// the four above — an artifact that ships without its licences, past the
// ceiling or unreproducible does not become acceptable by being installed
// locally rather than uploaded.
const localHelperArtifactDir = "local"

// helperArtifactPaths is every artifact this tree holds, in both directories,
// keyed the way a failure message needs to name them.
//
// THE LOCAL NAMES ARE READ, NOT LISTED, and that is a property of the build
// rather than laziness: `make helper-local` builds the host platform alone by
// default and every platform HELPER_LOCAL_TARGETS names otherwise — the release
// names linux/amd64, darwin/amd64 and darwin/arm64, because those are the
// platforms its two apps can run on. A list written here would be a second
// statement of that one and would go stale silently. The two things asserted
// about the directory are the two that are true of every build: it is not
// empty, and the HOST's own variant is in it — which is the artifact this job
// runs, and the one `make helper-local` always builds.
func helperArtifactPaths(t *testing.T) map[string]string {
	t.Helper()
	root := moduleRoot(t)
	paths := make(map[string]string, len(helperArtifactTargets)+1)
	for _, name := range helperArtifactTargets {
		paths[name] = filepath.Join(root, helperArtifactDir, name)
	}
	for _, name := range localHelperArtifactNames(t, root) {
		paths[localHelperArtifactDir+"/"+name] = filepath.Join(root, helperArtifactDir, localHelperArtifactDir, name)
	}
	return paths
}

func localHelperArtifactNames(t *testing.T, root string) []string {
	t.Helper()
	dir := filepath.Join(root, helperArtifactDir, localHelperArtifactDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v — every pane on this machine is served by an artifact in it, so a release that built none is not one a check may pass over", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gz") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("%s holds no artifact: `make helper-local` did not run, or it wrote somewhere //go:embed does not read", dir)
	}
	host := "nocx-helper-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
	if !slices.Contains(names, host) {
		t.Fatalf("%s holds %v and this host is %s/%s: the host's own variant must be among them, because this job runs it and because `make helper-local` builds the host platform by default", dir, names, runtime.GOOS, runtime.GOARCH)
	}
	return names
}

// sortedNames is a stable order for a log line or a walk, because a map's is
// not and two runs of the same job should read the same way.
func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
// byte document plus the embed table and alignment. The difference from the
// table above is the notices AND whatever landed on the branch after that
// measurement, which is why the two are not arithmetic.
//
// THE LOCAL VARIANTS ARE MEASURED HERE TOO (nocx-50w7p.7), and the largest of
// them is what the ceiling now has least room over. Measured on this tree on
// 2026-09-13, per target, after `make helpers` and `make helper-local`:
//
//	deployed  linux/amd64   17,125,744
//	          linux/arm64   16,384,688
//	          darwin/amd64   5,692,501
//	          darwin/arm64   5,391,490
//	local     linux/amd64   18,644,200   = the deployed figure + 1,518,456
//
// The 1.5 MB is the ssh client and nothing else: same source, same archives,
// same flags, one build tag apart. The headroom above the largest artifact is
// therefore 2,327,320 bytes (11%), down from 3,846,024 before the local
// variant existed — and the ceiling is deliberately NOT raised, because what
// it exists to catch is a helper that gained another archive's worth (~10 MB)
// or that re-acquired a client stack, and one client stack is the difference
// this variant IS. Those two darwin local numbers are absent because this host
// is linux/amd64; the release's helpers job builds and measures all three.
const maxHelperBytes int64 = 20 * 1024 * 1024

func TestMakeHelpersIsIdempotent(t *testing.T) {
	first := artifactSizes(t)
	runMakeHelpers(t)
	second := artifactSizes(t)

	for name, before := range first {
		after, still := second[name]
		if !still {
			t.Fatalf("%s disappeared across a rebuild: the two targets a release runs must produce the same set of artifacts twice", name)
		}
		if before != after {
			t.Fatalf("the helper build is not idempotent for %s: first decompressed size %d, second %d", name, before, after)
		}
	}
	if len(second) != len(first) {
		t.Fatalf("a rebuild changed the artifact set: %d artifacts before, %d after", len(first), len(second))
	}
}

func TestHelperArtifactsStayBelowSizeCeiling(t *testing.T) {
	// THE MEASUREMENT HOOK. The release helpers job runs with -v on a failure
	// and always prints a summary line, so the numbers a budget decision needs
	// are in the job's own output rather than in somebody's terminal: the
	// ceiling, the largest artifact, and each one's size.
	//
	// Every artifact in both directories is measured, the local variants
	// included: this machine's own helper is the same program plus an ssh
	// client, it is embedded in the app a person downloads, and a budget
	// derived from the deployed four alone would not have seen it.
	sizes := artifactSizes(t)
	names := make([]string, 0, len(sizes))
	for name := range sizes {
		names = append(names, name)
	}
	sort.Strings(names)
	largest, largestName := int64(0), ""
	for _, name := range names {
		size := sizes[name]
		if size > largest {
			largest, largestName = size, name
		}
		t.Logf("helper artifact %s: %d bytes decompressed", name, size)
	}
	t.Logf("largest helper artifact %s: %d bytes; ceiling %d bytes; headroom %d bytes",
		largestName, largest, maxHelperBytes, maxHelperBytes-largest)
	for _, name := range names {
		if size := sizes[name]; size > maxHelperBytes {
			t.Fatalf("helper artifact %s exceeds size ceiling: decompressed size %d, ceiling %d", name, size, maxHelperBytes)
		}
	}
}

func runMakeHelpers(t *testing.T) {
	t.Helper()
	root := moduleRoot(t)
	// THE LOCAL TARGETS COME FROM WHAT IS ON DISK. `make helper-local` builds
	// the host platform alone by default, so a job that built three (the
	// release: both darwin slices and linux/amd64) would have the other two
	// compared against themselves — unchanged because nothing rebuilt them,
	// which is not an idempotence check. Naming the platforms found in bin/local
	// is what makes the second half of this test a REBUILD of everything the
	// tree holds.
	targets := make([]string, 0, 4)
	for _, name := range localHelperArtifactNames(t, root) {
		plat := strings.TrimSuffix(strings.TrimPrefix(name, "nocx-helper-"), ".gz")
		goos, goarch, parsed := strings.Cut(plat, "-")
		if !parsed || goos == "" || goarch == "" {
			t.Fatalf("local artifact %s is not named nocx-helper-<goos>-<goarch>.gz", name)
		}
		targets = append(targets, goos+"/"+goarch)
	}
	cmd := exec.Command("make", "helpers", "helper-local", "HELPER_LOCAL_TARGETS="+strings.Join(targets, " "))
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make helpers helper-local: %v\n%s", err, output)
	}
}

func artifactSizes(t *testing.T) map[string]int64 {
	t.Helper()
	paths := helperArtifactPaths(t)
	names := sortedNames(paths)
	sizes := make(map[string]int64, len(names))
	for _, name := range names {
		file, err := os.Open(paths[name])
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
