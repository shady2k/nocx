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
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
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

func TestWSServer_DisconnectClosesSessions(t *testing.T) {
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
	if r.Result.SessionID == "" {
		t.Fatal("expected sessionId")
	}

	_ = conn.Close()

	time.Sleep(200 * time.Millisecond)

	if len(sess.List()) != 0 {
		t.Fatalf("expected 0 sessions after disconnect, got %d", len(sess.List()))
	}
}
