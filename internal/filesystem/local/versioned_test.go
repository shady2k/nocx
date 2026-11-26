package local

// The local provider's conditional write (stage 10, nocx-a62cl.11.1): the I/O
// that used to live in hashline, behind the seam the helper already had. These
// tests are about the guarantees a caller relies on — a changed file is refused,
// an occupied name is refused, a failed install leaves the original bytes, the
// file's mode survives — and each refusal is paired with the success it protects
// (AGENTS.md testing rule 3).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
)

// writeVersionFixture writes a file with a mode the install has to preserve.
// The mode is CHMOD'ed rather than left to the create call, because a create is
// subject to the process umask and the assertion is about the install, not
// about this machine's umask.
func writeVersionFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil { //nolint:gosec // the mode IS the fixture: the install must preserve it
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	if err := os.Chmod(path, 0o640); err != nil { //nolint:gosec // G302: the mode IS the fixture, see above
		t.Fatalf("Chmod(%s): %v", path, err)
	}
}

func TestReadVersionAnswersTheVersionOfTheBytesItReturned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	writeVersionFixture(t, path, "one\ntwo\n")
	data, version, err := New().ReadVersion(context.Background(), path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("bytes = %q, want the whole file", data)
	}
	if version.Size != int64(len(data)) || version.SHA256 != filesystem.ContentDigest(data) || version.MtimeNs == 0 {
		t.Fatalf("version = %+v, want the size and digest of exactly these bytes", version)
	}
}

func TestReadVersionRefusesAFileOverItsBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")
	writeVersionFixture(t, path, "0123456789")
	_, _, err := New().ReadVersion(context.Background(), path, 4)
	var limit *filesystem.ErrByteLimit
	if !errors.As(err, &limit) {
		t.Fatalf("ReadVersion error = %v, want *ErrByteLimit", err)
	}
	if limit.Limit != 4 || limit.Observed != 10 {
		t.Fatalf("limit = %+v, want the observed size and the bound", limit)
	}
}

func TestReadVersionRefusesAChangeLandingDuringTheRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "racing.txt")
	writeVersionFixture(t, path, "before\n")
	p := New()
	p.afterRead = func() {
		if err := os.WriteFile(path, []byte("after the read\n"), 0o640); err != nil { //nolint:gosec // a private fixture
			t.Errorf("WriteFile during read: %v", err)
		}
	}
	_, _, err := p.ReadVersion(context.Background(), path, 1<<20)
	var changed *filesystem.ErrChangedDuringRead
	if !errors.As(err, &changed) {
		t.Fatalf("ReadVersion error = %v, want *ErrChangedDuringRead", err)
	}
}

func TestCommitWithTheReadVersionInstallsAndKeepsTheMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.sh")
	writeVersionFixture(t, path, "before\n")
	p := New()
	_, version, err := p.ReadVersion(context.Background(), path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	installed, err := p.Commit(context.Background(), path, []byte("after\n"), filesystem.Expect{Version: &version})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test just created
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "after\n" {
		t.Fatalf("body = %q, want the installed bytes", body)
	}
	if installed.SHA256 != filesystem.ContentDigest([]byte("after\n")) {
		t.Fatalf("installed version = %+v, want the digest of the installed bytes", installed)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640 — the install must keep the file's permissions", info.Mode().Perm())
	}
}

func TestCommitRefusesAChangedFileAndKeepsItsBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	writeVersionFixture(t, path, "before\n")
	p := New()
	_, version, err := p.ReadVersion(context.Background(), path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	// Someone else writes the file between the read and the install.
	writeVersionFixture(t, path, "third party\n")
	_, err = p.Commit(context.Background(), path, []byte("mine\n"), filesystem.Expect{Version: &version})
	var changed *filesystem.ErrChangedSinceRead
	if !errors.As(err, &changed) {
		t.Fatalf("Commit error = %v, want *ErrChangedSinceRead", err)
	}
	body, readErr := os.ReadFile(path) //nolint:gosec // a path this test just created
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(body) != "third party\n" {
		t.Fatalf("body = %q, want the other writer's bytes untouched", body)
	}
}

func TestCommitAbsentCreatesAndRefusesAnOccupiedName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	p := New()
	if _, err := p.Commit(context.Background(), path, []byte("created\n"), filesystem.Expect{Absent: true}); err != nil {
		t.Fatalf("Commit(Absent) on a free name: %v", err)
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test just created
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "created\n" {
		t.Fatalf("body = %q, want the created bytes", body)
	}
	_, err = p.Commit(context.Background(), path, []byte("second\n"), filesystem.Expect{Absent: true})
	var exists *filesystem.ErrExists
	if !errors.As(err, &exists) {
		t.Fatalf("Commit(Absent) on an occupied name = %v, want *ErrExists", err)
	}
	body, readErr := os.ReadFile(path) //nolint:gosec // a path this test just created
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(body) != "created\n" {
		t.Fatalf("body = %q, want the first write untouched", body)
	}
}

func TestCommitKeepsTheOriginalBytesWhenTheInstallFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	writeVersionFixture(t, path, "before\n")
	p := New()
	_, version, err := p.ReadVersion(context.Background(), path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	p.rename = func(string, string) error { return errors.New("rename refused by the test") }
	if _, err := p.Commit(context.Background(), path, []byte("after\n"), filesystem.Expect{Version: &version}); err == nil {
		t.Fatal("Commit succeeded although the install failed")
	}
	body, readErr := os.ReadFile(path) //nolint:gosec // a path this test just created
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(body) != "before\n" {
		t.Fatalf("body = %q, want the original bytes after a failed install", body)
	}
	// And the temporary beside it is gone rather than leaked.
	entries, dirErr := os.ReadDir(filepath.Dir(path))
	if dirErr != nil {
		t.Fatalf("ReadDir: %v", dirErr)
	}
	for _, entry := range entries {
		if entry.Name() != "note.txt" {
			t.Fatalf("stray entry after a failed install: %s", entry.Name())
		}
	}
}

// The same Commit under concurrent calls: one wins, the other is refused
// changed-since-read, and the file ends with exactly one of the two bodies —
// the path lock is what makes that true rather than likely.
func TestCommitUnderTwoWritersInstallsExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contended.txt")
	writeVersionFixture(t, path, "original\n")
	p := New()
	_, version, err := p.ReadVersion(context.Background(), path, 1<<20)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := []byte("first writer\n")
			if i == 1 {
				body = []byte("second writer\n")
			}
			_, results[i] = p.Commit(context.Background(), path, body, filesystem.Expect{Version: &version})
		}()
	}
	wg.Wait()
	installed := 0
	for _, result := range results {
		if result == nil {
			installed++
			continue
		}
		var changed *filesystem.ErrChangedSinceRead
		if !errors.As(result, &changed) {
			t.Fatalf("loser's error = %v, want *ErrChangedSinceRead", result)
		}
	}
	if installed != 1 {
		t.Fatalf("installs = %d, want exactly 1", installed)
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test just created
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "first writer\n" && string(body) != "second writer\n" {
		t.Fatalf("body = %q, want the one writer's bytes", body)
	}
}
