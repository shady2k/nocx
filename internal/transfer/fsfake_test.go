package transfer_test

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/transfer"
)

// errDisconnected simulates an unavailable filesystem during a transfer.
var errDisconnected = fmt.Errorf("fake: lease poisoned, connection lost")

func notFound(op, p string) error {
	return fmt.Errorf("fake: %s %s: %w", op, p, fs.ErrNotExist)
}

// fakeFS is a writable filesystem with injectable failures. Its hooks
// run synchronously on the transfer's goroutine.
type fakeFS struct {
	files map[string]string

	// dead makes every call fail with errDisconnected — connection loss.
	dead bool
	// createErr fails every Create with this error.
	createErr error
	// writeErr, once set, fails the first Write issued after
	// failWriteAfterN bytes have reached the file.
	writeErr        error
	failWriteAfterN int
	// closeErr fails Close on a file handle. closeFailAt, when non-zero, is
	// the 1-based index of the close that fails; zero fails every close. A
	// KeepBoth transfer closes the reservation handle first and the temp's
	// second, so the two can be failed independently.
	closeErr    error
	closeFailAt int
	// removeErr fails every Remove.
	removeErr error
	// renameErr fails the atomic rename.
	renameErr error
	// onWrite fires after each write with the handle's running total.
	onWrite func(total int)

	closes int
	// creates counts every Create call, which is what shows a KeepBoth
	// search stopping on the first classifiable refusal rather than
	// spending its whole bound on it.
	creates int
	// writeSizes records the length of every Write call in order, which is
	// what proves the sink chunks rather than handing the lease one call
	// the size of the file (design D2).
	writeSizes []int
	removed    []string
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: make(map[string]string)}
}

// --- the RemoteFS surface ---------------------------------------------------

func (f *fakeFS) Create(p string) (transfer.RemoteFile, error) {
	f.creates++
	if f.dead {
		return nil, errDisconnected
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	if _, ok := f.files[p]; ok {
		return nil, fmt.Errorf("fake: create %s: %w", p, fs.ErrExist)
	}
	f.files[p] = ""
	return &fakeFile{fs: f, path: p}, nil
}

func (f *fakeFS) Rename(old, nw string) error {
	if f.dead {
		return errDisconnected
	}
	if f.renameErr != nil {
		return f.renameErr
	}
	c, ok := f.files[old]
	if !ok {
		return notFound("rename", old)
	}
	delete(f.files, old)
	f.files[nw] = c
	return nil
}

func (f *fakeFS) Remove(p string) error {
	if f.dead {
		return errDisconnected
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	if _, ok := f.files[p]; !ok {
		return notFound("remove", p)
	}
	delete(f.files, p)
	f.removed = append(f.removed, p)
	return nil
}

// fakeFile is one open handle. Write and Close can fail independently.
type fakeFile struct {
	fs      *fakeFS
	path    string
	written int
	closed  bool
}

func (h *fakeFile) Write(b []byte) (int, error) {
	if h.fs.dead {
		return 0, errDisconnected
	}
	h.fs.writeSizes = append(h.fs.writeSizes, len(b))
	if h.fs.writeErr != nil && h.written >= h.fs.failWriteAfterN {
		return 0, h.fs.writeErr
	}
	h.fs.files[h.path] += string(b)
	h.written += len(b)
	if h.fs.onWrite != nil {
		h.fs.onWrite(h.written)
	}
	return len(b), nil
}

func (h *fakeFile) Close() error {
	if h.closed {
		return fmt.Errorf("fake: %s closed twice", h.path)
	}
	h.closed = true
	h.fs.closes++
	if h.fs.dead {
		return errDisconnected
	}
	if h.fs.closeFailAt != 0 && h.fs.closes != h.fs.closeFailAt {
		return nil
	}
	return h.fs.closeErr
}

// --- what a test programs and then asks ------------------------------------

// put seeds a file that was already on the server before the upload.
func (f *fakeFS) put(p, content string) { f.files[p] = content }

// failWriteAfter fails the first Write issued once n bytes have landed.
func (f *fakeFS) failWriteAfter(n int, err error) {
	f.failWriteAfterN, f.writeErr = n, err
}

// failCloseAt fails the step-th Close; see closeFailAt.
func (f *fakeFS) failCloseAt(step int, err error) {
	f.closeFailAt, f.closeErr = step, err
}

func (f *fakeFS) content(p string) string { return f.files[p] }

func (f *fakeFS) exists(p string) bool { _, ok := f.files[p]; return ok }

// matching returns every path whose base name matches the glob, sorted, so
// a test can assert on what the transfer left in the directory.
func (f *fakeFS) matching(glob string) []string {
	var out []string
	for p := range f.files {
		if ok, err := path.Match(glob, path.Base(p)); err == nil && ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// paths returns every file on the fake, sorted — the whole post-state.
func (f *fakeFS) paths() []string {
	out := make([]string, 0, len(f.files))
	for p := range f.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// findStranded returns the one stranded path containing frag, or "".
func findStranded(stranded []string, frag string) string {
	for _, p := range stranded {
		if strings.Contains(p, frag) {
			return p
		}
	}
	return ""
}
