package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeDialogService returns canned paths; "" means the user cancelled. The
// file and directory pickers are canned independently — they are two methods
// of one capability, and a test must be able to fail one while the other
// succeeds.
type fakeDialogService struct {
	path string
	err  error
	dir  string

	dirErr error
}

func (f *fakeDialogService) OpenFile(_ context.Context) (string, error) {
	return f.path, f.err
}

func (f *fakeDialogService) OpenDirectory(_ context.Context) (string, error) {
	return f.dir, f.dirErr
}

// dialog.openFile is a control-plane capability (AD-1): the renderer cannot
// reach the Wails runtime, so it asks the backend. The runtime is often
// absent — the dev-web harness has no Wails at all — and the method must
// report itself unavailable rather than fail, so the surface can degrade to
// typing the path by hand.
func TestDialogOpenFile_UnavailableWithoutService(t *testing.T) {
	h := newInventoryHarness(t)
	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatalf("expected 'not available' error, got %s", string(resp))
	}
	if errResult.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601 (method not available)", errResult.Error.Code)
	}
}

func TestDialogOpenFile_ReturnsAbsolutePath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{path: "/home/dev/.ssh/id_ed25519"})

	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Error != nil {
		t.Fatalf("dialog.openFile: %+v", result.Error)
	}
	if result.Result.Path != "/home/dev/.ssh/id_ed25519" {
		t.Errorf("path = %q, want %q", result.Result.Path, "/home/dev/.ssh/id_ed25519")
	}
}

// A cancelled picker yields an empty path — the renderer treats "" as "no
// change", never as an error.
func TestDialogOpenFile_CancelledYieldsEmptyPath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{path: ""})

	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result.Path != "" {
		t.Errorf("path = %q, want empty on cancel", result.Result.Path)
	}
}

// ── dialog.openDirectory ────────────────────────────────────────────────
//
// The directory picker is the same capability as the file picker — one
// native dialog, one owner (DialogService) — so these tests assert the same
// three things openFile asserts (absence, a chosen absolute path, a
// cancelled picker), plus the two the file picker's tests never had to make
// explicit: a failure is distinguishable from a cancel, and the capability
// is busy for EVERY dialog method while one adapter has not returned.

// blockingDirectoryDialog is the NON-cooperative directory adapter: it takes
// the transport's context and cannot cancel (exactly like the real Wails
// runtime), so it blocks until released. It counts invocations so a test can
// prove no second picker was opened over the first.
type blockingDirectoryDialog struct {
	started chan struct{}
	release chan struct{}

	mu    sync.Mutex
	calls int
}

func (d *blockingDirectoryDialog) OpenFile(_ context.Context) (string, error) {
	return "/home/dev/.ssh/id_ed25519", nil
}

func (d *blockingDirectoryDialog) OpenDirectory(_ context.Context) (string, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	if n == 1 {
		close(d.started)
		<-d.release
	}
	return "/home/dev/collections", nil
}

// The absence is the ordinary case, not an edge one: the dev-web harness has
// no Wails at all, and that is the configuration the surface is developed in.
// The method must exist and report the RUNTIME unavailable — the message is
// what separates "no native dialog here" from "no such method", and the
// renderer degrades to typing the path by hand on the first, not the second.
func TestDialogOpenDirectory_UnavailableWithoutService(t *testing.T) {
	h := newInventoryHarness(t)
	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var errResult struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatalf("expected 'not available' error, got %s", string(resp))
	}
	if errResult.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601 (method not available)", errResult.Error.Code)
	}
	if errResult.Error.Message != "dialog not available" {
		t.Errorf("message = %q, want %q — an unregistered method also answers -32601, "+
			"so the message is what proves the method exists and the RUNTIME does not",
			errResult.Error.Message, "dialog not available")
	}
}

func TestDialogOpenDirectory_ReturnsAbsolutePath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{dir: "/home/dev/collections"})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Error != nil {
		t.Fatalf("dialog.openDirectory: %+v", result.Error)
	}
	if result.Result.Path != "/home/dev/collections" {
		t.Errorf("path = %q, want %q", result.Result.Path, "/home/dev/collections")
	}
}

// A cancelled picker is a RESULT, never an error: the renderer reads "" as
// "no change" and leaves the field alone.
func TestDialogOpenDirectory_CancelledYieldsEmptyPath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{dir: ""})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var result struct {
		Result *struct {
			Path string `json:"path"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Error != nil {
		t.Fatalf("a cancelled picker must not be an error: %+v", result.Error)
	}
	if result.Result == nil {
		t.Fatalf("a cancelled picker must answer a result, got %s", string(resp))
	}
	if result.Result.Path != "" {
		t.Errorf("path = %q, want empty on cancel", result.Result.Path)
	}
}

// …and the pair to it: a runtime FAILURE is an error, so the two cannot be
// confused by a caller reading only the path. This is the "for every external
// call there is a test where it fails" half; the success half is above.
func TestDialogOpenDirectory_AdapterFailureIsAnError(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{dirErr: errors.New("no directory chosen")})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var env struct {
		Result *json.RawMessage `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if env.Error == nil {
		t.Fatalf("a failing adapter must be an error, not a cancelled picker: %s", string(resp))
	}
	if env.Error.Code != -32603 {
		t.Errorf("code = %d, want -32603 (internal error)", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "dialog.openDirectory: ") {
		t.Errorf("message = %q, want it to name the method", env.Error.Message)
	}
	if !strings.Contains(env.Error.Message, "no directory chosen") {
		t.Errorf("message = %q, want the runtime's own error carried through", env.Error.Message)
	}
	if env.Result != nil {
		t.Errorf("a failure must carry no result, got %s", string(*env.Result))
	}
}

// The busy interval, stated with BOTH ends: from the moment the adapter is
// entered until the moment it actually RETURNS, every dialog.openDirectory
// from every connection is refused rather than queued — a second native
// picker must never stack over the first. The adapter here is the
// non-cooperative kind, which is the real Wails runtime.
func TestDialogOpenDirectory_BusyRefusesSecondFromAnotherConnection(t *testing.T) {
	h := newInventoryHarness(t)
	dlg := &blockingDirectoryDialog{started: make(chan struct{}), release: make(chan struct{})}
	h.ws.SetDialogService(dlg)

	sendControl(t, h.conn, "dialog.openDirectory", map[string]any{}, 1)
	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("directory adapter never invoked")
	}

	connB := connectWS(t, h.ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCall(t, connB, "dialog.openDirectory", map[string]any{})
	var errEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &errEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errEnv.Error == nil {
		t.Fatalf("a second dialog.openDirectory while the first adapter runs must be refused, got %s", resp)
	}
	if errEnv.Error.Code != SaturationErrorCode {
		t.Fatalf("refusal code = %d, want %d (control plane busy)", errEnv.Error.Code, SaturationErrorCode)
	}
	dlg.mu.Lock()
	calls := dlg.calls
	dlg.mu.Unlock()
	if calls != 1 {
		t.Fatalf("adapter invoked %d times; the second picker must not reach the native capability", calls)
	}

	// The other end of the interval: only the adapter's actual return frees it.
	close(dlg.release)
	waitDirectoryDialogFree(t, connB, "/home/dev/collections")
}

// One native capability, not two: while the FILE picker's adapter is still
// running, dialog.openDirectory is refused as well. Two pickers open at once
// is the defect, whichever method opened them.
func TestDialogOpenDirectory_RefusedWhileOpenFileIsOutstanding(t *testing.T) {
	h := newInventoryHarness(t)
	dlg := &blockingDialog{started: make(chan struct{}), release: make(chan struct{})}
	h.ws.SetDialogService(dlg)

	sendControl(t, h.conn, "dialog.openFile", map[string]any{}, 1)
	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("file adapter never invoked")
	}

	connB := connectWS(t, h.ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCall(t, connB, "dialog.openDirectory", map[string]any{})
	var errEnv struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &errEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errEnv.Error == nil {
		t.Fatalf("dialog.openDirectory while a file picker is open must be refused, got %s", resp)
	}
	if errEnv.Error.Code != SaturationErrorCode {
		t.Fatalf("refusal code = %d, want %d (control plane busy)", errEnv.Error.Code, SaturationErrorCode)
	}

	close(dlg.release)
	waitDirectoryDialogFree(t, connB, "/home/dev/collections")
}

// waitDirectoryDialogFree polls dialog.openDirectory until it succeeds,
// asserting the capability is no longer busy. It waits on an observable state
// change (the method answering a result), never on a duration.
func waitDirectoryDialogFree(t *testing.T, conn *websocket.Conn, wantPath string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := jsonrpcCall(t, conn, "dialog.openDirectory", map[string]any{})
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

// ── contract conformance ────────────────────────────────────────────────

// The DTO's own conformance: an absolute path, or an empty string for a
// cancelled picker. The `path` field is required and the key set is exact.
func TestDialogOpenDirectory_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "dialog.openDirectory.schema.json")
	raw, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: "/home/dev/collections"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "dialog.openDirectory DTO")

	rawCancel, err := json.Marshal(struct {
		Path string `json:"path"`
	}{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, rawCancel, "dialog.openDirectory cancelled DTO")
}

// The real method off the real socket, with the fake adapter standing in for
// the Wails runtime. A test that validates a payload the test itself built
// proves the struct is well-formed, not that the server sends it.
func TestDialogOpenDirectory_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "dialog.openDirectory.schema.json")
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{dir: "/home/dev/collections"})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("dialog.openDirectory: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "dialog.openDirectory result")
}

// The native picker is a SERIALISATION POINT, not an execution bound: one
// picker at a time, and a request that arrives while the capability is held
// WAITS for it rather than being refused (ADR-0026 item 4). The distinction
// is not academic — it is the whole reason the R2 sweep
// (TestSourceTicket_CannotBeMintedFromTheWire) could report the picker
// opening zero times while nothing about R2 had changed. dialog.openFile
// sorts immediately before dialog.openFileForUpload, the handler enqueues
// its response INSIDE the task and the permit is returned only after the
// task goroutine returns, so the sweep's next request landed in that tail
// window and was refused "Control plane busy" for doing nothing wrong.
//
// This test drives the window deterministically instead of waiting for a
// loaded machine to find it: the first adapter blocks with the capability
// held, the second request is submitted, and it must still be UNANSWERED —
// waiting, not refused — and must then succeed once the first releases.
// Against an instant-refusal dialog admission it fails on the first check.
func TestDialogCapability_QueuesRatherThanRefusingBehindItsOwnTail(t *testing.T) {
	// The wait bound is generous ON PURPOSE, and it is not what this test is
	// about. What is under test is the gate's CLASS — waiting versus instant
	// refusal — and the old wiring refuses instantly whatever the bound is,
	// so this still fails on the first check against it. Left at the
	// production 1 s the test would additionally require the release below
	// to land inside that second, which is a dependence on a fast machine:
	// green here, red on a loaded runner, and broken either way.
	// TestDialogOpenFile_NonCooperativeAdapterBusyUntilReturn is where the
	// production bound running out is the assertion.
	h := newInventoryHarness(t, WithDomainConflictWaitTimeout(time.Minute))
	dlg := &blockingDialog{started: make(chan struct{}), release: make(chan struct{})}
	h.ws.SetDialogService(dlg)

	connA := h.conn
	sendControl(t, connA, "dialog.openFile", map[string]any{}, 1)
	select {
	case <-dlg.started:
	case <-time.After(5 * time.Second):
		t.Fatal("dialog adapter never invoked")
	}

	// A second request arrives while the capability is held. Read it on its
	// own goroutine: a short read deadline would trip gorilla's permanent
	// error store, and the point is that NOTHING comes back yet.
	connB := connectWS(t, h.ws)
	defer func() { _ = connB.Close() }()
	answered := make(chan []byte, 1)
	go func() {
		_, resp, err := connB.ReadMessage()
		if err != nil {
			return
		}
		answered <- resp
	}()
	sendControl(t, connB, "dialog.openFile", map[string]any{}, 2)

	select {
	case resp := <-answered:
		t.Fatalf("the second dialog.openFile was answered while the capability was held: %s", resp)
	case <-time.After(200 * time.Millisecond):
	}

	// The capability frees; the queued request proceeds, and it proceeds
	// with a RESULT — the refusal is reserved for exhausting a bound.
	close(dlg.release)
	var resp []byte
	select {
	case resp = <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued dialog.openFile never ran after the capability freed")
	}
	var env struct {
		Error  *jsonrpcErrorObj `json:"error"`
		Result *struct {
			Path string `json:"path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if env.Error != nil {
		t.Fatalf("the queued request was refused (code %d); a conflict is a queue, not an overload", env.Error.Code)
	}
	if env.Result == nil || env.Result.Path != "/home/dev/.ssh/id_ed25519" {
		t.Fatalf("the queued request answered %s, want the adapter's path", resp)
	}
}
