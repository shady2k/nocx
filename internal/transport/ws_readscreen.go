package transport

// readScreen — the broker's first production request (design §2.2's pull
// half of the terminal boundary): the agent reads a session's screen through
// the renderer, because the renderer owns the grid (AD-6). The request
// travels broker → renderer as agent.readScreenRequest; the renderer answers
// agent.readScreenResolved with the SAME frame shape it pushes for
// agent.captureFrame — same row/cell/attribute/identity vocabulary, produced
// by the same renderer code — plus a closed outcome so a renderer that
// cannot produce the frame (a session it does not know, a capture aborted by
// disposal) answers honestly instead of hanging the run.
//
// The broker mechanism (request_broker.go) owns id minting, correlation,
// timeouts and terminalization; this file is one RequestKind plus the
// WSServer's four seams for it: the Conns snapshot, the per-connection
// Deliver, the read-loop Resolve registration and the ConnectionLost signal
// in connection teardown (ws.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// readScreenRequestTimeout bounds one readScreen request: the renderer
// answers a capture in milliseconds, and a renderer that never answers must
// terminalize the run through the broker's timeout rather than leaking a
// pending request (design acceptance 14). Generous: the capture fence can
// legitimately wait for a large write to finish parsing.
const readScreenRequestTimeout = 30 * time.Second

// maxResolutionErrorRunes bounds the renderer's failure sentence on a
// failed readScreen outcome. A sentence a person (or the model) reads,
// never an unbounded string.
const maxResolutionErrorRunes = 512

// errReadScreenNoRenderer is the readScreen kind's no-client answer: there
// is no renderer attached to read the screen. Named, the way the vault and
// password asks name their no-client outcomes.
var errReadScreenNoRenderer = errors.New("no renderer connected to read the screen")

// ── wire shapes ────────────────────────────────────────────────────────────

// readScreenRequestParams is what the broker sends the renderer (with the
// minted requestId merged in — marshalWithRequestID). sessionId is the
// session whose screen is read; region, when present, is an absolute buffer
// row span [start, end); absent means the visible screen.
type readScreenRequestParams struct {
	SessionID string            `json:"sessionId"`
	Region    *readScreenRegion `json:"region,omitempty"`
}

type readScreenRegion struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// readScreenResolvedParams is the renderer's answer: a closed outcome —
// "frame", carrying the captured frame body, or "failed", carrying why.
// The frame fields reuse the captureFrame vocabulary (ws_agent.go): one
// frame shape for the push and the pull, per design §2.4.
type readScreenResolvedParams struct {
	Outcome  string             `json:"outcome"` // "frame" | "failed"
	Error    string             `json:"error,omitempty"`
	Rows     []frameRowWire     `json:"rows,omitempty"`
	Cursor   *frameCursorWire   `json:"cursor,omitempty"`
	Identity *frameIdentityWire `json:"identity,omitempty"`
	Range    *frameRangeWire    `json:"range,omitempty"`
}

// readScreenFrameBody is the resolved result the broker's Request decodes
// into: the frame body only, requestId and outcome consumed by the
// correlation. The assistant executor reads this shape (its own minimal
// consumer view) to build the tool's windowed return.
type readScreenFrameBody struct {
	Rows     []frameRowWire     `json:"rows"`
	Cursor   *frameCursorWire   `json:"cursor"`
	Identity *frameIdentityWire `json:"identity"`
	Range    *frameRangeWire    `json:"range"`
}

// ── the kind ──────────────────────────────────────────────────────────────

// readScreenKind is the request exchange for one screen read. The renderer
// half of the wire contract lives here, as data — the mechanism's RequestKind
// design — so the WSServer's RequestScreen writes no request machinery.
// The resolution bound is budgetDocument: a live frame legitimately carries
// every cell of a screen with its attributes, far beyond the 1 KiB that
// bounds the closed-outcome resolvers; the frame VALIDATION (rows ≤ 10k,
// cols ≤ 2k, 5M chars) is the shape bound and the wire budget is the size
// bound, both documented at their owners.
func readScreenKind() RequestKind {
	return RequestKind{
		NotifyMethod:       "agent.readScreenRequest",
		ResolveMethod:      "agent.readScreenResolved",
		NoClientErr:        errReadScreenNoRenderer,
		Timeout:            readScreenRequestTimeout,
		MaxResolutionBytes: budgetDocument,
		Resolve:            resolveReadScreen,
	}
}

// resolveReadScreen maps an accepted resolution to the frame body result, or
// to the terminal error of a failed capture. The outcome was validated on
// the ingress (validateReadScreenResolvedRaw), so this is the meaning of the
// outcome, not a second shape check.
func resolveReadScreen(raw json.RawMessage) (json.RawMessage, error) {
	var p readScreenResolvedParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("readScreen: resolution: %w", err)
	}
	if p.Outcome == "failed" {
		return nil, fmt.Errorf("readScreen: the renderer could not capture the screen: %s", p.Error)
	}
	body, err := json.Marshal(readScreenFrameBody{
		Rows: p.Rows, Cursor: p.Cursor, Identity: p.Identity, Range: p.Range,
	})
	if err != nil {
		return nil, fmt.Errorf("readScreen: frame body: %w", err)
	}
	return body, nil
}

// ── ingress validation ─────────────────────────────────────────────────────

// validateReadScreenResolvedRaw is the resolution's per-field shape check,
// applied on the read-loop ingress before the broker consumes the request
// (registered as the method's params validator AND run under the broker's
// lock for the resolution method itself): the closed outcome, the frame body
// when the outcome is frame, the failure sentence when it is failed. A
// refused resolution leaves the pending request in place for a corrected
// retry.
func validateReadScreenResolvedRaw(raw json.RawMessage) string {
	var p readScreenResolvedParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	switch p.Outcome {
	case "frame":
		if p.Error != "" {
			return "a frame outcome carries no error"
		}
		if msg, _ := validateLiveFrameBody(p.Rows, p.Cursor, p.Identity, p.Range); msg != "" {
			return msg
		}
		return ""
	case "failed":
		if p.Error == "" {
			return "a failed outcome requires an error"
		}
		if utf8.RuneCountInString(p.Error) > maxResolutionErrorRunes {
			return "error exceeds the length bound"
		}
		return ""
	default:
		return "outcome must be one of frame, failed"
	}
}

// ── the WSServer seams ─────────────────────────────────────────────────────

// RequestScreen implements assistant.RendererRequester: the transport side
// of the readScreen tool. The grant has already narrowed the session (the
// capability the executor holds refuses out-of-grant sessions BEFORE this
// call), so the request names only the session the run may read. The broker
// mints the request id, delivers the notification to every attached renderer
// and waits for the resolution — terminalizing through the kind's timeout or
// the death of the renderers if none answers.
func (s *WSServer) RequestScreen(ctx context.Context, sessionID string, region *assistant.FrameRegion) (json.RawMessage, error) {
	if s.broker == nil {
		return nil, errors.New("readScreen: no renderer request broker is wired")
	}
	params := readScreenRequestParams{SessionID: sessionID}
	if region != nil {
		params.Region = &readScreenRegion{Start: region.Start, End: region.End}
	}
	var body json.RawMessage
	if err := s.broker.Request(ctx, readScreenKind(), params, &body); err != nil {
		return nil, fmt.Errorf("readScreen: %w", err)
	}
	return body, nil
}

// brokerSpecs declares the broker's resolution RPCs on the read-loop
// ingress: an answer to a pending request must never wait behind the lane,
// or the requestor — a tool running under the ask stream — would deadlock
// behind the very work the renderer is unblocking. The same disposition the
// two existing resolvers register with.
func (s *WSServer) brokerSpecs(immediate control.ImmediateSubmission) []methodSpec {
	return []methodSpec{
		reg(immediate, "agent.readScreenResolved", params(validateReadScreenResolvedRaw),
			func(w *wsConn, _ *connState, r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) {
					perr := s.broker.Resolve("agent.readScreenResolved", req.Params, w)
					if perr.Code != 0 {
						_ = w.TryError(req.ID, perr)
						return
					}
					_ = w.TryResult(req.ID, json.RawMessage(`{}`))
				}
			}),
		s.runResolutionSpec(immediate),
	}
}

// ── the run's grant ────────────────────────────────────────────────────────

// runGrantFor mints the default grant of ONE ask run (ADR-0020 decision 5:
// authority is granted per run, immutable once execution starts). The policy
// is the matrix of the amended §7, resolved by content.ResolvePolicy — the
// ONE place the order is stated: the workspace override when nocx-mp2vd
// lands, the global default now (the store the composition root wired).
// The mint adds the run's OWN session as the base scope of every row — a run
// can touch the lane it lives in, and nothing else unless the policy says
// so — and the matrix derives the grant's effects and the declaration
// filter's scope union (EffectPolicy.AsGrant).
//
// This is the workspace's default grant: the workspace concept (which
// sessions read as one story) is not wired yet, so the single-session
// default is the honest first form, and the workspace bead rehosts the
// mint. Unset, the run carries no grant — the model is offered no tools,
// which is the state before readScreen AND the deliberate production state
// until a policy is named: naming one is the one-line flip this seam makes,
// and the readScreen over-the-wire tests prove the machinery with the
// policy named at the harness.
func (s *WSServer) runGrantFor(sessionID string) *content.Grant {
	if s.agentPolicy == nil {
		return nil
	}
	// The session's own answers overlay the global policy — an "allow in
	// this session" is in force from the answer until the session ends, and
	// the store (ws_sessionpolicy.go) is what ends it. The run grant's base
	// scope is already this session, so the overlay carries no scope of its
	// own: the run cannot reach outside its session anyway.
	p := content.ResolvePolicy(s.agentPolicy.Policy(), nil, s.sessionPolicy.For(session.ID(sessionID)))
	g := p.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sessionID}})
	return &g
}
