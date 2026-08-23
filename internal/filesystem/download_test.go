package filesystem

// The read-stream seam at the binding, mirroring upload_test.go row for
// row. It is a mirror rather than a copy: each of these is the assertion
// that would fail if the download half were wired to the wrong field, held
// the guard for the transfer's length, or answered after its handle was
// over — and none of the upload tests can see any of that, because they
// exercise a different field.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

// stubSource is the read half a binding carries. It records what it was
// asked for and can be gated so a test can hold one Get in flight — the
// shape the close-does-not-wait interval needs.
type stubSource struct {
	mu    sync.Mutex
	opens []string
	gets  int
	body  string

	openErr error

	entered chan struct{} // closed when Get is reached, when gated
	release chan struct{} // the test closes it to let Get finish
}

func (s *stubSource) Open(path string) (*transfer.Download, error) {
	s.mu.Lock()
	s.opens = append(s.opens, path)
	s.mu.Unlock()
	if s.openErr != nil {
		return nil, s.openErr
	}
	return &transfer.Download{Path: path, Name: "a.txt", Size: int64(len(s.body))}, nil
}

func (s *stubSource) Get(_ context.Context, d *transfer.Download, w io.Writer, progress func(int64)) (int64, error) {
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	n, err := io.WriteString(w, s.body)
	if progress != nil {
		progress(int64(n))
	}
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	return int64(n), err
}

func (s *stubSource) opened() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.opens...)
}

// TestHandle_DownloaderIsRefusedOnABindingWithNoReadStreamHalf is rule R1
// in the read direction at this seam: a binding registered with no source
// refuses, and the refusal is that nil field rather than a check somebody
// performs. Reading a file off the wrong host is as wrong as writing to it.
//
// touchyProvider is what makes it more than a nil check: the refusal must
// cost no round trip, so a binding with no read half must answer from the
// missing field alone and never reach the provider.
func TestHandle_DownloaderIsRefusedOnABindingWithNoReadStreamHalf(t *testing.T) {
	reg := New()
	id, err := reg.Register(touchyProvider{t: t}, "s1", "", Capabilities{}) // nil source = no read-stream half
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = h.Downloader()

	var unsupported *ErrDownloadUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("R1: a binding with no read-stream seam refuses structurally; got %v", err)
	}
	if unsupported.BindingID != id {
		t.Errorf("the refusal names binding %q, want %q", unsupported.BindingID, id)
	}
}

// The two seams are INDEPENDENT, which is the thing a struct of
// capabilities makes possible and a pair of positional parameters made easy
// to cross. A binding that can be written to and not read from must refuse
// exactly one of the two, and the day somebody wires Capabilities.Source
// from the Uploader assertion this is the test that says so.
func TestHandle_TheTwoSeamsAreIndependent(t *testing.T) {
	reg := New()
	sink := &stubSink{}
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, err := h.Uploader(); err != nil {
		t.Fatalf("Uploader on a write-only binding: %v, want the sink", err)
	}
	var unsupported *ErrDownloadUnsupported
	if _, err := h.Downloader(); !errors.As(err, &unsupported) {
		t.Fatalf("Downloader on a write-only binding: %v, want ErrDownloadUnsupported", err)
	}
}

// TestHandle_DownloaderReachesTheSourceOnARemoteBinding is the paired
// success: on an ordinary binding the call hands back the very source the
// binding was registered with, and a transfer run on it carries the path,
// the bytes and the progress callback through.
func TestHandle_DownloaderReachesTheSourceOnARemoteBinding(t *testing.T) {
	src := &stubSource{body: "the file's bytes"}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "v1:abc", Capabilities{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	got, err := h.Downloader()
	if err != nil {
		t.Fatalf("Downloader on a remote binding: %v", err)
	}
	d, err := got.Open("/home/u/a.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var sink stringWriter
	var seen int64
	n, err := got.Get(context.Background(), d, &sink, func(total int64) { seen = total })
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if opens := src.opened(); len(opens) != 1 || opens[0] != "/home/u/a.txt" {
		t.Fatalf("source saw %v, want one open of /home/u/a.txt", opens)
	}
	if sink.String() != "the file's bytes" || n != int64(len("the file's bytes")) {
		t.Errorf("got %q (%d bytes), want the source's own", sink.String(), n)
	}
	if seen != n {
		t.Errorf("progress reported %d, want the source's callback to reach the caller's", seen)
	}
}

// TestHandle_DownloaderAfterReleaseIsRefused closes the second end of the
// handle's validity interval. The detached Source of D8 is what a transfer
// runs on, and a handle that is over must not be able to hand one out.
func TestHandle_DownloaderAfterReleaseIsRefused(t *testing.T) {
	src := &stubSource{}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	release()

	_, err = h.Downloader()
	var released *ErrHandleReleased
	if !errors.As(err, &released) {
		t.Fatalf("Downloader after release: %v, want ErrHandleReleased", err)
	}
	if len(src.opened()) != 0 {
		t.Error("a released handle reached the source")
	}
}

// TestHandle_DownloaderAfterBindingCloseIsRefused is the other way the
// interval ends: the binding closed underneath a still-unreleased handle.
func TestHandle_DownloaderAfterBindingCloseIsRefused(t *testing.T) {
	src := &stubSource{}
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{Source: src})
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

	_, err = h.Downloader()
	var released *ErrHandleReleased
	if !errors.As(err, &released) {
		t.Fatalf("Downloader after the binding closed: %v, want ErrHandleReleased", err)
	}
	if len(src.opened()) != 0 {
		t.Error("a closed binding reached the source")
	}
}

// TestClose_DoesNotWaitForADownloadRunningOnTheSource is design D8 at this
// seam, with both ends of the interval named.
//
// The guard opens at Downloader and closes when Downloader returns — not
// when the transfer does. So a Get that has not returned, and here one that
// will not return until the test says so, holds nothing: Close drains,
// tears the watches down and closes the provider while the read is still in
// flight. That is the property files.close and session teardown stand on,
// and it is asserted as a STATE — Close returns and the provider records
// itself closed — never as a duration.
func TestClose_DoesNotWaitForADownloadRunningOnTheSource(t *testing.T) {
	src := &stubSource{body: "x", entered: make(chan struct{}), release: make(chan struct{})}
	p := newStubProvider()
	reg := New()
	id, err := reg.Register(p, "s1", "", Capabilities{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.Downloader()
	if err != nil {
		t.Fatalf("Downloader: %v", err)
	}
	d, err := got.Open("/home/u/a.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	release() // the synchronous call is over; the transfer holds no guard

	done := make(chan error, 1)
	go func() {
		var w stringWriter
		_, err := got.Get(context.Background(), d, &w, nil)
		done <- err
	}()
	<-src.entered // the transfer is in flight and will not return yet

	if err := reg.Close(id); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !p.closed.Load() {
		t.Error("the provider was never closed")
	}

	close(src.release)
	if err := <-done; err != nil {
		t.Fatalf("get: %v", err)
	}
}

// stringWriter is the client end of a download in these tests.
type stringWriter struct {
	mu sync.Mutex
	b  []byte
}

func (w *stringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *stringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}
