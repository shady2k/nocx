package downloadsave

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/transfer"
)

type fixedPicker struct {
	path string
	call func()
	name string
}

func (p *fixedPicker) SaveFile(_ context.Context, name string) (string, error) {
	p.name = name
	if p.call != nil {
		p.call()
	}
	return p.path, nil
}

type observingSink struct {
	called  atomic.Bool
	outcome transfer.Outcome
	err     error
}

func (s *observingSink) Put(_ context.Context, _ transfer.Upload, r io.Reader, _ func(int64)) (transfer.Outcome, error) {
	s.called.Store(true)
	buf := make([]byte, 8192)
	for {
		_, err := r.Read(buf)
		if err == io.EOF {
			return s.outcome, s.err
		}
		if err != nil {
			return transfer.Outcome{}, err
		}
	}
}

func TestPrepareCancelDoesNotCreateCapabilityAndExpiryStartsAfterPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.bin")
	picker := &fixedPicker{path: ""}
	now := time.Unix(100, 0)
	svc := newTestService(t, picker, &observingSink{}, func() (string, error) { return "127.0.0.1:1", nil }, func() time.Time { return now }, nil)
	if handle, err := svc.Prepare(context.Background(), "cancel.bin"); err != nil || handle != "" {
		t.Fatalf("cancel prepare = %q, %v", handle, err)
	}
	if got := len(svc.entries); got != 0 {
		t.Fatalf("cancel left %d entries", got)
	}

	picker.path = path
	picker.call = func() { now = now.Add(2 * time.Minute) }
	handle, err := svc.Prepare(context.Background(), "suggested.bin")
	if err != nil || handle == "" {
		t.Fatalf("prepare = %q, %v", handle, err)
	}
	if picker.name != "suggested.bin" {
		t.Fatalf("suggested name = %q", picker.name)
	}
	if _, ok := svc.entries[handle]; !ok {
		t.Fatal("fresh post-dialog handle expired immediately")
	}
	now = now.Add(handleTTL)
	if result := svc.Save(context.Background(), handle, strings.Repeat("a", 64), 0); result.Outcome != "source-failed" {
		t.Fatalf("expired save outcome = %q", result.Outcome)
	}
}

func TestSaveStreamsAndRequiresSentTrailerBeforeAtomicPromotion(t *testing.T) {
	payload := strings.Repeat("download-data", 7000)
	for _, tc := range []struct {
		name      string
		trailer   string
		sizeDelta int64
		want      string
	}{
		{"sent", "sent", 0, "saved"},
		{"failed", "failed", 0, "source-failed"},
		{"missing", "", 0, "source-failed"},
		{"cancelled", "cancelled", 0, "cancelled"},
		{"short", "sent", 1, "source-failed"},
		{"extra", "sent", -1, "source-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "target.bin")
			if err := os.WriteFile(destination, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Trailer", "X-Nocx-Download-Status")
				_, _ = io.WriteString(w, payload)
				if tc.trailer != "" {
					w.Header().Set("X-Nocx-Download-Status", tc.trailer)
				}
			}))
			defer server.Close()
			picker := &fixedPicker{path: destination}
			svc := newTestService(t, picker, nil, func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, server.Client())
			handle, err := svc.Prepare(context.Background(), "target.bin")
			if err != nil {
				t.Fatal(err)
			}
			result := svc.Save(context.Background(), handle, strings.Repeat("b", 64), int64(len(payload))+tc.sizeDelta)
			if result.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", result.Outcome, tc.want)
			}
			if got := svc.Save(context.Background(), handle, strings.Repeat("b", 64), int64(len(payload))).Outcome; got != "source-failed" {
				t.Fatalf("reused handle outcome = %q", got)
			}
			got, err := os.ReadFile(destination) //nolint:gosec // destination is inside t.TempDir
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "saved" {
				if string(got) != payload {
					t.Fatalf("saved bytes mismatch: got %d bytes", len(got))
				}
			} else if string(got) != "previous" {
				t.Fatalf("source failure changed previous target: %q", got)
			}
		})
	}
}

func TestSaveRejectsUnsafeAddressTicketAndSizeBeforeHTTP(t *testing.T) {
	picker := &fixedPicker{path: filepath.Join(t.TempDir(), "target")}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Trailer", "X-Nocx-Download-Status")
		w.Header().Set("X-Nocx-Download-Status", "sent")
	}))
	defer server.Close()
	address := server.Listener.Addr().String()
	sink := &observingSink{}
	svc := newTestService(t, picker, sink, func() (string, error) { return address, nil }, time.Now, server.Client())
	for _, tc := range []struct {
		ticket string
		size   int64
		addr   string
	}{
		{strings.Repeat("A", 64), 0, address},
		{strings.Repeat("a", 64), -1, address},
		{strings.Repeat("a", 64), 0, "example.com:80"},
	} {
		handle, err := svc.Prepare(context.Background(), "file")
		if err != nil {
			t.Fatal(err)
		}
		address = tc.addr
		if got := svc.Save(context.Background(), handle, tc.ticket, tc.size).Outcome; got != "source-failed" {
			t.Fatalf("invalid input outcome = %q", got)
		}
		if got := svc.Save(context.Background(), handle, strings.Repeat("a", 64), 0).Outcome; got != "source-failed" {
			t.Fatalf("invalid outcome did not consume prepared handle: %q", got)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid requests reached HTTP server %d times", requests.Load())
	}
	if sink.called.Load() {
		t.Fatal("invalid request reached sink")
	}
}

func TestSaveClassifiesLocalWriteFailureAndOnlyReportsSuccessAfterSinkReturns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Trailer", "X-Nocx-Download-Status")
		_, _ = io.WriteString(w, "ok")
		w.Header().Set("X-Nocx-Download-Status", "sent")
	}))
	defer server.Close()
	picker := &fixedPicker{path: filepath.Join(t.TempDir(), "target")}
	svc := newTestService(t, picker, nil, func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, server.Client())
	handle, err := svc.Prepare(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	// A directory can become unwritable after Prepare even with a pinned root.
	if removeErr := os.Remove(filepath.Dir(picker.path)); removeErr != nil {
		t.Fatal(removeErr)
	}
	if got := svc.Save(context.Background(), handle, strings.Repeat("c", 64), 2).Outcome; got != "destination-failed" {
		t.Fatalf("write failure outcome = %q", got)
	}

	gate := make(chan struct{})
	blocking := &returnGateSink{entered: gate, release: make(chan struct{})}
	picker.path = filepath.Join(t.TempDir(), "unused")
	svc = newTestService(t, picker, blocking, func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, server.Client())
	handle, err = svc.Prepare(context.Background(), "unused")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Result, 1)
	go func() { result <- svc.Save(context.Background(), handle, strings.Repeat("d", 64), 2) }()
	<-gate
	select {
	case got := <-result:
		t.Fatalf("returned before sink promotion: %+v", got)
	default:
	}
	close(blocking.release)
	if got := <-result; got.Outcome != "saved" {
		t.Fatalf("success outcome = %q", got.Outcome)
	}
}

type returnGateSink struct {
	entered chan struct{}
	release chan struct{}
}

func (s *returnGateSink) Put(ctx context.Context, _ transfer.Upload, _ io.Reader, _ func(int64)) (transfer.Outcome, error) {
	close(s.entered)
	select {
	case <-s.release:
		return transfer.Outcome{State: transfer.StateWritten}, nil
	case <-ctx.Done():
		return transfer.Outcome{}, ctx.Err()
	}
}

func TestDiscardClosesRunningBodyAndIsIdempotent(t *testing.T) {
	entered := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Nocx-Download-Status")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		flusher.Flush()
		close(entered)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	picker := &fixedPicker{path: filepath.Join(t.TempDir(), "file")}
	sink := &waitingSink{entered: make(chan struct{})}
	svc := newTestService(t, picker, sink, func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, server.Client())
	handle, err := svc.Prepare(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Result, 1)
	go func() { result <- svc.Save(context.Background(), handle, strings.Repeat("e", 64), 0) }()
	<-entered
	<-sink.entered
	svc.Discard(handle)
	svc.Discard(handle)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("discard did not close HTTP request")
	}
	if got := <-result; got.Outcome != "cancelled" {
		t.Fatalf("cancel outcome = %q", got.Outcome)
	}
}

type waitingSink struct{ entered chan struct{} }

func (s *waitingSink) Put(ctx context.Context, _ transfer.Upload, r io.Reader, _ func(int64)) (transfer.Outcome, error) {
	close(s.entered)
	_, err := io.Copy(io.Discard, r)
	return transfer.Outcome{}, err
}

type sinkDestination struct {
	sink   transfer.Sink
	target transfer.Upload
}

func (d *sinkDestination) Put(ctx context.Context, size int64, reader io.Reader) (transfer.Outcome, error) {
	target := d.target
	target.Size = size
	return d.sink.Put(ctx, target, reader, nil)
}

func (*sinkDestination) Close() error { return nil }

func testDestinationFactory(sink transfer.Sink) func(string) (Destination, error) {
	return func(path string) (Destination, error) {
		if sink == nil {
			return local.PrepareDownload(path)
		}
		return &sinkDestination{sink: sink, target: transfer.Upload{
			DestDir: filepath.Dir(path), Name: filepath.Base(path), OnExists: transfer.Overwrite,
		}}, nil
	}
}

func newTestService(t *testing.T, picker Picker, sink transfer.Sink, address func() (string, error), now func() time.Time, client *http.Client) *Service {
	t.Helper()
	svc, err := New(Config{Picker: picker, PrepareDestination: testDestinationFactory(sink), Address: address, Client: client, Now: now, Random: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestNewRequiresClockAndRandomSource(t *testing.T) {
	base := Config{
		Picker:             &fixedPicker{},
		PrepareDestination: testDestinationFactory(&observingSink{}),
		Address:            func() (string, error) { return "127.0.0.1:1", nil },
	}
	if _, err := New(base); err == nil {
		t.Fatal("New accepted missing clock and random source")
	}
	base.Now = time.Now
	if _, err := New(base); err == nil {
		t.Fatal("New accepted missing random source")
	}
	base.Random = rand.Reader
	if _, err := New(base); err != nil {
		t.Fatalf("New rejected required dependencies: %v", err)
	}
}

func TestPrepareBoundsHandlesAndDiscardIsOneShot(t *testing.T) {
	picker := &fixedPicker{path: filepath.Join(t.TempDir(), "selected")}
	now := time.Now()
	svc := newTestService(t, picker, &observingSink{}, func() (string, error) {
		return "127.0.0.1:1", nil
	}, func() time.Time { return now }, nil)
	handles := make([]string, 0, maxHandles)
	for range maxHandles {
		handle, err := svc.Prepare(context.Background(), "file")
		if err != nil || len(handle) != 32 {
			t.Fatalf("prepare = %q, %v", handle, err)
		}
		handles = append(handles, handle)
	}
	if _, err := svc.Prepare(context.Background(), "overflow"); err == nil {
		t.Fatal("65th prepared handle was accepted")
	}
	svc.Discard(handles[0])
	svc.Discard(handles[0])
	if got := svc.Save(context.Background(), handles[0], strings.Repeat("a", 64), 0).Outcome; got != "cancelled" {
		t.Fatalf("discarded handle outcome = %q", got)
	}
	now = now.Add(handleTTL)
	if handle, err := svc.Prepare(context.Background(), "after-expiry"); err != nil || handle == "" {
		t.Fatalf("prepare after expiry = %q, %v", handle, err)
	}
	if handle, err := svc.Prepare(context.Background(), "replacement"); err != nil || handle == "" {
		t.Fatalf("prepare after discard = %q, %v", handle, err)
	}
}

func TestPromptCancellationKeepsPrepareWaitingUntilPromptReturns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	picker := &fixedPicker{
		path: filepath.Join(t.TempDir(), "selected"),
		call: func() {
			close(started)
			<-release
		},
	}
	svc := newTestService(t, picker, &observingSink{}, func() (string, error) {
		return "127.0.0.1:1", nil
	}, time.Now, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan string, 1)
	go func() {
		handle, _ := svc.Prepare(ctx, "file")
		result <- handle
	}()
	<-started
	cancel()
	select {
	case handle := <-result:
		t.Fatalf("prepare returned while prompt was still open: %q", handle)
	default:
	}
	close(release)
	if handle := <-result; handle != "" {
		t.Fatalf("cancelled prompt produced handle %q", handle)
	}
}

func TestZeroByteDownloadRequiresSentTrailer(t *testing.T) {
	for _, trailer := range []string{"sent", "failed", "cancelled"} {
		t.Run(trailer, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Trailer", "X-Nocx-Download-Status")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Fatal("response writer does not support flushing")
				}
				flusher.Flush()
				w.Header().Set("X-Nocx-Download-Status", trailer)
			}))
			defer server.Close()
			destination := filepath.Join(t.TempDir(), "empty")
			if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			svc := newTestService(t, &fixedPicker{path: destination}, nil,
				func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, server.Client())
			handle, err := svc.Prepare(context.Background(), "empty")
			if err != nil {
				t.Fatal(err)
			}
			want := "saved"
			if trailer == "failed" {
				want = "source-failed"
			}
			if trailer == "cancelled" {
				want = "cancelled"
			}
			if got := svc.Save(context.Background(), handle, strings.Repeat("f", 64), 0).Outcome; got != want {
				t.Fatalf("outcome = %q, want %q", got, want)
			}
			got, err := os.ReadFile(destination) //nolint:gosec // destination is inside t.TempDir
			if err != nil {
				t.Fatal(err)
			}
			if trailer == "sent" && len(got) != 0 {
				t.Fatalf("empty save left %d bytes", len(got))
			}
			if trailer != "sent" && string(got) != "old" {
				t.Fatalf("failed empty save changed target to %q", got)
			}
		})
	}
}

func TestHTTPProtocolRejectsRedirectStatusLengthAndUndeclaredTrailer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "redirect",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/elsewhere")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "status",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad", http.StatusBadGateway)
			},
		},
		{
			name: "content-length",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "0")
				w.Header().Set("Trailer", "X-Nocx-Download-Status")
				w.Header().Set("X-Nocx-Download-Status", "sent")
			},
		},
		{
			name: "undeclared-trailer",
			serve: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "x")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tc.serve))
			defer server.Close()
			sink := &observingSink{}
			svc := newTestService(t, &fixedPicker{path: filepath.Join(t.TempDir(), "file")}, sink,
				func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, server.Client())
			handle, err := svc.Prepare(context.Background(), "file")
			if err != nil {
				t.Fatal(err)
			}
			if got := svc.Save(context.Background(), handle, strings.Repeat("a", 64), 0).Outcome; got != "source-failed" {
				t.Fatalf("outcome = %q", got)
			}
			if sink.called.Load() {
				t.Fatal("invalid response reached sink")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTrailerValueWithoutInitialDeclarationIsRejectedBeforeSink(t *testing.T) {
	sink := &observingSink{}
	svc := newTestService(t, &fixedPicker{path: filepath.Join(t.TempDir(), "file")}, sink,
		func() (string, error) { return "127.0.0.1:1", nil }, time.Now, nil)
	svc.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Trailer: http.Header{
				"X-Nocx-Download-Status": {"sent"},
			},
			Body:    io.NopCloser(strings.NewReader("x")),
			Request: request,
		}, nil
	})
	handle, err := svc.Prepare(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.Save(context.Background(), handle, strings.Repeat("a", 64), 1).Outcome; got != "source-failed" {
		t.Fatalf("undeclared trailer outcome = %q", got)
	}
	if sink.called.Load() {
		t.Fatal("undeclared trailer reached sink")
	}
}
