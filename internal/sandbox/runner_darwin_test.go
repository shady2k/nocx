//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeatbeltEscapingAndFilesystemRights(t *testing.T) {
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
			{Path: readOnly, Access: ReadOnly, Kind: DirectoryRoot, Provenance: StandardRoot},
			{Path: readWrite, Access: ReadWrite, Kind: DirectoryRoot, Provenance: StandardRoot},
			{Path: "/bin/sh", Access: ReadOnly, Kind: ArtifactRoot, Provenance: TrustedArtifactRoot},
			{Path: "/dev/null", Access: ReadWrite, Kind: DeviceRoot, Provenance: WritableDeviceRoot},
		},
	}
	for _, path := range baseline("darwin") {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			t.Fatal(statErr)
		}
		actual, canonicalErr := canonicalDir(path)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		policy.Roots = append(policy.Roots, Root{Path: actual, Access: ReadOnly, Kind: DirectoryRoot, Provenance: SystemRoot})
	}
	const observerNonce = "0123456789abcdef0123456789abcdef"
	profile, err := CompileSeatbeltProfile(policy, observerNonce)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, `(deny default (with message "`+observerNonce+`"))`) {
		t.Fatal("Seatbelt profile does not annotate default denials with the observer nonce")
	}
	file, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	written := make(chan error, 1)
	go func() {
		_, writeErr := writer.WriteString(profile)
		closeErr := writer.Close()
		if writeErr != nil {
			written <- writeErr
		} else {
			written <- closeErr
		}
	}()
	const script = `exec 3<&-; test "$(cat "$1")" = readable || exit 10; if printf changed >"$1" 2>/dev/null; then exit 11; fi; printf writable >"$2/out.txt"`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, seatbeltExecutable, "-f", "/dev/fd/3", "/bin/sh", "-c", script, "seatbelt-test", input, readWrite)
	cmd.ExtraFiles = []*os.File{file}
	cmd.Env = []string{"PATH=/bin:/usr/bin"}
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("Seatbelt RO/RW policy failed: %v (%s)", runErr, output)
	}
	if writeErr := <-written; writeErr != nil {
		t.Fatal(writeErr)
	}
	data, err := os.ReadFile(input)
	if err != nil || string(data) != "readable" {
		t.Fatalf("read-only file changed: err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(readWrite, "out.txt")); err != nil || string(data) != "writable" {
		t.Fatalf("write root not usable: err=%v", err)
	}
}
