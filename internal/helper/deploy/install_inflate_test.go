package deploy_test

// THE INSTALL SENDS THE ARTIFACT'S OWN BYTES WHERE THE HOST CAN WRITE THE FILE
// ITSELF, AND NEVER REQUIRES THE TOOL THAT DOES IT (nocx-evj4b.1.3).
//
// The artifact is gzip on this side and the host is going to write the
// decompressed helper anyway, so an install that sends the compressed bytes and
// asks the host's gzip to produce the file crosses ~5 MB where the decompressed
// helper is 15.3 MB — measured on the e2e stand's paced link, once per host per
// update. These three tests are that rule and its two halves:
//
//   - the fast path writes the COMPRESSED bytes to the host's disk and asks for
//     one file, then commits exactly as before (rename, verify, marker);
//   - the fallback is every failure the seam can report — a host with no gzip is
//     a 127 the caller sees as an error — and it must end in the plain upload,
//     complete and correct, with nothing of the failed attempt left behind;
//   - the mode is the install's, because the file the host's gzip writes carries
//     the host's umask rather than 0700.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// inflatedTo decompresses in place with Go's own gzip, which is exactly what the
// host's `gzip -d` does: the .gz goes away and its name without the suffix
// becomes the file.
func inflatedTo(t *testing.T, fs *fakeFS, gzPath string) error {
	t.Helper()
	data, err := fs.ReadFile(gzPath)
	if err != nil {
		return err
	}
	payload, err := gunzipBytesErr(data)
	if err != nil {
		return err
	}
	fs.files[gzPath[:len(gzPath)-3]] = payload
	delete(fs.files, gzPath)
	return nil
}

// gunzipBytesErr decompresses without failing the test, so the seam can report
// the error the host's gzip would.
func gunzipBytesErr(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

func TestAnInstallThatTheHostCanDecompressSendsTheArtifactBytes(t *testing.T) {
	fs := newFakeFS()
	var inflated []string
	home := "/home/u"
	inflate := func(_ context.Context, gz string) error {
		inflated = append(inflated, gz)
		return inflatedTo(t, fs, gz)
	}

	binary, hash, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, home,
		deploy.Platform{GOOS: "linux", GOARCH: "amd64"}, nil, deploy.WithInflate(inflate))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if hash != syntheticHash {
		t.Fatalf("hash = %q, want %q", hash, syntheticHash)
	}
	// THE BYTES THAT CROSSED: the compressed artifact, written as one .gz file.
	if len(inflated) != 1 {
		t.Fatalf("the host was asked to decompress %d files, want exactly one", len(inflated))
	}
	if !strings.HasSuffix(inflated[0], ".gz") {
		t.Fatalf("the file handed to the host is %q, want a .gz", inflated[0])
	}
	// AND WHAT IT WROTE IS THE HELPER, complete and at the install's mode.
	if got := fs.files[binary]; !bytes.Equal(got, syntheticPayload) {
		t.Fatalf("the installed binary is %d bytes, want the %d-byte payload", len(got), len(syntheticPayload))
	}
	if mode := fs.modes[binary]; mode != 0o700 {
		t.Fatalf("the installed binary is %o, want 0700 (the host's gzip wrote it with the umask)", mode)
	}
	if !fs.hasMarker(path.Dir(binary)) {
		t.Fatal("the install is not marked complete")
	}
	// Nothing of the compressed file is left behind.
	for name := range fs.files {
		if strings.HasSuffix(name, ".gz") {
			t.Fatalf("%q was left behind after the install", name)
		}
	}
}

func TestAHostWithoutGzipFallsBackToThePlainUpload(t *testing.T) {
	fs := newFakeFS()
	inflate := func(_ context.Context, _ string) error {
		// The host's answer: `sh: gzip: not found`, exit 127.
		return errors.New("inflate: command exited 127: gzip: not found")
	}

	binary, hash, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u",
		deploy.Platform{GOOS: "linux", GOARCH: "amd64"}, nil, deploy.WithInflate(inflate))
	if err != nil {
		t.Fatalf("Ensure fell back and should still succeed: %v", err)
	}
	if hash != syntheticHash {
		t.Fatalf("hash = %q, want %q", hash, syntheticHash)
	}
	// The plain upload sent the DECOMPRESSED payload: the same install the
	// package has always done, over the bytes it always sent.
	if got := fs.files[binary]; !bytes.Equal(got, syntheticPayload) {
		t.Fatalf("the installed binary is %d bytes, want the %d-byte payload", len(got), len(syntheticPayload))
	}
	if mode := fs.modes[binary]; mode != 0o700 {
		t.Fatalf("the installed binary is %o, want 0700", mode)
	}
	if !fs.hasMarker(path.Dir(binary)) {
		t.Fatal("the fallback install is not marked complete")
	}
	for name := range fs.files {
		if strings.HasSuffix(name, ".gz") {
			t.Fatalf("%q was left behind by the failed attempt", name)
		}
	}
}

func TestAHostWhoseDecompressionWritesNothingIsNotTrusted(t *testing.T) {
	// A seam that reports success without producing the file is a host answer
	// the install cannot verify from the seam: the write it thinks it made is
	// missing, and the rename that follows fails rather than committing an
	// empty helper.
	fs := newFakeFS()
	inflate := func(_ context.Context, gz string) error {
		// gzip "succeeded" but wrote nothing: model it by removing the .gz.
		delete(fs.files, gz)
		return nil
	}
	_, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u",
		deploy.Platform{GOOS: "linux", GOARCH: "amd64"}, nil, deploy.WithInflate(inflate))
	if err == nil {
		t.Fatal("an install whose decompression produced no file was accepted")
	}
}
