package transport

// notify.raise (nocx-9zmc): the method the renderer calls to raise a
// notification, and the wire half of ADR-0029's provenance rule. The record
// carries sessionId, title and body and NOTHING else — kind, trust, level,
// attribution and at are stamped by this handler from the method invoked and
// the session registry, never read from the record. A schema proves a
// record's shape, never who assigned a field, which is why the protected
// fields are absent from the wire rather than validated on it; the decode
// below enforces that absence at the seam, so a frame the schema rejects is a
// JSON-RPC error rather than a silently ignored extra.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// NotifyRaiser raises one event through the notify pipeline (ADR-0029). The
// transport holds this narrow seam; internal/notify's *Router satisfies it
// without an adapter, the same signature-identical shape as the tunnel and
// discovery connectors.
type NotifyRaiser interface {
	Raise(ctx context.Context, ev notify.Event) notify.Outcome
}

// WithNotifyRaiser wires the notify pipeline into the server, enabling
// notify.raise. When absent, the method answers -32601. The composition root
// constructs the router (internal/app/app.go); without this line the whole
// notify package is reachable from its own tests and nowhere else (AGENTS.md
// check 5).
func WithNotifyRaiser(r NotifyRaiser) WSServerOption {
	return func(s *WSServer) { s.notifyRaiser = r }
}

// notifyRaiseParams is the wire shape of notify.raise: sessionId, title and
// body and nothing else (ADR-0029 §2.2). sessionId is ADDRESSING, not
// attribution — one WebSocket multiplexes many server-assigned sessions
// (AD-1), so the record must say which terminal parsed the sequence, and the
// handler rejects an id not live on this connection. Every attributed field
// is derived from the registry entry for that id.
type notifyRaiseParams struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

// decodeNotifyRaiseParams decodes the params with DisallowUnknownFields and
// refuses trailing input: a frame carrying trust, kind, level or any
// attribution field, or anything the schema does not name, is rejected here
// as invalid params. The absence of the protected fields is what makes
// provenance structural; the decode is the Go side of the schema's
// additionalProperties: false.
func decodeNotifyRaiseParams(raw []byte) (notifyRaiseParams, error) {
	var params notifyRaiseParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&params); err != nil {
		return params, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return params, errTrailingJSON
	}
	return params, nil
}

// errTrailingJSON reports a params object followed by a second JSON value.
var errTrailingJSON = errors.New("notify: trailing JSON after params")

// maxNotifyTextCodePoints bounds title and body. Both are untrusted
// presentation data written by whatever the user ran (ADR-0029 §2.3), and a
// banner shows a line or two — generous for anything a program legitimately
// announces, and far below a payload that costs anything to carry through the
// pipeline.
//
// The bound is DECLARED in contracts/notify.raise.schema.json as maxLength on
// title and body (AGENTS.md rule 5: the wire is a party to the contract), and
// this constant is the Go side of that one declaration. It is a constant
// rather than a read of the schema because the schema cannot be reached from
// here at run time: //go:embed cannot escape the package directory, and
// contracts/ deliberately belongs to neither party (ws_contract_test.go), so
// it is not in the shipped binary at all. What closes the gap is
// TestNotifyRaise_BoundIsTheContract, which reads the schema and fails if the
// two numbers ever differ — they cannot drift apart silently.
//
// The UNIT is Unicode code points, and it is the same unit on both sides on
// purpose. JSON Schema counts a string's length in characters as RFC 8259
// defines them — code points — and utf8.RuneCountInString counts runes, which
// are the same thing; an astral-plane character (one code point, two UTF-16
// code units) therefore costs one against the bound whichever side is
// counting. A bound that means two different things on two sides of the wire
// is the defect rule 5 exists to prevent, so the refusal below names the unit
// it counted.
const maxNotifyTextCodePoints = 4096

// validateNotifyRaiseRaw is notify.raise's declared params validator
// (registration.go: a control method without one does not build). It delegates
// the shape rule to decodeNotifyRaiseParams rather than restating it — the
// absence of the protected fields is ADR-0029's structural provenance and has
// exactly one owner. What it adds is what the decode cannot say: sessionId is
// required, and the two untrusted strings are bounded before anything carries
// them.
func validateNotifyRaiseRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "params are required"
	}
	p, err := decodeNotifyRaiseParams(raw)
	if err != nil {
		return "exactly sessionId, title and body"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if utf8.RuneCountInString(p.Title) > maxNotifyTextCodePoints {
		return fmt.Sprintf("title exceeds %d Unicode code points", maxNotifyTextCodePoints)
	}
	if utf8.RuneCountInString(p.Body) > maxNotifyTextCodePoints {
		return fmt.Sprintf("body exceeds %d Unicode code points", maxNotifyTextCodePoints)
	}
	return ""
}

// notifyRaiseHandlers answers notify.raise. It is a constructed type holding
// its capability (the raiser), its registries (the session registry for
// attribution, the connection's session set for the liveness check) and its
// Responder — never the *WSServer.
type notifyRaiseHandlers struct {
	raiser   NotifyRaiser
	registry session.Registry
	state    *connState
	// tab is this connection's per-connection (per-tab) identity,
	// backend-assigned, monotonic and never reused — the tab half of the
	// backend-stamped attribution (ADR-0029 §4.6).
	tab string
	r   Responder
}

func (h notifyRaiseHandlers) handleNotifyRaise(ctx context.Context, req jsonrpcRequest) {
	if h.raiser == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "notify.raise not available"})
		return
	}
	params, err := decodeNotifyRaiseParams(req.Params)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: exactly sessionId, title and body"})
		return
	}
	sid := session.ID(params.SessionID)
	sess, err := h.registry.Get(sid)
	if err != nil || !h.state.has(sid) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	// Stamping, all of it backend-owned: kind and trust from the method
	// invoked, level and attribution from the session registry. There is no
	// argument, header or method variant by which a renderer call produces an
	// attested event — this handler is the programRequest boundary of
	// ADR-0029 §2.2.
	out := h.raiser.Raise(ctx, notify.Event{
		SessionID: params.SessionID,
		Title:     params.Title,
		Body:      params.Body,
		Kind:      notify.KindProgramNotify,
		Trust:     notify.TrustProgramRequest,
		Level:     notify.LevelInfo,
		Attribution: notify.Attribution{
			// The backend this session runs on. Stamped with the same value
			// the session.ended source uses, because it is the same fact:
			// every session this build opens is on this machine (nocx-2gfh6).
			// Left empty, the renderer could not resolve the occurrence to a
			// tab at all — its lookup COMPARES the backend id, so that a
			// helper's sessions stay distinguishable once one lands — and a
			// program notification raised from a live tab rendered inert.
			Backend: commandnames.LocalRoute,
			Tab:     h.tab,
			Host:    sess.Host(),
			Session: string(sess.ID()),
		},
	})
	if out.Err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "notify.raise: " + out.Err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// notifySpecs declares the renderer-callable notify methods. They run on the
// ordinary lane: Raise is synchronous and can block on a sink invocation, so
// neither may run on the read loop.
//
// There are three of them and there is one reason: kind is stamped from the
// method invoked, so a second SOURCE is a second METHOD (ws_notify_bell.go
// says why at length). Anything that would let one method answer for two —
// a kind argument, a variant, a header — would put the caller in charge of
// the field this design keeps off the wire. notify.paneWorkFinished is the
// third and it is where that rule earns the most: it is the only renderer-
// callable source whose trust is heuristic, and the trust class is what
// keeps an inference off push (design §3.1). A caller able to name its own
// kind could have named a different one.
func (s *WSServer) notifySpecs() []methodSpec {
	return []methodSpec{
		reg(s.lane, "notify.raise", params(validateNotifyRaiseRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			return notifyRaiseHandlers{
				raiser:   s.notifyRaiser,
				registry: s.registry,
				state:    state,
				tab:      strconv.FormatUint(w.id, 10),
				r:        r,
			}.handleNotifyRaise
		}),
		s.notifyBellSpec(),
		s.notifyPaneWorkFinishedSpec(),
		s.notifyCatalogueSpec(),
	}
}
