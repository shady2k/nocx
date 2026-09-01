package transport

// Behavioral tests for the files.* control plane (fm-w8): the two guards
// on the wire, the files.changed addressing (reconnect survival, dirty-set
// accumulation), and the failure paths. The contract-conformance rows live
// in ws_contract_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport/outbound"
	"github.com/shady2k/nocx/internal/waittest"
)

// filesLocalFactory is the composition-root shape the tests share: local
// sessions get a real local provider rooted at the caller's verified cwd
// when one is sent; remote sessions refuse (the SFTP wave has not landed).
func filesLocalFactory(sess session.Session, rootPath string) (filesystem.Provider, error) {
	if sess.Kind() != session.KindLocal {
		return nil, errors.New("remote filesystems are not available yet")
	}
	if rootPath == "" {
		return local.New(), nil
	}
	return local.New(local.WithRoot(rootPath)), nil
}

// filesTestEnv boots a WSServer wired with the filesystem registry and the
// local provider factory, and connects one client (conn). filesPollInterval
// is shortened so the digest-poll watcher turns quickly.
type filesTestEnv struct {
	ws   *WSServer
	conn *websocket.Conn
}

func newFilesTestEnv(t *testing.T, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	all := append([]WSServerOption{
		WithFilesystemRegistry(filesystem.New()),
		WithFilesystemProviderFactory(filesLocalFactory),
	}, opts...)
	ws := NewWSServer(logger, reg, all...)
	ws.filesPollInterval = 20 * time.Millisecond
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &filesTestEnv{ws: ws, conn: conn}
}

// openSession opens a local session over the wire and returns its
// server-authoritative sessionId.
func (e *filesTestEnv) openSession(t *testing.T, id int) string {
	t.Helper()
	return openSessionOnConn(t, e.ws, e.conn, id)
}

// openBinding opens a filesystem binding over the wire and returns its
// bindingId.
func (e *filesTestEnv) openBinding(t *testing.T, sid, rootPath string, id int) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.conn, "files.open", map[string]any{
		"sessionId": sid,
		"rootPath":  rootPath,
	}, id)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.open: unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("files.open: %+v", envelope.Error)
	}
	var got struct {
		BindingID string `json:"bindingId"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("files.open: decode result: %v", err)
	}
	if got.BindingID == "" {
		t.Fatal("files.open returned an empty bindingId")
	}
	return got.BindingID
}

// readNotification answers with the params of the next notification
// carrying the given method — one already retained for this connection, or
// the next to arrive — and fails the test when the deadline passes with
// none.
//
// It reads through the inbox (ws_inbox_test.go), so a response or another
// notification seen on the way is kept rather than dropped. awaitNotification
// is the same wait without the t.Fatalf, for the tests that assert it fails.
func readNotification(t *testing.T, conn *websocket.Conn, method string, d time.Duration) json.RawMessage {
	t.Helper()
	params, err := awaitNotification(conn, method, d)
	if err != nil {
		t.Fatalf("waiting for %s notification: %v", method, err)
	}
	return params
}

// drainFilesChanged collects every files.changed params that arrives on
// the connection during the window. Used for the "delivered once" half of
// the re-attach assertions.
func drainFilesChanged(t *testing.T, conn *websocket.Conn, d time.Duration) []map[string]any {
	t.Helper()
	var got []map[string]any
	deadline := time.Now().Add(d)
	for {
		msg, err := awaitFrame(conn, deadline, isNotification("files.changed"))
		if err != nil {
			return got // the window closed, or the socket did
		}
		f, _ := decodeFrame(msg)
		var params map[string]any
		if err := json.Unmarshal(f.Params, &params); err == nil {
			got = append(got, params)
		}
	}
}

// watchDir installs a watch over the wire and returns the transport's
// watcher for the binding.
func (e *filesTestEnv) watchDir(t *testing.T, bid string, paths []string, id int) *filesWatcher {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.conn, "files.watch", map[string]any{
		"bindingId": bid,
		"paths":     paths,
	}, id)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.watch: unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.watch: %+v", envelope.Error)
	}
	e.ws.filesMu.Lock()
	w := e.ws.filesBindings[bid].watcher
	e.ws.filesMu.Unlock()
	if w == nil {
		t.Fatal("files.watch did not create a transport watcher")
	}
	return w
}

// ── §0 on the wire: the two guards ────────────────────────────────────────

// TestFilesOpen_RefusedForSessionTheConnectionDoesNotOwn is the wire-level
// enforcement of the one rule: B knows A's valid sessionId, and B's
// files.open fails (D15). Resolving through the global session registry
// instead of connState would open A's filesystem on B.
func TestFilesOpen_RefusedForSessionTheConnectionDoesNotOwn(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1) // conn A owns sid

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()

	resp := jsonrpcCallWithID(t, connB, "files.open", map[string]any{
		"sessionId": sid,
		"rootPath":  t.TempDir(),
	}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil {
		t.Fatal("B's files.open for A's session succeeded — §0 is broken on the wire")
	}
	if envelope.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", envelope.Error.Code)
	}
}

// TestFilesBindingID_RefusedOnAnotherConnection proves a bindingId is not a
// bearer token: every later call re-checks, through Registry.Acquire, that
// the binding's session belongs to the requesting connection.
func TestFilesBindingID_RefusedOnAnotherConnection(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, t.TempDir(), 2)

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()

	resp := jsonrpcCallWithID(t, connB, "files.list", map[string]any{
		"bindingId": bid,
		"path":      "/",
		"offset":    0,
		"limit":     10,
	}, 3)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil {
		t.Fatal("B's files.list on A's binding succeeded — the re-check is missing")
	}
}

// ── files.changed addressing ──────────────────────────────────────────────

// TestFilesChanged_ReachesNewConnectionAfterReattach drops the WebSocket
// with a watch active and asserts the notification reaches the NEW
// connection after attach. This is the assertion that fails if a binding
// stored its *wsConn: the old one is destroyed on the drop, and a stored
// connection would spend the rest of its life writing to a closed socket.
func TestFilesChanged_ReachesNewConnectionAfterReattach(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)

	// Baseline: the first poll tick lists the directory silently.
	waittest.WaitForTimeout(t, "watch baseline", wantWithin, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.paths[dir] != nil && w.paths[dir].rev != ""
	})

	// Drop the connection, then change the directory. The server closes
	// its side of the socket when it observes the drop, so the emit's
	// write fails and the path accumulates dirty instead of being lost.
	_ = e.conn.Close()
	waittest.WaitForTimeout(t, "server to observe the dropped files connection", wantWithin, func() bool {
		e.ws.connsMu.Lock()
		defer e.ws.connsMu.Unlock()
		return len(e.ws.conns) == 0
	})
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	waittest.WaitForTimeout(t, "dirty path", wantWithin, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, ok := w.dirty[dir]
		return ok
	})

	// Re-attach on a NEW connection: the flush must reach it.
	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	at := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 4)
	var atEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(at, &atEnv); err != nil {
		t.Fatalf("attach: unmarshal: %v", err)
	}
	if atEnv.Error != nil {
		t.Fatalf("attach: %+v", atEnv.Error)
	}

	raw := readNotification(t, connB, "files.changed", wantWithin)
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("files.changed: unmarshal: %v", err)
	}
	if params["bindingId"] != bid {
		t.Errorf("files.changed bindingId = %v, want %s", params["bindingId"], bid)
	}
	if params["path"] != dir {
		t.Errorf("files.changed path = %v, want %s", params["path"], dir)
	}
}

// TestFilesChanged_DirtyPathsDeliveredOnceOnReattach accumulates several
// dirty paths with no subscriber and asserts the re-attach delivers one
// notification per path — never a queue of events.
func TestFilesChanged_DirtyPathsDeliveredOnceOnReattach(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir1, dir2 := t.TempDir(), t.TempDir()
	bid := e.openBinding(t, sid, dir1, 2)
	w := e.watchDir(t, bid, []string{dir1, dir2}, 3)

	waittest.WaitForTimeout(t, "watch baseline", wantWithin, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.paths[dir1] != nil && w.paths[dir1].rev != "" &&
			w.paths[dir2] != nil && w.paths[dir2].rev != ""
	})

	// No subscriber: the same state a WebSocket drop leaves the session
	// in, made deterministic (the drop's socket teardown is timing).
	e.ws.getRx(session.ID(sid)).setSubscriber(nil, nil)

	if err := os.WriteFile(filepath.Join(dir1, "f1.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "f2.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	waittest.WaitForTimeout(t, "both dirty paths", wantWithin, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		_, ok1 := w.dirty[dir1]
		_, ok2 := w.dirty[dir2]
		return ok1 && ok2
	})

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	at := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 4)
	var atEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(at, &atEnv); err != nil {
		t.Fatalf("attach: unmarshal: %v", err)
	}
	if atEnv.Error != nil {
		t.Fatalf("attach: %+v", atEnv.Error)
	}

	delivered := drainFilesChanged(t, connB, 3*time.Second)
	if len(delivered) != 2 {
		t.Fatalf("delivered %d files.changed after re-attach, want exactly 2 (one per dirty path)", len(delivered))
	}
	paths := make(map[string]bool, len(delivered))
	for _, p := range delivered {
		if path, ok := p["path"].(string); ok {
			paths[path] = true
		}
	}
	if !paths[dir1] || !paths[dir2] {
		t.Errorf("delivered paths = %v, want both %s and %s", paths, dir1, dir2)
	}

	// One delivery, then silence: the directories are quiet now, so no
	// further notifications may arrive for the same change.
	if rest := drainFilesChanged(t, connB, 300*time.Millisecond); len(rest) != 0 {
		t.Errorf("extra files.changed after the flush: %v", rest)
	}
}

// TestFilesWatch_EmptySetStopsTheLoop proves collapsing every watch cannot
// leak a poll loop: after files.watch with no paths, the transport watcher
// is gone and further changes are never detected.
func TestFilesWatch_EmptySetStopsTheLoop(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	waittest.WaitForTimeout(t, "watch baseline", wantWithin, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.paths[dir] != nil && w.paths[dir].rev != ""
	})

	resp := jsonrpcCallWithID(t, e.conn, "files.watch", map[string]any{
		"bindingId": bid,
		"paths":     []string{},
	}, 4)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.watch: unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.watch: %+v", envelope.Error)
	}

	e.ws.filesMu.Lock()
	gone := e.ws.filesBindings[bid].watcher == nil
	e.ws.filesMu.Unlock()
	if !gone {
		t.Fatal("files.watch with an empty set left the transport watcher alive")
	}
}

// ── the watch baseline (fm-w14) ────────────────────────────────────────────

// slowFilesPollInterval widens the digest-poll cadence for the two
// "immediate change" tests below. The write must land strictly BEFORE the
// first poll tick on the unfixed code, whose baseline IS that tick; a
// 20 ms tick would leave the red proof hostage to a >20 ms CI scheduling
// stall between the watch response and the write. 500 ms makes the gap
// deterministic while the test itself still writes immediately and waits
// for nothing — exactly the gesture the defect swallowed.
const slowFilesPollInterval = 500 * time.Millisecond

// TestFilesChanged_ChangeImmediatelyAfterWatchIsAnnounced is the regression
// for the watch baseline blind spot: the baseline used to be taken on the
// FIRST poll tick after files.watch, so a change landing in the gap between
// the response and that tick was folded into the baseline and never
// announced. The write here happens immediately — microseconds after the
// watch response, a full interval before the first tick could establish the
// old baseline — and must still be announced.
func TestFilesChanged_ChangeImmediatelyAfterWatchIsAnnounced(t *testing.T) {
	e := newFilesTestEnv(t)
	// The first tick must not run before the write: widen the cadence so
	// the write strictly precedes it (see slowFilesPollInterval).
	e.ws.filesPollInterval = slowFilesPollInterval
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// No wait for a baseline, no sleep: the write is the fast gesture the
	// defect swallowed. If the baseline is taken synchronously inside
	// files.watch, it is already done by the time the response arrives.
	e.watchDir(t, bid, []string{dir}, 3)
	if err := os.WriteFile(filepath.Join(dir, "boom.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw := readNotification(t, e.conn, "files.changed", wantWithin)
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("files.changed: unmarshal: %v", err)
	}
	if params["bindingId"] != bid {
		t.Errorf("files.changed bindingId = %v, want %s", params["bindingId"], bid)
	}
	if params["path"] != dir {
		t.Errorf("files.changed path = %v, want %s", params["path"], dir)
	}
	if rev, _ := params["rev"].(string); rev == "" {
		t.Error("files.changed rev is empty — the listing was taken, the rev must be known")
	}
}

// TestFilesChanged_ChangeImmediatelyAfterWatchReplacementIsAnnounced is the
// set-replacement half of the same defect (spec §5.2): the replaced set's
// baseline used to reset to "established silently by the first poll after",
// so a change to a NEWLY added path inside that window was never announced.
func TestFilesChanged_ChangeImmediatelyAfterWatchReplacementIsAnnounced(t *testing.T) {
	e := newFilesTestEnv(t)
	// The first tick after the replacement must not run before the write
	// to the added path: same cadence widening as the new-watch test.
	e.ws.filesPollInterval = slowFilesPollInterval
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir1 := filepath.Join(root, "one")
	dir2 := filepath.Join(root, "two")
	if err := os.MkdirAll(dir1, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir1, err)
	}
	if err := os.MkdirAll(dir2, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir2, err)
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir1}, 3)

	// Replace the set with dir2 added. Its baseline must be taken at
	// replacement time, before the response — not by the first poll after.
	e.watchDir(t, bid, []string{dir1, dir2}, 4)
	if err := os.WriteFile(filepath.Join(dir2, "boom.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw := readNotification(t, e.conn, "files.changed", wantWithin)
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("files.changed: unmarshal: %v", err)
	}
	if params["bindingId"] != bid {
		t.Errorf("files.changed bindingId = %v, want %s", params["bindingId"], bid)
	}
	if params["path"] != dir2 {
		t.Errorf("files.changed path = %v, want %s", params["path"], dir2)
	}
}

// TestFilesChanged_ChangeBeforeWatchIsNotReplayed is the other end of the
// interval: a change that landed before files.watch must NOT be replayed —
// inotify semantics, and the reason the fix is not "announce everything".
// The baseline (taken inside files.watch) includes the pre-existing file,
// so many poll intervals must pass silently.
func TestFilesChanged_ChangeBeforeWatchIsNotReplayed(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "before.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)

	// Ten poll intervals (20 ms each in tests) of silence.
	if got := drainFilesChanged(t, e.conn, 200*time.Millisecond); len(got) != 0 {
		t.Errorf("files.changed fired for a change that predates the watch: %v", got)
	}
}

// ── failure paths ─────────────────────────────────────────────────────────

// TestFilesOpen_UnknownSessionRefused: a sessionId that no connection
// owns — and no session exists for — is refused at the connState gate.
func TestFilesOpen_UnknownSessionRefused(t *testing.T) {
	e := newFilesTestEnv(t)
	resp := jsonrpcCallWithID(t, e.conn, "files.open", map[string]any{
		"sessionId": strings.Repeat("0", 32),
		"rootPath":  t.TempDir(),
	}, 1)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("files.open with an unknown sessionId succeeded")
	}
	if envelope.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", envelope.Error.Code)
	}
}

// TestFilesStat_ClassifiesWithoutPaging proves a file link's classification
// costs one files.stat call, even when its parent would require many pages.
func TestFilesStat_ClassifiesWithoutPaging(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	file := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.stat", map[string]any{
		"bindingId": bid,
		"path":      file,
	}, 3)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("files.stat: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.stat: %+v", env.Error)
	}
	var got filesStatResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("files.stat: decode: %v", err)
	}
	if got.Kind != string(filesystem.KindRegular) {
		t.Errorf("kind = %q, want %q", got.Kind, filesystem.KindRegular)
	}
}

func TestFilesStat_DirectoryStillReachesDirectoryKind(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.stat", map[string]any{
		"bindingId": bid,
		"path":      child,
	}, 3)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("files.stat: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.stat: %+v", env.Error)
	}
	var got filesStatResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("files.stat: decode: %v", err)
	}
	if got.Kind != string(filesystem.KindDir) {
		t.Errorf("kind = %q, want %q", got.Kind, filesystem.KindDir)
	}
}

func TestFilesStat_MissingPathRefused(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.stat", map[string]any{
		"bindingId": bid,
		"path":      filepath.Join(dir, "missing"),
	}, 3)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("files.stat: unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("files.stat missing path error = %+v, want -32602", env.Error)
	}
	validateJSON(t, loadSchema(t, "files.stat.error.schema.json"), mustMarshal(env.Error.Data), "files.stat missing error data")
	var data struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(mustMarshal(env.Error.Data), &data); err != nil {
		t.Fatalf("files.stat missing error data: %v", err)
	}
	if data.Reason != "not-found" {
		t.Fatalf("files.stat missing reason = %q, want not-found", data.Reason)
	}
}

type statErrorProvider struct {
	filesystem.Provider
	err error
}

func (p *statErrorProvider) Stat(context.Context, string) (filesystem.Stat, error) {
	return filesystem.Stat{}, p.err
}

func TestFilesStat_UnreadablePathRefused(t *testing.T) {
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(func(_ session.Session, rootPath string) (filesystem.Provider, error) {
		return &statErrorProvider{
			Provider: local.New(local.WithRoot(rootPath)),
			err:      fmt.Errorf("wrapped stat failure: %w", &filesystem.ErrPermission{Path: "/secret", Err: os.ErrPermission}),
		}, nil
	}))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.stat", map[string]any{
		"bindingId": bid,
		"path":      filepath.Join(dir, "secret"),
	}, 3)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("files.stat: unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("files.stat unreadable path error = %+v, want -32602", env.Error)
	}
	validateJSON(t, loadSchema(t, "files.stat.error.schema.json"), mustMarshal(env.Error.Data), "files.stat permission error data")
	var data struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(mustMarshal(env.Error.Data), &data); err != nil {
		t.Fatalf("files.stat permission error data: %v", err)
	}
	if data.Reason != "permission-denied" {
		t.Fatalf("files.stat permission reason = %q, want permission-denied", data.Reason)
	}
}

func TestFilesStat_UnknownFailureCarriesUnavailableReason(t *testing.T) {
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(func(_ session.Session, rootPath string) (filesystem.Provider, error) {
		return &statErrorProvider{
			Provider: local.New(local.WithRoot(rootPath)),
			err:      errors.New("stat transport failed"),
		}, nil
	}))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.stat", map[string]any{
		"bindingId": bid,
		"path":      filepath.Join(dir, "unclear"),
	}, 3)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("files.stat: unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32603 {
		t.Fatalf("files.stat unknown error = %+v, want -32603", env.Error)
	}
	validateJSON(t, loadSchema(t, "files.stat.error.schema.json"), mustMarshal(env.Error.Data), "files.stat unknown error data")
	var data struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(mustMarshal(env.Error.Data), &data); err != nil {
		t.Fatalf("files.stat unknown error data: %v", err)
	}
	if data.Reason != "unavailable" {
		t.Fatalf("files.stat unknown reason = %q, want unavailable", data.Reason)
	}
}

func TestFilesStat_BindingGoneRefused(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	closeSession := jsonrpcCallWithID(t, e.conn, "close", map[string]string{"sessionId": sid}, 3)
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeSession, &closeEnv); err != nil {
		t.Fatalf("close: unmarshal: %v", err)
	}
	if closeEnv.Error != nil {
		t.Fatalf("close: %+v", closeEnv.Error)
	}

	resp := jsonrpcCallWithID(t, e.conn, "files.stat", map[string]any{
		"bindingId": bid,
		"path":      dir,
	}, 4)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("files.stat: unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("files.stat gone binding error = %+v, want -32602", env.Error)
	}
}

// TestFilesList_UnknownBindingRefused: every external call fails cleanly
// on a binding id that does not exist.
func TestFilesList_UnknownBindingRefused(t *testing.T) {
	e := newFilesTestEnv(t)
	resp := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": strings.Repeat("f", 32),
		"path":      "/",
		"offset":    0,
		"limit":     10,
	}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("files.list with an unknown bindingId succeeded")
	}
	if envelope.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", envelope.Error.Code)
	}
}

// TestFilesList_AfterSessionClosedRefuses: a session closed between lookup
// and use closes its bindings, and the next call is refused cleanly — no
// panic, no hang, and no read through a closed provider.
func TestFilesList_AfterSessionClosedRefuses(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// Sanity: the binding works while its session is alive.
	resp := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": bid,
		"path":      dir,
		"offset":    0,
		"limit":     10,
	}, 3)
	var okEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &okEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if okEnv.Error != nil {
		t.Fatalf("pre-close files.list: %+v", okEnv.Error)
	}

	// Close the terminal: closing the terminal closes its bindings (spec
	// §5.1).
	closeResp := jsonrpcCallWithID(t, e.conn, "close", map[string]string{"sessionId": sid}, 4)
	var closeEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(closeResp, &closeEnv); err != nil {
		t.Fatalf("close: unmarshal: %v", err)
	}
	if closeEnv.Error != nil {
		t.Fatalf("close: %+v", closeEnv.Error)
	}

	// The next call on the dead binding must refuse cleanly.
	after := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": bid,
		"path":      dir,
		"offset":    0,
		"limit":     10,
	}, 5)
	var afterEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(after, &afterEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if afterEnv.Error == nil {
		t.Fatal("files.list succeeded on a binding whose session closed")
	}
}

// ── files.reveal ──────────────────────────────────────────────────────────

type stubRevealer struct {
	mu     sync.Mutex
	paths  []string
	reveal func(path string) error
}

func (s *stubRevealer) Reveal(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reveal != nil {
		return s.reveal(path)
	}
	s.paths = append(s.paths, path)
	return nil
}

func (s *stubRevealer) revealed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.paths))
	copy(out, s.paths)
	return out
}

// TestFilesReveal_RemoteBindingRefused: a UI-only guard is one bug away
// from being no guard — the backend refuses a remote binding (spec §5.2).
func TestFilesReveal_RemoteBindingRefused(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, t.TempDir(), 2)

	// The SFTP wave stamps the attestation at files.open; inject it the
	// way that wave would, to prove the guard reads it.
	e.ws.filesMu.Lock()
	e.ws.filesBindings[bid].endpointID = "v1:attestation"
	e.ws.filesMu.Unlock()

	resp := jsonrpcCallWithID(t, e.conn, "files.reveal", map[string]any{
		"bindingId": bid,
		"path":      "/some/file",
	}, 3)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("files.reveal on a remote binding succeeded")
	}
}

// TestFilesReveal_UnavailableWithoutRevealer: an unwired revealer answers
// -32601 rather than pretending the file was revealed.
func TestFilesReveal_UnavailableWithoutRevealer(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, t.TempDir(), 2)
	resp := jsonrpcCallWithID(t, e.conn, "files.reveal", map[string]any{
		"bindingId": bid,
		"path":      "/some/file",
	}, 3)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("files.reveal without a wired revealer succeeded")
	}
	if envelope.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", envelope.Error.Code)
	}
}

// TestFilesReveal_LocalBindingSucceeds is the paired positive: on a local
// binding with a wired revealer, the path reaches the revealer and the
// method answers {}.
func TestFilesReveal_LocalBindingSucceeds(t *testing.T) {
	revealer := &stubRevealer{}
	e := newFilesTestEnv(t, WithFilesRevealer(revealer))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCallWithID(t, e.conn, "files.reveal", map[string]any{
		"bindingId": bid,
		"path":      filepath.Join(dir, "file.txt"),
	}, 3)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.reveal: %+v", envelope.Error)
	}
	got := revealer.revealed()
	if len(got) != 1 || got[0] != filepath.Join(dir, "file.txt") {
		t.Errorf("revealed paths = %v, want [%s]", got, filepath.Join(dir, "file.txt"))
	}
}

// TestFilesWatch_DegradesToPollingHonestly: the local provider cannot
// watch yet (the watching wave is later), so files.watch must report the
// degradation — mode polling with a reason — never a silent lie about
// watching being live.
func TestFilesWatch_DegradesToPollingHonestly(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	resp := jsonrpcCallWithID(t, e.conn, "files.watch", map[string]any{
		"bindingId": bid,
		"paths":     []string{dir},
	}, 3)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.watch: %+v", envelope.Error)
	}
	var got filesWatchResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "polling" {
		t.Errorf("mode = %q, want %q (watching is not available yet)", got.Mode, "polling")
	}
	// And NO reason: polling is the designed mode until the watching wave
	// lands, so there is nothing to warn about. A reason here would light
	// the §5.5 badge permanently, for everyone. The badge's premise is that
	// watching normally works and this binding fell back from it — a premise
	// that becomes true only when Live watching exists.
	if got.DegradedReason != "" {
		t.Errorf("degradedReason = %q, want empty — not-yet-built is not a degrade", got.DegradedReason)
	}
}

// TestFilesClose_DoesNotWaitOnABlockedNotificationWrite: a notification
// write is a non-blocking enqueue into the subscriber's outbound queue, so
// a subscriber whose pump is wedged (the deterministic stand-in for a
// socket with a full send buffer) must never hold a close hostage.
// files.close must still return promptly. This is the assertion that fails
// if stopping the watcher ever waits for the loop to exit: the loop parks
// on the wedged write holding no use-guard, and close drains the guards
// and proceeds.
func TestFilesClose_DoesNotWaitOnABlockedNotificationWrite(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	waittest.WaitForTimeout(t, "watch baseline", wantWithin, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.paths[dir] != nil && w.paths[dir].rev != ""
	})

	// Wedge the subscriber's outbound pump mid-write. The next
	// notification is a non-blocking enqueue, so the poll loop parks on
	// the queue — holding no guard — never on a socket write.
	wedge := newWedgedSocket()
	deadConn := &wsConn{out: outbound.New(wedge, outbound.Config{}), id: 0}
	t.Cleanup(func() { _ = wedge.Close() })
	e.ws.getRx(session.ID(sid)).setSubscriber(deadConn, nil)

	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Let the poll loop reach the blocked write; wait for its explicit entry
	// signal rather than assuming several poll intervals have elapsed.
	select {
	case <-wedge.started:
	case <-time.After(wantWithin):
		t.Fatal("watcher never reached the blocked notification write")
	}

	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "files.close",
		"params": map[string]any{"bindingId": bid},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = e.conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err = e.conn.SetReadDeadline(time.Now().Add(wantWithin)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	_, resp, err := e.conn.ReadMessage()
	if err != nil {
		t.Fatalf("files.close did not return within 5 s — a blocked notification write holds the close path: %v", err)
	}
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.close: %+v", envelope.Error)
	}
}

// attestedTestProvider stands in for the app package's
// endpointAttestedProvider: it embeds a real local provider and carries the
// endpoint attestation, so the transport's optional attester seam can be
// proven on the wire without importing app (which would cycle).
type attestedTestProvider struct {
	filesystem.Provider
	endpointID string
}

func (p *attestedTestProvider) EndpointID() string { return p.endpointID }

// TestFilesOpen_RendersProviderFactoryError: files.open on a session whose
// provider cannot be built — an SSH session whose connection is already
// dead — must fail with a rendered error (-32603 carrying the factory's
// words), never a panic or a hang. The factory error path is the remote
// failure mode the composition root's sftp wiring produces.
func TestFilesOpen_RendersProviderFactoryError(t *testing.T) {
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(func(session.Session, string) (filesystem.Provider, error) {
		return nil, errors.New("sftp provider for gone.example.com: ssh: connection lost")
	}))
	sid := e.openSession(t, 1)
	resp := jsonrpcCallWithID(t, e.conn, "files.open", map[string]any{
		"sessionId": sid,
	}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("files.open on a dead connection succeeded")
	}
	if envelope.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", envelope.Error.Code)
	}
	if !strings.Contains(envelope.Error.Message, "gone.example.com") {
		t.Errorf("error %q does not carry the factory's words", envelope.Error.Message)
	}
}

// TestFilesOpen_RemoteAttestationReachesTheWire: the composition root's
// attestation seam (filesystemEndpointAttester) must surface as the
// binding's endpointId — a non-null, versioned value the viewer keys on.
// Without this, a remote binding would report null and collapse into the
// local viewer's namespace (spec §5.1, D12).
func TestFilesOpen_RemoteAttestationReachesTheWire(t *testing.T) {
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(func(session.Session, string) (filesystem.Provider, error) {
		return &attestedTestProvider{Provider: local.New(), endpointID: "v1:attested"}, nil
	}))
	sid := e.openSession(t, 1)
	resp := jsonrpcCallWithID(t, e.conn, "files.open", map[string]any{
		"sessionId": sid,
	}, 2)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.open: %+v", envelope.Error)
	}
	var got filesOpenResult
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EndpointID == nil || *got.EndpointID != "v1:attested" {
		t.Errorf("endpointId = %v, want %q", got.EndpointID, "v1:attested")
	}
}

// ── the watch set is diffed, not replaced (nocx-8hdia.5) ───────────────────

// countingProvider wraps a real provider and counts List calls per path, so
// a test can assert what one files.watch actually enumerated. Only List is
// overridden; everything else is the real local provider's.
type countingProvider struct {
	filesystem.Provider
	mu    sync.Mutex
	lists map[string]int

	blockNextList bool
	listStarted   chan struct{}
	listRelease   chan struct{}
	listFinished  chan struct{}
	failNextList  error
}

func (p *countingProvider) blockNext() {
	p.mu.Lock()
	p.blockNextList = true
	p.mu.Unlock()
}

func (p *countingProvider) failNext(err error) {
	p.mu.Lock()
	p.failNextList = err
	p.mu.Unlock()
}

func (p *countingProvider) List(
	ctx context.Context,
	path string,
	page filesystem.Page,
) (filesystem.Listing, error) {
	p.mu.Lock()
	p.lists[path]++
	fail := p.failNextList
	p.failNextList = nil
	block := p.blockNextList
	if block {
		p.blockNextList = false
		close(p.listStarted)
	}
	p.mu.Unlock()
	if fail != nil {
		return filesystem.Listing{}, fail
	}
	if block {
		<-p.listRelease
	}
	listing, err := p.Provider.List(ctx, path, page)
	if block {
		close(p.listFinished)
	}
	return listing, err
}

func (p *countingProvider) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lists[path]
}

// TestFilesWatch_ReplacementBaselinesOnlyTheAddedPath is the cost half of
// the defect. files.watch REPLACES the set, and the client resends the whole
// set every time it changes — after a first listing, on every expand, on
// every collapse. Baselining the whole set therefore charged one full
// enumeration PER ALREADY-WATCHED DIRECTORY to every expand gesture, and the
// cost grew with the number of folders the person had open.
func TestFilesWatch_ReplacementBaselinesOnlyTheAddedPath(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	// No poll tick may run during the measurement: the loop lists too, and
	// what is being counted is what files.watch itself enumerated.
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dirs := make([]string, 3)
	for i := range dirs {
		dirs[i] = filepath.Join(root, string(rune('a'+i)))
		if err := os.MkdirAll(dirs[i], 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dirs[i], err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)

	e.watchDir(t, bid, []string{dirs[0], dirs[1]}, 3)
	before := []int{count(dirs[0]), count(dirs[1])}

	// One more folder expanded: the client resends the whole set with one
	// path added. Only that path is new information.
	e.watchDir(t, bid, []string{dirs[0], dirs[1], dirs[2]}, 4)

	if got := count(dirs[0]) - before[0]; got != 0 {
		t.Errorf("already-watched %s was listed %d more times, want 0", dirs[0], got)
	}
	if got := count(dirs[1]) - before[1]; got != 0 {
		t.Errorf("already-watched %s was listed %d more times, want 0", dirs[1], got)
	}
	if got := count(dirs[2]); got != 1 {
		t.Errorf("added %s was listed %d times, want exactly 1", dirs[2], got)
	}
}

// TestFilesChanged_AChangeIsNotSwallowedByAWatchSetReplacement is the
// correctness half, and it is why this is a defect rather than an
// inefficiency. filesPollPath announces when the fresh rev differs from
// w.paths[path]. Re-baselining an ALREADY-WATCHED path overwrites that
// comparison point with the current rev, so a change that landed after the
// last tick and before the replacement is folded into a baseline nobody saw
// and is never announced — the exact failure the synchronous baseline was
// introduced to prevent, reintroduced through the replacement path.
//
// The gesture is ordinary: a file appears in one folder, and before the next
// poll tick the person expands another folder.
func TestFilesChanged_AChangeIsNotSwallowedByAWatchSetReplacement(t *testing.T) {
	e := newFilesTestEnv(t)
	// The write and the replacement must both land before the first tick,
	// or the tick announces the change and the defect is hidden.
	e.ws.filesPollInterval = slowFilesPollInterval
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir1 := filepath.Join(root, "one")
	dir2 := filepath.Join(root, "two")
	for _, d := range []string{dir1, dir2} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir1}, 3)

	// The change nobody has been told about yet.
	if err := os.WriteFile(filepath.Join(dir1, "boom.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The unrelated gesture that must not lose it.
	e.watchDir(t, bid, []string{dir1, dir2}, 4)

	raw := readNotification(t, e.conn, "files.changed", wantWithin)
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("files.changed: unmarshal: %v", err)
	}
	if params["path"] != dir1 {
		t.Errorf("files.changed path = %v, want %s — the change to the "+
			"already-watched directory was swallowed by the replacement",
			params["path"], dir1)
	}
}

// ── the visibility gate (nocx-8hdia.1) ─────────────────────────────────────

// setVisible sends files.visible and fails on a refusal.
func (e *filesTestEnv) setVisible(t *testing.T, bid string, visible bool, id int) {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.conn, "files.visible", map[string]any{
		"bindingId": bid,
		"visible":   visible,
	}, id)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.visible: unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.visible: %+v", envelope.Error)
	}
}

// newCountingFilesEnv boots the env with a provider that counts List calls,
// so a test can assert what the poll loop actually enumerated.
func newCountingFilesEnv(t *testing.T) (*filesTestEnv, func(path string) int) {
	t.Helper()
	// The factory runs on the goroutine serving files.open and the counter is
	// read from the test's, with only a WebSocket between them — and the race
	// detector cannot derive a happens-before edge through a socket. So the
	// handle itself is guarded, not just the counts behind it.
	var mu sync.Mutex
	var prov *countingProvider
	factory := func(sess session.Session, rootPath string) (filesystem.Provider, error) {
		inner, err := filesLocalFactory(sess, rootPath)
		if err != nil {
			return nil, err
		}
		p := &countingProvider{Provider: inner, lists: map[string]int{}}
		mu.Lock()
		prov = p
		mu.Unlock()
		return p, nil
	}
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(factory))
	return e, func(path string) int {
		mu.Lock()
		p := prov
		mu.Unlock()
		if p == nil {
			return 0
		}
		return p.count(path)
	}
}

// TestFilesPoll_HiddenPanelIssuesNoListing is the gate itself. The Files
// poll is BACKEND-driven, so unlike Git and Ports the frontend cannot stop
// it by going away — a collapsed sidebar used to cost exactly as much as an
// open one, and one tick is a full enumeration of every watched directory
// (both providers enumerate before slicing the page, so Limit does not bound
// it). Design §5.5 has always required this: polling is "paused while
// visible() is false".
//
// Not a cheaper listing while hidden. None.
func TestFilesPoll_HiddenPanelIssuesNoListing(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir}, 3)

	// The panel goes away. A tick already in flight may still land, so the
	// measurement starts after one interval has passed.
	e.setVisible(t, bid, false, 4)
	interval := e.ws.filesPollInterval
	time.Sleep(2 * interval)
	before := count(dir)

	// Many intervals of a hidden panel must cost nothing at all.
	time.Sleep(10 * interval)
	if got := count(dir) - before; got != 0 {
		t.Errorf("hidden panel issued %d listings over 10 intervals, want 0", got)
	}
}

// TestFilesPoll_BecomingVisiblePollsImmediately is the other half, and it is
// what keeps the gate from costing freshness: a person who opens a panel is
// owed the truth now, not at the next tick. The interval here is an hour, so
// any listing at all can only be the becoming-visible poll.
func TestFilesPoll_BecomingVisiblePollsImmediately(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	// No ordinary tick may fire during this test: what is being measured is
	// the transition, not the cadence.
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	e.setVisible(t, bid, false, 4)
	before := count(dir)

	e.setVisible(t, bid, true, 5)
	waittest.WaitForTimeout(t, "the becoming-visible poll", wantWithin, func() bool {
		return count(dir) > before
	})
	if got := count(dir) - before; got != 1 {
		t.Errorf("becoming visible issued %d listings, want exactly 1", got)
	}
}

// TestFilesPoll_StayingVisibleIsNotAPollTrigger guards the edge rather than
// the value. The client re-sends its visibility on transitions it considers
// interesting, and if every "visible: true" produced a poll, the panel would
// have a second, undeclared cadence that the interval does not govern —
// which is the whole class of defect this epic exists to remove.
func TestFilesPoll_StayingVisibleIsNotAPollTrigger(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	before := count(dir)

	// Already visible: three more of the same must change nothing.
	for i := range 3 {
		e.setVisible(t, bid, true, 4+i)
	}
	time.Sleep(50 * time.Millisecond)
	if got := count(dir) - before; got != 0 {
		t.Errorf("re-asserting visibility issued %d listings, want 0", got)
	}
}

// TestFilesPoll_IdleLadderBacksOff pins the exact per-path idle and failure
// ladders without a server, goroutine, or wall clock.
func TestFilesPoll_IdleLadderBacksOff(t *testing.T) {
	const base = time.Second
	idleWant := []time.Duration{
		2 * base,
		2 * base,
		4 * base,
		10 * base,
		20 * base,
		20 * base,
	}
	for idleStep, expected := range idleWant {
		if got := filesPollWait(base, idleStep, 0, nil); got != expected {
			t.Fatalf("idle rung %d wait = %s, want %s", idleStep, got, expected)
		}
	}
	failureWant := []time.Duration{
		20 * base,
		40 * base,
		80 * base,
		120 * base,
		120 * base,
	}
	for failureStep, expected := range failureWant {
		if got := filesPollWait(base, 0, failureStep+1, nil); got != expected {
			t.Fatalf("failure rung %d wait = %s, want %s", failureStep+1, got, expected)
		}
	}
}

// TestFilesPoll_RemoteJitterIsBounded proves that only a remote path gets
// deterministic ±10% jitter; local paths retain exact ladder waits.
func TestFilesPoll_RemoteJitterIsBounded(t *testing.T) {
	const base = time.Second
	if got := filesPollWait(base, 0, 0, nil); got != 2*base {
		t.Fatalf("local wait = %s, want %s", got, 2*base)
	}
	remote := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // fixed non-security PRNG seed
	for i := range 100 {
		got := filesPollWait(base, 0, 0, remote)
		if got < 1800*time.Millisecond || got > 2200*time.Millisecond {
			t.Fatalf("remote wait %d = %s, want [1.8s, 2.2s]", i, got)
		}
	}
}

// TestFilesPoll_ChangeAnnouncementDoesNotResetOtherPaths proves that
// notification delivery does not mutate any path's scheduling state.
func TestFilesPoll_ChangeAnnouncementDoesNotResetOtherPaths(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "one"), filepath.Join(root, "two")}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, dirs, 3)
	w.mu.Lock()
	first := w.paths[dirs[0]]
	second := w.paths[dirs[1]]
	first.idleStep = filesPollMaxIdleStep
	first.dueAt = time.Now().Add(time.Hour)
	second.idleStep = 1
	second.dueAt = time.Now().Add(2 * time.Hour)
	w.mu.Unlock()
	e.ws.emitFilesChanged(w, dirs[0], "synthetic")
	w.mu.Lock()
	if first.idleStep != filesPollMaxIdleStep || second.idleStep != 1 {
		w.mu.Unlock()
		t.Fatalf("notification changed path ladders: first=%d second=%d", first.idleStep, second.idleStep)
	}
	w.mu.Unlock()
	if count(dirs[0])+count(dirs[1]) != 2 {
		t.Fatalf("notification performed a listing, counts: %d %d", count(dirs[0]), count(dirs[1]))
	}
}

// TestFilesPoll_ListActionResetsLadder proves that a user listing action
// resets the adaptive rung without spending a second full scan, then allows
// the next scheduled poll at the responsive rung.
func TestFilesPoll_ListActionResetsLadder(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	e.ws.filesPollInterval = 20 * time.Millisecond
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	before := count(dir)
	waittest.WaitForTimeout(t, "the idle ladder to advance", 5*time.Second, func() bool {
		return count(dir)-before >= 3
	})

	resp := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": bid,
		"path":      dir,
		"offset":    0,
		"limit":     1,
	}, 4)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.list: unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.list: %+v", envelope.Error)
	}
	afterAction := count(dir)
	waittest.WaitForTimeout(t, "the responsive poll after files.list", wantWithin,
		func() bool {
			return count(dir) > afterAction
		})
}

// TestFilesPoll_FailureLadderUsesRealListingErrors proves a provider listing
// failure reaches the separate retry ladder, and that a later successful
// listing returns to the responsive idle ladder.
func TestFilesPoll_FailureLadderUsesRealListingErrors(t *testing.T) {
	e, provider := newBlockingCountingFilesEnv(t)
	const base = 5 * time.Millisecond
	e.ws.filesPollInterval = base
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	p := provider()
	beforeFailure := p.count(dir)
	p.failNext(errors.New("synthetic listing failure"))

	waittest.WaitForTimeout(t, "the failure ladder to record the failed listing", wantWithin,
		func() bool {
			w.mu.Lock()
			defer w.mu.Unlock()
			entry := w.paths[dir]
			return entry != nil && entry.failureStep >= 1
		})
	w.mu.Lock()
	entry := w.paths[dir]
	if got := filesPollWait(base, entry.idleStep, entry.failureStep, nil); got != 20*base {
		w.mu.Unlock()
		t.Fatalf("first failure wait = %s, want %s", got, 20*base)
	}
	w.mu.Unlock()
	waittest.WaitForTimeout(t, "a successful listing after the failure", wantWithin,
		func() bool {
			w.mu.Lock()
			defer w.mu.Unlock()
			current := w.paths[dir]
			return current != nil && current.failureStep == 0 && p.count(dir) > beforeFailure
		})
	w.mu.Lock()
	entry = w.paths[dir]
	if got := filesPollWait(base, entry.idleStep, entry.failureStep, nil); got != 2*base {
		w.mu.Unlock()
		t.Fatalf("post-failure idle wait = %s, want %s", got, 2*base)
	}
	w.mu.Unlock()
}

// newBlockingCountingFilesEnv exposes the counting provider's one-shot List
// wedge so the timer test can release a deliberately slow poll and observe
// whether another scan was already pending.
func newBlockingCountingFilesEnv(t *testing.T) (*filesTestEnv, func() *countingProvider) {
	t.Helper()
	var mu sync.Mutex
	var prov *countingProvider
	factory := func(sess session.Session, rootPath string) (filesystem.Provider, error) {
		inner, err := filesLocalFactory(sess, rootPath)
		if err != nil {
			return nil, err
		}
		p := &countingProvider{
			Provider:     inner,
			lists:        map[string]int{},
			listStarted:  make(chan struct{}),
			listRelease:  make(chan struct{}),
			listFinished: make(chan struct{}),
		}
		mu.Lock()
		prov = p
		mu.Unlock()
		return p, nil
	}
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(factory))
	return e, func() *countingProvider {
		mu.Lock()
		defer mu.Unlock()
		return prov
	}
}

// TestFilesPoll_ArmsTimerAfterPoll proves that a slow scan does not leave a
// pending tick that starts the next scan immediately when the first returns.
func TestFilesPoll_ArmsTimerAfterPoll(t *testing.T) {
	e, provider := newBlockingCountingFilesEnv(t)
	e.ws.filesPollInterval = 10 * time.Millisecond
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	p := provider()
	p.blockNext()

	select {
	case <-p.listStarted:
	case <-time.After(wantWithin):
		t.Fatal("poll did not reach the deliberately slow listing")
	}
	beforeRelease := p.count(dir)
	close(p.listRelease)
	select {
	case <-p.listFinished:
	case <-time.After(wantWithin):
		t.Fatal("slow listing did not finish")
	}
	afterRelease := p.count(dir)
	if afterRelease != beforeRelease {
		t.Fatalf("listing count changed while the slow poll was completing: before=%d after=%d",
			beforeRelease, afterRelease)
	}

	time.Sleep(e.ws.filesPollInterval)
	if got := p.count(dir); got != afterRelease {
		t.Fatalf("next poll started immediately after slow work: count=%d want %d", got, afterRelease)
	}
	waittest.WaitForTimeout(t, "the next poll after the post-work wait", 5*time.Second, func() bool {
		return p.count(dir) > afterRelease
	})
}

func TestFilesPoll_DispatchesOnePathPerDispatch(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dirs := []string{
		filepath.Join(root, "one"),
		filepath.Join(root, "two"),
		filepath.Join(root, "three"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, dirs, 3)

	w.mu.Lock()
	w.markVisibleCatchUpLocked(time.Now())
	w.mu.Unlock()
	visited := make(map[string]bool, len(dirs))
	for i := range dirs {
		before := make(map[string]int, len(dirs))
		for _, dir := range dirs {
			before[dir] = count(dir)
		}
		if result := e.ws.filesPollTick(w); result == filesPollPathStopped {
			t.Fatal("poll stopped during visibility catch-up")
		}
		totalDelta := 0
		for _, dir := range dirs {
			delta := count(dir) - before[dir]
			if delta > 1 {
				t.Fatalf("dispatch %d listed %s %d times, want at most once", i, dir, delta)
			}
			if delta == 1 {
				visited[dir] = true
				totalDelta++
			}
		}
		if totalDelta != 1 {
			t.Fatalf("dispatch %d listed %d paths, want exactly one", i, totalDelta)
		}
	}
	for _, dir := range dirs {
		if !visited[dir] {
			t.Fatalf("catch-up never visited %s", dir)
		}
	}
}

func TestFilesPoll_PerPathLaddersAreIndependent(t *testing.T) {
	e, provider := newBlockingCountingFilesEnv(t)
	const base = 5 * time.Millisecond
	e.ws.filesPollInterval = base
	sid := e.openSession(t, 1)
	root := t.TempDir()
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	for _, dir := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, []string{firstPath, secondPath}, 3)
	w.mu.Lock()
	first := w.paths[firstPath]
	second := w.paths[secondPath]
	first.idleStep = 3
	second.idleStep = 1
	w.pollBase = base
	w.mu.Unlock()
	provider().failNext(errors.New("synthetic first-path failure"))
	result := e.ws.filesPollPath(w, w.handle, &filesPollSelection{
		path: firstPath, entry: first, generation: first.generation,
	})
	if result != filesPollPathFailed {
		t.Fatalf("first path result = %v, want failure", result)
	}
	w.mu.Lock()
	if first.failureStep != 1 || first.idleStep != 3 {
		t.Fatalf("failed path state = failure %d idle %d, want 1/3", first.failureStep, first.idleStep)
	}
	if second.failureStep != 0 || second.idleStep != 1 {
		t.Fatalf("healthy path changed after unrelated failure: failure %d idle %d", second.failureStep, second.idleStep)
	}
	second.failureStep = 2
	w.mu.Unlock()
	result = e.ws.filesPollPath(w, w.handle, &filesPollSelection{
		path: firstPath, entry: first, generation: first.generation,
	})
	if result != filesPollPathOK {
		t.Fatalf("first path recovery result = %v, want ok", result)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if first.failureStep != 0 || second.failureStep != 2 {
		t.Fatalf("recovery reset unrelated path: first failure %d second failure %d", first.failureStep, second.failureStep)
	}
}

func TestFilesPoll_FailedListDoesNotResetPath(t *testing.T) {
	e, provider := newBlockingCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	w.mu.Lock()
	w.paths[dir].idleStep = filesPollMaxIdleStep
	w.mu.Unlock()
	provider().failNext(errors.New("synthetic list failure"))
	resp := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{
		"bindingId": bid, "path": dir, "offset": 0, "limit": 1,
	}, 4)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.list: unmarshal: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("failed files.list unexpectedly succeeded")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if got := w.paths[dir].idleStep; got != filesPollMaxIdleStep {
		t.Fatalf("failed files.list reset idle rung to %d, want %d", got, filesPollMaxIdleStep)
	}
}

func TestFilesPoll_WatchReplacementRetainsPathState(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	for _, dir := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, []string{firstPath}, 3)
	w.mu.Lock()
	retained := w.paths[firstPath]
	retained.idleStep = 3
	retained.failureStep = 2
	w.mu.Unlock()
	e.watchDir(t, bid, []string{firstPath, secondPath}, 4)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.paths[firstPath] != retained || w.paths[firstPath].idleStep != 3 || w.paths[firstPath].failureStep != 2 {
		t.Fatal("retained path lost its scheduling state during watch replacement")
	}
	added := w.paths[secondPath]
	if added == nil || added.idleStep != 0 || added.failureStep != 0 || added.rev == "" {
		t.Fatalf("added path state = %+v, want fresh successful baseline", added)
	}
	if count(secondPath) != 1 {
		t.Fatalf("added path baseline count = %d, want 1", count(secondPath))
	}
}

func TestFilesPoll_UploadTargetsBothDestinations(t *testing.T) {
	e, count := newCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "one"), filepath.Join(root, "two")}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, dirs, 3)
	before := map[string]int{dirs[0]: count(dirs[0]), dirs[1]: count(dirs[1])}
	for _, dir := range dirs {
		e.ws.invalidateUploadDest(&runningTransfer{
			sessionID: session.ID(sid),
			bindingID: bid,
			upload:    transfer.Upload{DestDir: dir},
		})
	}
	waittest.WaitForTimeout(t, "both upload destinations to be polled", wantWithin, func() bool {
		return count(dirs[0]) > before[dirs[0]] && count(dirs[1]) > before[dirs[1]]
	})
	for _, dir := range dirs {
		if got := count(dir) - before[dir]; got != 1 {
			t.Fatalf("upload destination %s polled %d times, want 1", dir, got)
		}
	}
}

func TestFilesPoll_StaleCompletionDoesNotOverwriteReaddedPath(t *testing.T) {
	e, provider := newBlockingCountingFilesEnv(t)
	e.ws.filesPollInterval = time.Hour
	sid := e.openSession(t, 1)
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	w.mu.Lock()
	old := w.paths[dir]
	old.dueAt = time.Now()
	h := w.handle
	w.mu.Unlock()
	selection := &filesPollSelection{path: dir, entry: old, generation: old.generation}
	p := provider()
	p.blockNext()
	result := make(chan filesPollPathResult, 1)
	go func() { result <- e.ws.filesPollPath(w, h, selection) }()
	select {
	case <-p.listStarted:
	case <-time.After(wantWithin):
		t.Fatal("stale listing did not start")
	}
	w.mu.Lock()
	readded := newFilesPollEntry("new-baseline", old.generation+1, time.Hour, nil)
	w.paths[dir] = readded
	w.mu.Unlock()
	close(p.listRelease)
	select {
	case <-result:
	case <-time.After(wantWithin):
		t.Fatal("stale listing did not finish")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if got := w.paths[dir].rev; got != "new-baseline" {
		t.Fatalf("re-added path rev = %q, stale completion overwrote it", got)
	}
}

// TestFilesRefreshState_RoutineDeferralAtCeilingIsSilent keeps routine
// round-robin pacing from being mistaken for a stalled watcher. Every
// dispatch succeeds, so the aggregate observation window stays fresh even
// though each individual path is revisited less often at the ceiling.
func TestFilesRefreshState_RoutineDeferralAtCeilingIsSilent(t *testing.T) {
	e, _ := newCountingFilesEnv(t)
	e.ws.filesPollInterval = 5 * time.Millisecond
	e.ws.filesRefreshStateThreshold = 100 * time.Millisecond
	sid := e.openSession(t, 1)
	root := t.TempDir()
	paths := make([]string, maxWatchPaths)
	for i := range paths {
		paths[i] = filepath.Join(root, fmt.Sprintf("watched-%d", i))
		if err := os.MkdirAll(paths[i], 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", paths[i], err)
		}
	}
	bid := e.openBinding(t, sid, root, 2)
	e.watchDir(t, bid, paths, 3)

	if _, err := awaitNotification(e.conn, "files.refreshStateChanged", 100*time.Millisecond); err == nil {
		t.Fatal("routine successful dispatches emitted delayed state")
	}
}

func TestFilesRefreshState_CrossesAndRecoversOnlyOnEdges(t *testing.T) {
	now := time.Unix(100, 0)
	entry := &filesPollEntry{lastSuccessfulObservation: now}
	w := &filesWatcher{
		paths:        map[string]*filesPollEntry{"/watched": entry},
		visible:      true,
		refreshState: filesRefreshStateOK,
	}
	const threshold = time.Minute

	if state, changed := w.updateRefreshState(now.Add(threshold), threshold); state != filesRefreshStateDelayed || !changed {
		t.Fatalf("first threshold crossing = %q/%t, want delayed/true", state, changed)
	}
	if state, changed := w.updateRefreshState(now.Add(2*threshold), threshold); state != filesRefreshStateDelayed || changed {
		t.Fatalf("steady delayed state = %q/%t, want delayed/false", state, changed)
	}

	entry.lastSuccessfulObservation = now.Add(2 * threshold)
	if state, changed := w.updateRefreshState(now.Add(2*threshold), threshold); state != filesRefreshStateOK || !changed {
		t.Fatalf("recovery = %q/%t, want ok/true", state, changed)
	}
	if state, changed := w.updateRefreshState(now.Add(5*threshold/2), threshold); state != filesRefreshStateOK || changed {
		t.Fatalf("steady ok state = %q/%t, want ok/false", state, changed)
	}
}

func TestFilesRefreshState_HiddenTimeDoesNotCount(t *testing.T) {
	now := time.Unix(100, 0)
	entry := &filesPollEntry{lastSuccessfulObservation: now}
	w := &filesWatcher{
		paths:        map[string]*filesPollEntry{"/watched": entry},
		visible:      false,
		refreshState: filesRefreshStateOK,
		wake:         make(chan struct{}, 1),
	}
	const threshold = time.Minute

	if state, changed := w.updateRefreshState(now.Add(10*threshold), threshold); state != filesRefreshStateOK || changed {
		t.Fatalf("hidden state = %q/%t, want ok/false", state, changed)
	}
	w.setVisible(true)
	if state, changed := w.updateRefreshState(now.Add(10*threshold+threshold/2), threshold); state != filesRefreshStateOK || changed {
		t.Fatalf("fresh visible window = %q/%t, want ok/false", state, changed)
	}
}
