package transport

// The screen a subscriber is owed when it is installed (nocx-zg3k3.2.15).
//
// Frames the runtime published while a session had no subscriber were dropped
// here by design (screen.go), so installing one — the open's, after its ack,
// or an attach's — must ask for the frame the runtime holds now. Before this,
// the only thing that did was the resize an attach with a size happens to
// cause; an attach with no size, or an open whose shell had already drawn
// before the subscriber existed, waited for a program to print something.
//
// The resender here stands in for the helper's route: asked for a session, it
// publishes that session's latest frame through the same seam the real drain
// publishes through, exactly as the runtime's Resend reaches the transport.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
)

// latestFrameResender answers a resend by publishing the one frame it holds —
// "what the runtime had drawn" — for the session it was asked about.
type latestFrameResender struct {
	mu    sync.Mutex
	ws    *WSServer
	doc   []byte
	asked []string
	// published records what PublishScreenFrame ANSWERED for each session it
	// was asked to resend. It is diagnosis, not assertion (nocx-atpw5.1.24):
	// a wait that times out must say whether the frame was never asked for,
	// published and refused a subscriber, or published and delivered but not
	// retained — three different findings that one 30 s deadline otherwise
	// makes indistinguishable.
	published map[string]bool
}

func (r *latestFrameResender) ResendScreen(_ context.Context, sessionID string) error {
	r.mu.Lock()
	ws, doc := r.ws, r.doc
	r.asked = append(r.asked, sessionID)
	r.mu.Unlock()
	ok := ws.PublishScreenFrame(session.ID(sessionID), 7, doc)
	r.mu.Lock()
	if r.published == nil {
		r.published = make(map[string]bool, 1)
	}
	r.published[sessionID] = ok
	r.mu.Unlock()
	return nil
}

// describe is what a timed-out wait prints beside the deadline: the sessions
// the runtime was asked to resend, and what publishing each one answered.
func (r *latestFrameResender) describe() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.asked) == 0 {
		return "the runtime was never asked to resend a screen"
	}
	return fmt.Sprintf("resends asked for %v, publish answered %v", r.asked, r.published)
}

func newScreenBaselineServer(t *testing.T) (*WSServer, *latestFrameResender) {
	t.Helper()
	ctx, logger := logtest.New(t)
	resender := &latestFrameResender{doc: []byte(`{"revision":7,"marker":"DRAWN-BEFORE-ANY-SUBSCRIBER"}`)}
	ws := NewWSServer(logger, newRegWithStub(logger), WithScreenResender(resender))
	resender.mu.Lock()
	resender.ws = ws
	resender.mu.Unlock()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })
	return ws, resender
}

func isScreenFrame(msg []byte) bool {
	f, err := DecodeFrame(msg)
	return err == nil && f.MsgType == MsgTypeMetadata
}

func awaitScreenFrame(t *testing.T, conn *websocket.Conn, resender *latestFrameResender) Frame {
	t.Helper()
	msg, err := awaitFrame(conn, time.Now().Add(wantWithin), isScreenFrame)
	if err != nil {
		t.Fatalf("no screen frame reached the subscriber: %v; %s", err, resender.describe())
	}
	f, err := DecodeFrame(msg)
	if err != nil {
		t.Fatalf("decode screen frame: %v", err)
	}
	return f
}

func TestAnOpenedSessionsFirstScreenFrameIsTheOneTheRuntimeHolds(t *testing.T) {
	ws, resender := newScreenBaselineServer(t)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	sid := openSessionOnConn(t, ws, conn, 1)

	f := awaitScreenFrame(t, conn, resender)
	if string(f.Payload) != string(resender.doc) {
		t.Fatalf("the open's first screen frame is %q, want the frame the runtime held before the subscriber existed", f.Payload)
	}
	wantSID, _ := session.IDToBytes(session.ID(sid))
	if f.SessionID != wantSID {
		t.Fatalf("the screen frame names session %x, want %x", f.SessionID, wantSID)
	}
}

func TestAnAttachWithNoSizeReceivesTheScreenTheRuntimeHolds(t *testing.T) {
	ws, resender := newScreenBaselineServer(t)
	connA := connectWS(t, ws)
	defer func() { _ = connA.Close() }()
	sid := openSessionOnConn(t, ws, connA, 1)
	_ = awaitScreenFrame(t, connA, resender)

	connB := connectWS(t, ws)
	defer func() { _ = connB.Close() }()
	resp := jsonrpcCallWithID(t, connB, "attach", map[string]any{"sessionId": sid, "offset": 0}, 2)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal attach: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("attach: %+v", envelope.Error)
	}

	f := awaitScreenFrame(t, connB, resender)
	if string(f.Payload) != string(resender.doc) {
		t.Fatalf("the attacher's first screen frame is %q, want the frame the runtime holds", f.Payload)
	}
	resender.mu.Lock()
	asked := append([]string(nil), resender.asked...)
	resender.mu.Unlock()
	if len(asked) != 2 || asked[0] != sid || asked[1] != sid {
		t.Fatalf("the runtime was asked for %v, want one resend for the open and one for the attach of %s", asked, sid)
	}
}
