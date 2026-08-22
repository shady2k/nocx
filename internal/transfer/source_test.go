package transfer_test

// Every row of the download failure table, against a fake, the way
// sink_test.go does it for the write direction — plus, for every "returns
// an error when…", the paired assertion that on an ordinary server it
// succeeds.
//
// The table's columns are not the sink's, and that is the point. A download
// creates nothing on the source host, so there is no "dest" column and no
// "left behind" column: the host is untouched on every row. What replaces
// them is what the CLIENT ended up holding, because that is the thing a
// download can get wrong and cannot take back.
//
//	Failure                          Source host   Client holds        Reported
//	Open: no such file               untouched     nothing             fs.ErrNotExist, before a byte
//	Open: permission denied          untouched     nothing             fs.ErrPermission, before a byte
//	Open: not a regular file         untouched     nothing             ErrNotRegular, before a byte
//	Read fails mid-stream            untouched     a prefix            the reason, and how far it got
//	The file shrank after the open   untouched     a short prefix      SizeMismatchError
//	The file grew after the open     untouched     exactly Size        SizeMismatchError{AtLeast}
//	The client went away             untouched     a prefix            the reason
//	Cancelled by the person          untouched     a prefix            context.Canceled
//	Close of the pinned handle fails untouched     everything          the CALLER's to report; Get never closes

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

func openFor(t *testing.T, f *fakeReadFS, p string, chunk int) *transfer.Download {
	t.Helper()
	d, err := transfer.NewSource(f, chunk).Open(p)
	if err != nil {
		t.Fatalf("Open(%s): %v", p, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// ── the paired successes ─────────────────────────────────────────────────

// TestSourceGet_DeliversAnOrdinaryFile is the "and on an ordinary server it
// succeeds" half that every failure below is paired against. It also proves
// the two things about a download that nothing else asserts: the bytes are
// the file's, and the number Get returns is the number that arrived.
func TestSourceGet_DeliversAnOrdinaryFile(t *testing.T) {
	body := strings.Repeat("nocx", 4096) // 16 KiB, many chunks at 1 KiB
	f := newFakeReadFS()
	f.files["/srv/report.bin"] = body

	d := openFor(t, f, "/srv/report.bin", 1024)
	if d.Name != "report.bin" || d.Size != int64(len(body)) {
		t.Fatalf("Open = %+v; want name report.bin and size %d", d, len(body))
	}

	var w countingWriter
	var lastProgress int64
	sent, err := transfer.NewSource(f, 1024).
		Get(context.Background(), d, &w, func(total int64) { lastProgress = total })
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sent != int64(len(body)) {
		t.Errorf("sent = %d, want %d", sent, len(body))
	}
	if string(w.got) != body {
		t.Errorf("the client got %d bytes, not the file's %d", len(w.got), len(body))
	}
	if lastProgress != int64(len(body)) {
		t.Errorf("last progress = %d, want %d", lastProgress, len(body))
	}
}

// An empty file is a file. Zero bytes, no error, and a Content-Length of
// zero is a legitimate download rather than a refusal.
func TestSourceGet_AnEmptyFileIsAFile(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/empty"] = ""
	d := openFor(t, f, "/srv/empty", transfer.DefaultChunk)

	var w countingWriter
	sent, err := transfer.NewSource(f, transfer.DefaultChunk).Get(context.Background(), d, &w, nil)
	if err != nil || sent != 0 || len(w.got) != 0 {
		t.Fatalf("Get on an empty file = (%d, %v), client got %d bytes; want (0, nil, 0)", sent, err, len(w.got))
	}
}

// The chunk bound is what keeps ONE remote read inside the lease's lane
// timeout (D2 in the mirror direction), so it has to be a property of the
// engine and not of how much the fake felt like returning.
func TestSourceGet_ReadsInBoundedChunks(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/big"] = strings.Repeat("x", 5000)
	d := openFor(t, f, "/srv/big", 512)

	var w countingWriter
	if _, err := transfer.NewSource(f, 512).
		Get(context.Background(), d, &w, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	for i, n := range f.readSizes {
		if n > 512 {
			t.Fatalf("read %d asked for %d bytes, past the 512-byte chunk bound", i, n)
		}
	}
	for i, n := range w.writeSizes {
		if n > 512 {
			t.Fatalf("write %d carried %d bytes, past the 512-byte chunk bound", i, n)
		}
	}
	if len(f.readSizes) < 5000/512 {
		t.Fatalf("only %d reads for 5000 bytes at a 512-byte chunk", len(f.readSizes))
	}
}

// ── Open: the three refusals that happen before a byte moves ─────────────

func TestSourceOpen_RefusesBeforeAnyByteMoves(t *testing.T) {
	cases := map[string]struct {
		setup func(*fakeReadFS)
		want  error
	}{
		"no such file":       {func(f *fakeReadFS) {}, fs.ErrNotExist},
		"permission denied":  {func(f *fakeReadFS) { f.openErr = fs.ErrPermission }, fs.ErrPermission},
		"not a regular file": {func(f *fakeReadFS) { f.openErr = transfer.ErrNotRegular }, transfer.ErrNotRegular},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeReadFS()
			tc.setup(f)
			d, err := transfer.NewSource(f, transfer.DefaultChunk).Open("/srv/nope")
			if err == nil {
				_ = d.Close()
				t.Fatal("Open succeeded; want a refusal")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open error = %v; want one satisfying errors.Is against %v", err, tc.want)
			}
			if d != nil {
				t.Fatal("a refused Open returned a Download; nothing may hold a handle it did not get")
			}
		})
	}
}

// A size no framing can carry is refused rather than clamped, and the
// handle it would have held is closed on the way out — a refusal that
// leaked a descriptor would be the one failure path nobody ever sees.
func TestSourceOpen_RefusesANegativeSizeAndClosesTheHandle(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/odd"] = "abc"
	f.declaredSize = sizePtr(-1)

	if _, err := transfer.NewSource(f, transfer.DefaultChunk).Open("/srv/odd"); !errors.Is(err, transfer.ErrInvalidDownload) {
		t.Fatalf("Open error = %v; want ErrInvalidDownload", err)
	}
	if f.closes != 1 {
		t.Fatalf("closes = %d; a refused Open must not leak the handle it opened", f.closes)
	}
}

// ── mid-stream failures ──────────────────────────────────────────────────

// A read that fails part-way reports itself, and Get says how far it got.
// The bytes already handed over cannot be recalled — that is the whole
// asymmetry with upload — so the number is the only honest account.
func TestSourceGet_ReadFailsMidStream(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = strings.Repeat("y", 4096)
	f.readErr, f.failReadAfterN = errReadFailed, 1024
	d := openFor(t, f, "/srv/f", 256)

	var w countingWriter
	sent, err := transfer.NewSource(f, 256).
		Get(context.Background(), d, &w, nil)
	if !errors.Is(err, errReadFailed) {
		t.Fatalf("Get error = %v; want the read's own reason", err)
	}
	if sent != 1024 || len(w.got) != 1024 {
		t.Fatalf("sent = %d and the client holds %d; want 1024 of each", sent, len(w.got))
	}
}

// The file shrank between the open's fstat and the read. The engine refuses
// rather than sending a body short of the length its own framing declared:
// a truncated response the client keeps is a corrupt file, and the failure
// has to be visible as one.
func TestSourceGet_TheFileShrankAfterTheOpen(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = "only eight"
	f.declaredSize = sizePtr(4096)
	d := openFor(t, f, "/srv/f", transfer.DefaultChunk)

	var w countingWriter
	sent, err := transfer.NewSource(f, transfer.DefaultChunk).Get(context.Background(), d, &w, nil)
	var mismatch *transfer.SizeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Get error = %v; want a SizeMismatchError", err)
	}
	if mismatch.Declared != 4096 || mismatch.Got != int64(len("only eight")) || mismatch.AtLeast {
		t.Fatalf("mismatch = %+v; want the declared 4096 against the 10 delivered", mismatch)
	}
	if sent != int64(len("only eight")) {
		t.Fatalf("sent = %d, want 10", sent)
	}
}

// The file grew. Exactly the declared number of bytes is sent and the
// excess is never passed on: the client's framing promised Size, and bytes
// past it are not the file — they are protocol garbage on a connection
// something else has to parse.
func TestSourceGet_TheFileGrewAfterTheOpen(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = strings.Repeat("z", 4096)
	f.declaredSize = sizePtr(1000)
	d := openFor(t, f, "/srv/f", 256)

	var w countingWriter
	_, err := transfer.NewSource(f, 256).
		Get(context.Background(), d, &w, nil)
	var mismatch *transfer.SizeMismatchError
	if !errors.As(err, &mismatch) || !mismatch.AtLeast {
		t.Fatalf("Get error = %v; want a SizeMismatchError with AtLeast set", err)
	}
	if len(w.got) > 1000 {
		t.Fatalf("the client received %d bytes past the %d it was framed for", len(w.got)-1000, 1000)
	}
}

// The client went away. The write is the end this direction cannot bound
// from inside the loop, so what is asserted is the other half of the
// bargain: once the writer reports a failure, Get unwinds at once and
// reports it.
func TestSourceGet_TheClientWentAway(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = strings.Repeat("q", 4096)
	d := openFor(t, f, "/srv/f", 256)

	w := &countingWriter{failAfter: 512}
	sent, err := transfer.NewSource(f, 256).
		Get(context.Background(), d, w, nil)
	if !errors.Is(err, errClientGone) {
		t.Fatalf("Get error = %v; want the writer's own reason", err)
	}
	if sent > 512 {
		t.Fatalf("sent = %d; nothing may be counted as delivered past the write that failed", sent)
	}
}

// Cancellation is observed BETWEEN chunks, which is what bounds it by one
// remote read rather than by the whole transfer.
func TestSourceGet_CancelledByThePerson(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = strings.Repeat("c", 8192)
	d := openFor(t, f, "/srv/f", 256)

	ctx, cancel := context.WithCancel(context.Background())
	f.onRead = func(total int) {
		if total >= 512 {
			cancel()
		}
	}
	var w countingWriter
	sent, err := transfer.NewSource(f, 256).Get(ctx, d, &w, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v; want context.Canceled", err)
	}
	if sent >= 8192 {
		t.Fatalf("sent = %d; a cancelled download must stop short", sent)
	}
}

// A cancel that lands before the first chunk sends nothing at all. This is
// the row that has no upload counterpart worth stating and every download
// counterpart worth stating: the transfer is refused with the client
// holding zero bytes, which is the only outcome a download can cleanly
// undo.
func TestSourceGet_CancelledBeforeTheFirstChunkSendsNothing(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = strings.Repeat("c", 8192)
	d := openFor(t, f, "/srv/f", transfer.DefaultChunk)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var w countingWriter
	sent, err := transfer.NewSource(f, transfer.DefaultChunk).Get(ctx, d, &w, nil)
	if !errors.Is(err, context.Canceled) || sent != 0 || len(w.got) != 0 {
		t.Fatalf("Get = (%d, %v) with %d bytes at the client; want (0, context.Canceled, 0)", sent, err, len(w.got))
	}
}

// ── ownership ────────────────────────────────────────────────────────────

// Get never closes the handle, and the contract says so: Open transfers
// ownership to its caller, which closes on every path. A Get that closed
// would make the caller's own deferred close a double close on a lease
// handle.
func TestSourceGet_DoesNotCloseTheHandle(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = "abc"
	d, err := transfer.NewSource(f, transfer.DefaultChunk).Open("/srv/f")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var w countingWriter
	if _, err := transfer.NewSource(f, transfer.DefaultChunk).Get(context.Background(), d, &w, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.closes != 0 {
		t.Fatalf("closes = %d after Get; the caller owns the handle", f.closes)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close: %v; Close must be idempotent", err)
	}
	if f.closes != 1 {
		t.Fatalf("closes = %d after two Closes; want exactly 1", f.closes)
	}
}

// A Download the caller did not get from Open — a zero value, or one whose
// handle has already been closed — is refused rather than read from. The
// unexported handle is what makes the first case unreachable from another
// package at all; this is the second.
func TestSourceGet_RefusesADownloadWithNoOpenHandle(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = "abc"
	d, err := transfer.NewSource(f, transfer.DefaultChunk).Open("/srv/f")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = d.Close()

	var w countingWriter
	if _, err := transfer.NewSource(f, transfer.DefaultChunk).Get(context.Background(), d, &w, nil); !errors.Is(err, transfer.ErrInvalidDownload) {
		t.Fatalf("Get on a closed Download = %v; want ErrInvalidDownload", err)
	}
	if _, err := transfer.NewSource(f, transfer.DefaultChunk).Get(context.Background(), nil, &w, nil); !errors.Is(err, transfer.ErrInvalidDownload) {
		t.Fatalf("Get(nil) = %v; want ErrInvalidDownload", err)
	}
}

// The engine reports the READ end and the WRITE end in different words, so
// a person reading a failure can tell "the server stopped giving me the
// file" from "you stopped taking it".
func TestSourceGet_NamesWhichEndFailed(t *testing.T) {
	f := newFakeReadFS()
	f.files["/srv/f"] = strings.Repeat("w", 2048)
	f.readErr, f.failReadAfterN = errReadFailed, 256
	d := openFor(t, f, "/srv/f", 128)
	var w countingWriter
	_, err := transfer.NewSource(f, 128).Get(context.Background(), d, &w, nil)
	if err == nil || !strings.Contains(err.Error(), "read remote") {
		t.Fatalf("a failed remote read reported %v; want it to name the remote end", err)
	}

	g := newFakeReadFS()
	g.files["/srv/f"] = strings.Repeat("w", 2048)
	d2 := openFor(t, g, "/srv/f", 128)
	w2 := &countingWriter{failAfter: 128}
	_, err = transfer.NewSource(g, 128).Get(context.Background(), d2, w2, nil)
	if err == nil || !strings.Contains(err.Error(), "send") {
		t.Fatalf("a failed client write reported %v; want it to name the sending end", err)
	}
}

// The bytes delivered are the file's bytes in order — the assertion the
// chunking could break without any error appearing anywhere.
func TestSourceGet_PreservesTheBytesExactly(t *testing.T) {
	body := make([]byte, 3000)
	for i := range body {
		body[i] = byte(i % 251)
	}
	f := newFakeReadFS()
	f.files["/srv/bin"] = string(body)
	d := openFor(t, f, "/srv/bin", 97)

	var w countingWriter
	if _, err := transfer.NewSource(f, 97).
		Get(context.Background(), d, &w, nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(w.got, body) {
		t.Fatal("the delivered bytes are not the file's bytes")
	}
}
