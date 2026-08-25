package transport

// Worker H acceptance tests: context ownership in internal/transport.
//
// Three owners, each naming its closing event:
//
//  1. Request-scoped — the connection context created in handleSession,
//     which ends when handleSession returns. A probe, a dialog, git, files
//     and vault work all belong here: nothing about them may outlive the
//     connection that asked.
//  2. Server- or session-owned — outlives the connection on purpose and
//     keeps context.Background() with a comment naming owner and closing
//     event. pumpToRing (AD-9) is the canonical member.
//  3. Domain-owned commit interval — RestoreImport and vault Setup document
//     their own commit points; the transport never cancels across them.
//
// The structural test at the bottom is the sweep guard: any future
// context.Background() without a named owner and closing event fails the
// suite.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// probeCallRecorder is a cooperative Prober: the first call blocks until its
// context is cancelled (recording that it observed the cancellation);
// later calls succeed immediately. It also counts calls so a test can prove
// the admission slot was not double-booked.
type probeCallRecorder struct {
	mu        sync.Mutex
	calls     int
	started   chan struct{} // closed when the first probe is running
	cancelled chan struct{} // closed when the first probe observes ctx.Done
}

func (p *probeCallRecorder) ProbeWithResult(ctx context.Context, _ string, _ *ssh.ConnectConfig) (string, error) {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	p.mu.Unlock()
	if first {
		close(p.started)
		<-ctx.Done()
		close(p.cancelled)
		return "", ctx.Err()
	}
	return "fp", nil
}

// newProbeTestServerWithProber is newProbeTestServer with the prober injected
// by the caller instead of constructed from a canned error.
func newProbeTestServerWithProber(t *testing.T, prober Prober, resolver ProfileResolver) *WSServer {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	srv := NewWSServer(logger, newRegWithStub(logger),
		WithProfileResolver(resolver),
		WithProber(prober),
	)
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	return srv
}

func (p *probeCallRecorder) Probe(ctx context.Context, host string, cfg *ssh.ConnectConfig) error {
	_, err := p.ProbeWithResult(ctx, host, cfg)
	return err
}

func probeResolver() *fakeResolver {
	return &fakeResolver{
		resolveFn: func(profileID string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test"}, nil
		},
	}
}

// sendControl writes one JSON-RPC request without waiting for its response —
// for calls whose response arrives only after the test has acted (disconnect,
// release, ...).
func sendControl(t *testing.T, conn *websocket.Conn, method string, params any, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// --- acceptance 1: disconnect cancels a running probe; its admission slot
// --- is free for the next connection.

func TestConnectionsTest_DisconnectCancelsProbeAndFreesSlot(t *testing.T) {
	rec := &probeCallRecorder{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	srv := newProbeTestServerWithProber(t, rec, probeResolver())

	conn := connectWS(t, srv)
	sendControl(t, conn, "connections.test", map[string]any{"profileId": "ssh:test:1"}, 1)

	// The probe is running (off the read loop). Disconnect the socket: the
	// connection context must be cancelled, and the probe must observe it.
	select {
	case <-rec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}
	_ = conn.Close()

	select {
	case <-rec.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("running probe was not cancelled by the disconnect — the probe must run " +
			"off the read loop with the connection context, not on context.Background()")
	}

	// The admission slot must be free: a NEW connection's probe succeeds
	// (rather than being refused as saturated) once the cancelled task has
	// returned and released its permit.
	conn2 := connectWS(t, srv)
	defer func() { _ = conn2.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := jsonrpcCall(t, conn2, "connections.test", map[string]any{"profileId": "ssh:test:1"})
		var env struct {
			Error  *jsonrpcErrorObj `json:"error"`
			Result *struct {
				Outcome string `json:"outcome"`
			} `json:"result"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Result != nil {
			if env.Result.Outcome != "accepted" {
				t.Fatalf("probe outcome = %q, want accepted", env.Result.Outcome)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe admission slot never freed: last response %s", resp)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --- acceptance 2 (AD-9 sweep guard): a session opened before a disconnect
// --- still replays on reattach. This fails if the context-ownership pass
// --- gave the session pump a connection-scoped context.

func TestCtxOwnership_SessionSurvivesDisconnectAndReplays(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	connA := connectWS(t, ws)
	resp := jsonrpcCallWithID(t, connA, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID
	sidBytes, _ := session.IDToBytes(session.ID(sid))

	// Run a command and consume its output so the ring is live.
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("echo ctx-owner-test\n")}
	_ = connA.WriteMessage(websocket.BinaryMessage, f.Encode())
	var offset uint64
	readerA := newWSReader(connA)
	deadline := time.After(30 * time.Second)
loopA:
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ctx-owner-test output")
		case frame, ok := <-readerA.frames:
			if !ok {
				t.Fatal("connection closed before output arrived")
			}
			offset += uint64(len(frame.Payload))
			if strings.Contains(string(frame.Payload), "ctx-owner-test") {
				break loopA
			}
		}
	}

	// Disconnect. The session and its replay ring MUST survive (AD-9): the
	// pump runs on a server/session-owned context, never the connection's.
	_ = connA.Close()
	time.Sleep(200 * time.Millisecond)

	// Produce output while detached, through the registry.
	sessObj, err := sess.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	_, _ = sessObj.Write([]byte("echo ctx-owner-detached\n"))
	time.Sleep(500 * time.Millisecond)

	// Reattach: the buffered output replays.
	connB := connectWS(t, ws)
	defer func() { _ = connB.Close() }()
	respB := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    offset,
	}, 2)
	var at struct {
		Result struct {
			Resumed bool `json:"resumed"`
			Reset   bool `json:"reset"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respB, &at)
	if at.Result.Reset || !at.Result.Resumed {
		t.Fatalf("expected resumed, got reset=%v resumed=%v", at.Result.Reset, at.Result.Resumed)
	}

	readerB := newWSReader(connB)
	deadlineB := time.After(30 * time.Second)
	for {
		select {
		case <-deadlineB:
			t.Fatal("timed out waiting for ctx-owner-detached on reattach — the session pump " +
				"must not die with its WebSocket (AD-9)")
		case frame, ok := <-readerB.frames:
			if !ok {
				t.Fatal("connection closed before replay arrived")
			}
			if strings.Contains(string(frame.Payload), "ctx-owner-detached") {
				return
			}
		}
	}
}

// --- acceptance 4: dialog.openFile on disconnect ---------------------------

// blockingDialog is the NON-cooperative platform adapter: it receives the
// transport's context but cannot cancel (exactly like the real Wails runtime
// dialog), so it blocks until released. It counts invocations so a test can
// prove no second dialog was opened over the first.
type blockingDialog struct {
	started chan struct{}
	release chan struct{}

	mu    sync.Mutex
	calls int
}

func (d *blockingDialog) OpenFile(_ context.Context) (string, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	if n == 1 {
		close(d.started)
		<-d.release
	}
	return "/home/dev/.ssh/id_ed25519", nil
}

// The directory picker of the same non-cooperative adapter. It never blocks:
// these tests drive the FILE picker, and this method exists so the fake
// satisfies the whole capability — a test that needs a blocking directory
// picker uses blockingDirectoryDialog (ws_dialog_test.go).
func (d *blockingDialog) OpenDirectory(_ context.Context) (string, error) {
	return "/home/dev/collections", nil
}

// cancelAwareDialog is the cooperative adapter: it observes ctx.Done and
// returns promptly, the behaviour the platform contract permits where the
// native API allows it.
type cancelAwareDialog struct {
	started   chan struct{}
	cancelled chan struct{}

	mu    sync.Mutex
	calls int
}

func (d *cancelAwareDialog) OpenFile(ctx context.Context) (string, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	if n == 1 {
		close(d.started)
		<-ctx.Done()
		close(d.cancelled)
		return "", ctx.Err()
	}
	return "/home/dev/.ssh/id_ed25519", nil
}

func (d *cancelAwareDialog) OpenDirectory(_ context.Context) (string, error) {
	return "/home/dev/collections", nil
}

// waitDialogFree polls dialog.openFile until it succeeds, asserting the
// capability is no longer busy.
func waitDialogFree(t *testing.T, conn *websocket.Conn, wantPath string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := jsonrpcCall(t, conn, "dialog.openFile", map[string]any{})
		var env struct {
			Error  *jsonrpcErrorObj `json:"error"`
			Result *struct {
				Path string `json:"path"`
			} `json:"result"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Result != nil {
			if env.Result.Path != wantPath {
				t.Fatalf("path = %q, want %q", env.Result.Path, wantPath)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dialog capability never freed: last response %s", resp)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A non-cooperative adapter (the real Wails runtime cannot cancel the picker):
// the disconnect must send no response to the dead socket, keep the capability
// busy until the adapter actually returns, and never open a second dialog over
// the first for a reconnecting client.
func TestDialogOpenFile_NonCooperativeAdapterBusyUntilReturn(t *testing.T) {
	h := newInventoryHarness(t)
	dlg := &blockingDialog{started: make(chan struct{}), release: make(chan struct{})}
	h.ws.SetDialogService(dlg)

	connA := h.conn
	sendControl(t, connA, "dialog.openFile", map[string]any{}, 1)

	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("dialog adapter never invoked")
	}

	// Disconnect while the dialog is open.
	_ = connA.Close()

	// A reconnecting client must be refused while the adapter is still
	// running: a second dialog over the first is the defect.
	connB := connectWS(t, h.ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCall(t, connB, "dialog.openFile", map[string]any{})
	var errEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &errEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errEnv.Error == nil {
		t.Fatalf("second dialog.openFile while the first adapter is running must be refused, got %s", resp)
	}
	if errEnv.Error.Code != SaturationErrorCode {
		t.Fatalf("refusal code = %d, want %d (control plane busy)", errEnv.Error.Code, SaturationErrorCode)
	}
	dlg.mu.Lock()
	calls := dlg.calls
	dlg.mu.Unlock()
	if calls != 1 {
		t.Fatalf("adapter invoked %d times; the second dialog must not reach the native capability", calls)
	}

	// Only when the adapter ACTUALLY returns is the capability free again.
	close(dlg.release)
	waitDialogFree(t, connB, "/home/dev/.ssh/id_ed25519")
	dlg.mu.Lock()
	calls = dlg.calls
	dlg.mu.Unlock()
	if calls != 2 {
		t.Fatalf("adapter calls = %d after the retry, want 2", calls)
	}
}

// A cooperative adapter (the platform contract permits cancellation where the
// native API allows it) must observe the disconnect: the transport cancels the
// dialog's context, the adapter returns, the capability frees, and nothing is
// sent to the dead socket.
func TestDialogOpenFile_CancelAwareAdapterObservesDisconnect(t *testing.T) {
	h := newInventoryHarness(t)
	dlg := &cancelAwareDialog{started: make(chan struct{}), cancelled: make(chan struct{})}
	h.ws.SetDialogService(dlg)

	connA := h.conn
	sendControl(t, connA, "dialog.openFile", map[string]any{}, 1)

	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("dialog adapter never invoked")
	}

	_ = connA.Close()

	select {
	case <-dlg.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("dialog context was not cancelled by the disconnect — the adapter must " +
			"receive the connection context, never context.Background()")
	}

	// The cancelled adapter returned, so the capability is free for the next
	// connection.
	connB := connectWS(t, h.ws)
	defer func() { _ = connB.Close() }()
	waitDialogFree(t, connB, "/home/dev/.ssh/id_ed25519")
}

// --- acceptance 5: shutdown against a non-cooperative dependency ------------

// neverReturningProber ignores cancellation entirely (like a dependency that
// does not observe ctx) and blocks until released.
type neverReturningProber struct {
	started chan struct{}
	release chan struct{}
}

func (p *neverReturningProber) ProbeWithResult(ctx context.Context, _ string, _ *ssh.ConnectConfig) (string, error) {
	close(p.started)
	<-p.release
	return "", ctx.Err()
}

func (p *neverReturningProber) Probe(ctx context.Context, host string, cfg *ssh.ConnectConfig) error {
	_, err := p.ProbeWithResult(ctx, host, cfg)
	return err
}

// Stop must terminate within the documented maximum even when an in-flight
// probe ignores cancellation: it cancels admitted work, waits only the
// documented bound, then abandons it (work outside a commit interval is never
// waited for past that bound).
func TestStop_NonCooperativeProbeAbandonedWithinDocumentedMax(t *testing.T) {
	prober := &neverReturningProber{started: make(chan struct{}), release: make(chan struct{})}
	srv := newProbeTestServerWithProber(t, prober, probeResolver())
	srv.controlDrainTimeout = 100 * time.Millisecond

	conn := connectWS(t, srv)
	sendControl(t, conn, "connections.test", map[string]any{"profileId": "ssh:test:1"}, 1)
	select {
	case <-prober.started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}

	began := time.Now()
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(began)
	if elapsed > 2*time.Second {
		t.Fatalf("Stop took %v against a non-cooperative probe; it must return within the "+
			"documented maximum (%v) and abandon the work", elapsed, srv.controlDrainTimeout)
	}

	// Let the abandoned goroutine finish so the test can exit cleanly.
	close(prober.release)
}

// --- acceptance 3: every remaining context.Background() names its owner -----

// TestCtxOwnership_RemainingBackgroundNamesOwnerAndClosingEvent is the
// structural sweep guard: every context.Background() left in non-test
// transport code must sit under a comment naming its owner and its closing
// event. A mechanically "fixed" sweep that cancelled session survival would
// also fail the AD-9 test above; this one catches a future Background that
// forgets to justify itself.
func TestCtxOwnership_RemainingBackgroundNamesOwnerAndClosingEvent(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		// #nosec G304 — the glob is this package's own source directory
		// (derived from runtime.Caller), never external input: the test
		// audits the transport package's own files.
		data, err := os.ReadFile(f) //nolint:gosec // see above: package-own source files only
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, ln := range lines {
			if !strings.Contains(ln, "context.Background()") {
				continue
			}
			if !commentAboveNamesOwnerAndClosingEvent(lines, i) {
				violations = append(violations, fmt.Sprintf("%s:%d", filepath.Base(f), i+1))
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("context.Background() without a comment naming its owner and closing event:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// commentAboveNamesOwnerAndClosingEvent reports whether a // comment within
// the 12 lines above (or trailing) the given line names both the owner and
// the closing event. The two words may sit on different lines of the same
// comment block; matching is case-insensitive ("Closing event" == "closing
// event").
func commentAboveNamesOwnerAndClosingEvent(lines []string, idx int) bool {
	lo := idx - 12
	if lo < 0 {
		lo = 0
	}
	owner, closing := false, false
	for i := idx; i >= lo; i-- {
		ln := strings.ToLower(strings.TrimSpace(lines[i]))
		if !strings.HasPrefix(ln, "//") {
			continue
		}
		if strings.Contains(ln, "owner") {
			owner = true
		}
		if strings.Contains(ln, "closing event") {
			closing = true
		}
	}
	return owner && closing
}
