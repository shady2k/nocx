package log

// A LOG FILE HAS A CEILING (nocx-3an1s.1.7).
//
// Measured on an installed macOS build on 2026-11-05: nocx.log at 1388912907
// bytes, its tail holding dozens of one session's output-cap lines and nothing
// else. Nothing in this package bounded a file and nothing rotated one, so a
// writer that logged too often — however correct it was on its own — filled
// the person's disk and pushed every line they needed out of any tail.
//
// WHAT IS BOUNDED, AND WHAT IS KEPT. A file may reach MaxFileBytes; when it
// goes past, the writer cuts it back to its newest KeepFileBytes, whole lines
// only. That is a ceiling on the PATH, not on one handle: the window's several
// instances share nocx-window.log, and several helper generations share
// helper.log, so the decision to trim is made against the file on disk,
// re-read before every write, and a handle that was left behind by another
// process's rotation follows the path to the file a person will open.
//
// HOW THE CUT IS MADE. The kept tail is written to a temporary file beside the
// log — same directory, so the rename cannot cross a filesystem — and the
// temporary is renamed ONTO the log. A rename is atomic: a reader finds either
// the old file or the new one, never an empty or half-written one, and a
// failure at any point before the rename leaves the log exactly as it was.
// The alternative, truncating the log and writing the tail back, has a window
// in which the file is empty and a disk that fills inside it costs the whole
// log; measured reasoning for the choice, not a preference.
//
// What a cut can still lose is the line another process appended in the
// instant between this writer's decision and its rename. It is bounded by that
// instant (one record), it cannot be closed without making every log write
// take a cross-process lock, and losing one line is the right trade against a
// log that grows without limit.
//
// A TRIM THAT FAILS COSTS NEITHER THE LOG NOR THE PROCESS. Every failure —
// the temporary cannot be created, the disk is full, the rename is refused,
// the directory was removed — leaves the current handle appending, so the
// lines keep landing (visible, if the file is still the one at the path), and
// the next attempt waits rotationRetry rather than rewriting megabytes per
// line. Nothing here returns an error to the caller for a rotation: slog
// ignores a write error anyway, and a log that stopped taking lines to report
// its own trouble would be the diagnostic costing more than it diagnoses.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// MaxFileBytes is how large a log file in this product may become. 8 MiB
	// is tens of thousands of ordinary lines — more than a person reads in
	// one sitting, and small enough that a support log fits in a message.
	MaxFileBytes int64 = 8 << 20

	// KeepFileBytes is what a cut keeps: the newest of it, whole lines only.
	// Small enough that the next cut is far away, large enough to hold the
	// stretch of a failing session a person actually needs.
	KeepFileBytes int64 = 1 << 20

	// rotationRetry is how long a writer waits after a cut it could not make
	// before trying again. A full disk must not cost a rewrite attempt per
	// line; a minute of growth is a few megabytes at worst, and the next
	// attempt fixes it once the disk has room.
	rotationRetry = time.Minute
)

// File is a log file with a ceiling. It implements io.Writer and io.Closer and
// is safe for concurrent use; one handler writes through it per process, and
// other processes write through their own.
type File struct {
	mu  sync.Mutex
	now func() time.Time
	// path is the name a person opens. It is what every decision here is
	// made against — never the handle alone, which another process's cut can
	// leave pointing at a file nobody can reach.
	path    string
	f       *os.File
	size    int64
	max     int64
	keep    int64
	retryAt time.Time
}

// openFile is OpenFile with an explicit ceiling, so a test can drive the
// boundary without writing sixteen megabytes to reach it.
func openFile(path string, max, keep int64) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// O_RDWR because a cut reads the tail it keeps through this same handle;
	// O_APPEND because every writer must land at the file's end and not at an
	// offset it remembered, which is what makes several processes appending
	// to one path safe.
	// #nosec G304 — the path is the app data dir plus a fixed name, or a
	// test override; never external input.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	file := &File{now: time.Now, path: path, f: f, max: max, keep: keep}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	file.size = info.Size()
	file.mu.Lock()
	// An existing oversized log — the 1.3 GB one — is brought inside the
	// bound here, on the first open, and only its tail is read: the file is
	// cut to KeepFileBytes without reading a gigabyte of lines nobody will
	// look at.
	if file.size > file.max {
		file.trimLocked()
	}
	file.mu.Unlock()
	return file, nil
}

// Write appends one record and, when that takes the file past its ceiling,
// cuts it back to the recent lines.
func (w *File) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.followLocked()
	n, err := w.f.Write(p)
	if n > 0 {
		w.size += int64(n)
	}
	if err != nil {
		return n, err
	}
	if w.size > w.max {
		w.trimLocked()
	}
	return n, nil
}

// WriteString is Write for a string, the way os.File offers both.
func (w *File) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

// Close closes the handle. A cut that follows writes to a new file, so the
// handle this closes is always the current one.
func (w *File) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// followLocked makes the handle point at the file the PATH names.
//
// Several processes append to one log, so a handle can be left behind by
// another process's cut — writing lines into an inode nobody can open — and
// the file can be removed under a running process altogether. Both are the
// same fact: the path and this handle's file are not the same file. It is
// checked before the write, so the line the check was prompted by is not the
// one that lands in the wrong place.
func (w *File) followLocked() {
	if w.now().Before(w.retryAt) {
		return
	}
	pathInfo, err := os.Stat(w.path)
	if err != nil {
		// Removed (or its directory with it): logging continues, in a file
		// a person can open again.
		w.reopenLocked(0)
		return
	}
	mine, err := w.f.Stat()
	if err != nil || !os.SameFile(pathInfo, mine) {
		w.reopenLocked(pathInfo.Size())
		return
	}
	w.size = pathInfo.Size()
}

// reopenLocked adopts the file at the path. If it cannot be opened, the handle
// already held is kept and the next attempt waits: a write that still lands in
// a removed file is worth more than one that does not land at all.
func (w *File) reopenLocked(size int64) {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600) // #nosec G304 — see openFile.
	if err != nil {
		w.retryAt = w.now().Add(rotationRetry)
		return
	}
	_ = w.f.Close()
	w.f = f
	w.size = size
}

// trimLocked cuts the file back to its newest whole lines, through a temporary
// and a rename. Callers hold the mutex.
func (w *File) trimLocked() {
	if w.now().Before(w.retryAt) {
		return
	}
	info, err := w.f.Stat()
	if err != nil {
		w.retryAt = w.now().Add(rotationRetry)
		return
	}
	size := info.Size()
	if size <= w.max {
		w.size = size
		return
	}
	keep := w.keep
	if keep <= 0 || keep > size {
		keep = size
	}
	buf := make([]byte, keep)
	n, err := w.f.ReadAt(buf, size-keep)
	if err != nil && !errors.Is(err, io.EOF) {
		w.retryAt = w.now().Add(rotationRetry)
		return
	}
	tail := buf[:n]
	// WHOLE LINES: the kept stretch starts at the offset the size arithmetic
	// picked, which is in the middle of a line. Dropping to the first newline
	// costs at most one line and leaves a file that begins where a reader can
	// begin.
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	} else {
		tail = nil
	}

	tmpPath := rotateTempPath(w.path)
	// O_RDWR because the fd that is handed on becomes the log's handle, and
	// the next cut reads its tail through that same handle; O_APPEND so every
	// later record lands at the end whichever process wrote last.
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC|os.O_APPEND, 0o600) // #nosec G304 — derived from the log's own path.
	if err != nil {
		w.retryAt = w.now().Add(rotationRetry)
		return
	}
	if _, err := tmp.Write(tail); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		w.retryAt = w.now().Add(rotationRetry)
		return
	}
	if err := os.Rename(tmpPath, w.path); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		w.retryAt = w.now().Add(rotationRetry)
		return
	}
	// The temporary IS the new log now, and it is already open for appending:
	// nothing has to be reopened by name, so no failure can fall between the
	// rename and a working handle.
	_ = w.f.Close()
	w.f = tmp
	w.size = int64(len(tail))
	w.retryAt = time.Time{}
}

// rotateTempPath is where this process writes a cut's replacement. The pid is
// part of the name because several processes cut one path: two of them must
// never write one temporary, and a stale one from a crashed process is
// truncated by the next cut rather than followed by it.
func rotateTempPath(path string) string {
	return fmt.Sprintf("%s.%d.rotate", path, os.Getpid())
}
