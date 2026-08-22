package transfer_test

import (
	"fmt"
	"io"
	"io/fs"

	"github.com/shady2k/nocx/internal/transfer"
)

// errReadFailed is the fake's mid-stream read failure: an I/O error, or a
// lease that went away with a chunk still to come. It is deliberately not
// one of the classified sentinels — the engine must report it as itself
// rather than folding it into a shape it recognises.
var errReadFailed = fmt.Errorf("fake: read failed")

// errClientGone is what a writer reports when the far end stopped reading.
var errClientGone = fmt.Errorf("fake: the client went away")

// fakeReadFS is the read half of a host's filesystem, in memory. It is the
// mirror of fakeFS and exists for the same reason: every row of the
// download failure table has to be reachable without a server.
//
// It is not concurrency-safe and does not need to be — Get runs on one
// goroutine, and every test that changes the fake mid-transfer does so from
// a hook the engine itself calls, on that goroutine.
type fakeReadFS struct {
	files map[string]string

	// openErr fails every Open with this error.
	openErr error
	// declaredSize, when non-nil, is what Open REPORTS regardless of how
	// many bytes the file actually holds. It is how a file that shrank or
	// grew between the open and the read is reached: a real fstat measures
	// the object at open time and the object can still change underneath
	// it.
	declaredSize *int64
	// readErr fails the first Read issued after failReadAfterN bytes have
	// left the file.
	readErr        error
	failReadAfterN int
	// closeErr fails Close on the handle.
	closeErr error

	// onRead fires after each successful Read with the running total, so a
	// test can cancel a context at an exact point inside the copy.
	onRead func(total int)

	opens  int
	closes int
	// readSizes records the length of every Read the engine asked for, in
	// order, which is what proves it chunks rather than asking the lease
	// for the whole file in one call.
	readSizes []int
}

func newFakeReadFS() *fakeReadFS {
	return &fakeReadFS{files: make(map[string]string)}
}

func (f *fakeReadFS) Open(p string) (transfer.RemoteReader, int64, error) {
	f.opens++
	if f.openErr != nil {
		return nil, 0, f.openErr
	}
	data, ok := f.files[p]
	if !ok {
		return nil, 0, fmt.Errorf("fake: open %s: %w", p, fs.ErrNotExist)
	}
	size := int64(len(data))
	if f.declaredSize != nil {
		size = *f.declaredSize
	}
	return &fakeReadFile{fs: f, data: data}, size, nil
}

type fakeReadFile struct {
	fs   *fakeReadFS
	data string
	off  int
}

func (r *fakeReadFile) Read(p []byte) (int, error) {
	r.fs.readSizes = append(r.fs.readSizes, len(p))
	if r.fs.readErr != nil && r.off >= r.fs.failReadAfterN {
		return 0, r.fs.readErr
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	// A short read is what a real handle does whenever the chunk is larger
	// than what is buffered, so the fake never returns more than the
	// failure point either.
	if r.fs.readErr != nil && r.off+n > r.fs.failReadAfterN {
		n = r.fs.failReadAfterN - r.off
	}
	r.off += n
	if r.fs.onRead != nil {
		r.fs.onRead(r.off)
	}
	return n, nil
}

func (r *fakeReadFile) Close() error {
	r.fs.closes++
	return r.fs.closeErr
}

// countingWriter is the client end: it records what it received and can be
// made to fail after a chosen number of bytes, which is a client that went
// away mid-download.
type countingWriter struct {
	got        []byte
	failAfter  int // 0 means never
	writeSizes []int
	onWrite    func(total int)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writeSizes = append(w.writeSizes, len(p))
	if w.failAfter > 0 && len(w.got)+len(p) > w.failAfter {
		n := w.failAfter - len(w.got)
		w.got = append(w.got, p[:n]...)
		return n, errClientGone
	}
	w.got = append(w.got, p...)
	if w.onWrite != nil {
		w.onWrite(len(w.got))
	}
	return len(p), nil
}

func sizePtr(n int64) *int64 { return &n }
