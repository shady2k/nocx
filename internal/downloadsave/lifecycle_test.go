package downloadsave

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/transfer"
)

// A real on-disk sink whose file close is paused at the cleanup boundary.
type (
	closingFS   struct{ entered, release chan struct{} }
	closingFile struct {
		*os.File
		fs closingFS
	}
)

func (f closingFile) Close() error {
	close(f.fs.entered)
	<-f.fs.release
	return f.File.Close()
}

func (f closingFS) Create(path string) (transfer.RemoteFile, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // test-owned directory
	if err != nil {
		return nil, err
	}
	return closingFile{File: file, fs: f}, nil
}
func (closingFS) PosixRename(old, next string) error { return os.Rename(old, next) }
func (closingFS) Rename(old, next string) error {
	if err := os.Link(old, next); err != nil {
		return err
	}
	return os.Remove(old)
}
func (closingFS) Remove(path string) error { return os.Remove(path) }

type contextSink struct {
	transfer.Sink
	started chan context.Context
}

func (s contextSink) Put(ctx context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	s.started <- ctx
	return s.Sink.Put(ctx, u, r, progress)
}

func TestCloseJoinsCleanupIncludingAlreadyDiscardedSave(t *testing.T) {
	for _, discardFirst := range []bool{false, true} {
		name := "active"
		if discardFirst {
			name = "already-discarded"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Trailer", "X-Nocx-Download-Status")
				_, _ = io.WriteString(w, "body")
				w.Header().Set("X-Nocx-Download-Status", "sent")
			}))
			defer server.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "target")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			fs := closingFS{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(fs.release) }) }
			sink := contextSink{Sink: transfer.NewSink(fs, transfer.DefaultChunk), started: make(chan context.Context, 1)}
			svc := newTestService(t, &fixedPicker{path: path}, sink, func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, nil)
			t.Cleanup(release)
			handle, err := svc.Prepare(context.Background(), "target")
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan Result, 1)
			go func() { result <- svc.Save(context.Background(), handle, strings.Repeat("a", 64), 4) }()
			ctx := <-sink.started
			<-fs.entered
			if discardFirst {
				svc.Discard(handle)
			}
			closed := make(chan struct{})
			go func() { svc.Close(); close(closed) }()
			<-ctx.Done()
			select {
			case <-closed:
				t.Fatal("shutdown returned while file cleanup was paused")
			case <-time.After(100 * time.Millisecond):
			}
			release()
			<-closed
			if got := <-result; got.Outcome != "cancelled" {
				t.Fatalf("save outcome %q", got.Outcome)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "target" {
				t.Fatalf("cleanup left entries %v: %v", entries, err)
			}
			got, err := os.ReadFile(path) //nolint:gosec // test-owned path
			if err != nil || string(got) != "old" {
				t.Fatalf("cancel changed target to %q: %v", got, err)
			}
		})
	}
}

func TestExpiryReleasesDestinationBeforeNonCooperativePrompt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	now := time.Unix(100, 0)
	picker := &fixedPicker{path: filepath.Join(t.TempDir(), "file")}
	var destination *local.DownloadDestination
	svc, err := New(Config{
		Picker: picker,
		PrepareDestination: func(path string) (Destination, error) {
			prepared, err := local.PrepareDownload(path)
			destination = prepared
			return prepared, err
		},
		Address: func() (string, error) { return "127.0.0.1:1", nil },
		Now:     func() time.Time { return now }, Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	if _, err := svc.Prepare(context.Background(), "file"); err != nil {
		t.Fatal(err)
	}
	expired := destination
	now = now.Add(handleTTL)
	picker.call = func() { close(entered); <-release }
	t.Cleanup(unblock)
	prepared := make(chan string, 1)
	go func() { handle, _ := svc.Prepare(context.Background(), "file"); prepared <- handle }()
	<-entered
	if _, err := expired.Put(context.Background(), 0, strings.NewReader("")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expired destination was not closed before next prompt: %v", err)
	}
	closed := make(chan struct{})
	go func() { svc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for an uncooperative OS prompt")
	}
	unblock()
	if handle := <-prepared; handle != "" {
		t.Fatalf("shutdown prompt published handle %q", handle)
	}
}

func TestPreparedCancellationReportsCancelledWithoutGETOrReplacement(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Trailer", "X-Nocx-Download-Status")
		_, _ = io.WriteString(w, "new")
		w.Header().Set("X-Nocx-Download-Status", "sent")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, &fixedPicker{path: path}, nil, func() (string, error) { return server.Listener.Addr().String(), nil }, time.Now, nil)
	handle, err := svc.Prepare(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	svc.Discard(handle)
	if got := svc.Save(context.Background(), handle, strings.Repeat("a", 64), 3).Outcome; got != "cancelled" {
		t.Fatalf("cancelled prepared save outcome %q", got)
	}
	if requests.Load() != 0 {
		t.Fatal("cancelled prepared save fetched its ticket")
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil || string(got) != "old" {
		t.Fatalf("cancelled prepared save changed file to %q: %v", got, err)
	}
}
