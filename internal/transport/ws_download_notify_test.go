package transport

// The two download notifications, and the asymmetry between them.
//
// files.downloadProgress is live and LOSSY: current subscriber, resolved at
// emit time, dropped when there is none. files.downloadDone is RETAINED
// when there is none and flushed on attach. The reason is the same one the
// upload surface has: a lost progress frame costs a bar that jumps, and a
// lost terminal outcome leaves the indicator saying "downloading" for the
// rest of the session about a transfer that ended ten minutes ago.
//
// These are separate tests from the upload ones rather than a shared table,
// because what is being asserted is that the DOWNLOAD half is wired into
// the retention path at all — and no upload test can see that.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/transfer"
)

const pausingSourceReported = 2048

// pausingSource reports progress and then holds inside Get until released,
// so a progress notification is guaranteed rather than raced: the emitter
// chooses between the progress mailbox and the transfer's done channel, and
// a transfer that could settle first would legitimately emit nothing.
type pausingSource struct {
	reported  chan struct{}
	released  chan struct{}
	closeOnce sync.Once
}

func newPausingSource() *pausingSource {
	return &pausingSource{reported: make(chan struct{}), released: make(chan struct{})}
}

func (s *pausingSource) Open(path string) (*transfer.Download, error) {
	return transfer.NewSource(pinnedFS{body: strings.Repeat("z", 4096)}, transfer.DefaultChunk).Open(path)
}

func (s *pausingSource) Get(_ context.Context, d *transfer.Download, w io.Writer, progress func(int64)) (int64, error) {
	progress(pausingSourceReported)
	close(s.reported)
	<-s.released
	n, err := w.Write([]byte("done"))
	return int64(n), err
}

func (s *pausingSource) release() { s.closeOnce.Do(func() { close(s.released) }) }

// failingSource is a read half that fails part-way and says how far it got.
type failingSource struct {
	err  error
	sent int64
}

func (f *failingSource) Open(path string) (*transfer.Download, error) {
	return transfer.NewSource(pinnedFS{body: strings.Repeat("q", 4096)}, transfer.DefaultChunk).Open(path)
}

func (f *failingSource) Get(_ context.Context, _ *transfer.Download, _ io.Writer, progress func(int64)) (int64, error) {
	progress(f.sent)
	return f.sent, f.err
}

// TestFilesDownloadProgress_ReachesTheSubscriber is the live half: the
// frame goes to whoever is attached to the transfer's session NOW.
func TestFilesDownloadProgress_ReachesTheSubscriber(t *testing.T) {
	src := newPausingSource()
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	go func() { _, _ = getDownloadRaw(e.ws, started.Ticket) }()
	<-src.reported

	raw := readNotification(t, e.conn, "files.downloadProgress", wantWithin)
	var got filesTransferProgressParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TransferID != started.TransferID {
		t.Errorf("transferId %q, want %q", got.TransferID, started.TransferID)
	}
	if got.Bytes != pausingSourceReported {
		t.Errorf("bytes %d, want the %d the source reported", got.Bytes, pausingSourceReported)
	}
	if got.Total != started.Size {
		t.Errorf("total %d, want the size measured at mint (%d)", got.Total, started.Size)
	}
	src.release()
	awaitTransferState(t, e.ws, started.TransferID)
}

// The terminal outcome, live, on a transfer that failed: the reason and the
// count of what actually reached the far end — which on a download is the
// whole of the account, because bytes already sent cannot be recalled.
func TestFilesDownloadDone_CarriesTheReasonAndHowFarItGot(t *testing.T) {
	src := &failingSource{err: io.ErrUnexpectedEOF, sent: 1024}
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	go func() { _, _ = getDownloadRaw(e.ws, started.Ticket) }()

	raw := readNotification(t, e.conn, "files.downloadDone", wantWithin)
	var got filesDownloadDoneParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Outcome != downloadStateFailed {
		t.Fatalf("outcome %q, want %q", got.Outcome, downloadStateFailed)
	}
	if got.Error == "" {
		t.Error("a failed download carried no reason")
	}
	if got.Bytes != 1024 {
		t.Errorf("bytes %d, want the 1024 that actually left", got.Bytes)
	}
	if got.Name != "a.txt" {
		t.Errorf("name %q, want the file's own — a person told only 'it failed' cannot tell which download it was", got.Name)
	}
	if got.Total != started.Size {
		t.Errorf("total %d, want %d", got.Total, started.Size)
	}
}

// A cancelled download carries NO error. The underlying error is a context
// cancellation, which is not a fault: the person pressed cancel, or the
// binding went away underneath them.
func TestFilesDownloadDone_ACancelledDownloadIsNotAFailure(t *testing.T) {
	e := newDownloadTestEnv(t, WithTransferTicketTTL(0))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	raw := readNotification(t, e.conn, "files.downloadDone", wantWithin)
	var got filesDownloadDoneParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Outcome != downloadStateCancelled {
		t.Fatalf("outcome %q, want %q", got.Outcome, downloadStateCancelled)
	}
	if got.Error != "" {
		t.Errorf("a cancelled download reported %q as a fault; it is not one", got.Error)
	}
	_ = started
}

// TestFilesDownloadDone_IsRetainedAcrossAReconnect is the half that
// current-subscriber addressing alone does not give. A person can start a
// download of a large file, lose the connection, and the outcome must be
// waiting when they come back — otherwise the indicator says "downloading"
// for ever about a transfer that ended.
func TestFilesDownloadDone_IsRetainedAcrossAReconnect(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	body := "finished while nobody was watching"
	p := fixture(t, dir, "late.txt", body)
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	dropSubscriber(t, e, sid)
	code, gotBody, _, err := getDownloadFull(e.ws, started.Ticket, nil)
	if err != nil || code != http.StatusOK || gotBody != body {
		t.Fatalf("GET = %d %q %v, want 200 and the file", code, gotBody, err)
	}
	awaitTransferState(t, e.ws, started.TransferID)

	connB := reattach(t, e, sid, 4)
	raw := readNotification(t, connB, "files.downloadDone", wantWithin)
	var got filesDownloadDoneParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TransferID != started.TransferID || got.Outcome != downloadStateSent {
		t.Fatalf("got %+v, want the sent outcome of %s", got, started.TransferID)
	}
	if got.Bytes != int64(len(body)) {
		t.Errorf("bytes %d, want the whole %d", got.Bytes, len(body))
	}
	// Cleared on delivery: a second reattach replays nothing.
	connC := reattach(t, e, sid, 5)
	if again := drainNotifications(t, connC, "files.downloadDone", 300*time.Millisecond); len(again) != 0 {
		t.Fatalf("a second reattach replayed %d outcomes, want 0", len(again))
	}
}

// The two directions share ONE retained queue, in the order things
// happened, which is what keeps a reconnect from replaying two independent
// orders that interleave arbitrarily. This is the assertion that the shared
// store is a shared store rather than two that happen to look alike.
func TestTransferDone_OneRetainedQueueInOneOrder(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "download me")
	bid := e.openBinding(t, sid, dir, 2)

	down := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
	up := callUpload(t, e.conn, uploadParams(bid, dir, "b.txt", 5), 4).mustResult(t)

	dropSubscriber(t, e, sid)
	if code, _, _, err := getDownloadFull(e.ws, down.Ticket, nil); err != nil || code != http.StatusOK {
		t.Fatalf("GET = %d %v", code, err)
	}
	awaitTransferState(t, e.ws, down.TransferID)
	if code, resp := postUpload(t, e.ws, up.Ticket, []byte("hello")); code != http.StatusOK {
		t.Fatalf("POST = %d %q", code, resp)
	}
	awaitTransferState(t, e.ws, up.TransferID)

	connB := reattach(t, e, sid, 5)
	// Oldest first: the download settled before the upload did.
	if raw := readNotification(t, connB, "files.downloadDone", wantWithin); raw == nil {
		t.Fatal("the download's outcome was not flushed")
	}
	if raw := readNotification(t, connB, "files.uploadDone", wantWithin); raw == nil {
		t.Fatal("the upload's outcome was not flushed")
	}
}
