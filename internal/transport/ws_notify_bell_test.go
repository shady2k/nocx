package transport

// notify.bell conformance and failure paths (nocx-n3nfg).
//
// BEL is renderer-observed and renderer-reported, and it is a SECOND method
// rather than an argument on notify.raise. That is the whole reason this
// file exists as a sibling of ws_notify_test.go instead of another table row
// in it: kind is stamped "from the method invoked" (design §2), and ingress
// authority is closed — "no renderer-callable method can produce an attested
// event" (§3). A kind parameter on notify.raise would make the caller the
// one choosing, which is the forging §3 rejects one level up. The method
// name IS the choice, and these tests are what say so: the caller cannot
// send a kind, a trust, a title or a body, and the event that comes out
// carries KindBell and TrustProgramRequest regardless.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// notifyBellCallRaw writes a notify.bell request whose params are the
// verbatim bytes rawParams and returns the response. The frame is built by
// string concatenation, never re-marshaled, so the validated frame is
// byte-for-byte what went over the socket — and a params value the encoder
// would refuse still reaches the server exactly as written.
//
// The READ is the shared inbox primitive (awaitFrame/isResponseTo) rather
// than a second hand-rolled skip-the-notifications loop: a frame that is not
// this response is retained for whoever wants it, which is the behaviour
// ws_inbox_test.go owns.
func notifyBellCallRaw(t *testing.T, conn *websocket.Conn, id int, rawParams string) json.RawMessage {
	t.Helper()
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"notify.bell","params":%s}`, id, rawParams)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(req)); err != nil {
		t.Fatalf("write notify.bell: %v", err)
	}
	resp, err := awaitFrame(conn, time.Now().Add(wantWithin), isResponseTo(id))
	if err != nil {
		t.Fatalf("read response to notify.bell (id %d): %v", id, err)
	}
	return resp
}

// assertBellError sends a notify.bell request whose params are the verbatim
// bytes rawParams and asserts the response carries exactly wantCode and that
// no event reached the raiser. The second half is the one that matters: a
// refusal that still raised would be a refusal in the response only.
func assertBellError(t *testing.T, conn *websocket.Conn, raiser *fakeNotifyRaiser, rawParams string, wantCode int) {
	t.Helper()
	resp := notifyBellCallRaw(t, conn, 42, rawParams)
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error == nil {
		t.Fatalf("notify.bell succeeded (result %s), want error %d", envelope.Result, wantCode)
	}
	if envelope.Error.Code != wantCode {
		t.Errorf("code = %d (%s), want %d", envelope.Error.Code, envelope.Error.Message, wantCode)
	}
	if got := len(raiser.captured()); got != 0 {
		t.Errorf("raiser captured %d events, want 0", got)
	}
}

// TestNotifyBell_OverTheWireConformsToContract drives the real method
// through the real socket and validates the frame the client actually sent:
// the params bytes validated below are the bytes that went over the wire,
// written verbatim, not a struct the test marshaled (contracts/README.md —
// this is the row the directory exists for). The server's decode of that
// same frame must agree with the schema, which is the Go side of
// additionalProperties: false.
func TestNotifyBell_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.bell.schema.json")
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)

	sid := openSession(t, conn)

	rawParams := []byte(`{"sessionId":"` + sid + `"}`)
	resp := notifyBellCallRaw(t, conn, 2, string(rawParams))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("notify.bell: %+v", envelope.Error)
	}

	validateJSON(t, schema, rawParams, "notify.bell params (real socket, client frame)")

	got, err := decodeNotifyBellParams(rawParams)
	if err != nil {
		t.Fatalf("server decode of the on-wire frame: %v", err)
	}
	if got.SessionID != sid {
		t.Errorf("server decode of the on-wire frame = %+v, want sessionId %q", got, sid)
	}

	if n := len(raiser.captured()); n != 1 {
		t.Fatalf("raiser captured %d events, want 1", n)
	}
}

// TestNotifyBell_NoRaiser_MethodUnavailable pins the -32601 shape: without
// WithNotifyRaiser there is no pipeline to raise into, and the method says
// so before any decode or session lookup.
func TestNotifyBell_NoRaiser_MethodUnavailable(t *testing.T) {
	_, _, conn := newNotifyRaiserWS(t, nil)
	assertBellError(t, conn, &fakeNotifyRaiser{},
		`{"sessionId":"0123456789abcdef0123456789abcdef"}`, -32601)
}

// TestNotifyBell_UnknownSession_Refused is the liveness refusal, both
// shapes: an id the registry never knew, and one that is live on a DIFFERENT
// connection. The second is the interesting one, and the handler checks
// state.has(sid) for exactly it — one WebSocket multiplexes many
// server-assigned sessions (AD-1), so a bell must be refused rather than
// attributed across connections.
func TestNotifyBell_UnknownSession_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, ws, conn := newNotifyRaiserWS(t, raiser)
	_ = openSession(t, conn)

	t.Run("never known to the registry", func(t *testing.T) {
		assertBellError(t, conn, raiser, `{"sessionId":"0123456789abcdef0123456789abcdef"}`, -32602)
	})

	t.Run("live on a different connection", func(t *testing.T) {
		other := connectWS(t, ws)
		t.Cleanup(func() { _ = other.Close() })
		otherSid := openSession(t, other)
		assertBellError(t, conn, raiser, fmt.Sprintf(`{"sessionId":%q}`, otherSid), -32602)
	})
}

// TestNotifyBell_ProtectedField_Refused walks the fields a caller might try
// to smuggle. For notify.bell that list is LONGER than notify.raise's,
// because BEL carries no text either: title and body are stamped by the
// backend here, so they are protected fields too and a frame naming one is
// refused exactly like a frame naming trust. The session is live on the
// connection — the ONLY defect in each frame is the smuggled field.
func TestNotifyBell_ProtectedField_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	fields := map[string]string{
		"kind":        `"kind":"block.finished"`,
		"trust":       `"trust":"attested"`,
		"level":       `"level":"danger"`,
		"title":       `"title":"Ваш банк: подтвердите вход"`,
		"body":        `"body":"tap here"`,
		"attribution": `"attribution":{"tab":"1","host":"h","session":"s"}`,
		"at":          `"at":"2026-08-23T00:00:00Z"`,
	}
	for name, extra := range fields {
		t.Run(name, func(t *testing.T) {
			assertBellError(t, conn, raiser, fmt.Sprintf(`{"sessionId":%q,%s}`, sid, extra), -32602)
		})
	}
}

// TestNotifyBell_MissingSessionID_Refused pins the required half of the
// contract: sessionId is addressing and there is nothing else in the record,
// so an empty params object addresses nothing and must be refused rather
// than raised against whatever the connection happens to hold.
func TestNotifyBell_MissingSessionID_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	_ = openSession(t, conn)

	assertBellError(t, conn, raiser, `{}`, -32602)
	assertBellError(t, conn, raiser, `{"sessionId":""}`, -32602)
}

// TestNotifyBell_StampsBellProvenance is the assertion the whole method
// exists for: invoking notify.bell — and nothing else about the call — is
// what produces KindBell, and it produces TrustProgramRequest with it. The
// caller sent one field, a session id, so kind and trust cannot have come
// from the request; and attribution is non-empty, which proves the registry
// was its source, since the request cannot carry an attribution field at all
// (refused above).
func TestNotifyBell_StampsBellProvenance(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	reg, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	resp := notifyBellCallRaw(t, conn, 1, fmt.Sprintf(`{"sessionId":%q}`, sid))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("notify.bell: %+v", envelope.Error)
	}

	evs := raiser.captured()
	if len(evs) != 1 {
		t.Fatalf("raiser captured %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Kind != notify.KindBell {
		t.Errorf("kind = %q, want %q", ev.Kind, notify.KindBell)
	}
	if ev.Trust != notify.TrustProgramRequest {
		t.Errorf("trust = %q, want %q", ev.Trust, notify.TrustProgramRequest)
	}
	if ev.Level != notify.LevelInfo {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelInfo)
	}
	if ev.SessionID != sid {
		t.Errorf("sessionId = %q, want the addressed session %q", ev.SessionID, sid)
	}
	// A row with an empty title renders as an empty row in the notifications
	// panel (notifications-panel.tsx reads `title` and shows `body` only when
	// it is non-empty), so the title is stamped here — by the backend, from
	// the registry entry, never by the caller. The body stays empty: there is
	// nothing a BEL says, and a sentence nocx invented would only restate the
	// title.
	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry Get(%q): %v", sid, err)
	}
	if want := bellTitle(sess.Host()); ev.Title != want {
		t.Errorf("title = %q, want %q", ev.Title, want)
	}
	if ev.Body != "" {
		t.Errorf("body = %q, want empty: BEL carries no text", ev.Body)
	}
	if ev.Attribution.Session != string(sess.ID()) {
		t.Errorf("attribution.session = %q, want the registry entry %q", ev.Attribution.Session, sess.ID())
	}
	if ev.Attribution.Host != sess.Host() {
		t.Errorf("attribution.host = %q, want the registry entry %q", ev.Attribution.Host, sess.Host())
	}
	if ev.Attribution.Tab == "" {
		t.Error("attribution.tab is empty, want the connection's backend-assigned tab id")
	}
	// The backend this session runs on, stamped with the same value every
	// other source uses (nocx-2gfh6). The renderer's occurrence→tab lookup
	// COMPARES it, so an empty one renders the row unactivatable.
	if ev.Attribution.Backend != commandnames.LocalRoute {
		t.Errorf("attribution.backend = %q, want %q", ev.Attribution.Backend, commandnames.LocalRoute)
	}
}

// TestNotifyBell_RaiserFailure_InternalError is AGENTS.md rule 3 for the one
// external call this handler makes: the pipeline refuses. The request WAS
// forwarded (the failure is the pipeline's, not a dropped call) and the
// refusal reaches the renderer as -32603 rather than a silent success — a
// bell that was never recorded must not answer "done".
func TestNotifyBell_RaiserFailure_InternalError(t *testing.T) {
	raiser := &fakeNotifyRaiser{out: notify.Outcome{Err: errors.New("feed closed")}}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	resp := notifyBellCallRaw(t, conn, 1, fmt.Sprintf(`{"sessionId":%q}`, sid))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error == nil {
		t.Fatalf("notify.bell succeeded, want -32603")
	}
	if envelope.Error.Code != -32603 {
		t.Errorf("code = %d (%s), want -32603", envelope.Error.Code, envelope.Error.Message)
	}
	if got := len(raiser.captured()); got != 1 {
		t.Errorf("raiser captured %d events, want exactly 1 (the request was forwarded)", got)
	}
}

// TestNotifyBell_TrailingJSON_HandlerSeam pins the handler's own trailing
// check at the layer that owns it. The transport's whole-frame decode
// refuses such a frame as -32700 before the handler ever runs (proven for
// notify.raise in ws_notify_test.go, and the frame path is shared), so this
// exercises the seam directly: the decode refuses trailing input with the
// SAME errTrailingJSON notify.raise uses — one owner for the rule — and a
// request carrying such params is answered -32602 with nothing raised.
func TestNotifyBell_TrailingJSON_HandlerSeam(t *testing.T) {
	raw := []byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}{"x":1}`)
	if _, err := decodeNotifyBellParams(raw); err != errTrailingJSON {
		t.Fatalf("decodeNotifyBellParams error = %v, want errTrailingJSON", err)
	}
	raiser := &fakeNotifyRaiser{}
	responder := &fakeResponder{}
	h := notifyBellHandlers{raiser: raiser, state: &connState{}, r: responder}
	h.handleNotifyBell(context.Background(), jsonrpcRequest{
		ID:     json.RawMessage(`1`),
		Params: raw,
	})
	if responder.err == nil {
		t.Fatal("handler answered nothing, want -32602")
	}
	if responder.err.Code != -32602 {
		t.Errorf("code = %d (%s), want -32602", responder.err.Code, responder.err.Message)
	}
	if got := len(raiser.captured()); got != 0 {
		t.Errorf("raiser captured %d events, want 0", got)
	}
}
