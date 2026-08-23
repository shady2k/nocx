package transport

// Behavioural tests for files.download and files.downloadCancel — the
// download surface's control plane.
//
// Four of them are the rules rather than the features, and they are why the
// file exists: R1 (a binding with no read-stream half is refused, proved
// from the WIRE and not from the handle), the path rules (the same ones
// files.upload applies to its destination), D8 (closing a binding cancels
// its transfers instead of waiting for them), and the one that says out
// loud why R2 has no counterpart here.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// writeOnlyProvider is a provider that can be written to and NOT read from:
// it implements filesystem.Uploader and not filesystem.Downloader. It is
// declared here rather than borrowed from the upload tests because R1's
// negative case must be a deliberate fixture — a test that leaned on
// another file's provider happening to lack a method would go green for the
// wrong reason the day that file changed.
type writeOnlyProvider struct {
	filesystem.Provider
	sink transfer.Sink
}

func (p *writeOnlyProvider) Sink() transfer.Sink { return p.sink }

func writeOnlyFactory(sess session.Session, rootPath string) (filesystem.Provider, error) {
	p, err := filesLocalFactory(sess, rootPath)
	if err != nil {
		return nil, err
	}
	return &writeOnlyProvider{Provider: p, sink: transfer.NewSink(osRemoteFS{}, transfer.DefaultChunk)}, nil
}

// readableProvider carries a Source the test chose, so a test can decide
// how the read behaves without a server.
type readableProvider struct {
	filesystem.Provider
	source transfer.Source
}

func (p *readableProvider) Source() transfer.Source { return p.source }

func downloadFactoryWithSource(src transfer.Source) FilesystemProviderFactory {
	return func(sess session.Session, rootPath string) (filesystem.Provider, error) {
		p, err := filesLocalFactory(sess, rootPath)
		if err != nil {
			return nil, err
		}
		return &readableProvider{Provider: p, source: src}, nil
	}
}

// newDownloadTestEnv boots a server whose bindings can be read from — the
// REAL local provider, so the tests drive the real source, the real chunk
// loop and real files rather than a stand-in that agrees with the transport
// by construction.
func newDownloadTestEnv(t *testing.T, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	return newFilesTestEnv(t, opts...)
}

func newDownloadTestEnvWith(t *testing.T, factory FilesystemProviderFactory, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	return newUploadTestEnvWith(t, log.NewSlogAdapter(nil), factory, opts...)
}

type downloadResult struct {
	TransferID string `json:"transferId"`
	Ticket     string `json:"ticket"`
	URL        string `json:"url"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
}

type downloadEnvelope struct {
	Result json.RawMessage  `json:"result"`
	Error  *jsonrpcErrorObj `json:"error"`
}

func callDownload(t *testing.T, conn *websocket.Conn, params any, id int) downloadEnvelope {
	t.Helper()
	var env downloadEnvelope
	if err := json.Unmarshal(jsonrpcCallWithID(t, conn, "files.download", params, id), &env); err != nil {
		t.Fatalf("files.download: unmarshal: %v", err)
	}
	return env
}

func (e downloadEnvelope) mustResult(t *testing.T) downloadResult {
	t.Helper()
	if e.Error != nil {
		t.Fatalf("files.download: %+v", e.Error)
	}
	var got downloadResult
	if err := json.Unmarshal(e.Result, &got); err != nil {
		t.Fatalf("files.download: decode result: %v", err)
	}
	return got
}

func downloadParams(bid, path string) map[string]any {
	return map[string]any{"bindingId": bid, "path": path}
}

// fixture writes a file into dir and returns its absolute path.
func fixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ── R1: only from the host the tab is actually on ────────────────────────

// TestFilesDownload_IsRefusedOnABindingWithNoReadStreamHalf is R1 over the
// wire, and it is the most important test in this file. The refusal is a
// missing field on the binding rather than a check the handler performs, so
// what this proves is that the handler asks the BINDING and cannot answer
// anything the binding did not say — the same one call whose answer the
// transfer then runs on.
func TestFilesDownload_IsRefusedOnABindingWithNoReadStreamHalf(t *testing.T) {
	e := newDownloadTestEnvWith(t, writeOnlyFactory)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "bytes")
	bid := e.openBinding(t, sid, dir, 2)

	resp := callDownload(t, e.conn, downloadParams(bid, p), 3)

	if resp.Error == nil {
		t.Fatal("R1: a binding with no read-stream half must refuse a download")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code %d, want -32602: the caller named a binding that cannot do this, which is a property of the request", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "no read-stream seam") {
		t.Errorf("message %q, want the binding's own refusal", resp.Error.Message)
	}
	if n := len(e.ws.transfers.pick(func(*runningTransfer) bool { return true })); n != 0 {
		t.Fatalf("%d transfers were registered by a refused download", n)
	}
}

// A binding somebody else's connection owns is not reachable, and the check
// is Registry.Acquire's rather than this handler's — which is what makes it
// impossible to forget.
func TestFilesDownload_IsRefusedOnABindingThisConnectionDoesNotOwn(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "bytes")
	bid := e.openBinding(t, sid, dir, 2)

	other := connectWS(t, e.ws)
	defer func() { _ = other.Close() }()

	resp := callDownload(t, other, downloadParams(bid, p), 3)
	if resp.Error == nil {
		t.Fatal("a connection that owns neither the session nor the binding downloaded through it")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code %d, want -32602", resp.Error.Code)
	}
}

// ── R2 has no counterpart, and the test says why ─────────────────────────

// TestFilesDownload_NamesItsSourceAndThatIsCorrect is the assertion a
// reviewer coming from files.upload will look for, written as a test so
// that the answer is in the suite rather than only in a comment.
//
// files.upload has no sourcePath because a path on the BACKEND's disk is
// scoped by nothing. A download's path is scoped by the binding it is
// addressed through — the same caller can already enumerate it with
// files.list and read it with files.read on the same authority — so naming
// it is not new authority and no ticket is minted for it. What the request
// still cannot do is reach a binding it does not own, which is the test
// above, and it cannot smuggle a second path in under another name, which
// is the decoder's job and is what this asserts.
func TestFilesDownload_RefusesAnyParameterItDoesNotDeclare(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "bytes")
	bid := e.openBinding(t, sid, dir, 2)

	for name, extra := range map[string]string{
		"a second path":              "sourcePath",
		"a host":                     "host",
		"a size the caller invented": "size",
	} {
		t.Run(name, func(t *testing.T) {
			params := downloadParams(bid, p)
			params[extra] = "/etc/shadow"
			resp := callDownload(t, e.conn, params, 3)
			if resp.Error == nil {
				t.Fatalf("the decoder accepted %q; a tolerant decoder reads as 'that parameter is ignored'", extra)
			}
			if resp.Error.Code != -32602 {
				t.Errorf("code %d, want -32602", resp.Error.Code)
			}
		})
	}
}

// ── path rules, the same ones files.upload applies ───────────────────────

func TestFilesDownload_PathRules(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	cases := map[string]string{
		"empty":         "",
		"relative":      "relative/a.txt",
		"not clean":     "/tmp/../etc/passwd",
		"a bare dot":    ".",
		"a traversal":   "/tmp/./a.txt",
		"absurdly long": "/" + strings.Repeat("a", 1<<16),
	}
	id := 3
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			resp := callDownload(t, e.conn, downloadParams(bid, path), id)
			id++
			if resp.Error == nil {
				t.Fatalf("path %q was accepted", path)
			}
			if resp.Error.Code != -32602 {
				t.Errorf("code %d, want -32602 before anything is opened", resp.Error.Code)
			}
		})
	}
}

// The three refusals a real filesystem produces, each of which must arrive
// as something the person can act on rather than as "the server went
// wrong". A -32603 here would tell somebody who picked a folder to try
// again later.
func TestFilesDownload_RefusesWhatCannotBeStreamed(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"a missing file": filepath.Join(dir, "nope.txt"),
		"a directory":    sub,
	} {
		t.Run(name, func(t *testing.T) {
			resp := callDownload(t, e.conn, downloadParams(bid, path), 3)
			if resp.Error == nil {
				t.Fatalf("%s was accepted for download", name)
			}
			if resp.Error.Code != -32602 {
				t.Errorf("code %d, want -32602: this is a property of what the caller asked for", resp.Error.Code)
			}
		})
	}
}

// ── the paired success, over the real wire ───────────────────────────────

// TestFilesDownload_PinsAndMeasuresTheFile is the answer half: on an
// ordinary binding the call returns a ticket, a URL on this route, the
// file's base name and the size measured on the OPEN handle.
func TestFilesDownload_PinsAndMeasuresTheFile(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	body := strings.Repeat("nocx", 300)
	p := fixture(t, dir, "report.bin", body)
	bid := e.openBinding(t, sid, dir, 2)

	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	if !isLowerHex(got.TransferID, 32) {
		t.Errorf("transferId %q is not the 32-hex id the backend mints", got.TransferID)
	}
	if !isLowerHex(got.Ticket, uploadTicketHexLen) {
		t.Errorf("ticket %q is not the 64-hex credential", got.Ticket)
	}
	if got.URL != "/download/"+got.Ticket {
		t.Errorf("url %q, want /download/{ticket}", got.URL)
	}
	if got.Name != "report.bin" {
		t.Errorf("name %q, want the base name and never a path", got.Name)
	}
	if got.Size != int64(len(body)) {
		t.Errorf("size %d, want the file's %d", got.Size, len(body))
	}
	// The transfer exists and is waiting for somebody to fetch it.
	if rt := e.ws.transfers.get(got.TransferID); rt == nil || rt.dir != dirDownload {
		t.Fatal("the transfer was not registered as a download")
	}
}

// The name that goes back is a BASE name and never the directory. The
// person asked for one file, the directory is already on their screen, and
// a result that echoed the path would put it in the browser's save dialog.
func TestFilesDownload_AnswersABaseNameNeverAPath(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	sub := filepath.Join(dir, "deep")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	p := fixture(t, sub, "buried.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)

	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
	if strings.ContainsAny(got.Name, `/\`) {
		t.Fatalf("name %q carries a separator", got.Name)
	}
	if got.Name != "buried.txt" {
		t.Fatalf("name %q, want buried.txt", got.Name)
	}
}

// ── cancellation ─────────────────────────────────────────────────────────

// files.downloadCancel is idempotent by design: the person's cancel races
// the transfer's own completion every time, and losing that race is not a
// failure to show them.
func TestFilesDownloadCancel_IsIdempotent(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", strings.Repeat("x", 4096))
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	for i, id := range []int{4, 5, 6} {
		resp := jsonrpcCallWithID(t, e.conn, "files.downloadCancel", map[string]any{
			"transferId": started.TransferID,
		}, id)
		var env downloadEnvelope
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Error != nil {
			t.Fatalf("cancel %d: %+v", i, env.Error)
		}
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateCancelled {
		t.Fatalf("state %q, want %q", state, downloadStateCancelled)
	}
	// And a transfer that never existed is not an error either.
	resp := jsonrpcCallWithID(t, e.conn, "files.downloadCancel", map[string]any{
		"transferId": strings.Repeat("ff", 16),
	}, 7)
	var env downloadEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("cancelling a transfer that never existed: %+v", env.Error)
	}
}

// The two cancels are not interchangeable. Upload and download ids are the
// same shape and live in the same registry, so naming one at the other's
// method is expressible; honouring it would stop a transfer on a surface
// the person was not looking at.
func TestFilesDownloadCancel_WillNotCancelAnUpload(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	up := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 1<<20), 3).mustResult(t)

	resp := jsonrpcCallWithID(t, e.conn, "files.downloadCancel", map[string]any{
		"transferId": up.TransferID,
	}, 4)
	var env downloadEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.downloadCancel: %+v", env.Error)
	}
	rt := e.ws.transfers.get(up.TransferID)
	if rt == nil {
		t.Fatal("the upload vanished")
	}
	select {
	case <-rt.done:
		t.Fatal("files.downloadCancel stopped an UPLOAD")
	default:
	}
	// And the reverse, so the guard is not one-way.
	e2 := newDownloadTestEnv(t)
	sid2 := e2.openSession(t, 1)
	dir2 := t.TempDir()
	p := fixture(t, dir2, "a.txt", strings.Repeat("y", 4096))
	bid2 := e2.openBinding(t, sid2, dir2, 2)
	down := callDownload(t, e2.conn, downloadParams(bid2, p), 3).mustResult(t)
	resp = jsonrpcCallWithID(t, e2.conn, "files.uploadCancel", map[string]any{
		"transferId": down.TransferID,
	}, 4)
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.uploadCancel: %+v", env.Error)
	}
	drt := e2.ws.transfers.get(down.TransferID)
	select {
	case <-drt.done:
		t.Fatal("files.uploadCancel stopped a DOWNLOAD")
	default:
	}
}

// A cancel from a connection that does not own the transfer's session does
// nothing at all — quietly, because telling it apart from an already
// finished transfer would say whether the id names anything.
func TestFilesDownloadCancel_IgnoresAConnectionThatDoesNotOwnTheSession(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", strings.Repeat("x", 4096))
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	other := connectWS(t, e.ws)
	defer func() { _ = other.Close() }()
	resp := jsonrpcCallWithID(t, other, "files.downloadCancel", map[string]any{
		"transferId": started.TransferID,
	}, 4)
	var env downloadEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.downloadCancel: %+v", env.Error)
	}
	rt := e.ws.transfers.get(started.TransferID)
	select {
	case <-rt.done:
		t.Fatal("a connection that owns nothing cancelled somebody else's download")
	default:
	}
}

// ── D8: the transfer holds no use-guard ──────────────────────────────────

// TestFilesClose_CancelsARunningDownloadRatherThanWaitingForIt is D8 over
// the wire, and it is asserted with an UNRESPONSIVE source — one that does
// not honour cancellation — because that is the only shape in which the
// claim means anything. A source that checks ctx.Done would unwind on the
// cancel alone and the test would pass whatever the guard did.
func TestFilesClose_CancelsARunningDownloadRatherThanWaitingForIt(t *testing.T) {
	src := &unresponsiveSource{entered: make(chan struct{}), release: make(chan struct{})}
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src), withUploadUnwind(50*time.Millisecond))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	go func() {
		_, _ = getDownloadRaw(e.ws, started.Ticket)
	}()
	<-src.entered // the transfer is in flight and will not return yet

	// files.close must ANSWER — that is the assertion, and it is a state
	// rather than a duration: a binding whose close waited on the transfer
	// would still be inside this call when the test's own failsafe fired.
	resp := jsonrpcCallWithID(t, e.conn, "files.close", map[string]any{"bindingId": bid}, 4)
	var env downloadEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.close while a download ran: %+v", env.Error)
	}
	close(src.release)
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateCancelled {
		t.Fatalf("state %q, want %q", state, downloadStateCancelled)
	}
}

// unresponsiveSource does NOT honour cancellation: Get blocks until the
// test releases it, whatever the context says. It is the shape of every
// provider call that can outlive a cancel — a wedged lane call, a server
// that has stopped answering — and the only shape in which D8's teardown
// claim is worth asserting.
type unresponsiveSource struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *unresponsiveSource) Open(path string) (*transfer.Download, error) {
	return transfer.NewSource(pinnedFS{body: "held"}, transfer.DefaultChunk).Open(path)
}

func (s *unresponsiveSource) Get(context.Context, *transfer.Download, io.Writer, func(int64)) (int64, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return 0, context.Canceled
}

// pinnedFS is a one-file read surface, so a fixture source can hand back a
// real *transfer.Download without a filesystem.
type pinnedFS struct{ body string }

func (f pinnedFS) Open(string) (transfer.RemoteReader, int64, error) {
	return io.NopCloser(strings.NewReader(f.body)), int64(len(f.body)), nil
}

// A download that nobody fetches is cancelled by the ticket's own expiry
// timer, not left holding a descriptor for ever. Expiry is not one of the
// four ticket states: the timer drops the ticket AND cancels the transfer
// it named, at that moment, so a late fetch simply finds nothing.
func TestFilesDownload_AnUnfetchedTransferExpires(t *testing.T) {
	e := newDownloadTestEnv(t, WithTransferTicketTTL(0))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateCancelled {
		t.Fatalf("state %q, want %q — an unfetched download must not outlive its ticket", state, downloadStateCancelled)
	}
	code, _ := getDownload(t, e.ws, started.Ticket)
	if code != 404 && code != 410 {
		t.Fatalf("a fetch after expiry answered %d, want 410", code)
	}
}

// Shutting the server down cancels every transfer in flight, downloads
// included — the registry is one registry, so this needs no code of its own
// and that is exactly what is being asserted.
func TestServerStop_CancelsRunningDownloads(t *testing.T) {
	src := &unresponsiveSource{entered: make(chan struct{}), release: make(chan struct{})}
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src), withUploadUnwind(50*time.Millisecond))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "x")
	bid := e.openBinding(t, sid, dir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
	go func() { _, _ = getDownloadRaw(e.ws, started.Ticket) }()
	<-src.entered

	rt := e.ws.transfers.get(started.TransferID)
	stopped := make(chan error, 1)
	go func() { stopped <- e.ws.Stop(context.Background()) }()

	select {
	case <-rt.ctx.Done(): // the cancellation reached it
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown never cancelled the running download") // failsafe, not the assertion
	}
	close(src.release)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
