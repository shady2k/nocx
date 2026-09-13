package vtpin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// writeBundle replaces target i's headers asset with a real bundle of real
// headers, and re-pins it — the fetch tests need a bundle that unpacks, and a
// hand-written one would measure the fixture rather than the code.
func writeBundle(t *testing.T, f *fixture, i int) {
	t.Helper()
	target := &f.manifest.Targets[i]
	dir := filepath.Join(t.TempDir(), "ghostty")
	if err := os.MkdirAll(filepath.Join(dir, "vt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vt.h"), []byte("#pragma once // "+target.Name), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vt", "terminal.h"), []byte("// terminal"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.dir, target.Headers.Name)
	if err := PackHeaders(dir, out); err != nil {
		t.Fatal(err)
	}
	target.Headers = f.asset(t, target.Headers.Name)
}

func TestPackHeadersIsDeterministic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ghostty")
	if err := os.MkdirAll(filepath.Join(dir, "vt"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vt.h", "vt/terminal.h", "vt/key/encoder.h"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("contents of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := filepath.Join(t.TempDir(), "one.tar.gz")
	second := filepath.Join(t.TempDir(), "two.tar.gz")
	if err := PackHeaders(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := PackHeaders(dir, second); err != nil {
		t.Fatal(err)
	}
	a, _, err := HashFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := HashFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("two packs of one directory differ: %s vs %s — the manifest pins this hash", a, b)
	}
	// The gzip stream must not carry a modification time either: that is the
	// byte a tar-and-gzip pipeline leaks and this one must not.
	raw, err := os.ReadFile(first) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if raw[4] != 0 || raw[5] != 0 || raw[6] != 0 || raw[7] != 0 {
		t.Fatalf("gzip header carries an mtime (bytes %v); the bundle would differ between runs", raw[4:8])
	}
}

func TestPackUnpackRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ghostty")
	if err := os.MkdirAll(filepath.Join(dir, "vt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vt.h"), []byte("top"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vt", "terminal.h"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "h.tar.gz")
	if err := PackHeaders(dir, bundle); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := UnpackHeaders(bundle, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "ghostty", "vt", "terminal.h")) //nolint:gosec // a file this test just unpacked
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "inner" {
		t.Fatalf("unpacked %q, want %q", got, "inner")
	}
}

func TestPackRefusesAnEmptyDirectory(t *testing.T) {
	if err := PackHeaders(t.TempDir(), filepath.Join(t.TempDir(), "empty.tar.gz")); err == nil {
		t.Fatal("PackHeaders packed an empty directory; the manifest would pin a hash of nothing")
	}
}

func TestUnpackRefusesAPathThatEscapes(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "evil.tar.gz")
	file, err := os.Create(bundle) //nolint:gosec // a bundle this test is deliberately forging
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	body := []byte("owned")
	if err := tw.WriteHeader(&tar.Header{Name: "../../escaped.h", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := UnpackHeaders(bundle, dest); err == nil {
		t.Fatal("UnpackHeaders accepted a traversing entry")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escaped.h")); !os.IsNotExist(err) {
		t.Fatalf("the traversal wrote outside the destination (stat: %v)", err)
	}
}

func TestUnpackRefusesASymlinkEntry(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ghostty/vt.h", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "link.tar.gz")
	if err := os.WriteFile(bundle, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UnpackHeaders(bundle, t.TempDir()); err == nil {
		t.Fatal("UnpackHeaders accepted a symlink entry")
	}
}
