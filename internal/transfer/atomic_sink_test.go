package transfer_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

func TestAtomicSinkOverwriteStreamsDefaultChunksThenCommits(t *testing.T) {
	body := strings.Repeat("x", 2*transfer.DefaultChunk+1)
	f := &atomicFSFake{}
	var progress []int64
	out, err := transfer.NewAtomicSink(f, 0).Put(t.Context(), atomicUpload(transfer.Overwrite, len(body)),
		shortReads{strings.NewReader(body)}, func(n int64) { progress = append(progress, n) })
	if err != nil || out.State != transfer.StateWritten || out.FinalName != "a.txt" {
		t.Fatalf("Put = (%+v, %v); want written a.txt", out, err)
	}
	if got, want := progress, []int64{transfer.DefaultChunk, 2 * transfer.DefaultChunk, int64(len(body))}; !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %v, want %v", got, want)
	}
	if f.bytes.String() != body || f.keptBoth {
		t.Fatalf("unconditional overwrite did not send the requested bytes")
	}
	assertAtomicCalls(t, f, "open /home/u/a.txt", "write 262144", "write 262144", "write 1", "commit /home/u/a.txt")
}

func TestAtomicSinkKeepBothDoesNotResendBytes(t *testing.T) {
	f := &atomicFSFake{commitError: func(attempt int) error {
		if attempt == 1 {
			return fmt.Errorf("occupied: %w", fs.ErrExist)
		}
		return nil
	}}
	out, err := transfer.NewAtomicSink(f, 2).Put(t.Context(), atomicUpload(transfer.KeepBoth, 3), strings.NewReader("new"), nil)
	if err != nil || out.State != transfer.StateWritten || out.FinalName != "a (2).txt" || !f.keptBoth || f.bytes.String() != "new" {
		t.Fatalf("Put = (%+v, %v), bytes = %q; want one copy written as a (2).txt", out, err, f.bytes.String())
	}
	assertAtomicCalls(t, f, "open /home/u/a.txt", "write 2", "write 1", "commit absent /home/u/a (1).txt", "commit absent /home/u/a (2).txt")
}

func TestAtomicSinkKeepBothExhaustionAborts(t *testing.T) {
	f := &atomicFSFake{commitError: func(int) error { return fs.ErrExist }}
	_, err := transfer.NewAtomicSink(f, 2).Put(t.Context(), atomicUpload(transfer.KeepBoth, 3), strings.NewReader("new"), nil)
	var exhausted *transfer.NameExhaustedError
	if !errors.As(err, &exhausted) || exhausted.Attempts != 32 || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Put error = %v; want name exhaustion after 32 collisions", err)
	}
	if f.commits != 32 || f.bytes.String() != "new" || f.calls[len(f.calls)-1] != "abort" {
		t.Fatalf("want one copy, 32 commits and abort; got %v", f.calls)
	}
}

func TestAtomicSinkSkipNeverOpensOrReads(t *testing.T) {
	f := &atomicFSFake{}
	out, err := transfer.NewAtomicSink(f, 2).Put(t.Context(), atomicUpload(transfer.Skip, 2), nil, nil)
	if err != nil || out.State != transfer.StateSkipped || len(f.calls) != 0 {
		t.Fatalf("Put = (%+v, %v), calls = %v; want skipped with no calls", out, err, f.calls)
	}
}

func TestAtomicSinkOpenRefusalDoesNotCopy(t *testing.T) {
	f := &atomicFSFake{openErr: fs.ErrPermission}
	_, err := transfer.NewAtomicSink(f, 2).Put(t.Context(), atomicUpload(transfer.Overwrite, 2), nil, nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Put error = %v; want the open refusal", err)
	}
	assertAtomicCalls(t, f, "open /home/u/a.txt")
}

func TestAtomicSinkFailureAborts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      io.Reader
		writeErr  error
		commitErr error
		abortErr  error
		want      error
		calls     []string
	}{
		{name: "read", body: atomicReadError{}, want: fs.ErrClosed, calls: []string{"write 2", "abort"}},
		{name: "size mismatch", body: strings.NewReader("x"), calls: []string{"write 1", "abort"}},
		{name: "write", body: strings.NewReader("xy"), writeErr: fs.ErrPermission, abortErr: fs.ErrClosed, want: fs.ErrPermission, calls: []string{"write 2", "abort"}},
		{name: "commit", body: strings.NewReader("xy"), commitErr: fs.ErrPermission, want: fs.ErrPermission, calls: []string{"write 2", "commit absent /home/u/a (1).txt", "abort"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &atomicFSFake{writeErr: tc.writeErr, commitError: func(int) error { return tc.commitErr }, abortErr: tc.abortErr}
			_, err := transfer.NewAtomicSink(f, 2).Put(t.Context(), atomicUpload(transfer.KeepBoth, 2), tc.body, nil)
			var mismatch *transfer.SizeMismatchError
			if tc.want == nil && !errors.As(err, &mismatch) || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Put error = %v; want %v (or size mismatch)", err, tc.want)
			}
			if tc.abortErr != nil && !errors.Is(err, tc.abortErr) {
				t.Fatalf("abort error lost: %v", err)
			}
			assertAtomicCalls(t, f, append([]string{"open /home/u/a.txt"}, tc.calls...)...)
		})
	}
}

func TestAtomicSinkCancellationAbortsWithLiveContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &atomicFSFake{}
	_, err := transfer.NewAtomicSink(f, 2).Put(ctx, atomicUpload(transfer.Overwrite, 3), strings.NewReader("new"), func(int64) { cancel() })
	if !errors.Is(err, context.Canceled) || f.abortCtxErr != nil {
		t.Fatalf("Put error = %v, Abort context error = %v; want (canceled, nil)", err, f.abortCtxErr)
	}
	assertAtomicCalls(t, f, "open /home/u/a.txt", "write 2", "abort")
}

func TestAtomicSinkCancellationCannotInterruptCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &atomicFSFake{commitError: func(int) error { cancel(); return nil }}
	out, err := transfer.NewAtomicSink(f, 2).Put(ctx, atomicUpload(transfer.Overwrite, 0), strings.NewReader(""), nil)
	if err != nil || out.State != transfer.StateWritten || f.commitCtxErr != nil {
		t.Fatalf("Put = (%+v, %v), Commit context error = %v; want (written, nil, nil)", out, err, f.commitCtxErr)
	}
	assertAtomicCalls(t, f, "open /home/u/a.txt", "commit /home/u/a.txt")
}

func atomicUpload(decision transfer.Decision, size int) transfer.Upload {
	return transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: int64(size), OnExists: decision}
}

func assertAtomicCalls(t *testing.T, f *atomicFSFake, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls: %v; want %v", f.calls, want)
	}
}

type atomicFSFake struct {
	calls        []string
	bytes        bytes.Buffer
	keptBoth     bool
	commits      int
	openErr      error
	writeErr     error
	commitError  func(int) error
	abortErr     error
	abortCtxErr  error
	commitCtxErr error
}

func (f *atomicFSFake) OpenWrite(_ context.Context, path string, keepBoth bool) (transfer.AtomicWrite, error) {
	f.calls = append(f.calls, "open "+path)
	f.keptBoth = keepBoth
	return f, f.openErr
}

func (f *atomicFSFake) Write(p []byte) (int, error) {
	f.calls = append(f.calls, fmt.Sprintf("write %d", len(p)))
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.bytes.Write(p)
}

func (f *atomicFSFake) Commit(ctx context.Context, path string, size int64, expectAbsent bool) error {
	prefix := "commit "
	if expectAbsent {
		prefix += "absent "
	}
	f.calls = append(f.calls, prefix+path)
	if size != int64(f.bytes.Len()) {
		return fmt.Errorf("declared size %d vs uploaded size %d", size, f.bytes.Len())
	}
	f.commits++
	var err error
	if f.commitError != nil {
		err = f.commitError(f.commits)
	}
	f.commitCtxErr = ctx.Err()
	return err
}

func (f *atomicFSFake) Abort(ctx context.Context) error {
	f.calls = append(f.calls, "abort")
	f.abortCtxErr = ctx.Err()
	return f.abortErr
}

type shortReads struct{ io.Reader }

func (r shortReads) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), 4096)])
}

type atomicReadError struct{}

func (atomicReadError) Read(p []byte) (int, error) {
	return copy(p, "xy"), fs.ErrClosed
}
