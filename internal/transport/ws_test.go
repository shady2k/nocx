package transport

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
)

func TestWSServer_StartStop(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil))
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

func TestWSServer_ConnectAndExchange(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil))
	ctx := context.Background()

	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := ws.Stop(ctx); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()

	u := url.URL{Scheme: "ws", Host: ws.addr(), Path: "/session"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	err = conn.WriteMessage(websocket.BinaryMessage, []byte("echo ping\n"))
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if len(msg) == 0 {
		t.Fatal("expected non-empty response")
	}
}

func (s *WSServer) addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}
