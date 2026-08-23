package local_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/transfer"
)

// sinkOf is the seam under test, reached exactly the way the composition
// root reaches it: assert filesystem.Uploader on the provider and take the
// sink it hands back. Reaching it any other way would test a route the
// product does not use.
func sinkOf(t *testing.T) transfer.Sink {
	t.Helper()
	p := local.New()
	u, ok := any(p).(filesystem.Uploader)
	if !ok {
		t.Fatal("the local provider does not implement filesystem.Uploader; a browser drop on a local tab has bytes and no path, so it has nothing to upload through (D7, as corrected)")
	}
	sk := u.Sink()
	if sk == nil {
		t.Fatal("Sink() is nil; a live Uploader that returns none is a capability that refuses without saying why")
	}
	return sk
}

func upload(dir, name string, size int64, on transfer.Decision) transfer.Upload {
	return transfer.Upload{DestDir: dir, Name: name, Size: size, OnExists: on}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a path this test built under its own t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestProviderImplementsUploader is the assertion the composition root
// performs, performed here so the day it stops holding is a red test in the
// package that owns the seam rather than a tab that silently refuses.
func TestProviderImplementsUploader(t *testing.T) {
	if _, ok := any(local.New()).(filesystem.Uploader); !ok {
		t.Fatal("local.Provider must implement filesystem.Uploader")
	}
}

// TestSink_WritesTheFileIntoTheDestination is the happy path rule 2 asks
// for on this side: the bytes a browser drop carries land in the tab's
// directory, under the name they were dropped with, with the content they
// had — on the backend's own machine, which is the machine that tab's shell
// is on (R1).
func TestSink_WritesTheFileIntoTheDestination(t *testing.T) {
	dir := t.TempDir()
	var seen []int64
	out, err := sinkOf(t).Put(
		context.Background(),
		upload(dir, "notes.txt", 5, transfer.Overwrite),
		strings.NewReader("hello"),
		func(n int64) { seen = append(seen, n) },
	)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.State != transfer.StateWritten || out.FinalName != "notes.txt" {
		t.Fatalf("outcome %+v, want a written notes.txt", out)
	}
	if len(out.Stranded) != 0 {
		t.Errorf("stranded %v, want nothing left behind", out.Stranded)
	}
	if got := read(t, filepath.Join(dir, "notes.txt")); got != "hello" {
		t.Errorf("content %q, want %q", got, "hello")
	}
	if len(seen) == 0 || seen[len(seen)-1] != 5 {
		t.Errorf("progress %v, want it to end at the declared size", seen)
	}
}

// TestSink_LeavesNoTempBehindOnSuccess closes the temp interval's second
// end at this provider: the directory holds the destination and nothing
// else once Put returns.
func TestSink_LeavesNoTempBehindOnSuccess(t *testing.T) {
	dir := t.TempDir()
	if _, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 1, transfer.Overwrite), strings.NewReader("x"), nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "a.txt" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only a.txt", names)
	}
}

// TestSink_OverwriteReplacesTheDestination is D6 on this provider:
// os.Rename IS rename(2), so the replace is atomic and the fallback's
// two-rename window — the one where the destination holds nothing — is
// never entered here.
func TestSink_OverwriteReplacesTheDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 3, transfer.Overwrite), strings.NewReader("new"), nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.FinalName != "a.txt" {
		t.Errorf("final name %q, want the name asked for", out.FinalName)
	}
	if got := read(t, dest); got != "new" {
		t.Errorf("content %q, want the new content", got)
	}
	// The fallback would have left one of these; PosixRename never reports
	// unsupported here, so neither may exist.
	for _, e := range mustReadDir(t, dir) {
		if strings.Contains(e, ".nocx-bak-") || strings.Contains(e, ".nocx-upload-") {
			t.Errorf("%s survived; the .bak fallback must never run on this path", e)
		}
	}
}

// TestSink_KeepBothPicksAFreeName is D5's suffix search over os: O_EXCL is
// the arbiter, so the reservation is a real file at a real name and the
// original is untouched.
func TestSink_KeepBothPicksAFreeName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 3, transfer.KeepBoth), strings.NewReader("new"), nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.FinalName != "a (1).txt" {
		t.Fatalf("final name %q, want %q", out.FinalName, "a (1).txt")
	}
	if got := read(t, filepath.Join(dir, "a.txt")); got != "old" {
		t.Errorf("the original now reads %q; KeepBoth must not touch it", got)
	}
	if got := read(t, filepath.Join(dir, "a (1).txt")); got != "new" {
		t.Errorf("the new file reads %q, want the uploaded content", got)
	}
}

// TestSink_SkipWritesNothing — the person answered the collision question
// with "skip", so the directory is exactly as they left it.
func TestSink_SkipWritesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 3, transfer.Skip), strings.NewReader("new"), nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.State != transfer.StateSkipped {
		t.Errorf("state %q, want %q", out.State, transfer.StateSkipped)
	}
	if got := read(t, filepath.Join(dir, "a.txt")); got != "old" {
		t.Errorf("content %q, want the file untouched", got)
	}
}

// ── the failure paths, one test per external call that can fail ──────────

// TestSink_RefusesADestinationThatIsNotThere is the create failing. nocx
// does not conjure the directory: it says the write did not happen, and
// leaves nothing behind.
func TestSink_RefusesADestinationThatIsNotThere(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	out, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 1, transfer.Overwrite), strings.NewReader("x"), nil)
	if err == nil {
		t.Fatal("Put into a directory that does not exist succeeded")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %v, want one satisfying errors.Is(fs.ErrNotExist) — the sink's fallback and the transport both read it", err)
	}
	if len(out.Stranded) != 0 {
		t.Errorf("stranded %v, want nothing: nothing was created", out.Stranded)
	}
}

// TestSink_ClassifiesAPermissionRefusal is the trap RemoteFS.Create names.
// A read-only directory must NOT read as a lost O_EXCL race: if it did,
// KeepBoth would spend all 32 attempts on it and then tell the person there
// is no free name, which is false rather than merely vague.
func TestSink_ClassifiesAPermissionRefusal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the write bit, so there is no refusal to classify")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // a path this test built under its own t.TempDir()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // a path this test built under its own t.TempDir()

	_, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 1, transfer.KeepBoth), strings.NewReader("x"), nil)
	if err == nil {
		t.Fatal("Put into a read-only directory succeeded")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error %v, want one satisfying errors.Is(fs.ErrPermission)", err)
	}
	var exhausted *transfer.NameExhaustedError
	if errors.As(err, &exhausted) {
		t.Error("a read-only directory was reported as \"no free name\"; the refusal was left unclassified")
	}
}

// TestSink_RefusesASourceShorterThanDeclared is the reader failing
// mid-transfer — a browser tab closed while its body was streaming. The
// destination must be untouched and the temp must be gone.
func TestSink_RefusesASourceShorterThanDeclared(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := sinkOf(t).Put(context.Background(), upload(dir, "a.txt", 10, transfer.Overwrite), strings.NewReader("short"), nil)
	if err == nil {
		t.Fatal("a body shorter than Content-Length was accepted")
	}
	if got := read(t, dest); got != "old" {
		t.Errorf("the destination reads %q; a failed transfer must not touch it", got)
	}
	if len(out.Stranded) != 0 {
		t.Errorf("stranded %v, want the temp removed — its state is known and it is ours", out.Stranded)
	}
	if names := mustReadDir(t, dir); len(names) != 1 {
		t.Errorf("directory holds %v, want only the untouched destination", names)
	}
}

// TestSink_HonoursCancellation — the transfer is cancelled before it
// starts, so nothing is written and nothing is left behind.
func TestSink_HonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := sinkOf(t).Put(ctx, upload(dir, "a.txt", 1, transfer.Overwrite), strings.NewReader("x"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want context.Canceled", err)
	}
	if len(out.Stranded) != 0 {
		t.Errorf("stranded %v, want nothing", out.Stranded)
	}
	if names := mustReadDir(t, dir); len(names) != 0 {
		t.Errorf("directory holds %v, want nothing: a cancel writes no file", names)
	}
}

// ── the RemoteFS contracts os must keep, asserted through the sink's
// own idiom rather than by reaching for the unexported adapter ───────────

// TestSink_ReplacesAnExistingDestinationWithoutTheFallback pins the one
// contract that decides whether the two-rename window can ever open here:
// PosixRename must succeed against an existing destination, so it never
// reports ErrPosixRenameUnsupported and the fallback is unreachable. Two
// consecutive overwrites are the observable form of it.
func TestSink_ReplacesAnExistingDestinationWithoutTheFallback(t *testing.T) {
	dir := t.TempDir()
	sk := sinkOf(t)
	for _, content := range []string{"one", "two", "three"} {
		if _, err := sk.Put(context.Background(), upload(dir, "a.txt", int64(len(content)), transfer.Overwrite), strings.NewReader(content), nil); err != nil {
			t.Fatalf("Put %q: %v", content, err)
		}
	}
	if got := read(t, filepath.Join(dir, "a.txt")); got != "three" {
		t.Errorf("content %q, want the last write", got)
	}
	if names := mustReadDir(t, dir); len(names) != 1 {
		t.Errorf("directory holds %v, want one file: every promote replaced atomically", names)
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
