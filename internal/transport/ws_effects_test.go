package transport

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
)

func TestPublishSessionEffect_ReachesSubscriberAsIdentityBearingNotification(t *testing.T) {
	ws, sid, _, _, sock := newScreenPublishFixture(t)
	effect := proto.EffectFrame{EffectID: 7, Generation: 4, Kind: proto.EffectClipboard, Body: []byte("payload")}
	if !ws.PublishSessionEffect(sid, effect) {
		t.Fatal("effect was not published")
	}
	frames := awaitCapturedFrames(t, sock, 1)
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(frames[0].Data, &msg); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if msg.Method != "session.effect" {
		t.Fatalf("method = %q", msg.Method)
	}
	var params sessionEffectParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.SessionID != string(sid) || params.Generation != "4" || params.EffectID != "7" || params.Kind != "clipboard" || params.Body != "payload" {
		t.Fatalf("params = %+v", params)
	}
}

func TestSessionEffectDTOConformsToContract(t *testing.T) {
	raw, err := json.Marshal(sessionEffectParams{SessionID: "0123456789abcdef0123456789abcdef", Generation: "4", EffectID: "7", Kind: "clipboard", Body: "text"})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "session.effect.schema.json"), raw, "session.effect DTO params")
}

func TestSessionEffect_OverTheWireConformsToContract(t *testing.T) {
	ws, _ := newScreenBaselineServer(t)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	sid := openSessionOnConn(t, ws, conn, 1)
	effect := proto.EffectFrame{Generation: 4, EffectID: 7, Kind: proto.EffectClipboard, Body: []byte("once")}
	if !ws.PublishSessionEffect(session.ID(sid), effect) {
		t.Fatal("effect not accepted by attached session")
	}
	raw, err := awaitFrame(conn, time.Now().Add(wantWithin), func(raw []byte) bool {
		var msg struct {
			Method string `json:"method"`
		}
		return json.Unmarshal(raw, &msg) == nil && msg.Method == "session.effect"
	})
	if err != nil {
		t.Fatalf("session.effect not received: %v", err)
	}
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Method != "session.effect" {
		t.Fatalf("method = %q", msg.Method)
	}
	validateJSON(t, loadSchema(t, "session.effect.schema.json"), msg.Params, "session.effect over-the-wire params")
	var params sessionEffectParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.SessionID != sid || params.Generation != "4" || params.EffectID != "7" || params.Kind != "clipboard" || params.Body != "once" {
		t.Fatalf("wire params = %+v", params)
	}
}

func TestPublishSessionEffect_RefusesUnknownKind(t *testing.T) {
	ws, sid, _, _, sock := newScreenPublishFixture(t)
	if ws.PublishSessionEffect(sid, proto.EffectFrame{Generation: 1, EffectID: 1, Kind: proto.EffectKind(255)}) {
		t.Fatal("unknown effect kind was accepted")
	}
	sock.mu.Lock()
	defer sock.mu.Unlock()
	if len(sock.frames) != 0 {
		t.Fatalf("unknown effect kind reached socket in %d frames", len(sock.frames))
	}
}
