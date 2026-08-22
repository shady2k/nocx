package transport

// api.request.cancel, asked of the seam a person reaches: a token the caller
// minted, a send in flight under it, and a Stop that ends THAT one.
//
// NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state —
// the fake sender reporting it has been asked, a response arriving with a
// particular id — and the sender itself blocks on a channel plus its own
// context rather than on a sleep, so a cancelled send returns because it was
// cancelled and never because time passed.
//
// ONE PROPERTY IS NOT TESTED HERE, AND IT IS SAID RATHER THAN LEFT TO BE
// NOTICED. handleSend registers the token BEFORE it asks for the api gate,
// so a Stop pressed while the snapshot is still queued finds the run — and
// there is no observable, from this side of the socket, between "the frame
// was accepted" and "the sender was called". Every test below therefore
// waits for the sender, which is the later of the two, and none of them
// would go red if the registration moved back after the gate. The reason
// the registration is early is in handleSend's own comment.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// pending starts a send and does NOT read its response, so the exchange is
// genuinely outstanding while the test does something else on the socket.
func pending(t *testing.T, conn *websocket.Conn, handle, token string, id int) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "api.request.send",
		"params": map[string]any{"handle": handle, "relPath": "ping.json", "token": token},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write send: %v", err)
	}
}

// inbox reads responses off one socket and hands them back BY ID. A plain
// read-until-mine loop would discard whatever else was in flight, and these
// tests deliberately have two things in flight at once.
type inbox struct {
	conn *websocket.Conn
	held map[int]*vaultRPCResult
}

func newInbox(conn *websocket.Conn) *inbox {
	return &inbox{conn: conn, held: map[int]*vaultRPCResult{}}
}

func (b *inbox) await(t *testing.T, id int) *vaultRPCResult {
	t.Helper()
	if msg, ok := b.held[id]; ok {
		delete(b.held, id)
		return msg
	}
	for {
		_ = b.conn.SetReadDeadline(time.Now().Add(wantWithin))
		_, data, err := b.conn.ReadMessage()
		if err != nil {
			t.Fatalf("read while waiting for id %d: %v", id, err)
		}
		var msg vaultRPCResult
		if json.Unmarshal(data, &msg) != nil || msg.ID == 0 {
			continue
		}
		if msg.ID == id {
			return &msg
		}
		b.held[msg.ID] = &msg
	}
}

// call writes a request and waits for its own answer, through the inbox, so
// it cannot swallow a response another part of the test is waiting for.
func (b *inbox) call(t *testing.T, method string, params map[string]any, id int) *vaultRPCResult {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := b.conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	return b.await(t, id)
}

func decodeSend(t *testing.T, msg *vaultRPCResult) apiSendResponse {
	t.Helper()
	if msg.Error != nil {
		t.Fatalf("api.request.send: %+v", msg.Error)
	}
	var got apiSendResponse
	if err := json.Unmarshal(msg.Result, &got); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	return got
}

// A CANCELLED EXCHANGE COMES BACK AS A RUN, and as `stopped` rather than as
// a failure — which is the difference a surface tones from. Before this
// there was nothing to stop at all: no party named a running exchange.
func TestAPIRequestCancel_StopsTheExchangeAndItComesBackStopped(t *testing.T) {
	sender := &recordingSender{block: make(chan struct{})}
	defer close(sender.block)
	conn := newAPIWSServerWithSender(t, sender)
	box := newInbox(conn)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollectionVia(t, box, root, 1)

	pending(t, conn, handle, "run-1", 2)
	waitFor(t, "the send to reach the sender", wantWithin, func() bool { return sender.count() == 1 })

	if resp := box.call(t, "api.request.cancel", map[string]any{"token": "run-1"}, 3); resp.Error != nil {
		t.Fatalf("api.request.cancel: %+v", resp.Error)
	}

	got := decodeSend(t, box.await(t, 2))
	if got.Outcome != "stopped" {
		t.Errorf("outcome = %q, want stopped", got.Outcome)
	}
	if got.Response != nil {
		t.Errorf("a stopped exchange carries a response: %+v", *got.Response)
	}
	if got.Failure == nil || got.Failure.Phase != "stopped" {
		t.Errorf("failure = %+v, want phase stopped", got.Failure)
	}
	// And it is still a RUN: what was being sent when it was stopped.
	if got.Request.Text == "" {
		t.Error("a stopped exchange carries no request text")
	}
}

// THE INTERVAL'S FAR END. Once the exchange has settled the token resolves
// to nothing, so a second Stop is refused rather than silently accepted —
// and it changes nothing about the run that already came back.
func TestAPIRequestCancel_ASecondCancelAfterItSettledChangesNothing(t *testing.T) {
	sender := &recordingSender{}
	conn := newAPIWSServerWithSender(t, sender)
	box := newInbox(conn)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollectionVia(t, box, root, 1)

	first := decodeSend(t, box.call(t, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json", "token": "run-1"}, 2))
	if first.Outcome != "answered" {
		t.Fatalf("outcome = %q, want answered", first.Outcome)
	}

	resp := box.call(t, "api.request.cancel", map[string]any{"token": "run-1"}, 3)
	if resp.Error == nil {
		t.Fatal("cancelling a settled exchange was accepted; the token stops resolving when it settles")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", resp.Error.Code)
	}
	// The socket is still usable and the run is still what it was: a
	// refused Stop is not a failure of anything else.
	again := decodeSend(t, box.call(t, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json", "token": "run-2"}, 4))
	if again.Outcome != "answered" {
		t.Errorf("outcome = %q on the next send, want answered", again.Outcome)
	}
}

// TWO EXCHANGES IN FLIGHT CANCEL INDEPENDENTLY. A registry keyed by
// anything coarser than the token would end both, which is exactly what a
// person with two requests open must never get from one Stop.
func TestAPIRequestCancel_TwoInFlightExchangesCancelIndependently(t *testing.T) {
	sender := &recordingSender{block: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(sender.block)
		}
	}()
	conn := newAPIWSServerWithSender(t, sender)
	box := newInbox(conn)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollectionVia(t, box, root, 1)

	pending(t, conn, handle, "run-a", 2)
	pending(t, conn, handle, "run-b", 3)
	waitFor(t, "both sends to reach the sender", wantWithin, func() bool { return sender.count() == 2 })

	if resp := box.call(t, "api.request.cancel", map[string]any{"token": "run-a"}, 4); resp.Error != nil {
		t.Fatalf("api.request.cancel run-a: %+v", resp.Error)
	}

	if got := decodeSend(t, box.await(t, 2)); got.Outcome != "stopped" {
		t.Errorf("run-a outcome = %q, want stopped", got.Outcome)
	}
	// run-b is still running: releasing the sender is what ends it, and it
	// ends ANSWERED. If the Stop had reached it, this would be stopped.
	released = true
	close(sender.block)
	if got := decodeSend(t, box.await(t, 3)); got.Outcome != "answered" {
		t.Errorf("run-b outcome = %q, want answered — one Stop ended the other run too", got.Outcome)
	}
}

// A TOKEN IS ONE WINDOW'S NAME. Two windows may choose the same one, and one
// window's Stop must not end the other's exchange — so the registry is keyed
// by the connection as well as by the token.
func TestAPIRequestCancel_OneWindowsTokenDoesNotStopAnothersRun(t *testing.T) {
	sender := &recordingSender{block: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(sender.block)
		}
	}()
	ws, conn := newAPIServerAndConn(t, sender)
	box := newInbox(conn)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollectionVia(t, box, root, 1)

	pending(t, conn, handle, "same-name", 2)
	waitFor(t, "the send to reach the sender", wantWithin, func() bool { return sender.count() == 1 })

	// A second window, cancelling the same NAME.
	other := newInbox(connectWS(t, ws))
	resp := other.call(t, "api.request.cancel", map[string]any{"token": "same-name"}, 9)
	if resp.Error == nil {
		t.Fatal("one window stopped another window's run by naming the same token")
	}

	released = true
	close(sender.block)
	if got := decodeSend(t, box.await(t, 2)); got.Outcome != "answered" {
		t.Errorf("outcome = %q, want answered — the other window's Stop reached this run", got.Outcome)
	}
}

// A token already naming a running exchange refuses the SEND. Two exchanges
// under one name would leave Stop guessing which it meant, and the refusal
// is the caller's to fix — it minted the duplicate.
func TestAPIRequestSend_ATokenAlreadyInFlightIsRefused(t *testing.T) {
	sender := &recordingSender{block: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(sender.block)
		}
	}()
	conn := newAPIWSServerWithSender(t, sender)
	box := newInbox(conn)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollectionVia(t, box, root, 1)

	pending(t, conn, handle, "run-1", 2)
	waitFor(t, "the send to reach the sender", wantWithin, func() bool { return sender.count() == 1 })

	resp := box.call(t, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json", "token": "run-1"}, 3)
	if resp.Error == nil {
		t.Fatal("a second send under a token already in flight was accepted")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", resp.Error.Code)
	}
	if sender.count() != 1 {
		t.Errorf("the sender was asked %d times; the refused send must not have gone out", sender.count())
	}

	// AND THE NAME COMES BACK. Once the first exchange settles the token is
	// free again, which is the far end of the interval stated as behaviour
	// rather than as a comment.
	released = true
	close(sender.block)
	box.await(t, 2)
	if again := box.call(t, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json", "token": "run-1"}, 4); again.Error != nil {
		t.Fatalf("the token was still held after its exchange settled: %+v", again.Error)
	}
}

// A send with no token at all is refused: a run that cannot be named is a
// run with no Stop, and "usually stoppable" is not a capability a surface
// can draw a button for.
func TestAPIRequestSend_RefusesASendThatNamesNoToken(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})
	box := newInbox(conn)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollectionVia(t, box, root, 1)

	resp := box.call(t, "api.request.send", map[string]any{"handle": handle, "relPath": "ping.json"}, 2)
	if resp.Error == nil {
		t.Fatal("a send with no token was accepted")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", resp.Error.Code)
	}
}

// openAPICollectionVia is openAPICollection through an inbox, so opening a
// folder cannot swallow a send response the test has outstanding.
func openAPICollectionVia(t *testing.T, box *inbox, root string, id int) string {
	t.Helper()
	resp := box.call(t, "api.collections.open", map[string]any{"path": root}, id)
	if resp.Error != nil {
		t.Fatalf("api.collections.open: %+v", resp.Error)
	}
	var got apiOpenResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal open: %v", err)
	}
	if got.Handle == "" {
		t.Fatal("api.collections.open answered an empty handle")
	}
	return got.Handle
}
