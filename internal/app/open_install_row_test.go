package app

// A first open's helper install is ONE Operations row for its whole duration,
// with what it is doing, cancellable; the row goes when the install settles;
// an install that is not needed shows no row at all (nocx-3an1s.2.10,
// ADR-8eej3e66 §8).
//
// This is driven through the REAL transport and the real helper registry: a
// real `open` frame arrives on a real socket, the real deploy.Ensure runs
// against a fake install lease whose write is parked, and the assertions are
// on the notifications that come back on that socket. The renderer's half —
// one row in the DOM, and Cancel reaching rpc.cancel — is asserted in
// frontend/src/operations/install-operations.test.tsx and in
// e2e/helper-install-row.spec.ts.
//
// NOTHING HERE WAITS OUT A DURATION. The install parks on a channel the test
// owns, so "the install takes minutes" is a state rather than a sleep, and
// the cancellation is ordered against the response to the rpc.cancel frame.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/transport"
)

// openRowFrame is one JSON-RPC frame read off the socket, reduced to what
// these tests ask of it.
type openRowFrame struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result *struct {
		SessionID string `json:"sessionId"`
	} `json:"result"`
}

// operationRowParamsFor decodes one operation.row's params.
type operationRowParamsFor struct {
	RequestID json.RawMessage `json:"requestId"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	Machine   string          `json:"machine"`
	Done      int64           `json:"done"`
	Total     int64           `json:"total"`
}

// installRowStand is the far stand plus the socket both tests read.
type installRowStand struct {
	reg  *helperRegistry
	inst *fakeInstallConn
	home string
	ws   *transport.WSServer
	conn *websocket.Conn
}

func newInstallRowStand(t *testing.T, hold bool) *installRowStand {
	t.Helper()
	sl := logtest.Slog(t)
	appLog := log.NewSlogAdapter(sl)
	stand := startFarStand(t, &recordingSpawner{}, helpersession.ConnectedIdleGrace, func() {})
	stand.reg.tools = &fakeFarTools{}

	home := t.TempDir()
	inst := newFakeInstallConn(home)
	if hold {
		inst.holdWrite = make(chan struct{})
		inst.writeStarted = make(chan struct{})
	}
	stand.provider.home = home
	stand.provider.install = inst

	ws := transport.NewWSServer(appLog, stand.reg.registry,
		transport.WithProfileResolver(traceResolver{host: "host.example:22"}),
		transport.WithHelperSessionOpener(registryOpener{reg: stand.reg}),
	)
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	return &installRowStand{reg: stand.reg, inst: inst, home: home, ws: ws, conn: dialTraceWSServer(t, ws)}
}

// readFrame reads the next text frame, skipping the binary data plane.
func readFrame(t *testing.T, conn *websocket.Conn, what string) openRowFrame {
	t.Helper()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		kind, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s: %v", what, err)
		}
		if kind != websocket.TextMessage {
			continue
		}
		var frame openRowFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode %s: %v", what, err)
		}
		return frame
	}
}

// readRow reads frames until an operation.row arrives, and records every
// method it passed on the way so a caller can assert what did NOT come.
func readRow(t *testing.T, conn *websocket.Conn, what string) (operationRowParamsFor, json.RawMessage, []string) {
	t.Helper()
	var seen []string
	for {
		frame := readFrame(t, conn, what)
		if frame.Method != "operation.row" {
			seen = append(seen, frame.Method)
			continue
		}
		var params operationRowParamsFor
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			t.Fatalf("decode operation.row: %v", err)
		}
		return params, frame.Params, seen
	}
}

// TestAFirstOpenReportsItsHelperInstallAsOneOperationsRow is the whole first
// open, seen from the wire: the row appears while the payload is still at
// byte zero (its whole duration, not after a threshold), it names what the
// install is doing and how far it has got, it is ONE row, and it goes when
// the install settles. A repeat open — the install already complete — shows
// no row at all.
func TestAFirstOpenReportsItsHelperInstallAsOneOperationsRow(t *testing.T) {
	stand := newInstallRowStand(t, true)
	gate := stand.inst.holdWrite
	release := sync.OnceFunc(func() { close(gate) })
	defer release()

	sendOpen(t, stand.conn, "ssh:install-row", 1)
	// The install is now parked inside its first write: nothing has moved on
	// the wire, and anything the row is going to say has already been said.
	select {
	case <-stand.inst.writeStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("the open never reached the helper install")
	}

	first, firstRaw, before := readRow(t, stand.conn, "the install's first row")
	if len(before) != 0 {
		t.Fatalf("frames arrived before the install's row: %v", before)
	}
	// THE REAL NOTIFICATION OFF THE REAL SOCKET, against its schema — the
	// check a struct-shaped assertion cannot make (contracts/README.md).
	validateAppContract(t, loadAppContractSchema(t, "operation.row.schema.json"), firstRaw, "operation.row params (real socket)")
	if string(first.RequestID) != "1" {
		t.Fatalf("the row is addressed to request %s, want the open's own id 1", first.RequestID)
	}
	if first.Kind != "install" {
		t.Fatalf("the row's kind is %q, want install", first.Kind)
	}
	if first.Title != "Copying the nocx helper" {
		t.Fatalf("the row says %q, want what the install is doing", first.Title)
	}
	if first.Machine != "tester@host.example" {
		t.Fatalf("the row names machine %q, want the destination as a person writes it", first.Machine)
	}
	if first.Done != 0 || first.Total != int64(len(syntheticPayload)) {
		t.Fatalf("the row reports %d of %d bytes, want 0 of the artifact's size (%d): the row exists from the start of the transfer", first.Done, first.Total, len(syntheticPayload))
	}

	// The install is released; it finishes, then reports that it is checking
	// what arrived, then the row goes.
	release()
	var titles []string
	var last operationRowParamsFor
	for {
		frame := readFrame(t, stand.conn, "the install to settle")
		switch frame.Method {
		case "operation.row":
			if err := json.Unmarshal(frame.Params, &last); err != nil {
				t.Fatalf("decode operation.row: %v", err)
			}
			titles = append(titles, last.Title)
			if string(last.RequestID) != "1" {
				t.Fatalf("a second row appeared for request %s: one install is one row", last.RequestID)
			}
		case "operation.rowDone":
			validateAppContract(t, loadAppContractSchema(t, "operation.rowDone.schema.json"), frame.Params, "operation.rowDone params (real socket)")
			var done struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if err := json.Unmarshal(frame.Params, &done); err != nil {
				t.Fatalf("decode operation.rowDone: %v", err)
			}
			if string(done.RequestID) != "1" {
				t.Fatalf("the row that went was request %s, want 1", done.RequestID)
			}
			goto settled
		}
	}
settled:
	if last.Title != "Checking the nocx helper" || last.Total != 0 {
		t.Fatalf("the last row before it went says %q with total %d, want the verification stage and no bar", last.Title, last.Total)
	}
	if len(titles) == 0 {
		t.Fatal("the install never reported that it had copied the bytes")
	}
	if !stand.inst.hasCompleteInstall(stand.home) {
		t.Fatal("the install the row reported did not complete")
	}

	// ── a repeat open, now that the install is complete, takes no row ─────
	//
	// Read frames up to the open's own result: notifications are ordered on
	// one connection, so anything the second open reported would have arrived
	// before its answer. Nothing may be an operation.row.
	sendOpen(t, stand.conn, "ssh:install-row", 2)
	for {
		frame := readFrame(t, stand.conn, "the second open to answer")
		if frame.Method == "operation.row" {
			t.Fatalf("a repeat open of a host whose helper is installed reported a row: %s", string(frame.Params))
		}
		if frame.ID != nil && string(*frame.ID) == "2" {
			if frame.Error != nil {
				t.Fatalf("the second open failed: %+v", frame.Error)
			}
			return
		}
	}
}

// TestCancellingTheInstallEndsTheOpenAndNothingLaterTrustsIt is the person's
// Cancel: rpc.cancel for the open's id ends the open (it answers an error
// rather than opening a pane), the install stops before its marker, and the
// NEXT open installs again rather than reusing the half-written tree.
func TestCancellingTheInstallEndsTheOpenAndNothingLaterTrustsIt(t *testing.T) {
	stand := newInstallRowStand(t, true)
	gate := stand.inst.holdWrite
	release := sync.OnceFunc(func() { close(gate) })
	defer release()

	sendOpen(t, stand.conn, "ssh:install-row", 1)
	select {
	case <-stand.inst.writeStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("the open never reached the helper install")
	}
	// The row is on the wire before anything has moved; what it says is the
	// first test's subject, and here it only has to exist.
	_, _, _ = readRow(t, stand.conn, "the install's row")

	// ── the person presses Cancel ─────────────────────────────────────────
	//
	// As a REQUEST, so the answer proves the backend processed the
	// withdrawal before the install is released: the write then returns to a
	// cancelled context, which is the state a real slow link reaches one
	// chunk later.
	writeFrame(t, stand.conn, map[string]any{
		"jsonrpc": "2.0", "id": 99, "method": "rpc.cancel",
		"params": map[string]any{"id": 1},
	})
	for {
		frame := readFrame(t, stand.conn, "rpc.cancel to be processed")
		if frame.ID != nil && string(*frame.ID) == "99" {
			break
		}
	}
	release()

	// The open ends with an error — a cancelled open is not a pane.
	for {
		frame := readFrame(t, stand.conn, "the cancelled open to answer")
		if frame.ID == nil || string(*frame.ID) != "1" {
			continue
		}
		if frame.Error == nil {
			t.Fatal("a cancelled open answered a session result")
		}
		break
	}
	if stand.inst.hasCompleteInstall(stand.home) {
		t.Fatal("the cancelled install left a complete install behind: a later open would trust a half-written helper")
	}

	// ── the next open reinstalls, and completes ───────────────────────────
	sendOpen(t, stand.conn, "ssh:install-row", 2)
	row, _, _ := readRow(t, stand.conn, "the next open's install row")
	if row.Title != "Copying the nocx helper" {
		t.Fatalf("the open after a cancelled install says %q, want a fresh install", row.Title)
	}
	for {
		frame := readFrame(t, stand.conn, "the second install to settle")
		if frame.Method == "operation.rowDone" {
			break
		}
	}
	if !stand.inst.hasCompleteInstall(stand.home) {
		t.Fatal("the install after a cancelled one did not complete")
	}
}

func writeFrame(t *testing.T, conn *websocket.Conn, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}
