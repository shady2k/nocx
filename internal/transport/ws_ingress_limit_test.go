package transport

// Ingress limits: a WebSocket read limit, an envelope-only control-frame
// decode, and a per-method params budget. The user-visible contract is that
// one oversized control frame can neither spend the read loop parsing it nor
// freeze the binary input of a session on the same connection.

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
)

// ── T1: the read limit ──────────────────────────────────────────────────────

// TestControlFrame_AboveReadLimit_ClosesCleanly pins the chosen failure mode
// for a frame far above the read limit: the connection fails CLEANLY (gorilla
// refuses the frame at the protocol layer with close code 1009 — the read
// loop never even sees it), while the server keeps serving other
// connections. The alternative — surviving the frame — is not expressible
// with gorilla's streaming reader, so clean failure is the documented
// behaviour.
func TestControlFrame_AboveReadLimit_ClosesCleanly(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	frame := strings.Repeat("a", wsReadLimit+1)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		// The server may refuse mid-write; the close below is what matters.
		t.Logf("write of over-limit frame failed early: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var closeErr *websocket.CloseError
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue // anything before the close is not the point
		}
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseMessageTooBig {
			t.Fatalf("expected close code 1009 (message too big), got: %v", err)
		}
		break
	}

	// The server survived: a fresh connection opens a session end to end.
	conn2 := connectWS(t, ws)
	defer func() { _ = conn2.Close() }()
	resp := jsonrpcCall(t, conn2, "open", map[string]uint16{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
	})
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal open response: %v", err)
	}
	if r.Error != nil {
		t.Fatalf("session open after over-limit close failed: %s", r.Error.Message)
	}
	if r.Result.SessionID == "" {
		t.Fatal("open after over-limit close returned no session id")
	}
}

// ── T2: an oversized frame must not stall binary input on the same socket ──

// TestOversizedFrame_DoesNotStallSessionInput is the user-visible point of
// the whole task, over the real socket and modelled on
// TestDeadSession_DoesNotFreezeAnotherPane: a control frame far above its
// method's params budget arrives, and the binary input that follows it still
// reaches the PTY of a session on the SAME connection. The frame is refused
// by length — the read loop never parses it — and the connection survives to
// carry the keystrokes.
func TestOversizedFrame_DoesNotStallSessionInput(t *testing.T) {
	live := newLiveChannel()
	t.Cleanup(func() { _ = live.Close() })

	ws := stallServer(t, live)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	sid := openSSHOverSocket(t, ws, conn, 1)

	// 2 MiB of params on an ordinary method: above the default budget,
	// comfortably below the read limit.
	big := strings.Repeat("x", 2<<20)
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 99, "method": "settings.set",
		"params": map[string]any{"key": "k", "value": big},
	})
	if err != nil {
		t.Fatalf("marshal oversized frame: %v", err)
	}
	if len(raw) > wsReadLimit {
		t.Fatalf("test frame %d B exceeds read limit %d B", len(raw), wsReadLimit)
	}
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}

	// Binary input sent right after the oversized frame must reach the PTY.
	sendData(t, conn, sid, "hostname\n")

	// The oversized frame is answered with the budget rejection.
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var env struct {
			ID    *int `json:"id"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			continue
		}
		if env.ID == nil || *env.ID != 99 {
			continue // notification or another exchange
		}
		if env.Error == nil || env.Error.Code != -32602 {
			t.Fatalf("oversized frame: expected budget rejection -32602, got %+v", env.Error)
		}
		break
	}

	deadline := time.After(15 * time.Second)
	for {
		if live.received() == "hostname\n" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the session's input never reached its PTY while the oversized frame was in flight (got %q)",
				live.received())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ── T4: the resolver methods carry a deliberately tiny budget ───────────────

// TestResolverMethods_RejectAboveTinyBudget pins the two methods that run on
// the read loop itself: vault.unlockResolved and connections.passwordResolved
// get the smallest budget, so the loop can never be made to spend real work
// on them. A normal-size resolve still reaches the handler (its own answer
// comes back, not the budget gate's).
func TestResolverMethods_RejectAboveTinyBudget(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	bigRequestID := strings.Repeat("r", 2<<10) // frame well above budgetTiny

	// Each resolver has its OWN outcome vocabulary — unsealed for the vault,
	// submitted for a password — and sending one method the other's word is
	// how this test used to pass a payload the product cannot send.
	outcomes := map[string]string{
		"vault.unlockResolved":         "unsealed",
		"connections.passwordResolved": "submitted",
	}
	for _, method := range []string{"vault.unlockResolved", "connections.passwordResolved"} {
		t.Run(method, func(t *testing.T) {
			resp := jsonrpcCall(t, conn, method, map[string]any{
				"requestId": bigRequestID, "outcome": outcomes[method],
			})
			var env struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(resp, &env); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if env.Error == nil || env.Error.Code != -32602 {
				t.Fatalf("oversized resolver frame: expected -32602, got %+v", env.Error)
			}
			if !strings.Contains(env.Error.Message, "size budget") {
				t.Fatalf("oversized resolver frame: unexpected message %q", env.Error.Message)
			}

			// A normal-size resolve passes the gate and reaches the handler:
			// the answer is the handler's "unknown request id", not the budget
			// message — proving the tiny budget still admits real traffic.
			normal := jsonrpcCall(t, conn, method, map[string]any{
				"requestId": "no-such-ask", "outcome": outcomes[method],
			})
			var nEnv struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(normal, &nEnv); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if nEnv.Error == nil || nEnv.Error.Message != "Unknown request id" {
				t.Fatalf("normal-size resolve did not reach the handler: %+v", nEnv.Error)
			}
		})
	}
}

// ── T5: the frame path decides on envelope + length alone ───────────────────

// TestFramePath_RejectsOversizedByLengthBeforeParamsDecode is the structural
// assertion that the read loop never decodes params for an ordinary method.
// The frame path must decide on the envelope and the frame LENGTH alone: two
// oversized frames for the same method — one with valid params, one whose
// params region is not even JSON — must produce the identical budget
// rejection. If the loop ever looked at params content, the second frame
// would have produced a parse error instead. (The old code, which parsed the
// whole frame before dispatch, fails both assertions.)
func TestFramePath_RejectsOversizedByLengthBeforeParamsDecode(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	big := strings.Repeat("x", 2<<20)
	valid := `{"jsonrpc":"2.0","id":7,"method":"settings.set","params":{"key":"k","value":"` + big + `"}}`
	garbage := `{"jsonrpc":"2.0","id":8,"method":"settings.set","params":{"key":"k","value":"` + big + `  `

	reject := func(t *testing.T, frame string, wantID int) string {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		for {
			_, resp, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			var env struct {
				ID    *int `json:"id"`
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(resp, &env); err != nil {
				continue
			}
			if env.ID == nil || *env.ID != wantID {
				continue
			}
			if env.Error == nil || env.Error.Code != -32602 {
				t.Fatalf("frame %d: expected budget rejection -32602, got %+v", wantID, env.Error)
			}
			return env.Error.Message
		}
	}

	validMsg := reject(t, valid, 7)
	garbageMsg := reject(t, garbage, 8)
	if validMsg != garbageMsg {
		t.Fatalf("frame path is params-content-dependent: valid params said %q, garbage params said %q",
			validMsg, garbageMsg)
	}

	// An in-budget frame whose params are malformed is still rejected as a
	// parse error (parity with the old whole-frame parse) — but that check
	// happens AFTER the budget gate and is bounded by it. The id is lost to
	// the parse-error response, so read it manually.
	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":9,"method":"settings.set","params":{"key":"k","value":"unterminated`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var env struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			continue
		}
		if env.Error == nil {
			continue // a notification — not the parse-error response
		}
		if env.Error.Code != -32700 {
			t.Fatalf("in-budget malformed params: expected -32700, got %d", env.Error.Code)
		}
		break
	}
}

// ── T6: standard JSON string semantics for the envelope ─────────────────────

// TestEnvelope_EscapedMethodDispatchesIdentically pins that the envelope
// decode follows standard JSON semantics: a method whose name is escaped
// ("sessions.\u0073tatus") must dispatch to the same handler and answer
// identically to its unescaped form. The hand-rolled scanner kept escapes
// verbatim, inventing a private method-name language in which the escaped
// form was a different method and answered "Method not found".
func TestEnvelope_EscapedMethodDispatchesIdentically(t *testing.T) {
	_, conn := newBareTransport(t)

	escaped := rawCallByID(t, conn,
		`{"jsonrpc":"2.0","id":1,"method":"sessions.\u0073tatus","params":{"profileIds":[]}}`, 1)
	plain := rawCallByID(t, conn,
		`{"jsonrpc":"2.0","id":1,"method":"sessions.status","params":{"profileIds":[]}}`, 1)

	if string(escaped) != string(plain) {
		t.Fatalf("escaped method answered differently from its unescaped form:\n  escaped: %s\n  plain:   %s",
			escaped, plain)
	}
}

// ── T7: an invalid escape is a parse error ──────────────────────────────────

// TestEnvelope_InvalidEscapeIsParseError: "\q" is not valid JSON. The old
// scanner skipped whatever followed a backslash and treated the method as a
// (nonexistent) literal name; the frame must be refused as a parse error
// instead.
func TestEnvelope_InvalidEscapeIsParseError(t *testing.T) {
	_, conn := newBareTransport(t)

	writeRaw(t, conn, `{"jsonrpc":"2.0","id":1,"method":"vault\qstatus"}`)
	code, msg := readErrorCode(t, conn)
	if code != -32700 {
		t.Fatalf("invalid escape: expected parse error -32700, got %d %q", code, msg)
	}
}

// ── T8: a raw control character in a string is a parse error ────────────────

// TestEnvelope_RawControlCharIsParseError: JSON forbids raw control
// characters inside strings. The byte sits in params, after method, so it
// exercises the full-decode stage rather than the envelope pass.
func TestEnvelope_RawControlCharIsParseError(t *testing.T) {
	_, conn := newBareTransport(t)

	// A literal 0x01 byte inside the "value" string.
	writeRaw(t, conn, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"settings.set\",\"params\":{\"key\":\"k\",\"value\":\"a\x01b\"}}")
	code, msg := readErrorCode(t, conn)
	if code != -32700 {
		t.Fatalf("raw control char: expected parse error -32700, got %d %q", code, msg)
	}
}

// ── T9: a mismatched container is a parse error ─────────────────────────────

// TestEnvelope_MismatchedContainerIsParseError: a closing bracket that does
// not match its opener is not JSON. The old scanner tracked only a numeric
// nesting depth, so `[}}` and `{]` looked structurally closed.
func TestEnvelope_MismatchedContainerIsParseError(t *testing.T) {
	_, conn := newBareTransport(t)

	for _, params := range []string{"[}}", `{"a":{]}}`} {
		writeRaw(t, conn, `{"jsonrpc":"2.0","id":1,"method":"settings.set","params":`+params+`}`)
		code, msg := readErrorCode(t, conn)
		if code != -32700 {
			t.Fatalf("mismatched container %s: expected parse error -32700, got %d %q", params, code, msg)
		}
	}
}

// ── T10: an invalid primitive is a parse error ──────────────────────────────

// TestEnvelope_InvalidPrimitiveIsParseError: "garbage" is not a JSON
// literal. The old scanner captured it as the request id verbatim, which
// made the response itself invalid JSON on the wire.
func TestEnvelope_InvalidPrimitiveIsParseError(t *testing.T) {
	_, conn := newBareTransport(t)

	writeRaw(t, conn, `{"id":garbage,"method":"vault.status"}`)
	code, msg := readErrorCode(t, conn)
	if code != -32700 {
		t.Fatalf("invalid primitive: expected parse error -32700, got %d %q", code, msg)
	}
}

// ── T11: garbage after params is a parse error AND no handler runs ──────────

// TestEnvelope_GarbageAfterParamsIsParseError_NoHandlerRuns: trailing bytes
// after the params member make the whole frame not JSON. The frame must be
// refused as a parse error and never dispatched. On this bare server the
// vault.status handler would answer -32601 "vault not available"; seeing
// -32700 instead is the proof that no handler ran.
func TestEnvelope_GarbageAfterParamsIsParseError_NoHandlerRuns(t *testing.T) {
	_, conn := newBareTransport(t)

	writeRaw(t, conn, `{"jsonrpc":"2.0","id":1,"method":"vault.status","params":{} this is not JSON}`)
	code, msg := readErrorCode(t, conn)
	if code != -32700 {
		t.Fatalf("garbage after params: expected parse error -32700 (no dispatch), got %d %q", code, msg)
	}
}

// ── T12: a duplicate method cannot take one budget and dispatch as another ──

// TestEnvelope_DuplicateMethod_CannotBypassBudgetTier: the frame repeats the
// "method" member. The envelope pass reads the FIRST (backup.preview — the
// 8 MiB document budget); a full JSON decode resolves the LAST
// (vault.unlockResolved — the 1 KiB resolver tier). The frame is ~2 KiB:
// inside the document budget, far above the resolver tier. A code that
// selects the budget after the full decode would admit an oversized payload.
func TestEnvelope_DuplicateMethod_CannotBypassBudgetTier(t *testing.T) {
	ws, conn := newBareTransport(t)
	defer func() { _ = conn.Close() }()
	defer func() { _ = ws.Stop(context.Background()) }()

	frame := `{"jsonrpc":"2.0","id":1,"method":"backup.preview","method":"vault.unlockResolved","params":{"contents":"` +
		strings.Repeat("x", 2<<10) + `"}}`
	writeRaw(t, conn, frame)
	code, msg := readErrorCode(t, conn)
	if code != -32600 {
		t.Fatalf("duplicate method: expected -32600 (method mismatch), got %d %q", code, msg)
	}
}

// ── T13: document-tier methods admit frames above the default budget ────────

// TestDocumentBudget_AdmitsFrameAboveDefaultBudget proves that a method on the
// document budget tier (profiles.tabbyPreview, 8 MiB) accepts a frame that
// would be refused on the default budget (64 KiB). The frame is ~128 KiB —
// comfortably above the default budget, far below the document ceiling.
func TestDocumentBudget_AdmitsFrameAboveDefaultBudget(t *testing.T) {
	ws, conn := newBareTransport(t)
	defer func() { _ = conn.Close() }()
	defer func() { _ = ws.Stop(context.Background()) }()

	// A frame above the default budget but inside the document budget.
	// The payload is a plausible import frame: >64 KiB of JSON.
	payload := strings.Repeat("x", 128<<10) // 128 KiB
	frame := `{"jsonrpc":"2.0","id":1,"method":"profiles.tabbyPreview","params":{"contents":"` +
		payload + `"}}`
	writeRaw(t, conn, frame)

	// The budget gate must admit it — read the response and check it's not
	// a budget rejection (-32602).
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if env.Error != nil && env.Error.Code == -32602 && strings.Contains(env.Error.Message, "size budget") {
		t.Fatalf("document-tier method refused by budget gate: %+v", env.Error)
	}
	// The method itself will fail (parse error on the garbage payload), but
	// the budget gate must have admitted it — the error code must not be
	// -32602 "size budget".
}

// TestDocumentBudget_ImportTabbyAdmitsLargeFrame proves profiles.importTabby
// also uses the document budget.
func TestDocumentBudget_ImportTabbyAdmitsLargeFrame(t *testing.T) {
	ws, conn := newBareTransport(t)
	defer func() { _ = conn.Close() }()
	defer func() { _ = ws.Stop(context.Background()) }()

	payload := strings.Repeat("x", 128<<10) // 128 KiB
	frame := `{"jsonrpc":"2.0","id":1,"method":"profiles.importTabby","params":{"planToken":"` +
		payload + `"}}`
	writeRaw(t, conn, frame)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if env.Error != nil && env.Error.Code == -32602 && strings.Contains(env.Error.Message, "size budget") {
		t.Fatalf("document-tier method refused by budget gate: %+v", env.Error)
	}
}

func TestEnvelope_HugeStringBeforeMethod_BoundedAllocation(t *testing.T) {
	// heapGrowth runs decodeEnvelope and returns the HeapAlloc delta (bytes),
	// with the frame built and resident BEFORE the measurement so the delta
	// reflects only what decodeEnvelope allocates.
	heapGrowth := func(frame []byte) uint64 {
		// Force the backing array fully resident.
		_ = frame[0]
		_ = frame[len(frame)-1]
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		_, _ = decodeEnvelope(frame)
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		if after.HeapAlloc > before.HeapAlloc {
			return after.HeapAlloc - before.HeapAlloc
		}
		return 0
	}

	// A 16 MiB params string sitting before method: the tokenizer is capped
	// at envelopeScanCap bytes, so it must refuse the frame without
	// materialising the string — the allocation inside decodeEnvelope must
	// stay proportional to the cap, not to the frame.
	frame := []byte(`{"params":"` + strings.Repeat("x", 16<<20) + `","method":"settings.set"}`)
	_, lastErr := decodeEnvelope(frame)
	grew := heapGrowth(frame)
	if !errors.Is(lastErr, errEnvelopeTooLarge) {
		t.Fatalf("16 MiB string before method: expected errEnvelopeTooLarge, got %v", lastErr)
	}
	t.Logf("16 MiB string: heap grew %.1f KiB (frame is %.1f MiB)", float64(grew)/1024, float64(len(frame))/(1<<20))

	// A 16 MiB whitespace frame: the tokenizer exhausts the cap inside the
	// whitespace and the first-byte probe must not scan beyond the cap
	// either — the error path stays O(cap), never O(frame).
	ws := []byte(strings.Repeat(" ", 16<<20))
	_, wsErr := decodeEnvelope(ws)
	wsGrew := heapGrowth(ws)
	if !errors.Is(wsErr, errEnvelopeNotObject) {
		t.Fatalf("16 MiB whitespace: expected errEnvelopeNotObject, got %v", wsErr)
	}
	t.Logf("16 MiB whitespace: heap grew %.1f KiB", float64(wsGrew)/1024)

	const allocBound = 256 << 10 // 256 KiB — 64x less than the 16 MiB frames, ~17x the measured growth
	for name, g := range map[string]uint64{"16 MiB string": grew, "16 MiB whitespace": wsGrew} {
		if g > allocBound {
			t.Fatalf("decodeEnvelope grew the heap by %.1f KiB for %s — the frame was materialised",
				float64(g)/1024, name)
		}
	}
}

// ── T14: a duplicate envelope member before method is refused ───────────────

// TestEnvelope_DuplicateParamsBeforeMethodRejected: a repeated top-level
// envelope member makes the request ambiguous, so the envelope pass refuses
// it outright. (A repeated method AFTER the first occurrence is caught by
// the method-equality check — see
// TestEnvelope_DuplicateMethod_CannotBypassBudgetTier.)
func TestEnvelope_DuplicateParamsBeforeMethodRejected(t *testing.T) {
	_, conn := newBareTransport(t)

	writeRaw(t, conn, `{"params":{},"params":{},"method":"settings.set"}`)
	code, msg := readErrorCode(t, conn)
	if code != -32600 {
		t.Fatalf("duplicate params member: expected -32600, got %d %q", code, msg)
	}
}

// ── helpers for the envelope tests ──────────────────────────────────────────

// newBareTransport returns a started WSServer with a stub session registry
// and no other wiring, plus a connected client. A method dispatched to a
// handler that depends on missing wiring answers -32601 — exactly what
// distinguishes "handler ran" from "refused before dispatch" in the
// parse-error tests above.
func newBareTransport(t *testing.T) (*WSServer, *websocket.Conn) {
	t.Helper()
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return ws, conn
}

// writeRaw sends a verbatim control frame.
func writeRaw(t *testing.T, conn *websocket.Conn, frame string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write raw frame: %v", err)
	}
}

// rawCallByID writes a verbatim frame and reads control messages until a
// response with the given id arrives, returning its raw bytes.
func rawCallByID(t *testing.T, conn *websocket.Conn, frame string, id int) json.RawMessage {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write raw frame: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var idCheck struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(resp, &idCheck); err != nil {
			continue
		}
		if idCheck.ID != id {
			continue
		}
		return resp
	}
}

// readErrorCode reads control messages until one carries an error envelope
// and returns its code and message. Parse-error and invalid-request
// responses carry a null id, so the error envelope is the only reliable
// correlation. The deadline is short on purpose: a frame the server accepts
// (old-scanner behavior for some of the frames above) produces no -32700
// response, and a missing answer should fail fast, not after 30 s.
func readErrorCode(t *testing.T, conn *websocket.Conn) (int, string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var env struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(resp, &env); err != nil {
			continue
		}
		if env.Error == nil {
			continue // a result or notification — not what we're waiting for
		}
		return env.Error.Code, env.Error.Message
	}
}
