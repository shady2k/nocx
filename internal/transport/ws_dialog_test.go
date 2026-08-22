package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fakeDialogService returns canned paths; "" means the user cancelled.
type fakeDialogService struct {
	path string
	err  error
}

func (f *fakeDialogService) OpenFile(_ context.Context) (string, error) {
	return f.path, f.err
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
