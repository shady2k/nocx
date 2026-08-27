package transport

// notify.paneWorkFinished conformance and failure paths (nocx-n3nfg).
//
// This is the pipeline's only HEURISTIC source, and that is what these tests
// are about. Design §3.1 confines heuristic to local attention and forbids
// it push; the router enforces the confinement, but the confinement is only
// worth anything if the trust class cannot be chosen by the caller. So the
// assertions here are the same two the bell's are — the caller cannot send a
// kind, a trust, a title or a body, and the event that comes out carries
// KindPaneWorkFinished and TrustHeuristic regardless — and the second one
// matters more here than anywhere else: an event that arrived claiming
// `attested` would be an inference with a push route.
//
// It is a sibling file rather than more rows in ws_notify_test.go for the
// reason ws_notify_bell_test.go is one: kind is stamped from the method
// invoked, so a source is a method, and a method's behaviour is tested where
// the method lives.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// paneWorkFinishedCallRaw writes a notify.paneWorkFinished request whose
// params are the verbatim bytes rawParams and returns the response. The
// frame is built by string concatenation, never re-marshaled, so the
// validated frame is byte-for-byte what went over the socket — and a params
// value the encoder would refuse still reaches the server exactly as
// written.
func paneWorkFinishedCallRaw(t *testing.T, conn *websocket.Conn, id int, rawParams string) json.RawMessage {
	t.Helper()
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"notify.paneWorkFinished","params":%s}`, id, rawParams)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(req)); err != nil {
		t.Fatalf("write notify.paneWorkFinished: %v", err)
	}
	resp, err := awaitFrame(conn, time.Now().Add(wantWithin), isResponseTo(id))
	if err != nil {
		t.Fatalf("read response to notify.paneWorkFinished (id %d): %v", id, err)
	}
	return resp
}

// assertPaneWorkFinishedError sends a request whose params are the verbatim
// bytes rawParams and asserts the response carries exactly wantCode and that
// no event reached the raiser. The second half is the one that matters: a
// refusal that still raised would be a refusal in the response only.
func assertPaneWorkFinishedError(t *testing.T, conn *websocket.Conn, raiser *fakeNotifyRaiser, rawParams string, wantCode int) {
	t.Helper()
	resp := paneWorkFinishedCallRaw(t, conn, 42, rawParams)
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error == nil {
		t.Fatalf("notify.paneWorkFinished succeeded (result %s), want error %d", envelope.Result, wantCode)
	}
	if envelope.Error.Code != wantCode {
		t.Errorf("code = %d (%s), want %d", envelope.Error.Code, envelope.Error.Message, wantCode)
	}
	if got := len(raiser.captured()); got != 0 {
		t.Errorf("raiser captured %d events, want 0", got)
	}
}

// TestNotifyPaneWorkFinished_OverTheWireConformsToContract drives the real
// method through the real socket and validates the frame the client actually
// sent: the params bytes validated below are the bytes that went over the
// wire, written verbatim, not a struct the test marshaled (contracts/
// README.md — this is the row the directory exists for). The server's decode
// of that same frame must agree with the schema, which is the Go side of
// additionalProperties: false.
func TestNotifyPaneWorkFinished_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.paneWorkFinished.schema.json")
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)

	sid := openSession(t, conn)

	rawParams := []byte(`{"sessionId":"` + sid + `"}`)
	resp := paneWorkFinishedCallRaw(t, conn, 2, string(rawParams))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("notify.paneWorkFinished: %+v", envelope.Error)
	}

	validateJSON(t, schema, rawParams, "notify.paneWorkFinished params (real socket, client frame)")

	got, err := decodeNotifyPaneWorkFinishedParams(rawParams)
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

// TestNotifyPaneWorkFinished_NoRaiser_MethodUnavailable pins the -32601
// shape: without WithNotifyRaiser there is no pipeline to raise into, and
// the method says so before any decode or session lookup.
func TestNotifyPaneWorkFinished_NoRaiser_MethodUnavailable(t *testing.T) {
	_, _, conn := newNotifyRaiserWS(t, nil)
	assertPaneWorkFinishedError(t, conn, &fakeNotifyRaiser{},
		`{"sessionId":"0123456789abcdef0123456789abcdef"}`, -32601)
}

// TestNotifyPaneWorkFinished_UnknownSession_Refused is the liveness refusal,
// all three shapes it has to cover here. Two are bell's: an id the registry
// never knew, and one live on a DIFFERENT connection — one WebSocket
// multiplexes many server-assigned sessions (AD-1), so a settle must be
// refused rather than attributed across connections.
//
// The third is this method's own, and it is the reason the check is load-
// bearing rather than defensive. The renderer arms a five-second timer when
// a pane goes idle; the session that pane is on can be replaced inside those
// five seconds (a reattach mints a new id — AD-7). The renderer declines to
// fire in that case, but "declines to" is a promise, and this is what makes
// it structural: a frame naming the dead id is refused, and nothing is
// recorded against a session that no longer exists.
func TestNotifyPaneWorkFinished_UnknownSession_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, ws, conn := newNotifyRaiserWS(t, raiser)
	_ = openSession(t, conn)

	t.Run("never known to the registry", func(t *testing.T) {
		assertPaneWorkFinishedError(t, conn, raiser, `{"sessionId":"0123456789abcdef0123456789abcdef"}`, -32602)
	})

	t.Run("live on a different connection", func(t *testing.T) {
		other := connectWS(t, ws)
		t.Cleanup(func() { _ = other.Close() })
		otherSid := openSession(t, other)
		assertPaneWorkFinishedError(t, conn, raiser, fmt.Sprintf(`{"sessionId":%q}`, otherSid), -32602)
	})
}

// TestNotifyPaneWorkFinished_ProtectedField_Refused walks the fields a
// caller might try to smuggle. The list is bell's, and title is on it for a
// sharper reason: this source HAS text within reach — the pane title the
// inference was drawn from — and that title is a string a program wrote. A
// frame naming it would be a program supplying the words of a notification
// whose kind it did not have to earn. The session is live on the connection,
// so the ONLY defect in each frame is the smuggled field.
func TestNotifyPaneWorkFinished_ProtectedField_Refused(t *testing.T) {
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
			assertPaneWorkFinishedError(t, conn, raiser, fmt.Sprintf(`{"sessionId":%q,%s}`, sid, extra), -32602)
		})
	}
}

// TestNotifyPaneWorkFinished_MissingSessionID_Refused pins the required half
// of the contract: sessionId is addressing and there is nothing else in the
// record, so an empty params object addresses nothing and must be refused
// rather than raised against whatever the connection happens to hold.
func TestNotifyPaneWorkFinished_MissingSessionID_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	_ = openSession(t, conn)

	assertPaneWorkFinishedError(t, conn, raiser, `{}`, -32602)
	assertPaneWorkFinishedError(t, conn, raiser, `{"sessionId":""}`, -32602)
}

// TestNotifyPaneWorkFinished_StampsHeuristicProvenance is the assertion the
// whole method exists for, and the one that keeps an inference off a phone:
// invoking notify.paneWorkFinished — and nothing else about the call — is
// what produces KindPaneWorkFinished, and it produces TrustHeuristic with
// it. The caller sent one field, a session id, so kind and trust cannot have
// come from the request; and attribution is non-empty, which proves the
// registry was its source, since the request cannot carry an attribution
// field at all (refused above).
func TestNotifyPaneWorkFinished_StampsHeuristicProvenance(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	reg, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	resp := paneWorkFinishedCallRaw(t, conn, 1, fmt.Sprintf(`{"sessionId":%q}`, sid))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("notify.paneWorkFinished: %+v", envelope.Error)
	}

	evs := raiser.captured()
	if len(evs) != 1 {
		t.Fatalf("raiser captured %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Kind != notify.KindPaneWorkFinished {
		t.Errorf("kind = %q, want %q", ev.Kind, notify.KindPaneWorkFinished)
	}
	if ev.Trust != notify.TrustHeuristic {
		t.Errorf("trust = %q, want %q", ev.Trust, notify.TrustHeuristic)
	}
	if ev.Level != notify.LevelInfo {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelInfo)
	}
	if ev.SessionID != sid {
		t.Errorf("sessionId = %q, want the addressed session %q", ev.SessionID, sid)
	}
	// The words are the rule (design §3.4 rule 3): the row hedges, and it
	// never says "the agent finished". Stamped here, by the backend, from
	// the registry entry — never by the caller, whose only field is an id.
	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry Get(%q): %v", sid, err)
	}
	if want := paneWorkFinishedTitle(sess.Host()); ev.Title != want {
		t.Errorf("title = %q, want %q", ev.Title, want)
	}
	if ev.Body != "" {
		t.Errorf("body = %q, want empty: the hedge is in the title", ev.Body)
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
	if ev.Attribution.Backend != commandnames.LocalRoute {
		t.Errorf("attribution.backend = %q, want %q", ev.Attribution.Backend, commandnames.LocalRoute)
	}
}

// TestNotifyPaneWorkFinished_TitleHedges pins the wording rule directly,
// because it is a rule and not a preference: design §3.4 rule 3 says the
// label is "work in the pane seems to have finished" and NEVER "the agent
// finished", since BRAILLE_SPINNER matches `npm install` under ora and
// `docker pull` as readily as it matches an agent. A test that only compared
// the title to the function that produced it (as the provenance test above
// does, correctly, for the stamping question) would go on passing if
// somebody rewrote the function to say "Claude finished".
func TestNotifyPaneWorkFinished_TitleHedges(t *testing.T) {
	for _, host := range []string{"", "build-01"} {
		got := paneWorkFinishedTitle(host)
		if !strings.Contains(got, "seems") {
			t.Errorf("title for host %q = %q, want the hedge design §3.4 rule 3 requires", host, got)
		}
		for _, forbidden := range []string{"agent", "Agent", "Claude"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("title for host %q = %q, must not claim %q: the classifier cannot tell one from `docker pull`", host, got, forbidden)
			}
		}
	}
	if got := paneWorkFinishedTitle("build-01"); !strings.Contains(got, "build-01") {
		t.Errorf("title = %q, want the host: a feed of several panes is unreadable without it", got)
	}
}

// TestNotifyPaneWorkFinished_RaiserFailure_InternalError is AGENTS.md rule 3
// for the one external call this handler makes: the pipeline refuses. The
// request WAS forwarded (the failure is the pipeline's, not a dropped call)
// and the refusal reaches the renderer as -32603 rather than a silent
// success.
func TestNotifyPaneWorkFinished_RaiserFailure_InternalError(t *testing.T) {
	raiser := &fakeNotifyRaiser{out: notify.Outcome{Err: errors.New("feed closed")}}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	resp := paneWorkFinishedCallRaw(t, conn, 1, fmt.Sprintf(`{"sessionId":%q}`, sid))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error == nil {
		t.Fatalf("notify.paneWorkFinished succeeded, want -32603")
	}
	if envelope.Error.Code != -32603 {
		t.Errorf("code = %d (%s), want -32603", envelope.Error.Code, envelope.Error.Message)
	}
	if got := len(raiser.captured()); got != 1 {
		t.Errorf("raiser captured %d events, want exactly 1 (the request was forwarded)", got)
	}
}

// TestNotifyPaneWorkFinished_TrailingJSON_HandlerSeam pins the handler's own
// trailing check at the layer that owns it. The transport's whole-frame
// decode refuses such a frame as -32700 before the handler ever runs, so
// this exercises the seam directly: the decode refuses trailing input with
// the SAME errTrailingJSON notify.raise uses — one owner for the rule — and
// a request carrying such params is answered -32602 with nothing raised.
func TestNotifyPaneWorkFinished_TrailingJSON_HandlerSeam(t *testing.T) {
	raw := []byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}{"x":1}`)
	if _, err := decodeNotifyPaneWorkFinishedParams(raw); err != errTrailingJSON {
		t.Fatalf("decodeNotifyPaneWorkFinishedParams error = %v, want errTrailingJSON", err)
	}
	raiser := &fakeNotifyRaiser{}
	responder := &fakeResponder{}
	h := notifyPaneWorkFinishedHandlers{raiser: raiser, state: &connState{}, r: responder}
	h.handleNotifyPaneWorkFinished(context.Background(), jsonrpcRequest{
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
