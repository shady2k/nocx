package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

func TestTransportPing_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "transport.ping.schema.json")
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck

	raw := jsonrpcCall(t, conn, "transport.ping", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode transport.ping response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("transport.ping: %+v", envelope.Error)
	}
	if len(envelope.Result) == 0 {
		t.Fatal("transport.ping returned no result")
	}
	validateJSON(t, schema, envelope.Result, "transport.ping result")
}

func TestHeartbeat_ReadDeadlineClosesSilentPeer(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	// Set directly rather than through an option: an exported option nothing in
	// production calls is dead weight the ratchet is right to reject, and this
	// test lives in the same package.
	ws.heartbeatReadWindow = 100 * time.Millisecond
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("silent peer remained connected past the server read deadline")
	}
}

func TestHeartbeat_PingWithinWindowKeepsConnectionAlive(t *testing.T) {
	const (
		window    = 100 * time.Millisecond
		pingEvery = 20 * time.Millisecond
		pings     = 16
	)
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ws.heartbeatReadWindow = window
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	defer conn.Close() //nolint:errcheck
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	for id := 1; id <= pings; id++ {
		<-ticker.C
		raw := jsonrpcCallWithID(t, conn, "transport.ping", map[string]any{}, id)
		var envelope struct {
			Result json.RawMessage  `json:"result"`
			Error  *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode ping %d response: %v", id, err)
		}
		if envelope.Error != nil {
			t.Fatalf("ping %d: %+v", id, envelope.Error)
		}
		if len(envelope.Result) == 0 {
			t.Fatalf("ping %d returned no result", id)
		}
	}
}
