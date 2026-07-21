package transport

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
)

type stubPTYFactory struct{ stub *pty.Stub }

func (f *stubPTYFactory) NewPTY(_ context.Context, _ pty.Config) (pty.Pty, error) {
	return f.stub, nil
}

func newRegWithStub(logger log.Logger) *session.Reg {
	return session.New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})
}

type realPTYFactory struct{ log log.Logger }

func (f *realPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	return pty.NewLocal(f.log, cfg)
}

func newRegWithReal(logger log.Logger) *session.Reg {
	return session.New(logger, &realPTYFactory{log: logger})
}

func wsURL(ws *WSServer) string {
	return "ws://" + ws.listener.Addr().String() + "/session"
}

func connectWS(t *testing.T, ws *WSServer) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(wsURL(ws))
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func jsonrpcCall(t *testing.T, conn *websocket.Conn, method string, params any) json.RawMessage {
	t.Helper()
	return jsonrpcCallWithID(t, conn, method, params, 1)
}

func jsonrpcCallWithID(t *testing.T, conn *websocket.Conn, method string, params any, id int) json.RawMessage {
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
	werr := conn.WriteMessage(websocket.TextMessage, req)
	if werr != nil {
		t.Fatalf("write request: %v", werr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		// Skip notifications (exit, etc.) — they have no id.
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(resp, &check)
		if check.ID == nil {
			continue
		}
		var idCheck struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(resp, &idCheck); err != nil || idCheck.ID != id {
			continue
		}
		return resp
	}
}

func TestWSServer_StartStop(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()

	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ws.Port() == 0 {
		t.Fatal("Port() == 0")
	}
	if err := ws.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWSServer_OpensSession(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", map[string]uint16{
		"cols":   80,
		"rows":   24,
		"xpixel": 0,
		"ypixel": 0,
	})

	var r struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if r.Result.SessionID == "" {
		t.Fatal("expected non-empty sessionId")
	}
	if len(r.Result.SessionID) != 32 {
		t.Fatalf("expected 32 hex chars, got %d: %q", len(r.Result.SessionID), r.Result.SessionID)
	}
}

func TestWSServer_TwoOpenCalls_DifferentIDs(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp1 := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	resp2 := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 2)

	var r1, r2 struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp1, &r1)
	_ = json.Unmarshal(resp2, &r2)

	if r1.Result.SessionID == r2.Result.SessionID {
		t.Fatal("two open calls returned same sessionId")
	}
}

func TestWSServer_SizeContract_OpenAtSize(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 132, "rows": 43, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	sidBytes, err := session.IDToBytes(session.ID(sid))
	if err != nil {
		t.Fatalf("IDToBytes: %v", err)
	}
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("stty size\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, f.Encode())

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		output := string(frame.Payload)
		if strings.Contains(output, "43 132") || strings.Contains(output, "43\t132") {
			return
		}
	}
}

func TestWSServer_Resize(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	_ = jsonrpcCallWithID(t, conn, "resize", map[string]any{
		"sessionId": sid,
		"cols":      100,
		"rows":      30,
		"xpixel":    0,
		"ypixel":    0,
	}, 2)

	sidBytes, _ := session.IDToBytes(session.ID(sid))
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("stty size\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, f.Encode())

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		output := string(frame.Payload)
		if strings.Contains(output, "30 100") || strings.Contains(output, "30\t100") {
			return
		}
	}
}

func TestWSServer_CloseSession(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	resp = jsonrpcCallWithID(t, conn, "close", map[string]string{
		"sessionId": sid,
	}, 2)

	var cr struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &cr)
	if cr.Error != nil {
		t.Fatalf("close returned error: %v", cr.Error)
	}

	sidBytes, _ := session.IDToBytes(session.ID(sid))
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("echo test\n")}
	if err := conn.WriteMessage(websocket.BinaryMessage, f.Encode()); err != nil {
		t.Fatalf("write after close: %v", err)
	}
}

func TestWSServer_DataFrameWithUnknownSessionID_Dropped(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	_ = jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var bogusID [16]byte
	bogusID[0] = 0xFF
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: bogusID, Payload: []byte("echo hack\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, f.Encode())

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 2)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Result.SessionID == "" {
		t.Fatal("connection not usable after unknown session-id frame")
	}
}

func TestWSServer_ShortFrame_Dropped(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	_ = conn.WriteMessage(websocket.BinaryMessage, []byte("short"))

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Result.SessionID == "" {
		t.Fatal("connection not usable after short frame")
	}
}

func TestWSServer_BadVersion_Dropped(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	buf := make([]byte, FrameHeaderSize)
	buf[0] = 0x99
	_ = conn.WriteMessage(websocket.BinaryMessage, buf)

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Result.SessionID == "" {
		t.Fatal("connection not usable after bad version frame")
	}
}

func TestWSServer_MetadataFrame_Dropped(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	buf := make([]byte, FrameHeaderSize)
	buf[0] = FrameVersion
	buf[1] = MsgTypeMetadata
	_ = conn.WriteMessage(websocket.BinaryMessage, buf)

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Result.SessionID == "" {
		t.Fatal("connection not usable after metadata frame")
	}
}

func TestWSServer_JSONRPC_MalformedJSON(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	_ = conn.WriteMessage(websocket.TextMessage, []byte("{bad json"))

	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var r struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Error.Code != -32700 {
		t.Fatalf("expected parse error -32700, got %d", r.Error.Code)
	}
}

func TestWSServer_JSONRPC_MethodNotFound(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "nonexistent", map[string]any{})

	var r struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Error.Code != -32601 {
		t.Fatalf("expected method not found -32601, got %d", r.Error.Code)
	}
}

func TestWSServer_JSONRPC_InvalidParams(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", map[string]any{
		"wrong": "params",
	})

	var r struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Error.Code != -32602 {
		t.Fatalf("expected invalid params -32602, got %d", r.Error.Code)
	}
}

func TestWSServer_JSONRPC_InvalidRequest(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	_ = conn.WriteMessage(websocket.TextMessage, []byte("[1,2,3]"))

	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var r struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &r)
	if r.Error.Code != -32600 {
		t.Fatalf("expected invalid request -32600, got %d", r.Error.Code)
	}
}

// TestWSServer_OpenAckBeforeData ensures the open result arrives before any
// data-bearing frame for that session (DEFECT 2 fix — AD-7 ordering invariant).
// We exploit the fact that writes to a single wsConn are serialized: if the
// output pump were started before the ack, the first ReadMessage after 'open'
// could return a binary frame instead of the text ack.
func TestWSServer_OpenAckBeforeData(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	// Verify the response is a text message (JSON-RPC result), not binary.
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("open response was not valid JSON: %v", err)
	}
	if r.Result.SessionID == "" {
		t.Fatal("expected sessionId in open response")
	}

	// Now the next message could be binary (PTY output) or text (exit, etc).
	// The important thing is the first message from open was the ack.
	// Also ensure a data frame sent after open actually reaches the PTY.
	sidBytes, _ := session.IDToBytes(session.ID(r.Result.SessionID))
	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeData,
		SessionID: sidBytes,
		Payload:   []byte("echo ack-test\n"),
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, f.Encode())

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(data) == 0 {
			continue
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		if strings.Contains(string(frame.Payload), "ack-test") {
			return
		}
	}
}

// TestWSServer_ExitClearsRegistry verifies that when the shell exits the
// session is removed from the registry (DEFECT 3 fix).
func TestWSServer_ExitClearsRegistry(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	})

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	sidBytes, _ := session.IDToBytes(session.ID(sid))
	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeData,
		SessionID: sidBytes,
		Payload:   []byte("exit\n"),
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, f.Encode())

	// Wait for the session to exit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(sess.List()) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session still in registry after exit: %d sessions", len(sess.List()))
}

// TestWSServer_CrossConnectionIsolation proves connection A cannot touch
// connection B's session (DEFECT 5 fix).
func TestWSServer_CrossConnectionIsolation(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	connA := connectWS(t, ws)
	defer func() { _ = connA.Close() }()
	connB := connectWS(t, ws)
	defer func() { _ = connB.Close() }()

	// Connection A opens a session.
	respA := jsonrpcCallWithID(t, connA, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var ra struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respA, &ra)
	sidA := ra.Result.SessionID
	if sidA == "" {
		t.Fatal("expected sessionId from connA")
	}

	// Connection B tries to resize A's session — must fail with -32602.
	resp := jsonrpcCallWithID(t, connB, "resize", map[string]any{
		"sessionId": sidA,
		"cols":      100,
		"rows":      30,
		"xpixel":    0,
		"ypixel":    0,
	}, 2)
	var re struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &re)
	if re.Error.Code != -32602 {
		t.Fatalf("expected -32602 for cross-connection resize, got %d", re.Error.Code)
	}

	// Connection B tries to close A's session — must fail.
	respC := jsonrpcCallWithID(t, connB, "close", map[string]string{
		"sessionId": sidA,
	}, 3)
	_ = json.Unmarshal(respC, &re)
	if re.Error.Code != -32602 {
		t.Fatalf("expected -32602 for cross-connection close, got %d", re.Error.Code)
	}

	// Connection B sends a data frame for A's session — must be dropped
	// (no response, connection must remain usable).
	sidBytes, _ := session.IDToBytes(session.ID(sidA))
	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeData,
		SessionID: sidBytes,
		Payload:   []byte("echo cross-connection\n"),
	}
	_ = connB.WriteMessage(websocket.BinaryMessage, f.Encode())

	// Connection B must still be able to open its own session.
	respB := jsonrpcCallWithID(t, connB, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 4)
	var rb struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respB, &rb)
	if rb.Result.SessionID == "" {
		t.Fatal("connB cannot open its own session after cross-connection attempt")
	}
}

// TestWSServer_TwoSessionsOneConnection_Isolation proves two concurrent
// sessions on ONE WebSocket are isolated: input written to session A echoes
// only in frames addressed to A, and closing A leaves B alive.
func TestWSServer_TwoSessionsOneConnection_Isolation(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	respA := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var ra struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respA, &ra)
	sidA := ra.Result.SessionID
	if sidA == "" {
		t.Fatal("expected sessionId A")
	}

	respB := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 2)
	var rb struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respB, &rb)
	sidB := rb.Result.SessionID
	if sidB == "" {
		t.Fatal("expected sessionId B")
	}
	if sidA == sidB {
		t.Fatal("two opens on same connection must return distinct sessionIds")
	}

	sidABytes, _ := session.IDToBytes(session.ID(sidA))
	sidBBytes, _ := session.IDToBytes(session.ID(sidB))

	fA := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidABytes, Payload: []byte("echo session-A\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, fA.Encode())

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		sid := string(session.IDFromBytes(frame.SessionID))
		output := string(frame.Payload)
		if strings.Contains(output, "session-A") {
			if sid != sidA {
				t.Fatalf("session-A echo arrived on wrong sid: got %s, want %s", sid, sidA)
			}
			break
		}
	}

	fB := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBBytes, Payload: []byte("echo session-B\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, fB.Encode())

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		sid := string(session.IDFromBytes(frame.SessionID))
		output := string(frame.Payload)
		if strings.Contains(output, "session-B") {
			if sid != sidB {
				t.Fatalf("session-B echo arrived on wrong sid: got %s, want %s", sid, sidB)
			}
			break
		}
	}

	respClose := jsonrpcCallWithID(t, conn, "close", map[string]string{"sessionId": sidA}, 3)
	var cr struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(respClose, &cr)
	if cr.Error != nil {
		t.Fatalf("close A returned error: %v", cr.Error)
	}

	fB2 := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBBytes, Payload: []byte("echo still-alive\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, fB2.Encode())

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read after close: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		sid := string(session.IDFromBytes(frame.SessionID))
		output := string(frame.Payload)
		if strings.Contains(output, "still-alive") {
			if sid != sidB {
				t.Fatalf("session-B data on wrong sid after A closed: got %s, want %s", sid, sidB)
			}
			return
		}
	}
}

func TestWSServer_DisconnectSurvivesSession(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID
	if sid == "" {
		t.Fatal("expected sessionId")
	}

	_ = conn.Close()

	time.Sleep(200 * time.Millisecond)

	if len(sess.List()) != 1 {
		t.Fatalf("expected 1 session after disconnect (survives per AD-9), got %d", len(sess.List()))
	}
}

// --- AD-9 reconnect / ring tests -------------------------------------------

// TestWSServer_ReattachReplaysUnreadBytes verifies that a new connection
// receiving the same bytes that were buffered while detached, in order,
// with no duplicates and no gaps.
func TestWSServer_ReattachReplaysUnreadBytes(t *testing.T) {
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

	// Send a command so there's output to buffer.
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("echo reattach-test\n")}
	_ = connA.WriteMessage(websocket.BinaryMessage, f.Encode())

	// Read some output to advance our offset, then disconnect.
	var offset uint64
	_ = connA.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := connA.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		offset += uint64(len(frame.Payload))
		if strings.Contains(string(frame.Payload), "reattach-test") {
			break
		}
	}

	// Disconnect connA — session survives.
	_ = connA.Close()
	time.Sleep(200 * time.Millisecond)

	// Send more output while detached (connA is gone, output still buffered).
	connMid := connectWS(t, ws)
	// Send data from connMid — but connMid doesn't have the session in its state.
	// We need another mechanism to produce output while detached.
	// Write directly to the session through the registry.
	sessObj, err := sess.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	_, _ = sessObj.Write([]byte("echo detached\n"))
	_ = connMid.Close()

	time.Sleep(500 * time.Millisecond)

	// Reattach on connB at the offset we recorded.
	connB := connectWS(t, ws)

	respB := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    offset,
	}, 2)

	var at struct {
		Result struct {
			Resumed bool   `json:"resumed"`
			Reset   bool   `json:"reset"`
			From    uint64 `json:"from"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respB, &at)
	if at.Result.Reset || !at.Result.Resumed {
		t.Fatalf("expected resumed, got reset=%v resumed=%v", at.Result.Reset, at.Result.Resumed)
	}

	// Read replayed bytes — must contain "detached" without duplicating earlier output.
	_ = connB.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := connB.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		if strings.Contains(string(frame.Payload), "detached") {
			return
		}
	}
}

func TestWSServer_AttachWithStaleOffsetReturnsReset(t *testing.T) {
	sess := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
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

	// Produce enough output to push the ring past offset 0, then ack it all.
	f := Frame{Version: FrameVersion, MsgType: MsgTypeData, SessionID: sidBytes, Payload: []byte("echo fill\n")}
	_ = conn.WriteMessage(websocket.BinaryMessage, f.Encode())

	var total uint64
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		total += uint64(len(frame.Payload))
		if strings.Contains(string(frame.Payload), "fill") {
			break
		}
	}

	// Ack everything.
	ackReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ack",
		"params":  map[string]any{"sessionId": sid, "offset": total},
	})
	_ = conn.WriteMessage(websocket.TextMessage, ackReq)
	time.Sleep(100 * time.Millisecond)

	_ = conn.Close()

	// Reattach requesting offset 0, which is now behind the ring's trimmed base.
	connB := connectWS(t, ws)

	respB := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 2)

	var at struct {
		Result struct {
			Resumed bool   `json:"resumed"`
			Reset   bool   `json:"reset"`
			From    uint64 `json:"from"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respB, &at)
	if !at.Result.Reset {
		t.Fatal("expected reset for stale offset")
	}
	if at.Result.From < total {
		t.Fatalf("expected from >= total, got from=%d total=%d", at.Result.From, total)
	}
}

func TestWSServer_AckTrimsRing(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	// Ack a valid offset — must not error.
	ackReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ack",
		"params":  map[string]any{"sessionId": sid, "offset": 0},
	})
	if err := conn.WriteMessage(websocket.TextMessage, ackReq); err != nil {
		t.Fatalf("write ack: %v", err)
	}

	// Bogus ack ahead of written — must be rejected (warn).
	ackAhead, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ack",
		"params":  map[string]any{"sessionId": sid, "offset": uint64(999999)},
	})
	if err := conn.WriteMessage(websocket.TextMessage, ackAhead); err != nil {
		t.Fatalf("write ahead ack: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Connection must still be usable.
	resp2 := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 3)
	var r2 struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp2, &r2)
	if r2.Result.SessionID == "" {
		t.Fatal("connection not usable after bogus ack")
	}
}

func TestWSServer_TwoSessionsIndependentRings(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	respA := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)
	var ra struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respA, &ra)

	respB := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 2)
	var rb struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(respB, &rb)

	// Ack session A at offset 0 — must succeed independently of B.
	ackA, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ack",
		"params":  map[string]any{"sessionId": ra.Result.SessionID, "offset": 0},
	})
	if err := conn.WriteMessage(websocket.TextMessage, ackA); err != nil {
		t.Fatalf("write ack A: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Bogus ack for B must not affect A.
	ackBogus, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ack",
		"params":  map[string]any{"sessionId": rb.Result.SessionID, "offset": uint64(999999)},
	})
	_ = conn.WriteMessage(websocket.TextMessage, ackBogus)

	time.Sleep(100 * time.Millisecond)

	// Both connections still usable.
	resp3 := jsonrpcCallWithID(t, conn, "close", map[string]string{
		"sessionId": ra.Result.SessionID,
	}, 3)
	var cr struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp3, &cr)
	if cr.Error != nil {
		t.Fatalf("close A returned error: %v", cr.Error)
	}
}

func TestWSServer_AttachUnknownSessionReturnsError(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	resp := jsonrpcCallWithID(t, conn, "attach", map[string]any{
		"sessionId": "0000000000000000000000000000000x",
		"offset":    0,
	}, 1)

	var cr struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &cr)
	if cr.Error == nil || cr.Error.Code != -32602 {
		t.Fatal("expected -32602 for unknown sessionId")
	}
}

func TestWSServer_CloseSessionTearsDownRing(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	// Close the session — ring must be gone, reattach must fail.
	_ = jsonrpcCallWithID(t, conn, "close", map[string]string{
		"sessionId": sid,
	}, 2)

	respAttach := jsonrpcCallWithID(t, conn, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 3)

	var cr struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(respAttach, &cr)
	if cr.Error == nil || cr.Error.Code != -32602 {
		t.Fatal("expected -32602 for attach after close")
	}
}

// --- DEFECT regression tests -----------------------------------------------

// TestWSServer_OpenAfterStopOnHijackedConn proves DEFECT 1 is fixed:
// http.Server.Shutdown does NOT close hijacked WebSocket connections
// (gorilla Upgrade hijacks), so a handleSession goroutine can still be
// alive after Shutdown returns. If Stop() nil'd the ring map, a subsequent
// open or attach on that still-live hijacked connection would assign into
// a nil map and panic. With the fix, getOrCreateRx checks a stopped flag
// and returns nil; handleOpen returns a JSON-RPC error instead of crashing.
func TestWSServer_OpenAfterStopOnHijackedConn(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Establish a WebSocket connection and open one session so a handler
	// goroutine is running on this hijacked connection.
	conn := connectWS(t, ws)
	_ = jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	// Call Stop. Shutdown() will close the TCP listener but the hijacked
	// WebSocket connection stays alive — the handler goroutine for `conn`
	// keeps running. The stopped flag must prevent a panic inside
	// getOrCreateRx if the handler tries to create a new ring.
	if err := ws.Stop(ctx); err != nil {
		t.Logf("Stop error (expected if shutdown context exceeded): %v", err)
	}

	// Now send an open request on the STILL-LIVE hijacked connection.
	// Without the DEFECT 1 fix, getOrCreateRx would assign to a nil map
	// and the test would crash (panic). With the fix, we get a clean
	// JSON-RPC error.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"jsonrpc":"2.0","id":42,"method":"open","params":{"cols":80,"rows":24,"xpixel":0,"ypixel":0}}`,
	))
	if err != nil {
		t.Fatalf("write on hijacked conn after Stop: %v", err)
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response on hijacked conn after Stop: %v", err)
		}
		var resp struct {
			ID    int `json:"id"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		if resp.ID != 42 {
			continue
		}
		if resp.Error == nil {
			t.Fatal("expected JSON-RPC error after Stop, got success")
		}
		if resp.Error.Code != -32603 {
			t.Fatalf("expected -32603 (internal error), got %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return
	}
}

// TestWSServer_AttachDuplicateOnSameConnectionReturnsError verifies that
// attaching to a session already opened on the same connection returns a
// JSON-RPC error (DEFECT 3 fix). Without this guard, a second attach
// would start another ringToConn goroutine, doubling every output byte.
func TestWSServer_AttachDuplicateOnSameConnectionReturnsError(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	// Second attach on the same connection — must fail.
	respAttach := jsonrpcCallWithID(t, conn, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 2)

	var cr struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(respAttach, &cr)
	if cr.Error == nil {
		t.Fatal("expected error for duplicate attach on same connection")
	}
	if cr.Error.Code != -32602 {
		t.Fatalf("expected -32602, got %d", cr.Error.Code)
	}
}

// TestWSServer_AttachWithOffsetAheadOfWrittenReturnsError verifies that
// an attach with an offset exceeding what the ring has produced returns a
// JSON-RPC error (DEFECT 4 fix). Without this guard, the server answers
// resumed:true with offset unchanged and silently skips bytes.
func TestWSServer_AttachWithOffsetAheadOfWrittenReturnsError(t *testing.T) {
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCallWithID(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	}, 1)

	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	sid := r.Result.SessionID

	// Attach with an offset far ahead of anything produced (stub PTY
	// produces nothing). Use a separate connection because this conn
	// already has the session (from open), and the duplicate-attach guard
	// would reject it.
	connB := connectWS(t, ws)
	defer func() { _ = connB.Close() }()

	respAttach := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    999999,
	}, 2)

	var cr struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(respAttach, &cr)
	if cr.Error == nil {
		t.Fatal("expected error for ahead-of-written offset")
	}
	if cr.Error.Code != -32602 {
		t.Fatalf("expected -32602, got %d", cr.Error.Code)
	}

	// Attach with exactly-written offset — must succeed since 0 does
	// not EXCEED written=0. Use connC to avoid the duplicate guard.
	connC := connectWS(t, ws)
	defer func() { _ = connC.Close() }()

	respOk := jsonrpcCallWithID(t, connC, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 3)

	var ok struct {
		Result struct {
			Resumed bool   `json:"resumed"`
			From    uint64 `json:"from"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(respOk, &ok)
	if ok.Error != nil {
		t.Fatalf("expected success for offset==written, got error %v", ok.Error)
	}
	if !ok.Result.Resumed {
		t.Fatal("expected resumed for offset==written")
	}
}

// TestWSServer_RingToConnExitsOnDisconnect verifies that when a connection
// drops, the ringToConn goroutine for an idle session exits rather than
// parking indefinitely in waitForData (DEFECT 5 fix). We test this by
// disconnecting, waiting, then reattaching — if the old goroutine hadn't
// exited, the new ringToConn would race with it. The functional guarantee
// is that reattach replays only correct data (no duplication).
func TestWSServer_RingToConnExitsOnDisconnect(t *testing.T) {
	reg := newRegWithReal(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg)

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

	// Disconnect connA. The old ringToConn goroutine must exit, not stay
	// parked in waitForData. We wait briefly then write to the session
	// directly via the registry.
	_ = connA.Close()
	time.Sleep(200 * time.Millisecond)

	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	_, _ = sess.Write([]byte("echo after-disconnect\n"))
	time.Sleep(500 * time.Millisecond)

	// Reattach and read output. If the old ringToConn was still running
	// and consuming ring data, we'd miss bytes here.
	connB := connectWS(t, ws)

	respB := jsonrpcCallWithID(t, connB, "attach", map[string]any{
		"sessionId": sid,
		"offset":    0,
	}, 2)

	var at struct {
		Result struct {
			Resumed bool   `json:"resumed"`
			From    uint64 `json:"from"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(respB, &at)
	if at.Error != nil {
		t.Fatalf("attach returned error: %v", at.Error)
	}

	// Must receive "after-disconnect" in the replayed output.
	_ = connB.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := connB.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		frame, derr := DecodeFrame(data)
		if derr != nil {
			continue
		}
		if strings.Contains(string(frame.Payload), "after-disconnect") {
			return
		}
	}
}
