package transport

// notify.raise conformance and failure paths (nocx-9zmc).
//
// The wire half of ADR-0029's provenance rule: the record carries sessionId,
// title and body and NOTHING else, and kind, trust, level, attribution and
// at are stamped by the handler from the method invoked and the session
// registry, never read from the record. The DTO test pins the struct's wire
// shape; the over-the-wire test drives the real method through the real
// socket and validates the frame the client actually sent; the failure tests
// pin the JSON-RPC code and that a refused request never becomes an event;
// the stamping test proves a renderer call can never mint an attested event.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// fakeNotifyRaiser is the transport-side double for NotifyRaiser: it
// captures every event handed to it and answers with a fixed outcome.
type fakeNotifyRaiser struct {
	mu     sync.Mutex
	events []notify.Event
	out    notify.Outcome
}

func (f *fakeNotifyRaiser) Raise(_ context.Context, ev notify.Event) notify.Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return f.out
}

func (f *fakeNotifyRaiser) captured() []notify.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notify.Event, len(f.events))
	copy(out, f.events)
	return out
}

// fakeResponder records the last response a handler tried to send, so a
// handler can be exercised without a socket. Used where the transport's
// frame parse would refuse the request before the handler ever runs.
type fakeResponder struct {
	mu     sync.Mutex
	err    *RPCError
	result json.RawMessage
}

func (f *fakeResponder) TryResult(_ json.RawMessage, result json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = append(f.result[:0], result...)
	return nil
}

func (f *fakeResponder) TryError(_ json.RawMessage, rpcErr RPCError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := rpcErr
	f.err = &e
	return nil
}

func (f *fakeResponder) TryNotify(string, json.RawMessage) error { return nil }

// newNotifyRaiserWS builds a started WSServer over a stub-backed registry,
// with the raiser wired (nil leaves WithNotifyRaiser unwired — the -32601
// shape) and one connected client. The registry and server handles are
// returned so a test can prove attribution against the registry entry
// itself and dial sibling connections for the cross-connection case.
func newNotifyRaiserWS(t *testing.T, r NotifyRaiser) (*session.Reg, *WSServer, *websocket.Conn) {
	t.Helper()
	reg := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), reg, WithNotifyRaiser(r))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return reg, ws, conn
}

// openSession opens a local session over the wire and returns its id. The
// session is live on `conn` (handleOpen registers it with the connection's
// state) and in the server's registry, so notify.raise can address it.
func openSession(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	resp := jsonrpcCall(t, conn, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	})
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal open response: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("open: %+v", r.Error)
	}
	if r.Result.SessionID == "" {
		t.Fatal("open returned an empty sessionId")
	}
	return r.Result.SessionID
}

// notifyRaiseResp is the response envelope of notify.raise: a result of {}
// on success, a JSON-RPC error otherwise.
type notifyRaiseResp struct {
	Result json.RawMessage  `json:"result"`
	Error  *jsonrpcErrorObj `json:"error"`
}

// notifyRaiseCallRaw writes a notify.raise request whose params are the
// verbatim bytes rawParams and returns the response. The frame is built by
// string concatenation, never re-marshaled, so a params value the encoder
// would refuse (trailing JSON) still reaches the server exactly as written —
// and the validated frame is byte-for-byte what went over the socket.
func notifyRaiseCallRaw(t *testing.T, conn *websocket.Conn, id int, rawParams string) json.RawMessage {
	t.Helper()
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"notify.raise","params":%s}`, id, rawParams)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(req)); err != nil {
		t.Fatalf("write notify.raise: %v", err)
	}
	// Read until the matching response id, skipping notifications — the
	// same loop jsonrpcCallWithID uses; jsonrpcCallWithID cannot carry
	// params the encoder refuses, which is why this writer exists.
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(resp, &check)
		if check.ID == nil {
			continue
		}
		var idCheck struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(resp, &idCheck)
		if idCheck.ID != id {
			continue
		}
		return resp
	}
}

// assertRaiseError sends a notify.raise request whose params are the
// verbatim bytes rawParams and asserts the response carries exactly wantCode
// and that no event reached the raiser.
func assertRaiseError(t *testing.T, conn *websocket.Conn, raiser *fakeNotifyRaiser, rawParams string, wantCode int) {
	t.Helper()
	resp := notifyRaiseCallRaw(t, conn, 42, rawParams)
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error == nil {
		t.Fatalf("notify.raise succeeded (result %s), want error %d", envelope.Result, wantCode)
	}
	if envelope.Error.Code != wantCode {
		t.Errorf("code = %d (%s), want %d", envelope.Error.Code, envelope.Error.Message, wantCode)
	}
	if got := len(raiser.captured()); got != 0 {
		t.Errorf("raiser captured %d events, want 0", got)
	}
}

func TestNotifyRaise_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.raise.schema.json")
	cases := map[string]notifyRaiseParams{
		"local session": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Title:     "build done",
			Body:      "exit 0",
		},
		// No omitempty on any field: an empty title/body must still marshal
		// as present, because the schema requires all three keys.
		"empty presentation": {
			SessionID: "0123456789abcdef0123456789abcdef",
			Title:     "",
			Body:      "",
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "notify.raise params DTO")
		})
	}
}

// TestNotifyRaise_OverTheWireConformsToContract drives the real method
// through the real socket and validates the frame the client actually sent:
// the params bytes validated below are the bytes that went over the wire,
// written verbatim, not a struct the test marshaled. The server's decode of
// that same frame must agree with the schema (the decode is the Go side of
// additionalProperties: false), and the event the raiser received must carry
// exactly the frame's sessionId, title and body.
func TestNotifyRaise_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.raise.schema.json")
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)

	sid := openSession(t, conn)

	rawParams := []byte(`{"sessionId":"` + sid + `","title":"build done","body":"exit 0"}`)
	resp := notifyRaiseCallRaw(t, conn, 2, string(rawParams))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("notify.raise: %+v", envelope.Error)
	}

	validateJSON(t, schema, rawParams, "notify.raise params (real socket, client frame)")

	got, err := decodeNotifyRaiseParams(rawParams)
	if err != nil {
		t.Fatalf("server decode of the on-wire frame: %v", err)
	}
	if got.SessionID != sid || got.Title != "build done" || got.Body != "exit 0" {
		t.Errorf("server decode of the on-wire frame = %+v", got)
	}

	evs := raiser.captured()
	if len(evs) != 1 {
		t.Fatalf("raiser captured %d events, want 1", len(evs))
	}
	if ev := evs[0]; ev.SessionID != sid || ev.Title != "build done" || ev.Body != "exit 0" {
		t.Errorf("event presentation = %+v, want the frame's sessionId/title/body", ev)
	}
}

// TestNotifyRaise_NoRaiser_MethodUnavailable pins the -32601 shape: without
// WithNotifyRaiser the whole notify package is reachable from its own tests
// and nowhere else, and the method answers that the capability is absent
// before any decode or session lookup.
func TestNotifyRaise_NoRaiser_MethodUnavailable(t *testing.T) {
	_, _, conn := newNotifyRaiserWS(t, nil)
	assertRaiseError(t, conn, &fakeNotifyRaiser{},
		`{"sessionId":"0123456789abcdef0123456789abcdef","title":"x","body":"y"}`, -32601)
}

// TestNotifyRaise_ProtectedField_Refused walks each protected field by name:
// trust, kind, level, attribution and at are absent from the wire rather
// than validated on it (ADR-0029 §2.2), so a frame that smuggles one must
// be refused. The session is live on the connection — the ONLY defect in
// the frame is the smuggled field — and refusal means no event was raised.
func TestNotifyRaise_ProtectedField_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	fields := map[string]string{
		"trust":       `"trust":"attested"`,
		"kind":        `"kind":"block.finished"`,
		"level":       `"level":"danger"`,
		"attribution": `"attribution":{"tab":"1","host":"h","session":"s"}`,
		"at":          `"at":"2026-08-14T00:00:00Z"`,
	}
	for name, extra := range fields {
		t.Run(name, func(t *testing.T) {
			rawParams := fmt.Sprintf(`{"sessionId":%q,"title":"build done","body":"exit 0",%s}`, sid, extra)
			assertRaiseError(t, conn, raiser, rawParams, -32602)
		})
	}
}

// TestNotifyRaise_UnknownSession_Refused covers both shapes of an id that is
// not live on this connection: one the registry never knew, and one that is
// live on a DIFFERENT connection — the second is the interesting case, and
// the handler checks h.state.has(sid) for exactly it. One WebSocket
// multiplexes many sessions (AD-1), so a raise must be refused rather than
// attributed across connections.
func TestNotifyRaise_UnknownSession_Refused(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, ws, conn := newNotifyRaiserWS(t, raiser)
	_ = openSession(t, conn)

	t.Run("never known to the registry", func(t *testing.T) {
		rawParams := `{"sessionId":"0123456789abcdef0123456789abcdef","title":"build done","body":"exit 0"}`
		assertRaiseError(t, conn, raiser, rawParams, -32602)
	})

	t.Run("live on a different connection", func(t *testing.T) {
		// A second connection opens its own session: the registry knows the
		// id, but conn's connection state does not.
		other := connectWS(t, ws)
		t.Cleanup(func() { _ = other.Close() })
		otherSid := openSession(t, other)
		rawParams := fmt.Sprintf(`{"sessionId":%q,"title":"build done","body":"exit 0"}`, otherSid)
		assertRaiseError(t, conn, raiser, rawParams, -32602)
	})
}

// TestNotifyRaise_TrailingJSON_OverTheWire pins what the wire actually does
// with a params object followed by a second JSON value: the transport's
// whole-frame decode refuses it as -32700 Parse error BEFORE the handler
// runs (a frame is one JSON value; trailing content is malformed at the
// frame level). The handler's own trailing check (errTrailingJSON → -32602)
// is therefore defense in depth, pinned at the seam in the test below. The
// honest wire assertion is the code the wire really produces — and that the
// refusal neither raised an event nor killed the connection.
func TestNotifyRaise_TrailingJSON_OverTheWire(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"notify.raise","params":{"sessionId":%q,"title":"t","body":"b"}{"x":1}}`, sid)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The parse-error response carries id null — the one shape the read
	// helpers skip as a notification — so read it directly.
	var envelope struct {
		ID    *json.RawMessage `json:"id"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		if err := json.Unmarshal(resp, &envelope); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
		}
		if envelope.Error == nil {
			continue // a notification, not the answer
		}
		break
	}
	if envelope.Error.Code != -32700 {
		t.Errorf("code = %d (%s), want -32700 Parse error", envelope.Error.Code, envelope.Error.Message)
	}
	if got := len(raiser.captured()); got != 0 {
		t.Errorf("raiser captured %d events, want 0", got)
	}

	// The refusal must not kill the connection: a valid raise still works.
	good := fmt.Sprintf(`{"sessionId":%q,"title":"t","body":"b"}`, sid)
	resp := notifyRaiseCallRaw(t, conn, 8, good)
	var env notifyRaiseResp
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal post-refusal response: %v\nraw: %s", err, string(resp))
	}
	if env.Error != nil {
		t.Fatalf("post-refusal raise: %+v", env.Error)
	}
}

// TestNotifyRaise_TrailingJSON_HandlerSeam pins the handler's own trailing
// check — the -32602 the brief names — at the layer that owns it. The
// transport never lets such a frame reach the handler (proven above), so
// this exercises the seam directly: decode refuses trailing input with
// errTrailingJSON, and a request carrying such params is answered -32602
// with no event raised.
func TestNotifyRaise_TrailingJSON_HandlerSeam(t *testing.T) {
	raw := []byte(`{"sessionId":"0123456789abcdef0123456789abcdef","title":"t","body":"b"}{"x":1}`)
	if _, err := decodeNotifyRaiseParams(raw); err != errTrailingJSON {
		t.Fatalf("decodeNotifyRaiseParams error = %v, want errTrailingJSON", err)
	}
	raiser := &fakeNotifyRaiser{}
	responder := &fakeResponder{}
	h := notifyRaiseHandlers{raiser: raiser, state: &connState{}, r: responder}
	h.handleNotifyRaise(context.Background(), jsonrpcRequest{
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

// TestNotifyRaise_RaiserFailure_InternalError pins the -32603 shape: the
// handler forwarded the request (the raiser WAS invoked — the failure is the
// pipeline's, not a dropped request), and the sink's error reaches the
// renderer as an internal error rather than a silent success.
func TestNotifyRaise_RaiserFailure_InternalError(t *testing.T) {
	raiser := &fakeNotifyRaiser{out: notify.Outcome{Err: errors.New("sink refused")}}
	_, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	rawParams := fmt.Sprintf(`{"sessionId":%q,"title":"build done","body":"exit 0"}`, sid)
	resp := notifyRaiseCallRaw(t, conn, 1, rawParams)
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error == nil {
		t.Fatalf("notify.raise succeeded, want -32603")
	}
	if envelope.Error.Code != -32603 {
		t.Errorf("code = %d (%s), want -32603", envelope.Error.Code, envelope.Error.Message)
	}
	if got := len(raiser.captured()); got != 1 {
		t.Errorf("raiser captured %d events, want exactly 1 (the request was forwarded)", got)
	}
}

// TestNotifyRaise_StampsProgramRequestProvenance is the assertion that says
// a renderer call can never mint an attested event: kind and trust come from
// the method invoked (notify.raise IS the programRequest boundary of
// ADR-0029 §2.2), level is stamped by nocx, and attribution comes from the
// session registry entry for the addressed id, never from the request — the
// request cannot even carry an attribution field (refused above), so any
// non-empty attribution proves the registry was the source.
func TestNotifyRaise_StampsProgramRequestProvenance(t *testing.T) {
	raiser := &fakeNotifyRaiser{}
	reg, _, conn := newNotifyRaiserWS(t, raiser)
	sid := openSession(t, conn)

	rawParams := []byte(`{"sessionId":"` + sid + `","title":"build done","body":"exit 0"}`)
	resp := notifyRaiseCallRaw(t, conn, 1, string(rawParams))
	var envelope notifyRaiseResp
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("notify.raise: %+v", envelope.Error)
	}

	evs := raiser.captured()
	if len(evs) != 1 {
		t.Fatalf("raiser captured %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Kind != notify.KindProgramNotify {
		t.Errorf("kind = %q, want %q", ev.Kind, notify.KindProgramNotify)
	}
	if ev.Trust != notify.TrustProgramRequest {
		t.Errorf("trust = %q, want %q", ev.Trust, notify.TrustProgramRequest)
	}
	if ev.Level != notify.LevelInfo {
		t.Errorf("level = %q, want %q", ev.Level, notify.LevelInfo)
	}
	sess, err := reg.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry Get(%q): %v", sid, err)
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
	// The backend this session runs on, stamped with the same value the
	// session.ended source uses (nocx-2gfh6). Without it the renderer cannot
	// resolve the occurrence to a tab at all — its lookup compares the backend
	// id, deliberately, so that a relay's sessions stay distinguishable — and
	// a program notification raised from a live tab rendered unactivatable.
	if ev.Attribution.Backend != commandnames.LocalRoute {
		t.Errorf("attribution.backend = %q, want %q", ev.Attribution.Backend, commandnames.LocalRoute)
	}
}

// ── the bound on title and body (nocx-jiwq.3) ──────────────────────────

// TestNotifyRaise_BoundIsTheContract is what stops the two enforcers of one
// bound drifting apart. The bound is DECLARED in the schema and the Go
// validator carries a constant, because contracts/ cannot be reached from the
// shipped binary (//go:embed cannot escape a package directory, and the
// directory deliberately belongs to neither party). So the declaration is
// read here and compared: a number changed on one side and not the other
// fails, rather than shipping a wire contract and a validator that disagree —
// which is the whole of AGENTS.md rule 5.
//
// It also pins that the schema NAMES the unit. A bound stated without its
// unit is the defect wearing a new coat: both sides can say "4096" and mean
// different payloads.
func TestNotifyRaise_BoundIsTheContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(contractDir, "notify.raise.schema.json")) //nolint:gosec // test-only path under contracts/
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var doc struct {
		Properties map[string]struct {
			MaxLength   *int   `json:"maxLength"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	for _, field := range []string{"title", "body"} {
		prop, ok := doc.Properties[field]
		if !ok {
			t.Fatalf("contract declares no %q property", field)
		}
		if prop.MaxLength == nil {
			t.Fatalf("contract declares no maxLength on %q: the bound would exist in the Go validator and nowhere on the wire", field)
		}
		if *prop.MaxLength != maxNotifyTextCodePoints {
			t.Errorf("%s: contract maxLength = %d, Go validator bound = %d — one bound, declared once", field, *prop.MaxLength, maxNotifyTextCodePoints)
		}
		if !strings.Contains(prop.Description, "code points") {
			t.Errorf("%s: the contract's description does not name the unit it counts; the Go refusal says %q", field, fmt.Sprintf("exceeds %d Unicode code points", maxNotifyTextCodePoints))
		}
	}
}

// TestNotifyRaise_TextBound_SameUnitOnBothSides drives a payload exactly at
// the bound and one code point over it through the real socket, and puts the
// SAME bytes past the schema. Four rows, and the astral ones are the reason
// the unit had to be chosen rather than assumed: "😀" is one code point and
// TWO UTF-16 code units, so 4096 of them are 4096 against a code-point bound
// and 8192 against a code-unit one. A side counting code units would refuse a
// payload the other accepts, which is a wire contract that means two things.
//
// The schema assertion and the socket assertion are made on the same bytes on
// purpose: agreeing about a payload the test constructed twice would prove
// only that the test is consistent.
func TestNotifyRaise_TextBound_SameUnitOnBothSides(t *testing.T) {
	schema := loadSchema(t, "notify.raise.schema.json")

	cases := []struct {
		name    string
		char    string
		count   int
		refused bool
	}{
		{"ascii at the bound", "a", maxNotifyTextCodePoints, false},
		{"ascii one code point over", "a", maxNotifyTextCodePoints + 1, true},
		{"astral at the bound", "😀", maxNotifyTextCodePoints, false},
		{"astral one code point over", "😀", maxNotifyTextCodePoints + 1, true},
	}
	for _, tc := range cases {
		for _, field := range []string{"title", "body"} {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				// One server per row: assertRaiseError proves nothing was
				// raised by counting the raiser's events from zero, and the
				// accepted rows raise.
				raiser := &fakeNotifyRaiser{}
				_, _, conn := newNotifyRaiserWS(t, raiser)
				sid := openSession(t, conn)

				text := strings.Repeat(tc.char, tc.count)
				params := notifyRaiseParams{SessionID: sid, Title: "ok", Body: "ok"}
				if field == "title" {
					params.Title = text
				} else {
					params.Body = text
				}
				rawParams, err := json.Marshal(params)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}

				schemaErr := validateJSONErr(schema, rawParams)
				if tc.refused && schemaErr == nil {
					t.Errorf("the contract accepts %d code points of %q in %s; the bound is %d", tc.count, tc.char, field, maxNotifyTextCodePoints)
				}
				if !tc.refused && schemaErr != nil {
					t.Errorf("the contract refuses %d code points of %q in %s, which is exactly the bound:\n%v", tc.count, tc.char, field, schemaErr)
				}

				if tc.refused {
					assertRaiseError(t, conn, raiser, string(rawParams), -32602)
					// The refusal names the unit it counted, so the two
					// sides cannot be read as bounding different things.
					resp := notifyRaiseCallRaw(t, conn, 43, string(rawParams))
					var envelope notifyRaiseResp
					if err := json.Unmarshal(resp, &envelope); err != nil {
						t.Fatalf("unmarshal: %v", err)
					}
					if envelope.Error == nil {
						t.Fatal("second refusal succeeded")
					}
					want := fmt.Sprintf("%s exceeds %d Unicode code points", field, maxNotifyTextCodePoints)
					if !strings.Contains(envelope.Error.Message, want) {
						t.Errorf("refusal = %q, want it to contain %q", envelope.Error.Message, want)
					}
					return
				}

				resp := notifyRaiseCallRaw(t, conn, 44, string(rawParams))
				var envelope notifyRaiseResp
				if err := json.Unmarshal(resp, &envelope); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if envelope.Error != nil {
					t.Fatalf("a payload exactly at the bound was refused: %+v", envelope.Error)
				}
				evs := raiser.captured()
				if len(evs) != 1 {
					t.Fatalf("raiser captured %d events, want 1", len(evs))
				}
				got := evs[0].Title
				if field == "body" {
					got = evs[0].Body
				}
				if got != text {
					t.Errorf("%s reached the pipeline as %d code points, want the %d that were sent", field, len([]rune(got)), tc.count)
				}
			})
		}
	}
}
