package log

// A LOG FILE HAS A CEILING (nocx-3an1s.1.7).
//
// Measured on an installed build: nocx.log at 1388912907 bytes, its tail
// holding nothing but one session's output-cap lines. Nothing in this package
// bounded a file, so a defect in one writer became a disk-full on somebody's
// laptop. The assertions here are the ones a person relies on: the file stops
// growing at a stated size, what survives is the RECENT lines, and a rotation
// that cannot happen does not take the log with it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// writeLine writes one line whose text is unique and whose length is fixed, so
// a test can name a line it expects to survive and know how much it wrote.
func writeLine(t *testing.T, f *File, i int) {
	t.Helper()
	if _, err := fmt.Fprintf(f, "line %04d %s\n", i, strings.Repeat("x", 48)); err != nil {
		t.Fatalf("write line %d: %v", i, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // this test's own temp directory
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// The two ends of the invariant: the file never holds more than its stated
// size, and when it is cut it is the oldest lines that go — the file begins on
// a whole line and ends with the last thing written.
func TestFile_StopsAtItsStatedSizeAndKeepsTheNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nocx.log")
	const max, keep = 4096, 1024
	f, err := openFile(path, max, keep)
	if err != nil {
		t.Fatalf("openFile: %v", err)
	}
	for i := 0; i < 400; i++ {
		writeLine(t, f, i)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > max {
		t.Fatalf("log file is %d bytes, above the %d it states", info.Size(), max)
	}
	body := readFile(t, path)
	if !strings.Contains(body, "line 0399 ") {
		t.Fatalf("the newest line is not in the file; its last 200 bytes are\n%q", lastBytes(body, 200))
	}
	if strings.Contains(body, "line 0000 ") {
		t.Fatal("the oldest line survived a trim; the file is not keeping the RECENT lines")
	}
	if !strings.HasPrefix(body, "line ") {
		t.Fatalf("the file starts mid-line: %q", firstLine(body))
	}
	// Kept, not merely cut: a trim that emptied the file would pass every
	// assertion above except this one.
	if info.Size() < keep/2 {
		t.Fatalf("a trim left %d bytes, far below the %d it keeps", info.Size(), keep)
	}
}

func lastBytes(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[len(s)-n:]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Two processes append to one file — the window's several instances share
// nocx-window.log, and the helper's generations share helper.log — so the
// ceiling is a property of the PATH, not of one handle. Two handles here are
// how that is exercised without starting a second process: each one sees the
// other's bytes through the file itself.
func TestFile_TwoWritersOnOnePathShareTheCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nocx-window.log")
	const max, keep = 4096, 1024
	first, err := openFile(path, max, keep)
	if err != nil {
		t.Fatalf("first openFile: %v", err)
	}
	second, err := openFile(path, max, keep)
	if err != nil {
		t.Fatalf("second openFile: %v", err)
	}
	for i := 0; i < 400; i++ {
		writeLine(t, first, i)
		writeLine(t, second, i)
	}
	for _, f := range []*File{first, second} {
		if closeErr := f.Close(); closeErr != nil {
			t.Fatalf("close: %v", closeErr)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > max {
		t.Fatalf("two writers took the file to %d bytes, above the %d both state", info.Size(), max)
	}
	if body := readFile(t, path); !strings.Contains(body, "line 0399 ") {
		t.Fatalf("the last line either writer wrote is gone; tail is\n%q", lastBytes(body, 200))
	}
}

// The file is removed under a running process — a person clearing the profile,
// a cleanup script, a test. Logging must continue into a file a person can
// read, and the handle must not start returning errors.
func TestFile_RecreatesAFileRemovedUnderIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nocx.log")
	f, err := openFile(path, 4096, 1024)
	if err != nil {
		t.Fatalf("openFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	writeLine(t, f, 0)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeLine(t, f, 1)
	writeLine(t, f, 2)

	body := readFile(t, path)
	if !strings.Contains(body, "line 0002 ") {
		t.Fatalf("logging did not resume in a recreated file; it holds\n%q", body)
	}
	if strings.Contains(body, "line 0000 ") {
		t.Fatal("unreachable: the recreated file should not hold a line from before its removal")
	}
}

// A CLOSED LOG FILE IS CLOSED (nocx-atpw5.1.45). App.Shutdown closes the log
// last, after its final line, and a goroutine the composition root started can
// still say one after that: measured 2026-11-05 in internal/app, the lifecycle
// channel's pump logged "lifecycle ledger read failed" 22 ms after "application
// stopped", and the lifecycle bridge said both of its closing lines after it.
//
// Those writes used to land in a file recreated by the writer itself: follow
// sees a closed handle as "the path is not mine" and reopens it with O_CREATE.
// A test whose cleanup then removes the log's directory fails with "directory
// not empty" — a red test about a tidy-up, in a directory nobody had written to
// since. Close has returned; nothing may put the file back on disk.
func TestFile_AClosedFileIsNotRecreatedByALateWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nocx.log")
	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	writeLine(t, f, 0)
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the closed log: %v", err)
	}

	if _, err := f.WriteString("line 0001 late\n"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("a write after Close returned %v, want os.ErrClosed", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a write after Close put the log back at %s (stat error %v)", path, err)
	}
	// The directory a cleanup removes is exactly what must stay empty, and
	// that is the shape the failure took.
	if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
		t.Fatalf("the log's directory holds %d entries after a write to a closed log (err %v)", len(left), err)
	}
	// Close is idempotent: App.Shutdown runs more than once in the tests that
	// call it by hand, and a second Close must not report the first.
	if err := f.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// A rotation that cannot happen must cost neither the log nor the process: the
// lines keep landing, the file keeps growing for as long as the obstruction
// lasts, and the next attempt after it is gone trims again.
func TestFile_ARefusedRotationKeepsLoggingAndRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nocx.log")
		const max, keep = 2048, 512
		// Where this process's rotation writes its replacement, a directory:
		// the create cannot succeed, however much room the disk has.
		if err := os.Mkdir(rotateTempPath(path), 0o700); err != nil {
			t.Fatalf("block the rotation target: %v", err)
		}
		f, err := openFile(path, max, keep)
		if err != nil {
			t.Fatalf("openFile: %v", err)
		}
		defer func() { _ = f.Close() }()
		for i := 0; i < 100; i++ {
			writeLine(t, f, i)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Size() <= max {
			t.Fatalf("the fixture did not reach the ceiling: %d bytes", info.Size())
		}
		if body := readFile(t, path); !strings.Contains(body, "line 0099 ") {
			t.Fatal("a refused rotation lost the newest line")
		}

		if removeErr := os.Remove(rotateTempPath(path)); removeErr != nil {
			t.Fatalf("clear the obstruction: %v", removeErr)
		}
		// The retry is bounded, not immediate: a full disk must not cost a
		// rewrite attempt per line. Virtual time, so the assertion is about
		// the rule and not about how fast this machine is.
		time.Sleep(rotationRetry + time.Second)
		writeLine(t, f, 100)
		info, err = os.Stat(path)
		if err != nil {
			t.Fatalf("stat after the retry: %v", err)
		}
		if info.Size() > max {
			t.Fatalf("the file is %d bytes after a retry that could have trimmed it to %d", info.Size(), keep)
		}
		if body := readFile(t, path); !strings.Contains(body, "line 0100 ") {
			t.Fatal("the retried rotation lost the line that triggered it")
		}
	})
}

// An old log — the 1.3 GB one the bead was filed about — is brought inside the
// bound on the FIRST open, and only its tail is read: a startup must not spend
// a gigabyte of I/O, or memory, before the window appears.
func TestOpenFile_TrimsAnOversizedFileOnFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nocx.log")
	// A sparse gigabyte, so the test does not write one. Its content is a
	// hole of zero bytes, then one real line: the shape of a log whose head
	// was produced long ago.
	const hole = 1 << 30
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create the log: %v", err)
	}
	if err := os.Truncate(path, hole); err != nil {
		t.Fatalf("make the sparse file: %v", err)
	}
	appendRaw(t, path, "\nthe last line a person needed\n")

	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > MaxFileBytes {
		t.Fatalf("the first open left a %d-byte log, above the stated %d", info.Size(), MaxFileBytes)
	}
	body := readFile(t, path)
	if !strings.Contains(body, "the last line a person needed") {
		t.Fatalf("the trim did not keep the newest line; the file holds %q", lastBytes(body, 200))
	}
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // this test's own temp directory
	if err != nil {
		t.Fatalf("append open: %v", err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("append: %v", err)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("append close: %v", closeErr)
	}
}
