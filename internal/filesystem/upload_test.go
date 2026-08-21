package filesystem

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

// stubSink is the write half a remote binding carries. It records what it
// was given, answers with what the test set, and can be gated so a test can
// hold one Put in flight — the shape the close-waits interval needs.
type stubSink struct {
	mu      sync.Mutex
	uploads []transfer.Upload
	body    string

	out transfer.Outcome
	err error

	entered chan struct{} // closed when Put is reached, when gated
	release chan struct{} // the test closes it to let Put finish
}

func (s *stubSink) Put(ctx context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	b, _ := io.ReadAll(r)
	if progress != nil {
		progress(int64(len(b)))
	}
	s.mu.Lock()
	s.uploads = append(s.uploads, u)
	s.body = string(b)
	s.mu.Unlock()
	return s.out, s.err
}

func (s *stubSink) calls() []transfer.Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transfer.Upload(nil), s.uploads...)
}

// touchyProvider fails the test if any provider method is reached. It is how
// "the refusal costs no round trip" is asserted: a local binding's Upload
// must answer from the nil sink alone.
type touchyProvider struct {
	t *testing.T
}

func (p touchyProvider) fail(method string) {
	p.t.Helper()
	p.t.Errorf("R1: a refused Upload must not reach the provider; %s was called", method)
}

func (p touchyProvider) Root(context.Context) (Root, error) { p.fail("Root"); return Root{}, nil }
func (p touchyProvider) List(context.Context, string, Page) (Listing, error) {
	p.fail("List")
	return Listing{}, nil
}

func (p touchyProvider) Read(context.Context, string, int64) (Content, error) {
	p.fail("Read")
	return Content{}, nil
}

func (p touchyProvider) Watch(context.Context, string) (Watch, error) {
	p.fail("Watch")
	return nil, nil
}

func (p touchyProvider) Canonical(_ context.Context, path string) (string, error) {
	p.fail("Canonical")
	return path, nil
}
func (p touchyProvider) Close() error { return nil }

func anUpload() transfer.Upload {
	return transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 1, OnExists: transfer.Overwrite}
}

// TestHandle_UploadIsRefusedOnALocalBinding is R1 of the upload design: a
// local binding has no sink, so the refusal is a nil field rather than a
// check somebody performs — and a tab where a person typed `ssh srv-01` by
// hand is a KindLocal session, so it takes this same refusal by this same
// route.
func TestHandle_UploadIsRefusedOnALocalBinding(t *testing.T) {
	reg := New()
	id, err := reg.Register(touchyProvider{t: t}, "s1", "", nil) // nil sink = local
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = h.Upload(context.Background(), anUpload(), strings.NewReader("x"), func(int64) {})

	var unsupported *ErrUploadUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("R1: a local binding has no write seam, so the refusal is structural; got %v", err)
	}
	if unsupported.BindingID != id {
		t.Errorf("the refusal names binding %q, want %q", unsupported.BindingID, id)
	}
}

// TestHandle_UploadReachesTheSinkOnARemoteBinding is the paired success: on
// an ordinary remote binding the same call goes through, carrying the
// instruction, the bytes and the progress callback to the sink.
func TestHandle_UploadReachesTheSinkOnARemoteBinding(t *testing.T) {
	sink := &stubSink{out: transfer.Outcome{State: transfer.StateWritten, FinalName: "a.txt"}}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "v1:abc", sink)
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	var seen int64
	out, err := h.Upload(context.Background(), anUpload(), strings.NewReader("x"), func(n int64) { seen = n })
	if err != nil {
		t.Fatalf("Upload on a remote binding: %v", err)
	}
	if out.State != transfer.StateWritten || out.FinalName != "a.txt" {
		t.Errorf("outcome %+v, want the sink's answer back verbatim", out)
	}
	calls := sink.calls()
	if len(calls) != 1 || calls[0].Name != "a.txt" || calls[0].DestDir != "/home/u" {
		t.Fatalf("sink saw %+v, want one upload of a.txt into /home/u", calls)
	}
	if sink.body != "x" {
		t.Errorf("sink read %q, want the reader's bytes", sink.body)
	}
	if seen != 1 {
		t.Errorf("progress reported %d, want the sink's callback to reach the caller's", seen)
	}
}

// TestHandle_UploadReportsWhatAFailedTransferLeft is the failure path of the
// one external call this method makes: an error and a non-empty Stranded are
// not alternatives (transfer §6), so the handle must return both rather than
// dropping the outcome on the error.
func TestHandle_UploadReportsWhatAFailedTransferLeft(t *testing.T) {
	boom := errors.New("promote failed")
	sink := &stubSink{
		out: transfer.Outcome{Stranded: []string{"/home/u/a.txt.nocx-upload-9f"}},
		err: boom,
	}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", sink)
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	out, err := h.Upload(context.Background(), anUpload(), strings.NewReader("x"), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("error %v, want the sink's failure unwrapped", err)
	}
	if len(out.Stranded) != 1 || out.Stranded[0] != "/home/u/a.txt.nocx-upload-9f" {
		t.Errorf("outcome %+v, want the stranded path the sink named", out)
	}
}

// TestHandle_UploadAfterReleaseIsRefused closes the second end of the
// handle's validity interval: from Acquire until release every method is
// valid, and after release every method — Upload included — is not.
func TestHandle_UploadAfterReleaseIsRefused(t *testing.T) {
	sink := &stubSink{}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", sink)
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	release()

	_, err = h.Upload(context.Background(), anUpload(), strings.NewReader("x"), nil)
	var released *ErrHandleReleased
	if !errors.As(err, &released) {
		t.Fatalf("Upload after release: %v, want ErrHandleReleased", err)
	}
	if len(sink.calls()) != 0 {
		t.Error("a released handle reached the sink")
	}
}

// TestHandle_UploadAfterBindingCloseIsRefused is the other way the interval
// ends: the binding closed underneath a still-unreleased handle.
func TestHandle_UploadAfterBindingCloseIsRefused(t *testing.T) {
	sink := &stubSink{}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", sink)
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	release() // close waits on the guard; drop it first
	if closeErr := reg.Close(id); closeErr != nil {
		t.Fatal(closeErr)
	}

	_, err = h.Upload(context.Background(), anUpload(), strings.NewReader("x"), nil)
	var released *ErrHandleReleased
	if !errors.As(err, &released) {
		t.Fatalf("Upload after the binding closed: %v, want ErrHandleReleased", err)
	}
	if len(sink.calls()) != 0 {
		t.Error("a closed binding reached the sink")
	}
}

// TestClose_WaitsForAnUploadInFlight states the guard as an interval with
// both ends: Upload counts itself before it reaches the sink and drops the
// count only after the sink returns, so Close cannot reach the provider
// under a running transfer.
//
// It is deliberately not a statement about how LONG a transfer may hold the
// binding — design D8 says the running transfer must not hold this guard at
// all, and the layer that starts one asynchronously is what keeps files.close
// prompt. What this pins is that the guard, while held, is honoured.
func TestClose_WaitsForAnUploadInFlight(t *testing.T) {
	sink := &stubSink{entered: make(chan struct{}), release: make(chan struct{})}
	p := newStubProvider()
	reg := New()
	id, err := reg.Register(p, "s1", "", sink)
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}

	uploaded := make(chan error, 1)
	go func() {
		_, err := h.Upload(context.Background(), anUpload(), strings.NewReader("x"), nil)
		release()
		uploaded <- err
	}()
	<-sink.entered // the transfer is in flight, holding the guard

	closed := make(chan error, 1)
	go func() { closed <- reg.Close(id) }()

	// The provider is not closed while the sink call is in flight. Observed
	// as a state, not as a duration: the close goroutine cannot report
	// before we release the sink, and the provider's closed flag is the
	// thing being asserted.
	if p.closed.Load() {
		t.Fatal("the provider closed underneath a running upload")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned while the upload held the guard: %v", err)
	default:
	}

	close(sink.release)
	if err := <-uploaded; err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("close: %v", err)
	}
	if !p.closed.Load() {
		t.Error("the provider was never closed")
	}
}
