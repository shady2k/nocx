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

// The largest stripped target measured after the SSH/SFTP split is 4,212,352
// bytes (darwin/amd64). A 5 MiB ceiling leaves 1,030,528 bytes (24.5%) for
// ordinary helper growth while still rejecting a reintroduced client stack.
const maxHelperBytes int64 = 5 * 1024 * 1024

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
	for name, size := range artifactSizes(t) {
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
