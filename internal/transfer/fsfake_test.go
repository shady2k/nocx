package transfer_test

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/transfer"
)

// errDisconnected is the fake's lost-lease error: once dead is set every
// call fails with it, including Remove. That is the shape of a poisoned
// FSConn — the transfer's own lease is gone, so nothing the sink knows how
// to do can still reach the server, and the temp it created stays there.
var errDisconnected = fmt.Errorf("fake: lease poisoned, connection lost")

// errExists is what an O_EXCL create hits when the name is taken. It is
// deliberately NOT fs.ErrExist: SFTP v3 answers EEXIST as a generic
// SSH_FX_FAILURE (the same protocol fact internal/shellintegration pays for
// at install_remote.go:33), so a sink that keyed its KeepBoth retry on a
// classified EEXIST would work here and fail against a real server.
var errExists = fmt.Errorf("fake: file exists")

// errNoPosixRename wraps the package's sentinel rather than being it, which
// is how internal/ssh will report it: the sink must key on errors.Is, never
// on identity.
var errNoPosixRename = fmt.Errorf("fake: %w", transfer.ErrPosixRenameUnsupported)

func notFound(op, p string) error {
	return fmt.Errorf("fake: %s %s: %w", op, p, fs.ErrNotExist)
}

// fakeFS is the write half of an SFTP lease, in memory. It stands in for
// ssh.FSConn the way internal/filesystem/sftp/fsfake_test.go stands in for
// the read half, and it exists so every row of design §6 can be reached:
// each external call the sink makes has a switch here that makes it fail.
//
// It is not concurrency-safe and does not need to be. Put runs on one
// goroutine, and every test that changes the fake mid-transfer does so from
// a hook the sink itself calls, on that same goroutine.
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
	// noPosixRename makes PosixRename report the extension as unsupported,
	// which is what sends the sink down the fallback.
	noPosixRename bool
	// renameFailAt is the 1-based index of the v3 Rename call that fails.
	// PosixRename counts only when it actually renames, so with
	// noPosixRename set 1 is dest→bak and 2 is temp→dest.
	renameFailAt int
	renameErr    error

	// onWrite fires after each successful Write with the running total for
	// that handle; onRename fires after each successful rename with its
	// 1-based index. They are how a test kills the connection, or cancels
	// the context, at an exact point inside the transfer.
	onWrite  func(total int)
	onRename func(n int)

	renames int
	closes  int
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
		return nil, errExists // O_EXCL
	}
	f.files[p] = ""
	return &fakeFile{fs: f, path: p}, nil
}

func (f *fakeFS) PosixRename(old, nw string) error {
	if f.dead {
		return errDisconnected
	}
	if f.noPosixRename {
		return errNoPosixRename
	}
	return f.rename(old, nw, true)
}

func (f *fakeFS) Rename(old, nw string) error {
	if f.dead {
		return errDisconnected
	}
	return f.rename(old, nw, false)
}

// rename moves old to nw. replace is the posix-rename@openssh.com semantic;
// without it this is SFTP v3, which refuses an existing destination —
// nocx-340t, and the whole reason the fallback exists.
func (f *fakeFS) rename(old, nw string, replace bool) error {
	f.renames++
	if f.renameFailAt != 0 && f.renames == f.renameFailAt {
		if f.renameErr != nil {
			return f.renameErr
		}
		return fmt.Errorf("fake: rename %s -> %s refused", old, nw)
	}
	c, ok := f.files[old]
	if !ok {
		return notFound("rename", old)
	}
	if _, taken := f.files[nw]; taken && !replace {
		return errExists
	}
	delete(f.files, old)
	f.files[nw] = c
	if f.onRename != nil {
		f.onRename(f.renames)
	}
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

// fakeFile is one open handle. Its Write and Close are the lane calls the
// real FSFile makes, so both are failable independently.
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

// failRenameAt fails the step-th v3 rename; see renameFailAt.
func (f *fakeFS) failRenameAt(step int) { f.renameFailAt = step }

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
