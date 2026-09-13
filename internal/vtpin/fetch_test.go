package vtpin

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a manifest plus the bytes its hashes are about: a directory
// holding every asset under its published name, exactly as a release does.
type fixture struct {
	manifest *Manifest
	dir      string
	contents map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{manifest: validManifest(), dir: t.TempDir(), contents: map[string][]byte{}}
	f.add(t, f.manifest.Source.Name, "the source tarball")
	f.manifest.Source = f.asset(t, f.manifest.Source.Name)
	for i := range f.manifest.Targets {
		target := &f.manifest.Targets[i]
		f.add(t, target.Archive.Name, "archive bytes for "+target.Name)
		target.Archive = f.asset(t, target.Archive.Name)
	}
	for i := range f.manifest.Targets {
		writeBundle(t, f, i)
	}
	return f
}

func (f *fixture) add(t *testing.T, name, body string) {
	t.Helper()
	blob := []byte(body + " (" + name + ")")
	if err := os.WriteFile(filepath.Join(f.dir, name), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	f.contents[name] = blob
}

func (f *fixture) asset(t *testing.T, name string) Asset {
	t.Helper()
	sum, size, err := HashFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return Asset{Name: name, SHA256: sum, Bytes: size}
}

func (f *fixture) tamper(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte("substituted"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRefusesATamperedArchive(t *testing.T) {
	f := newFixture(t)
	target := f.manifest.Targets[0]
	f.tamper(t, target.Archive.Name)

	server := httptest.NewServer(http.FileServer(http.Dir(f.dir)))
	defer server.Close()

	root := t.TempDir()
	err := f.manifest.Fetch(root, NewFetcher(server.URL), FetchOptions{})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("fetch of a substituted archive returned %v, want a MismatchError", err)
	}
	if mismatch.Want != target.Archive.SHA256 {
		t.Fatalf("mismatch names %q as the pinned hash, want %q", mismatch.Want, target.Archive.SHA256)
	}
	// Nothing may be left where a build would find it: a link that silently
	// uses the substituted bytes is the failure this check exists to stop.
	if _, statErr := os.Stat(VendorArchivePath(root, target.Name)); !os.IsNotExist(statErr) {
		t.Fatalf("the substituted archive was left in place at %s (stat: %v)", VendorArchivePath(root, target.Name), statErr)
	}
	if _, statErr := os.Stat(VendorArchivePath(root, target.Name) + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("a partial download was left behind (stat: %v)", statErr)
	}
}

func TestFetchRefusesATamperedHeadersBundle(t *testing.T) {
	f := newFixture(t)
	target := f.manifest.Targets[1]
	f.tamper(t, target.Headers.Name)

	root := t.TempDir()
	err := f.manifest.Fetch(root, NewFetcher(f.dir), FetchOptions{})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("fetch of a substituted headers bundle returned %v, want a MismatchError", err)
	}
	// The first target verified and unpacked; the second was refused and
	// must not have been unpacked at all.
	if _, statErr := os.Stat(filepath.Join(VendorDir(root, target.Name), "include")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused bundle was still unpacked (stat: %v)", statErr)
	}
	if _, statErr := os.Stat(VendorIncludeDir(root, f.manifest.Targets[0].Name)); statErr != nil {
		t.Fatalf("the first target's headers are missing: %v", statErr)
	}
}

// TestFetchLeavesVerifiedBytesAloneWhenTheNextAssetIsBad is the partial-failure
// case: the tree already holds verified artifacts, the release is then
// tampered with, and the verified bytes must survive untouched — a fetch that
// clears its destination before it verifies turns one bad asset into an
// unbuildable tree.
func TestFetchLeavesVerifiedBytesAloneWhenTheNextAssetIsBad(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	if err := f.manifest.Fetch(root, NewFetcher(f.dir), FetchOptions{}); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	good := VendorArchivePath(root, f.manifest.Targets[0].Name)
	tampered := f.manifest.Targets[1]
	f.tamper(t, tampered.Archive.Name)
	// The tree also has to be missing that one file: a fetch that already
	// holds the pinned bytes does not look at the release again — that is
	// what makes a re-run free — so the tampered asset is only reached by a
	// tree that needs it.
	if err := os.Remove(VendorArchivePath(root, tampered.Name)); err != nil {
		t.Fatal(err)
	}

	err := f.manifest.Fetch(root, NewFetcher(f.dir), FetchOptions{})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("fetch returned %v, want a MismatchError for the tampered target", err)
	}
	got, _, err := HashFile(good)
	if err != nil {
		t.Fatalf("the first target's verified archive is gone: %v", err)
	}
	if got != f.manifest.Targets[0].Archive.SHA256 {
		t.Fatal("the first target's verified archive was rewritten by a later failure")
	}
}

func TestFetchVerifiesWithoutDownloadingTwice(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.FileServer(http.Dir(f.dir)).ServeHTTP(w, r)
	}))
	defer server.Close()

	if err := f.manifest.Fetch(root, NewFetcher(server.URL), FetchOptions{}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	wantRequests := 2 * len(f.manifest.Targets)
	if requests != wantRequests {
		t.Fatalf("%d downloads for %d targets, want %d", requests, len(f.manifest.Targets), wantRequests)
	}
	for _, target := range f.manifest.Targets {
		sum, _, err := HashFile(VendorArchivePath(root, target.Name))
		if err != nil {
			t.Fatal(err)
		}
		if sum != target.Archive.SHA256 {
			t.Fatalf("%s archive on disk is %s, pinned %s", target.Name, sum, target.Archive.SHA256)
		}
		if _, err := os.Stat(filepath.Join(VendorIncludeDir(root, target.Name), "ghostty", "vt.h")); err != nil {
			t.Fatalf("%s headers were not unpacked: %v", target.Name, err)
		}
	}

	// Second pass: everything is already the pinned file, so nothing is
	// downloaded and nothing is rewritten.
	requests = 0
	if err := f.manifest.Fetch(root, NewFetcher(server.URL), FetchOptions{}); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if requests != 0 {
		t.Fatalf("a second fetch made %d requests; a verified tree must not be re-downloaded", requests)
	}
}

func TestFetchRefusesAMissingAssetByName(t *testing.T) {
	f := newFixture(t)
	gone := f.manifest.Targets[2].Archive.Name
	if err := os.Remove(filepath.Join(f.dir, gone)); err != nil {
		t.Fatal(err)
	}
	err := f.manifest.Fetch(t.TempDir(), NewFetcher(f.dir), FetchOptions{})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) || !mismatch.Missing {
		t.Fatalf("fetch returned %v, want a MismatchError saying %s is not there", err, gone)
	}
	if mismatch.Asset != gone {
		t.Fatalf("the missing-asset error names %q, want %q", mismatch.Asset, gone)
	}
}

func TestFetchWithSourceKeepsTheControlledCopy(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	// No targets: this test is about the source tarball. Fetch does not
	// require a target list — Validate does, and the fixture is validated by
	// its other tests.
	f.manifest.Targets = nil
	if err := f.manifest.Fetch(root, NewFetcher(f.dir), FetchOptions{WithSource: true}); err != nil {
		t.Fatalf("fetch --with-source: %v", err)
	}
	path := filepath.Join(SourceDir(root), f.manifest.Source.Name)
	got, _, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != f.manifest.Source.SHA256 {
		t.Fatalf("the source copy on disk is %s, pinned %s", got, f.manifest.Source.SHA256)
	}

	// Without the flag, the source is not fetched at all: a build does not
	// need it, and a job that downloads 10 MB it never reads is a job that
	// waits longer for nothing.
	other := t.TempDir()
	if err := f.manifest.Fetch(other, NewFetcher(f.dir), FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SourceDir(other)); !os.IsNotExist(err) {
		t.Fatalf("the source was fetched without --with-source (stat: %v)", err)
	}
}

func TestResolveAssetUsesTheManifestTemplate(t *testing.T) {
	m := validManifest()
	target := m.Targets[0]
	got := m.ResolveAsset(target.Archive)
	want := "https://example.invalid/releases/download/" + m.Release.Tag + "/" + target.Archive.Name
	if got != want {
		t.Fatalf("ResolveAsset = %q, want %q", got, want)
	}
}

func TestFetcherReadsFileURLs(t *testing.T) {
	f := newFixture(t)
	f.manifest.Targets = nil
	root := t.TempDir()
	base := "file://" + filepath.ToSlash(f.dir)
	if err := f.manifest.Fetch(root, NewFetcher(base), FetchOptions{WithSource: true}); err != nil {
		t.Fatalf("fetch from %s: %v", base, err)
	}
	if _, err := os.Stat(filepath.Join(SourceDir(root), f.manifest.Source.Name)); err != nil {
		t.Fatalf("nothing was fetched from a file:// base: %v", err)
	}
}

func TestFetchReportsWhatItDid(t *testing.T) {
	f := newFixture(t)
	f.manifest.Targets = nil
	var lines []string
	opts := FetchOptions{WithSource: true, Log: func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}}
	if err := f.manifest.Fetch(t.TempDir(), NewFetcher(f.dir), opts); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("fetch logged %v; a build log has to say whether it downloaded or verified", lines)
	}
	if !strings.Contains(lines[0], "fetched") {
		t.Fatalf("the log line %q does not say whether the file was downloaded or verified", lines[0])
	}
}
