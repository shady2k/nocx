package update

// The download's three promises (nocx-vh2hr.1.15): a slow body is not a
// failure, a silent body is one, and a body that stops halfway continues from
// where it stopped — with the signed SHA-256 still the only acceptance.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// flushBody pushes what the handler wrote, so a test's body arrives in the
// chunks it means rather than in one write at the end.
func flushBody(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func stagedFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "download")
}

func manifestOf(t *testing.T, payload []byte) (string, int64) {
	t.Helper()
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), int64(len(payload))
}

// SLOWER THAN THE OLD TOTAL BOUND. 12 chunks of 100 ms is 1.2 s, which the old
// `http.Client{Timeout: 30s}` would not have cut — the point is the SHAPE: no
// total bound exists any more, and this one is timed by the server, not slept
// by the test.
func TestASlowBodyIsNotAFailure(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4800)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		for i := 0; i < len(payload); i += 400 {
			end := i + 400
			if end > len(payload) {
				end = len(payload)
			}
			_, _ = w.Write(payload[i:end])
			flushBody(w)
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer server.Close()

	dest := stagedFile(t)
	sum, size := manifestOf(t, payload)
	u := &updater{httpClient: DownloadClient()}
	if err := u.downloadVerified(context.Background(), server.URL, sum, size, dest); err != nil {
		t.Fatalf("a slow body failed: %v", err)
	}
	got, err := os.ReadFile(dest) //nolint:gosec // the staged download
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("staged bytes differ from the served body (%v)", err)
	}
}

// A BODY THAT STOPS. No bytes for longer than the stall window is a named
// failure that says what happened, not a total-time cut-off.
func TestASilentBodyFailsAsAStall(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("partial"))
		flushBody(w)
		<-release // no more bytes, for as long as the test needs
	}))
	defer func() { close(release); server.Close() }()

	dest := stagedFile(t)
	sum, size := manifestOf(t, bytes.Repeat([]byte("y"), 4000))
	u := &updater{httpClient: DownloadClient()}
	u.stallOverride = 200 * time.Millisecond
	var timedOut atomic.Bool
	done := make(chan error, 1)
	go func() { done <- u.downloadVerified(context.Background(), server.URL, sum, size, dest) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDownloadStalled) {
			t.Fatalf("a silent body produced %v, want the stall error", err)
		}
	case <-time.After(10 * time.Second):
		timedOut.Store(true)
	}
	if timedOut.Load() {
		t.Fatal("a silent body never failed: the stall timer is not armed")
	}
}

// A BODY THAT STOPS HALFWAY, THEN RESUMES WITH A RANGE. The server drops the
// first response mid-body and honours the continuation.
func TestADroppedBodyResumesWithRange(t *testing.T) {
	payload := bytes.Repeat([]byte("z"), 3000)
	var ranged atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-Range") == `"v1"` && r.Header.Get("Range") != "" {
			ranged.Add(1)
		}
		if ranged.Load() == 0 && r.Header.Get("Range") == "" {
			// First attempt: half the bytes and a broken connection.
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload[:len(payload)/2])
			flushBody(w)
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			return
		}
		// Continuation: the rest, by range.
		start := len(payload) / 2
		if raw := r.Header.Get("Range"); raw != "" {
			var from int
			if _, err := fmt.Sscanf(raw, "bytes=%d-", &from); err == nil {
				start = from
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start:])
	}))
	defer server.Close()

	dest := stagedFile(t)
	sum, size := manifestOf(t, payload)
	u := &updater{httpClient: DownloadClient()}
	if err := u.downloadVerified(context.Background(), server.URL, sum, size, dest); err != nil {
		t.Fatalf("a resumable download failed: %v", err)
	}
	if ranged.Load() == 0 {
		t.Fatal("the continuation was not a ranged request")
	}
	got, err := os.ReadFile(dest) //nolint:gosec // the staged download
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("resumed bytes differ from the served body (%v)", err)
	}
}

// PROGRESS IS MONOTONIC AND ENDS AT THE TOTAL.
func TestProgressIsMonotonicAndEndsAtTheTotal(t *testing.T) {
	payload := bytes.Repeat([]byte("p"), 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var seen []int64
	var total int64
	u := &updater{httpClient: DownloadClient(), progress: func(done, sum int64) {
		seen = append(seen, done)
		total = sum
	}}
	dest := stagedFile(t)
	sum, size := manifestOf(t, payload)
	if err := u.downloadVerified(context.Background(), server.URL, sum, size, dest); err != nil {
		t.Fatal(err)
	}
	if total != size {
		t.Fatalf("progress named total %d, manifest says %d", total, size)
	}
	if len(seen) == 0 || seen[len(seen)-1] != size {
		t.Fatalf("progress ended at %v, want %d", seen, size)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("progress went backwards: %v", seen)
		}
	}
}
