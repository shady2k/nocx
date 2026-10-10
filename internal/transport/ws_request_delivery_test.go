package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/transport/outbound"
)

type gatedWebSocket struct {
	conn    *websocket.Conn
	started chan struct{}
	release <-chan struct{}
}

func (s *gatedWebSocket) ReadMessage() (int, []byte, error)  { return s.conn.ReadMessage() }
func (s *gatedWebSocket) SetWriteDeadline(t time.Time) error { return s.conn.SetWriteDeadline(t) }
func (s *gatedWebSocket) Close() error                       { return s.conn.Close() }
func (s *gatedWebSocket) WriteMessage(kind int, data []byte) error {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-s.release
	return s.conn.WriteMessage(kind, data)
}

func TestRendererDeliverRequestSurvivesSaturatedDataQueue(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade test peer: %v", err)
			return
		}
		accepted <- conn
	}))
	defer server.Close()

	peer, _, dialErr := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if dialErr != nil {
		t.Fatalf("dial test peer: %v", dialErr)
	}
	defer func() {
		if closeErr := peer.Close(); closeErr != nil {
			t.Errorf("close test peer: %v", closeErr)
		}
	}()
	serverConn := <-accepted
	defer func() {
		if closeErr := serverConn.Close(); closeErr != nil {
			t.Errorf("close server WebSocket: %v", closeErr)
		}
	}()

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSocket := func() { releaseOnce.Do(func() { close(release) }) }
	sock := &gatedWebSocket{conn: serverConn, started: make(chan struct{}), release: release}
	out := outbound.New(sock, outbound.Config{QueueDepth: 1})
	t.Cleanup(out.Close)
	// Release the socket before closing outbound so every fatal assertion can
	// unwind the pump rather than leaving its write blocked.
	t.Cleanup(releaseSocket)
	wc := &wsConn{out: out}

	if err := out.TryEnqueue(outbound.TextMessage, []byte("first")); err != nil {
		t.Fatalf("enqueue frame held by pump: %v", err)
	}
	select {
	case <-sock.started:
	case <-time.After(5 * time.Second):
		t.Fatal("outbound pump did not enter the held WebSocket write")
	}
	if err := out.TryEnqueue(outbound.TextMessage, []byte("data queue full")); err != nil {
		t.Fatalf("fill refreshable data queue: %v", err)
	}

	request := json.RawMessage(`{"requestId":"req-1","sessionId":"worker-pane"}`)
	if err := (&WSServer{}).rendererDeliver(wc, "agent.runRequest", request); err != nil {
		t.Fatalf("deliver broker request while data queue is full: %v", err)
	}

	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set peer read deadline: %v", err)
	}
	releaseSocket()
	// The first frame was already held by the pump. The critical request
	// must be the very next frame, ahead of refreshable queued data.
	if _, frame, err := peer.ReadMessage(); err != nil {
		t.Fatalf("read held frame from WebSocket peer: %v", err)
	} else if string(frame) != "first" {
		t.Fatalf("held frame = %q, want first", frame)
	}
	_, frame, err := peer.ReadMessage()
	if err != nil {
		t.Fatalf("read critical request after held frame: %v", err)
	}
	var message struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(frame, &message); err != nil {
		t.Fatalf("critical frame is not JSON-RPC: %s: %v", frame, err)
	}
	if message.Method != "agent.runRequest" {
		t.Fatalf("frame after held write = %q, want agent.runRequest before refreshable data", message.Method)
	}
	if message.JSONRPC != "2.0" || string(message.Params) != string(request) {
		t.Fatalf("request notification = %+v, want unchanged JSON-RPC request params %s", message, request)
	}
	// The refreshable frame remains deliverable after the critical request.
	if _, frame, err := peer.ReadMessage(); err != nil {
		t.Fatalf("read refreshable frame after request: %v", err)
	} else if string(frame) != "data queue full" {
		t.Fatalf("frame after critical request = %q, want queued refreshable data", frame)
	}
}

func TestCriticalRequestCapacityExhaustionFailsBrokerRequest(t *testing.T) {
	sock := &blockingSocket{released: make(chan struct{}), writeStarted: make(chan struct{}, 1)}
	var releaseOnce sync.Once
	releaseSocket := func() { releaseOnce.Do(func() { close(sock.released) }) }
	out := outbound.New(sock, outbound.Config{QueueDepth: 1})
	t.Cleanup(out.Close)
	// This cleanup runs before out.Close and also covers fatal assertions
	// while the pump is held in WriteMessage.
	t.Cleanup(releaseSocket)
	wc := &wsConn{out: out}

	if err := out.TryEnqueue(outbound.TextMessage, []byte("held")); err != nil {
		t.Fatalf("enqueue frame held by pump: %v", err)
	}
	select {
	case <-sock.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("outbound pump did not enter the held write")
	}
	for i := 0; i < outbound.DefaultCriticalQueueDepth; i++ {
		if err := out.TryEnqueueCriticalFrame(outbound.TextMessage, []byte("critical")); err != nil {
			t.Fatalf("fill critical queue at frame %d: %v", i+1, err)
		}
	}

	server := &WSServer{}
	broker := NewBroker(func() []Conn { return []Conn{wc} }, server.rendererDeliver)
	kind := RequestKind{
		NotifyMethod:  "agent.runRequest",
		ResolveMethod: "agent.runResolved",
		NoClientErr:   errors.New("no renderer"),
		Resolve:       func(raw json.RawMessage) (json.RawMessage, error) { return raw, nil },
	}
	var result map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := broker.Request(ctx, kind, map[string]string{"command": "echo ready"}, &result)
	if !errors.Is(err, ErrRequestUndelivered) {
		t.Fatalf("broker request error = %v, want explicit undelivered result", err)
	}
	if !out.Stalled() {
		t.Fatal("critical capacity exhaustion did not mark the connection stalled")
	}
	releaseSocket()
	select {
	case <-out.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("closed outbound pump did not exit after the held write was released")
	}
}
