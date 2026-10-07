package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
)

func TestPublishSessionEffect_ReachesSubscriberAsIdentityBearingNotification(t *testing.T) {
	ws, sid, _, _, sock := newScreenPublishFixture(t)
	effect := proto.EffectFrame{EffectID: 7, Generation: 4, Kind: proto.EffectNotification, Title: []byte("Tests failed"), Body: []byte("2 failed")}
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
	if params.SessionID != string(sid) || params.Generation != "4" || params.EffectID != "7" || params.Kind != "notification" || params.Title != "Tests failed" || params.Body != "2 failed" {
		t.Fatalf("params = %+v", params)
	}
}

func TestPromptBoundaryIsQueuedBetweenBytesBeforeAndAfterItsOffset(t *testing.T) {
	ws, sid, sidBytes, wconn, sock := newScreenPublishFixture(t)
	rx := ws.getRx(sid)
	before := []byte("prompt-prefix\x1b]133;B\x07")
	after := []byte("live-suffix")
	if err := rx.ring.write(before); err != nil {
		t.Fatalf("write bytes through OSC 133 B: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		ws.ringToConn(ctx, wconn, sidBytes, rx, 0)
	}()
	// The pump is already active and has queued the complete prefix through B
	// before the independent effect arrives. The suffix is made available only
	// after that effect has been accepted into the same outbound queue.
	prefixFrame := awaitCapturedFrames(t, sock, 1)[0]
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 9, Kind: proto.EffectPromptBoundary,
		StreamOffset: uint64(len(before)),
	}) {
		t.Fatal("prompt boundary was not accepted")
	}
	if err := rx.ring.write(after); err != nil {
		t.Fatalf("write live suffix: %v", err)
	}
	frames := awaitCapturedFrames(t, sock, 3)
	if string(prefixFrame.Data) != string(frames[0].Data) {
		t.Fatal("first queued prefix frame changed while collecting the complete order")
	}
	cancel()
	<-pumpDone

	if frames[0].MsgType != websocket.BinaryMessage || frames[1].MsgType != websocket.TextMessage || frames[2].MsgType != websocket.BinaryMessage {
		t.Fatalf("outbound frame types = [%d %d %d], want data/effect/data", frames[0].MsgType, frames[1].MsgType, frames[2].MsgType)
	}
	beforeFrame, err := DecodeFrame(frames[0].Data)
	if err != nil {
		t.Fatalf("decode prefix frame: %v", err)
	}
	afterFrame, err := DecodeFrame(frames[2].Data)
	if err != nil {
		t.Fatalf("decode suffix frame: %v", err)
	}
	if string(beforeFrame.Payload) != string(before) || string(afterFrame.Payload) != string(after) {
		t.Fatalf("data around prompt boundary = %q / %q, want %q / %q", beforeFrame.Payload, afterFrame.Payload, before, after)
	}
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(frames[1].Data, &msg); err != nil || msg.Method != "session.effect" {
		t.Fatalf("middle frame = %s, decode err=%v, want session.effect", frames[1].Data, err)
	}
}

func TestPromptBoundaryPendingForClosedSubscriberIsDiscarded(t *testing.T) {
	ws, sid, sidBytes, wconn, sock := newScreenPublishFixture(t)
	rx := ws.getRx(sid)
	if err := rx.ring.write([]byte("prefix and suffix")); err != nil {
		t.Fatalf("write session ring: %v", err)
	}
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 9, Kind: proto.EffectPromptBoundary, StreamOffset: 6,
	}) {
		t.Fatal("prompt boundary was not accepted")
	}
	if !rx.clearSubscriber(wconn) {
		t.Fatal("subscriber did not clear")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		ws.ringToConn(ctx, wconn, sidBytes, rx, 0)
	}()
	<-pumpDone
	sock.mu.Lock()
	defer sock.mu.Unlock()
	if len(sock.frames) != 0 {
		t.Fatalf("closed subscriber received %d frames, want none", len(sock.frames))
	}
}

func TestPromptBoundaryHasReservedSlotWhenQueueIsFullOfOrdinaryEffects(t *testing.T) {
	ws, sid, _, wconn, _ := newScreenPublishFixture(t)
	rx := ws.getRx(sid)
	params := json.RawMessage(`{}`)
	for i := 0; i < maxPendingSessionEffects-1; i++ {
		accepted, coalesced := rx.queueEffect(wconn, 0, false, "test.effect", params)
		if !accepted || coalesced {
			t.Fatalf("ordinary effect %d: accepted=%v coalesced=%v", i, accepted, coalesced)
		}
	}
	accepted, coalesced := rx.queueEffect(wconn, 100, true, "session.effect", params)
	if !accepted || coalesced {
		t.Fatalf("reserved prompt boundary slot: accepted=%v coalesced=%v", accepted, coalesced)
	}
	if accepted, _ := rx.queueEffect(wconn, 0, false, "test.effect", params); accepted {
		t.Fatal("ordinary effect consumed the boundary's reserved slot")
	}
	rx.deliveryMu.Lock()
	defer rx.deliveryMu.Unlock()
	if len(rx.pendingEffects) != maxPendingSessionEffects || !rx.pendingEffects[len(rx.pendingEffects)-1].waitForBytes {
		t.Fatalf("pending queue = %d entries, last fence=%v; want full bounded queue ending in reserved prompt boundary", len(rx.pendingEffects), rx.pendingEffects[len(rx.pendingEffects)-1].waitForBytes)
	}
}

func TestPromptBoundaryCoalescingPreservesOrdinaryOrderAndHoldsSuffix(t *testing.T) {
	ws, sid, sidBytes, wconn, sock := newScreenPublishFixture(t)
	rx := ws.getRx(sid)
	firstThroughB := []byte("prompt-one\x1b]133;B\x07")
	middleThroughB2 := []byte("between-prompts\x1b]133;B\x07")
	prefix := append(append([]byte(nil), firstThroughB...), middleThroughB2...)
	suffix := []byte("live-suffix-after-second-B")
	if err := rx.ring.write(append(append([]byte(nil), prefix...), suffix...)); err != nil {
		t.Fatalf("write output including suffix: %v", err)
	}
	ordinary := func(order int) json.RawMessage {
		raw, err := json.Marshal(struct {
			Order int `json:"order"`
		}{Order: order})
		if err != nil {
			t.Fatalf("marshal ordinary effect order: %v", err)
		}
		return raw
	}
	for i := 0; i < maxPendingSessionEffects/2; i++ {
		if accepted, _ := rx.queueEffect(wconn, 0, false, "test.effect", ordinary(i)); !accepted {
			t.Fatalf("ordinary effect %d before old boundary was refused", i)
		}
	}
	oldOffset := uint64(len(firstThroughB))
	if accepted, coalesced := rx.queueEffect(wconn, oldOffset, true, "session.effect", json.RawMessage(`{"kind":"promptBoundary","effectId":"old"}`)); !accepted || coalesced {
		t.Fatalf("old boundary: accepted=%v coalesced=%v", accepted, coalesced)
	}
	for i := maxPendingSessionEffects / 2; i < maxPendingSessionEffects-1; i++ {
		if accepted, _ := rx.queueEffect(wconn, 0, false, "test.effect", ordinary(i)); !accepted {
			t.Fatalf("ordinary effect %d after old boundary was refused", i)
		}
	}
	if len(rx.pendingEffects) != maxPendingSessionEffects {
		t.Fatalf("mixed queue length = %d, want full bound %d", len(rx.pendingEffects), maxPendingSessionEffects)
	}
	latestParams, err := json.Marshal(sessionEffectParams{
		SessionID: string(sid), Generation: "2", EffectID: "new", Kind: "promptBoundary",
	})
	if err != nil {
		t.Fatalf("marshal latest boundary: %v", err)
	}
	accepted, coalesced := rx.queueEffect(wconn, uint64(len(prefix)), true, "session.effect", latestParams)
	if !accepted || !coalesced {
		t.Fatalf("newer boundary: accepted=%v coalesced=%v, want oldest-boundary replacement", accepted, coalesced)
	}
	rx.deliveryMu.Lock()
	if len(rx.pendingEffects) != maxPendingSessionEffects {
		rx.deliveryMu.Unlock()
		t.Fatalf("coalesced queue length = %d, want bound %d", len(rx.pendingEffects), maxPendingSessionEffects)
	}
	gotOrders := make([]int, 0, maxPendingSessionEffects-1)
	for _, pending := range rx.pendingEffects {
		if pending.waitForBytes {
			if pending.offset != uint64(len(prefix)) {
				rx.deliveryMu.Unlock()
				t.Fatalf("retained boundary offset = %d, want %d", pending.offset, len(prefix))
			}
			continue
		}
		var params struct {
			Order int `json:"order"`
		}
		if decodeErr := json.Unmarshal(pending.params, &params); decodeErr != nil {
			rx.deliveryMu.Unlock()
			t.Fatalf("decode ordinary effect params: %v", decodeErr)
		}
		gotOrders = append(gotOrders, params.Order)
	}
	rx.deliveryMu.Unlock()
	if len(gotOrders) != maxPendingSessionEffects-1 {
		t.Fatalf("ordinary effects retained = %d, want %d", len(gotOrders), maxPendingSessionEffects-1)
	}
	for i, order := range gotOrders {
		if order != i {
			t.Fatalf("ordinary effect order[%d] = %d, want %d", i, order, i)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		ws.ringToConn(ctx, wconn, sidBytes, rx, 0)
	}()
	frames := awaitCapturedFrames(t, sock, maxPendingSessionEffects+2)
	cancel()
	<-pumpDone
	for i := 0; i < maxPendingSessionEffects-1; i++ {
		if frames[i].MsgType != websocket.TextMessage {
			t.Fatalf("ordinary effect %d frame type = %d, want text", i, frames[i].MsgType)
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Order int `json:"order"`
			} `json:"params"`
		}
		if decodeErr := json.Unmarshal(frames[i].Data, &msg); decodeErr != nil || msg.Method != "test.effect" || msg.Params.Order != i {
			t.Fatalf("ordinary frame %d = %s, err=%v", i, frames[i].Data, decodeErr)
		}
	}
	prefixFrame, err := DecodeFrame(frames[maxPendingSessionEffects-1].Data)
	if err != nil || frames[maxPendingSessionEffects-1].MsgType != websocket.BinaryMessage || string(prefixFrame.Payload) != string(prefix) {
		t.Fatalf("queued prefix frame = %+v, decode err=%v", prefixFrame, err)
	}
	var boundary struct {
		Method string              `json:"method"`
		Params sessionEffectParams `json:"params"`
	}
	if frames[maxPendingSessionEffects].MsgType != websocket.TextMessage || json.Unmarshal(frames[maxPendingSessionEffects].Data, &boundary) != nil || boundary.Method != "session.effect" || boundary.Params.Kind != "promptBoundary" || boundary.Params.EffectID != "new" {
		t.Fatalf("queued fence frame = %s, want latest promptBoundary", frames[maxPendingSessionEffects].Data)
	}
	suffixFrame, err := DecodeFrame(frames[maxPendingSessionEffects+1].Data)
	if err != nil || frames[maxPendingSessionEffects+1].MsgType != websocket.BinaryMessage || string(suffixFrame.Payload) != string(suffix) {
		t.Fatalf("suffix escaped before the queued fence: %+v, decode err=%v", suffixFrame, err)
	}
}

func TestSessionEffectDTOConformsToContract(t *testing.T) {
	raw, err := json.Marshal(sessionEffectParams{SessionID: "0123456789abcdef0123456789abcdef", Generation: "4", EffectID: "7", Kind: "notification", Title: "title", Body: "text"})
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
	effect := proto.EffectFrame{Generation: 4, EffectID: 7, Kind: proto.EffectNotification, Title: []byte("Deploy"), Body: []byte("staging ready")}
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
	if params.SessionID != sid || params.Generation != "4" || params.EffectID != "7" || params.Kind != "notification" || params.Title != "Deploy" || params.Body != "staging ready" {
		t.Fatalf("wire params = %+v", params)
	}
}

func TestPromptBoundaryDTOAndRealSocketHaveNoPayload(t *testing.T) {
	ws, sid, _, _, sock := newScreenPublishFixture(t)
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{Generation: 2, EffectID: 9, Kind: proto.EffectPromptBoundary}) {
		t.Fatal("prompt boundary was not published")
	}
	frames := awaitCapturedFrames(t, sock, 1)
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(frames[0].Data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Method != "session.effect" {
		t.Fatalf("method = %q", msg.Method)
	}
	validateJSON(t, loadSchema(t, "session.effect.schema.json"), msg.Params, "promptBoundary DTO params")
	var params sessionEffectParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Kind != "promptBoundary" || params.Title != "" || params.Body != "" || params.Generation != "2" || params.EffectID != "9" {
		t.Fatalf("params = %+v", params)
	}
}

func TestPromptBoundary_OverTheWireConformsToContract(t *testing.T) {
	ws, _ := newScreenBaselineServer(t)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	sid := openSessionOnConn(t, ws, conn, 1)
	if !ws.PublishSessionEffect(session.ID(sid), proto.EffectFrame{Generation: 2, EffectID: 9, Kind: proto.EffectPromptBoundary}) {
		t.Fatal("event not accepted")
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
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "session.effect.schema.json"), msg.Params, "promptBoundary over-the-wire params")
	var params sessionEffectParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Kind != "promptBoundary" || params.Title != "" || params.Body != "" || params.SessionID != sid || params.Generation != "2" || params.EffectID != "9" {
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

func TestRecoverySessionEffectDTOIsNonceFree(t *testing.T) {
	schema := loadSchema(t, "session.effect.schema.json")
	params := sessionEffectParams{SessionID: "0123456789abcdef0123456789abcdef", Generation: "1", EffectID: "2", Kind: "recovery", EpisodeID: "rec-0123456789abcdef0123456789abcdef"}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, raw, "recovery session.effect DTO")
	for _, field := range []string{`"fence":"` + strings.Repeat("a", 64) + `"`, `"generation":"` + strings.Repeat("b", 64) + `"`} {
		bad := strings.TrimSuffix(string(raw), "}") + "," + field + "}"
		if err := validateJSONErr(schema, []byte(bad)); err == nil {
			t.Fatalf("schema accepted raw nonce field %s", field[:strings.IndexByte(field, ':')])
		}
	}
}
