package transport

// A second probe waits; it is not refused (nocx-bxafj).
//
// endpoints.probe is the Test button beside an AI endpoint. It was admitted
// through a capacity-one INSTANT-REFUSAL gate on the argument that a probe
// "competes for a scarce worker", and the refusal is where two things went
// wrong.
//
// The narrow one is a window. The handler enqueues its response inside the
// task and the permit is returned only after the task's goroutine has
// returned; between those two moments the client already has its answer and
// the slot still reads as taken. A sequential client that answers and asks
// again lands in it and is told -32004 control-saturated with retryAfterMs 0
// — the server admitting the refusal means nothing. It opens only under load,
// which is why it was seen once in CI and never reproduced on demand.
//
// The wide one is a person. The renderer disables the Test button of the row
// whose probe is in flight (endpoints-section.tsx keys `rowProbing` by
// endpoint id), so pressing the SAME button twice cannot reach the server at
// all. Pressing a DIFFERENT endpoint's Test can, because that button is
// enabled — and the gate is one per server. Test on A, then Test on B, is a
// refusal with nothing wrong.
//
// So the gate moves to the waiting class, which is the answer this repository
// already gave for the identical defect on the native file picker
// (ws.go's buildControlPlane, ADR-0026 item 4): a probe is a SERIALISATION
// POINT — one at a time, the second may proceed once the first is done — and
// not a scarce worker. Only exhausting the wait bound is a refusal now.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/assistant"
)

func TestEndpointsProbe_ASecondProbeWaitsRatherThanBeingRefused(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var started int
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			started++
			if started == 1 {
				close(inFlight)
				<-release
			}
			return assistant.ProbeResult{
				EndpointName: p.Name, Model: p.Model, Kind: probeKindFor(p),
				OK: true, ElapsedMS: 1, At: time.Now(),
			}, nil
		},
	})

	// Two connections, because the two presses are two different endpoints'
	// Test buttons and the renderer would send them independently.
	second := connectWS(t, h.ws)
	t.Cleanup(func() { _ = second.Close() })

	params := func(name string) map[string]any {
		return map[string]any{"name": name, "baseUrl": "http://127.0.0.1:1/v1", "key": "sk", "model": "m"}
	}

	firstDone := make(chan json.RawMessage, 1)
	go func() { firstDone <- jsonrpcCall(t, h.conn, "endpoints.probe", params("A")) }()

	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the first probe never reached the client")
	}

	// The second press, while the first is genuinely in flight. It must not
	// come back as a saturation refusal — it must wait for the gate.
	secondDone := make(chan json.RawMessage, 1)
	go func() { secondDone <- jsonrpcCall(t, second, "endpoints.probe", params("B")) }()

	select {
	case raw := <-secondDone:
		t.Fatalf("the second probe was answered while the first still held the gate: %s", raw)
	case <-time.After(200 * time.Millisecond):
		// Waiting, which is the point.
	}

	close(release)
	for _, ch := range []chan json.RawMessage{firstDone, secondDone} {
		select {
		case raw := <-ch:
			if code := errorCodeOf(t, raw); code != 0 {
				t.Fatalf("probe answered with error code %d: %s", code, raw)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a probe never completed after the gate freed")
		}
	}
}

// errorCodeOf returns the JSON-RPC error code of a response, or 0 when it
// carries a result.
func errorCodeOf(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Error == nil {
		return 0
	}
	return env.Error.Code
}

var _ = websocket.TextMessage
