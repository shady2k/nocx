package transport

// notify.paneWorkFinished (nocx-n3nfg): the method the renderer calls when a
// pane's title went working → idle and STAYED idle for the settle window,
// and the third — and last — renderer-callable source of ADR-0029's
// pipeline.
//
// WHY A THIRD METHOD RATHER THAN AN ARGUMENT. The same reason there is a
// second one, and ws_notify_bell.go argues it at length: the design's
// event-field table says kind is "stamped from the method invoked", and §3
// closes ingress authority — no renderer-callable method may produce an
// attested event. A kind parameter on notify.raise would make the CALLER the
// one choosing, which is the forging §3 rejects moved one level up. So the
// method name is the choice, and stamping it here is what makes the choice
// unforgeable. There are now three such methods and one rule, not three
// rules.
//
// WHY IT IS HEURISTIC, AND WHY THAT IS NOT A HEDGE. The classifier behind it
// (frontend/src/agent-status.ts) matches ANY braille glyph in a title, which
// is `npm install` under ora, `docker pull`, and half of all TUIs — plus
// Claude Code's ✳ for the idle half. Design §3.4 rule 3 is explicit that the
// label is "work in the pane seems to have finished" and NEVER "the agent
// finished", and the trust class is heuristic for exactly that reason.
// TrustHeuristic is what confines the event to local attention — toast, dock
// badge, tab dot — and keeps it off push (§3.1). The router enforces that;
// this method's job is to make the trust unforgeable, and it does it the
// only way the design allows: by stamping it from the method invoked.
//
// The record carries sessionId and NOTHING else, for a sharper reason than
// bell's. A bell has no text to send. This source HAS text within reach —
// the pane title the inference was drawn from — and that title is a string a
// PROGRAM wrote. Putting it on the wire would let a program supply the words
// of a notification whose kind it did not have to earn, which is the whole
// of ADR-0029 §2.2. So title and body are protected fields here as they are
// for bell, the decode below refuses a frame naming one, and the backend
// stamps the words from its own registry entry.

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

// notifyPaneWorkFinishedParams is the wire shape: sessionId and nothing else
// (ADR-0029 §2.2). sessionId is ADDRESSING, not attribution — one WebSocket
// multiplexes many server-assigned sessions (AD-1), so the record must say
// which pane settled, and the handler rejects an id not live on this
// connection. Every attributed field is derived from the registry entry for
// that id.
//
// The liveness check earns its keep twice over here. For bell it refuses a
// cross-connection attribution; for this method it ALSO refuses the one race
// the renderer's state machine cannot close from its own side — a settle
// timer armed five seconds ago against a session the pane has since
// replaced. The renderer declines to fire in that case (pane-work-finished.
// ts checks the pane's session at fire time), and this is the backstop that
// makes the check structural rather than a promise.
type notifyPaneWorkFinishedParams struct {
	SessionID string `json:"sessionId"`
}

// decodeNotifyPaneWorkFinishedParams decodes the params with
// DisallowUnknownFields and refuses trailing input: a frame carrying kind,
// trust, level, title, body or any attribution field, or anything the schema
// does not name, is rejected here as invalid params. The absence of the
// protected fields is what makes provenance structural; this decode is the
// Go side of the schema's additionalProperties: false.
//
// It reuses errTrailingJSON rather than minting a third sentinel: "a params
// object followed by a second JSON value" is one rule with one owner
// (ws_notify.go).
//
// It is byte-for-byte the shape decodeNotifyBellParams has, and that is a
// duplication worth naming rather than hiding: three notify methods now
// decode "exactly sessionId" three times over, and the honest resolution is
// one shared session-addressed decode the three point at. It is not done
// here because this change may not touch ws_notify_bell.go, and a helper
// only one caller uses would be the same duplication with an extra hop.
func decodeNotifyPaneWorkFinishedParams(raw []byte) (notifyPaneWorkFinishedParams, error) {
	var params notifyPaneWorkFinishedParams
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

// validateNotifyPaneWorkFinishedRaw is the method's declared params
// validator (registration.go: a control method without one does not build).
// It delegates the shape rule to the decode rather than restating it, and
// adds the one thing the decode cannot say: sessionId is required.
//
// There is no length bound to add, and that is not an omission: the one
// field is a session id the backend itself minted and is about to look up.
func validateNotifyPaneWorkFinishedRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "params are required"
	}
	p, err := decodeNotifyPaneWorkFinishedParams(raw)
	if err != nil {
		return "exactly sessionId"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	return ""
}

// paneWorkFinishedTitle is the title one settled pane carries, stamped by
// the backend from the registry entry for the same reason kind and trust
// are: the only text within reach of this source is a title a program wrote.
//
// THE WORDS ARE THE RULE, not decoration. Design §3.4 rule 3 forbids "the
// agent finished" and requires the hedge, because the classifier cannot tell
// an agent from `docker pull`. "Seems to" is that hedge, and it is the same
// sentence Settings shows against the toggle that governs this kind
// (internal/notify/catalogue.go: "Work seems to have finished") — one
// wording, so the row a person sees and the switch they flipped say the same
// thing.
//
// The shape follows bellTitle and sessionEndedTitle, the other sources that
// have to invent their own words: the host is what distinguishes one pane's
// row from another's in a feed, so it goes in the title when there is one.
// The body stays empty deliberately — the hedge is already in the title, and
// the reason for it is on the Settings row; a body restating either would be
// a second wording of one fact, and the notifications panel omits an empty
// body rather than printing a blank line.
func paneWorkFinishedTitle(host string) string {
	if host == "" {
		return "Work seems to have finished"
	}
	return "Work seems to have finished on " + host
}

// notifyPaneWorkFinishedHandlers answers notify.paneWorkFinished.
// Constructed with its capability (the raiser), its registries (the session
// registry for attribution, the connection's session set for the liveness
// check) and its Responder — never the *WSServer.
type notifyPaneWorkFinishedHandlers struct {
	raiser   NotifyRaiser
	registry session.Registry
	state    *connState
	// tab is this connection's per-connection (per-tab) identity,
	// backend-assigned, monotonic and never reused — the tab half of the
	// backend-stamped attribution (ADR-0029 §4.6).
	tab string
	r   Responder
}

func (h notifyPaneWorkFinishedHandlers) handleNotifyPaneWorkFinished(ctx context.Context, req jsonrpcRequest) {
	if h.raiser == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "notify.paneWorkFinished not available"})
		return
	}
	params, err := decodeNotifyPaneWorkFinishedParams(req.Params)
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
	// request: kind and trust from the method invoked (notify.paneWorkFinished
	// IS the heuristic source of design §3), the title and level from nocx,
	// attribution from the session registry entry for the addressed id. The
	// frame that got here could carry exactly one field, so there is nothing
	// else any of it could have come from.
	out := h.raiser.Raise(ctx, notify.Event{
		SessionID: params.SessionID,
		Title:     paneWorkFinishedTitle(sess.Host()),
		Body:      "",
		Kind:      notify.KindPaneWorkFinished,
		Trust:     notify.TrustHeuristic,
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
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "notify.paneWorkFinished: " + out.Err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// notifyPaneWorkFinishedSpec declares the control method. It runs on the
// ordinary lane for the same reason notify.raise and notify.bell do: Raise is
// synchronous and can block on a sink invocation, so it must never run on the
// read loop.
func (s *WSServer) notifyPaneWorkFinishedSpec() methodSpec {
	return reg(s.lane, "notify.paneWorkFinished", params(validateNotifyPaneWorkFinishedRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
		return notifyPaneWorkFinishedHandlers{
			raiser:   s.notifyRaiser,
			registry: s.registry,
			state:    state,
			tab:      strconv.FormatUint(w.id, 10),
			r:        r,
		}.handleNotifyPaneWorkFinished
	})
}
