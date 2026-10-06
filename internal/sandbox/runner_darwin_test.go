//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSeatbeltEscapingAndFilesystemRights(t *testing.T) {
	if err := backendAvailable(); err != nil {
		t.Fatalf("native Seatbelt unavailable: %v", err)
	}
	root := t.TempDir()
	weird := filepath.Join(root, "quoted-\"\\-ü")
	readOnly := filepath.Join(weird, "read-only")
	readWrite := filepath.Join(weird, "read-write")
	runtimeRoot := filepath.Join(root, "runtime")
	for _, dir := range []string{readOnly, readWrite, runtimeRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	readOnly, err = canonicalDir(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	readWrite, err = canonicalDir(readWrite)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot, err = canonicalDir(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(readOnly, "input.txt")
	if err := os.WriteFile(input, []byte("readable"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Version: PolicyVersion, Backend: MacOSSeatbelt, BackendVersion: MacOSBaselineVersion,
		Runtime: RuntimePaths{Root: runtimeRoot},
		Roots: []Root{
			{Path: "/usr", Access: ReadOnly, Kind: DirectoryRoot, Provenance: SystemRoot},
			{Path: "/bin", Access: ReadOnly, Kind: DirectoryRoot, Provenance: SystemRoot},
			{Path: "/System/Library", Access: ReadOnly, Kind: DirectoryRoot, Provenance: SystemRoot},
			{Path: readOnly, Access: ReadOnly, Kind: DirectoryRoot, Provenance: StandardRoot},
			{Path: readWrite, Access: ReadWrite, Kind: DirectoryRoot, Provenance: StandardRoot},
			{Path: "/bin/sh", Access: ReadOnly, Kind: ArtifactRoot, Provenance: TrustedArtifactRoot},
			{Path: "/dev/null", Access: ReadWrite, Kind: DeviceRoot, Provenance: WritableDeviceRoot},
		},
	}
	profile, err := CompileSeatbeltProfile(policy)
	if err != nil {
		t.Fatal(err)
	}
	file, err := privateSeatbeltProfile(runtimeRoot, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	const script = `exec 3<&-; test "$(cat "$1")" = readable || exit 10; if printf changed >"$1" 2>/dev/null; then exit 11; fi; printf writable >"$2/out.txt"`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, seatbeltExecutable, "-f", "/dev/fd/3", "/bin/sh", "-c", script, "seatbelt-test", input, readWrite)
	cmd.ExtraFiles = []*os.File{file}
	cmd.Env = []string{"PATH=/bin:/usr/bin"}
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("Seatbelt RO/RW policy failed: %v (%s)", runErr, output)
	}
	data, err := os.ReadFile(input)
	if err != nil || string(data) != "readable" {
		t.Fatalf("read-only file changed: err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(readWrite, "out.txt")); err != nil || string(data) != "writable" {
		t.Fatalf("write root not usable: err=%v", err)
	}
}
