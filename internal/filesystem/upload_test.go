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
// "the refusal costs no round trip" is asserted: the Upload of a binding
// with no write half must answer from the nil sink alone.
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

// TestHandle_UploaderIsRefusedOnABindingWithNoWriteHalf is R1 of the upload
// design at this seam: a binding registered with no sink refuses, and the
// refusal is that nil field rather than a check somebody performs.
//
// It said "a local binding" until D7 was corrected. Both shipped providers
// can write now — a browser drop on a local tab has bytes and no path, so
// it uploads onto the backend's own machine, which is the machine that
// tab's shell is on — so "local" is no longer what makes a binding refuse.
// What makes one refuse is having nothing to write through, which is what
// this asserts and what the next provider that cannot write will inherit.
func TestHandle_UploaderIsRefusedOnABindingWithNoWriteHalf(t *testing.T) {
	reg := New()
	id, err := reg.Register(touchyProvider{t: t}, "s1", "", Capabilities{}) // nil sink = no write half
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = h.Uploader()

	var unsupported *ErrUploadUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("R1: a binding with no write seam refuses structurally; got %v", err)
	}
	if unsupported.BindingID != id {
		t.Errorf("the refusal names binding %q, want %q", unsupported.BindingID, id)
	}
}

// TestHandle_UploaderReachesTheSinkOnARemoteBinding is the paired success:
// on an ordinary remote binding the call hands back the very sink the
// binding was registered with, and a transfer run on it carries the
// instruction, the bytes and the progress callback through.
func TestHandle_UploaderReachesTheSinkOnARemoteBinding(t *testing.T) {
	sink := &stubSink{out: transfer.Outcome{State: transfer.StateWritten, FinalName: "a.txt"}}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "v1:abc", Capabilities{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	sk, err := h.Uploader()
	if err != nil {
		t.Fatalf("Uploader on a remote binding: %v", err)
	}
	var seen int64
	out, err := sk.Put(context.Background(), anUpload(), strings.NewReader("x"), func(n int64) { seen = n })
	if err != nil {
		t.Fatalf("Put on a remote binding: %v", err)
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

// TestHandle_UploaderAfterReleaseIsRefused closes the second end of the
// handle's validity interval: from Acquire until release every method is
// valid, and after release every method — Uploader included — is not. The
// detached Sink of D8 is what a transfer runs on, and a handle that is over
// must not be able to hand one out.
func TestHandle_UploaderAfterReleaseIsRefused(t *testing.T) {
	sink := &stubSink{}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	release()

	_, err = h.Uploader()
	var released *ErrHandleReleased
	if !errors.As(err, &released) {
		t.Fatalf("Uploader after release: %v, want ErrHandleReleased", err)
	}
	if len(sink.calls()) != 0 {
		t.Error("a released handle reached the sink")
	}
}

// TestHandle_UploaderAfterBindingCloseIsRefused is the other way the
// interval ends: the binding closed underneath a still-unreleased handle.
func TestHandle_UploaderAfterBindingCloseIsRefused(t *testing.T) {
	sink := &stubSink{}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{Sink: sink})
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

	_, err = h.Uploader()
	var released *ErrHandleReleased
	if !errors.As(err, &released) {
		t.Fatalf("Uploader after the binding closed: %v, want ErrHandleReleased", err)
	}
	if len(sink.calls()) != 0 {
		t.Error("a closed binding reached the sink")
	}
}

// TestClose_DoesNotWaitForATransferRunningOnTheSink is design D8 at this
// seam, stated with both ends of the interval.
//
// The guard opens at Uploader and closes when Uploader returns — not when
// the transfer does. So a Put that has not returned, and here one that will
// not return until the test says so, holds nothing: Close drains, tears the
// watches down and closes the provider while the sink call is still in
// flight. That is the property files.close and session teardown stand on,
// and the version of this test that came before asserted its opposite.
//
// What makes closing the provider under a live Put safe is the lease
// underneath (see Handle.Uploader): closing it unblocks a call already in
// flight and makes every later one fail. Asserted here as a state — Close
// returns and the provider records itself closed — never as a duration.
func TestClose_DoesNotWaitForATransferRunningOnTheSink(t *testing.T) {
	sink := &stubSink{entered: make(chan struct{}), release: make(chan struct{})}
	p := newStubProvider()
	reg := New()
	id, err := reg.Register(p, "s1", "", Capabilities{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	sk, err := h.Uploader()
	if err != nil {
		t.Fatalf("Uploader: %v", err)
	}
	release() // the synchronous call is over; the transfer holds no guard

	put := make(chan error, 1)
	go func() {
		_, err := sk.Put(context.Background(), anUpload(), strings.NewReader("x"), nil)
		put <- err
	}()
	<-sink.entered // the transfer is in flight and will not return yet

	if err := reg.Close(id); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !p.closed.Load() {
		t.Error("the provider was never closed")
	}

	close(sink.release)
	if err := <-put; err != nil {
		t.Fatalf("put: %v", err)
	}
}
