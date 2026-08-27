package transport

// notify.bell (nocx-n3nfg): the method the renderer calls when a program
// printed BEL, and the second renderer-callable source of ADR-0029's
// pipeline.
//
// WHY A SECOND METHOD RATHER THAN AN ARGUMENT. The design's event-field
// table says kind is "stamped from the method invoked", and §3 closes
// ingress authority: no renderer-callable method can produce an attested
// event, and notify.raise and BEL are always programRequest. A kind
// parameter on notify.raise would satisfy neither — it would make the CALLER
// the one choosing the kind, which is the forging §3 rejects, moved one
// level up. So the method name is the choice, and stamping it here is what
// makes the choice unforgeable.
//
// The record carries sessionId and NOTHING else. BEL has no title and no
// body, so unlike notify.raise there are no presentation fields on the wire
// at all: title is stamped here from the session registry entry and body is
// left empty. That makes title and body PROTECTED fields for this method,
// and the decode below refuses a frame naming one exactly as it refuses a
// frame naming trust — a program that could put its own text on a bell
// would have notify.raise's payload with none of its accounting.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// notifyBellParams is the wire shape of notify.bell: sessionId and nothing
// else (ADR-0029 §2.2). sessionId is ADDRESSING, not attribution — one
// WebSocket multiplexes many server-assigned sessions (AD-1), so the record
// must say which terminal parsed the BEL, and the handler rejects an id not
// live on this connection. Every attributed field is derived from the
// registry entry for that id.
type notifyBellParams struct {
	SessionID string `json:"sessionId"`
}

// decodeNotifyBellParams decodes the params with DisallowUnknownFields and
// refuses trailing input: a frame carrying kind, trust, level, title, body
// or any attribution field, or anything the schema does not name, is
// rejected here as invalid params. The absence of the protected fields is
// what makes provenance structural; this decode is the Go side of the
// schema's additionalProperties: false.
//
// It reuses errTrailingJSON rather than minting a second sentinel: "a params
// object followed by a second JSON value" is one rule with one owner
// (ws_notify.go), and two errors for it would be two things to keep in step.
func decodeNotifyBellParams(raw []byte) (notifyBellParams, error) {
	var params notifyBellParams
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

// validateNotifyBellRaw is notify.bell's declared params validator
// (registration.go: a control method without one does not build). It
// delegates the shape rule to decodeNotifyBellParams rather than restating
// it, and adds the one thing the decode cannot say: sessionId is required.
//
// There is no length bound to add here, and that is not an omission. The
// bound on notify.raise exists because title and body are untrusted text a
// program supplied; notify.bell carries no text at all, and its one field is
// a session id the backend itself minted and is about to look up.
func validateNotifyBellRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "params are required"
	}
	p, err := decodeNotifyBellParams(raw)
	if err != nil {
		return "exactly sessionId"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	return ""
}

// bellTitle is the title a bell occurrence carries. It is stamped by the
// backend, from the registry entry, for the same reason kind and trust are:
// a BEL is one byte with no text, so the alternative is either an empty row
// in the notifications panel (which reads `title` for the row and shows
// `body` only when non-empty) or a title the program chose — and a program
// choosing the words on a notification is what the whole provenance rule is
// about.
//
// The shape follows sessionEndedTitle (ws.go), the other source that has to
// invent its own words: the host is what distinguishes one pane's bell from
// another's in a feed, so it goes in the title when there is one. The body
// stays empty deliberately — "a program rang the bell" would restate the
// title in a longer sentence, and the panel already omits an empty body.
func bellTitle(host string) string {
	if host == "" {
		return "Bell"
	}
	return "Bell on " + host
}

// notifyBellHandlers answers notify.bell. Constructed with its capability
// (the raiser), its registries (the session registry for attribution, the
// connection's session set for the liveness check) and its Responder — never
// the *WSServer.
type notifyBellHandlers struct {
	raiser   NotifyRaiser
	registry session.Registry
	state    *connState
	// tab is this connection's per-connection (per-tab) identity,
	// backend-assigned, monotonic and never reused — the tab half of the
	// backend-stamped attribution (ADR-0029 §4.6).
	tab string
	r   Responder
}

func (h notifyBellHandlers) handleNotifyBell(ctx context.Context, req jsonrpcRequest) {
	if h.raiser == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "notify.bell not available"})
		return
	}
	params, err := decodeNotifyBellParams(req.Params)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: exactly sessionId"})
		return
	}
	sid := session.ID(params.SessionID)
	sess, err := h.registry.Get(sid)
	if err != nil || !h.state.has(sid) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	// Stamping, all of it backend-owned and none of it reachable from the
	// request: kind and trust from the method invoked (notify.bell IS the
	// bell source of design §3), the title and level from nocx, attribution
	// from the session registry entry for the addressed id. The frame that
	// got here could carry exactly one field, so there is nothing else it
	// could have come from.
	out := h.raiser.Raise(ctx, notify.Event{
		SessionID: params.SessionID,
		Title:     bellTitle(sess.Host()),
		Body:      "",
		Kind:      notify.KindBell,
		Trust:     notify.TrustProgramRequest,
		Level:     notify.LevelInfo,
		Attribution: notify.Attribution{
			// The backend this session runs on, the same value every other
			// source stamps (nocx-2gfh6). The renderer's occurrence→tab
			// lookup compares it, so an empty one is a feed row that cannot
			// be activated.
			Backend: commandnames.LocalRoute,
			Tab:     h.tab,
			Host:    sess.Host(),
			Session: string(sess.ID()),
		},
	})
	if out.Err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "notify.bell: " + out.Err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// notifyBellSpec declares the notify.bell control method. It runs on the
// ordinary lane for the same reason notify.raise does: Raise is synchronous
// and can block on a sink invocation, so it must never run on the read loop.
func (s *WSServer) notifyBellSpec() methodSpec {
	return reg(s.lane, "notify.bell", params(validateNotifyBellRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
		return notifyBellHandlers{
			raiser:   s.notifyRaiser,
			registry: s.registry,
			state:    state,
			tab:      strconv.FormatUint(w.id, 10),
			r:        r,
		}.handleNotifyBell
	})
}
