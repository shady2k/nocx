package transport

// The CLIENT HOST (nocx-uo1k6, design D3): the native-host capabilities the
// coordinator cannot perform itself, asked of an attached client.
//
// Until the daemon split the backend WAS the Wails process, so main.go could
// inject a Wails-backed DialogService, UrlOpener and AttentionHost straight
// into the composition root. The coordinator has no window now — it has zero
// or more attached clients, one of which may be able to show a dialog — so
// those seams are implemented by ASKING a client, over the same control plane
// as everything else (AD-1), and answering honestly when no client is
// attached.
//
// WHICH CLIENT SERVES AN ASK IS NOT DECIDED HERE. ADR-0026 §16 already
// decided it for the two existing asks and the broker implements it: an ask
// is delivered to every attached connection and the FIRST to resolve it wins,
// with `consume` guaranteeing there is exactly one answer. This file adds no
// routing, no priority and no ownership, because re-answering that question
// is the "second implementation of one concept" AGENTS.md calls a regression
// with a delay fuse. The consequence is stated rather than papered over: with
// two windows attached, both are asked, and the product is single-window
// today.
//
// Everything else about the exchange is ADR-0026's too: the request context
// derives from the asking task's, a disconnect terminalizes the ask through
// the broker's ConnectionLost, the native picker keeps its capacity-one
// WAITING gate in the dialog handler, and the shutdown drain cancels the
// asking task.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/transport/control"
)

// HostCapability names one native-host effect a client can perform for the
// coordinator. A CLOSED server vocabulary: the transport builds every value,
// the schema enumerates the same seven, and a client never invents one.
type HostCapability string

const (
	// HostCapOpenFile is the native file picker.
	HostCapOpenFile HostCapability = "dialog.file"
	// HostCapOpenDirectory is the native directory picker.
	HostCapOpenDirectory HostCapability = "dialog.directory"
	// HostCapOpenURL opens an http(s) URL in the platform browser.
	HostCapOpenURL HostCapability = "shell.openUrl"
	// HostCapBanner presents one desktop notification banner.
	HostCapBanner HostCapability = "attention.banner"
	// HostCapBadge sets the dock badge count.
	HostCapBadge HostCapability = "attention.badge"
	// HostCapBounce requests the attention bounce.
	HostCapBounce HostCapability = "attention.bounce"
	// HostCapFocusWindow brings the client's window to the front.
	HostCapFocusWindow HostCapability = "window.focus"
)

// ErrNoUIHost is the shared sentinel behind every "no UI host attached"
// answer. Each capability names its OWN error below so a caller can tell
// which surface is missing, and every one of them wraps this so a caller that
// only wants "there is nobody to ask" can say so in one test.
var ErrNoUIHost = errors.New("no UI host attached")

// The per-capability no-client answers. A daemon with no window attached must
// say so — never a hang, never a silent success, and never a value invented
// on the client's behalf.
var (
	// ErrNoDialogHost is dialog.openFile/openDirectory with nobody attached.
	ErrNoDialogHost = fmt.Errorf("%w to open a native dialog", ErrNoUIHost)
	// ErrNoURLHost is shell.openUrl with nobody attached.
	ErrNoURLHost = fmt.Errorf("%w to open a URL", ErrNoUIHost)
	// ErrNoAttentionHost is a banner, badge or bounce with nobody attached.
	ErrNoAttentionHost = fmt.Errorf("%w to present a desktop notification", ErrNoUIHost)
	// ErrNoWindowHost is a window raise with nobody attached.
	ErrNoWindowHost = fmt.Errorf("%w to focus a window", ErrNoUIHost)
)

// hostAskTimeout bounds the asks a client answers immediately: opening a URL,
// presenting a banner, setting the badge, raising the window. None of them
// waits on a person, so a client that has not answered in this long is not
// thinking — it is gone in a way its socket has not reported yet, and the
// caller gets an honest timeout instead of a wait with no end.
const hostAskTimeout = 15 * time.Second

// maxHostResolutionBytes bounds one host.resolved envelope. A resolution
// carries at most an absolute path (PATH_MAX is 4 KiB on Linux) or a bounded
// failure sentence, so the mechanism default of 1 KiB is too tight and the
// 64 KiB frame budget is far looser than anything this exchange can mean.
const maxHostResolutionBytes = 8 << 10 // 8 KiB

// maxHostErrorRunes bounds the client's failure sentence. A sentence a person
// reads in a toast or a log line, never an unbounded string.
const maxHostErrorRunes = 512

// maxHostPathRunes bounds a picked path. Generous against every platform's
// PATH_MAX so a legitimate deep path is never refused, and finite so a client
// cannot answer with a document.
const maxHostPathRunes = 8192

// ── wire shapes ────────────────────────────────────────────────────────────

// hostRequestParams is what the broker sends the client (with the minted
// requestId merged in — marshalWithRequestID). The members after capability
// are that capability's arguments; a capability that takes none sends none
// (contracts/host.request.schema.json).
type hostRequestParams struct {
	Capability HostCapability `json:"capability"`
	URL        string         `json:"url,omitempty"`
	Title      string         `json:"title,omitempty"`
	Body       string         `json:"body,omitempty"`
	SessionID  string         `json:"sessionId,omitempty"`
	Count      *int           `json:"count,omitempty"`
}

// hostResolvedParams is the client's answer: a closed outcome — "ok" (the
// effect happened, with a picker's chosen path), "cancelled" (a person
// dismissed a picker, which is an outcome and not a failure), "failed" with
// why, or "unavailable" — this client has no such native surface at all.
//
// The last two are not one outcome with two spellings. "failed" is an effect
// that was ATTEMPTED and did not happen: a denied permission, a thrown
// binding, a dead D-Bus. "unavailable" is a client that was never able to —
// a plain browser has no OS banner and never will — and it resolves to the
// same per-capability ErrNoUIHost as no client at all, because from the
// coordinator's side both mean there is nobody who can perform this. That
// distinction is load-bearing: notify's one exemption from the failure feed
// is written against absence, so folding absence into failure puts a "Not
// delivered" row in the notification centre for every notification a
// browser-hosted client is ever asked to present.
type hostResolvedParams struct {
	Outcome string `json:"outcome"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

// hostAnswerBody is the resolved result the broker's Request decodes into.
// The outcome itself is consumed by the correlation; what survives is what a
// caller can act on.
type hostAnswerBody struct {
	Path      string `json:"path"`
	Cancelled bool   `json:"cancelled"`
}

// HostAsk is one client-host request as its caller states it. The transport
// builds the wire params from it; a caller never writes JSON.
type HostAsk struct {
	// Capability is which effect is asked for. Required.
	Capability HostCapability
	// URL is the http(s) address for HostCapOpenURL. The transport has
	// already refused anything that is not one (ws_openurl.go).
	URL string
	// Title, Body and SessionID are HostCapBanner's. SessionID is what the
	// client hands back on a click (host.attentionActivated).
	Title, Body, SessionID string
	// Count is HostCapBadge's dock badge count; 0 clears it.
	Count int
}

// HostAnswer is what the client reported. Path is set only by a picker;
// Cancelled distinguishes a person dismissing a picker from an effect that
// happened, so a caller never has to read a dismissal out of an empty string
// alone.
type HostAnswer struct {
	Path      string
	Cancelled bool
}

// ── the kind ───────────────────────────────────────────────────────────────

// hostKind is the request exchange for one capability. Every capability
// shares one notification method and one resolution method — the exchange is
// identical in shape and only its argument members differ — and differs in
// exactly the two places the difference is real: the no-client error a caller
// receives, and how long the ask may wait.
func hostKind(cap HostCapability) RequestKind {
	return RequestKind{
		NotifyMethod:       "host.request",
		ResolveMethod:      "host.resolved",
		NoClientErr:        noHostErrFor(cap),
		Timeout:            hostTimeoutFor(cap),
		MaxResolutionBytes: maxHostResolutionBytes,
		Validate:           validateHostResolvedRaw,
		Resolve:            resolveHostAnswerFor(cap),
	}
}

// noHostErrFor names the capability whose surface is missing. One sentence
// per capability, all wrapping ErrNoUIHost.
func noHostErrFor(cap HostCapability) error {
	switch cap {
	case HostCapOpenFile, HostCapOpenDirectory:
		return ErrNoDialogHost
	case HostCapOpenURL:
		return ErrNoURLHost
	case HostCapBanner, HostCapBadge, HostCapBounce:
		return ErrNoAttentionHost
	case HostCapFocusWindow:
		return ErrNoWindowHost
	}
	return ErrNoUIHost
}

// hostTimeoutFor bounds the wait. A PICKER waits on a person and therefore
// has NO timeout of its own: it ends on the caller's context, on the death of
// every client that received it, or when the person acts — exactly the
// contract DialogService already documents for an adapter that cannot dismiss
// a shown picker, and the capacity-one waiting gate in ws_dialog.go is what
// stops a second one stacking meanwhile. Everything else is answered by code
// and is bounded.
func hostTimeoutFor(cap HostCapability) time.Duration {
	switch cap {
	case HostCapOpenFile, HostCapOpenDirectory:
		return 0
	}
	return hostAskTimeout
}

// resolveHostAnswerFor maps an accepted resolution to the answer body, or to
// the terminal error of a failed effect. The outcome was validated on the
// ingress (validateHostResolvedRaw), so this is the MEANING of the outcome,
// not a second shape check.
func resolveHostAnswerFor(cap HostCapability) func(json.RawMessage) (json.RawMessage, error) {
	return func(raw json.RawMessage) (json.RawMessage, error) {
		var p hostResolvedParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("resolution: %w", err)
		}
		switch p.Outcome {
		case "failed":
			return nil, fmt.Errorf("the UI host could not perform %s: %s", cap, p.Error)
		case "unavailable":
			// Absence, in the same words a missing client gets. The
			// client's sentence rides along when it sent one, so a log
			// still says which surface was missing and why.
			if p.Error == "" {
				return nil, noHostErrFor(cap)
			}
			return nil, fmt.Errorf("%w: %s", noHostErrFor(cap), p.Error)
		}
		body, err := json.Marshal(hostAnswerBody{
			Path:      p.Path,
			Cancelled: p.Outcome == "cancelled",
		})
		if err != nil {
			return nil, fmt.Errorf("answer body: %w", err)
		}
		return body, nil
	}
}

// ── ingress validation ─────────────────────────────────────────────────────

// validateHostResolvedRaw is the resolution's per-field shape check, applied
// on the read-loop ingress before the broker consumes the request AND under
// the broker's lock for the resolution method itself. A refused resolution
// leaves the pending request in place for a corrected retry, so a broken
// client cannot turn a garbage outcome into a silent ask failure.
func validateHostResolvedRaw(raw json.RawMessage) string {
	var p hostResolvedParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	switch p.Outcome {
	case "ok":
		if p.Error != "" {
			return "an ok outcome carries no error"
		}
		if utf8.RuneCountInString(p.Path) > maxHostPathRunes {
			return "path exceeds the length bound"
		}
		return ""
	case "cancelled":
		if p.Error != "" {
			return "a cancelled outcome carries no error"
		}
		if p.Path != "" {
			return "a cancelled outcome carries no path"
		}
		return ""
	case "failed":
		if p.Error == "" {
			return "a failed outcome requires an error"
		}
		if utf8.RuneCountInString(p.Error) > maxHostErrorRunes {
			return "error exceeds the length bound"
		}
		if p.Path != "" {
			return "a failed outcome carries no path"
		}
		return ""
	case "unavailable":
		// The sentence is optional here and required for "failed": a
		// failure has a reason only the client knows, while absence is
		// fully named by the capability the ask already carried.
		if utf8.RuneCountInString(p.Error) > maxHostErrorRunes {
			return "error exceeds the length bound"
		}
		if p.Path != "" {
			return "an unavailable outcome carries no path"
		}
		return ""
	default:
		return "outcome must be one of ok, cancelled, failed, unavailable"
	}
}

// hostAttentionActivatedParams is the client telling the coordinator that a
// person clicked a banner it presented.
type hostAttentionActivatedParams struct {
	SessionID string `json:"sessionId"`
}

// validateHostAttentionActivatedRaw is that notification's shape check: an
// addressing session id and nothing else.
func validateHostAttentionActivatedRaw(raw json.RawMessage) string {
	var p hostAttentionActivatedParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if utf8.RuneCountInString(p.SessionID) > maxIDRunes {
		return "sessionId exceeds the id length bound"
	}
	return ""
}

// ── the WSServer seams ─────────────────────────────────────────────────────

// RequestHost asks an attached client to perform one native-host capability
// and waits for its answer. It is the ONE entry point the client-host
// adapters (internal/app/clienthost) reach the wire through, so a capability
// implementation writes no request machinery at all.
//
// With no client attached it returns the capability's own no-client error,
// immediately. It never blocks forever on nobody: the broker terminalizes on
// the death of every recipient, on an undeliverable notification, on the
// caller's context and — for everything but a picker — on the kind's timeout.
func (s *WSServer) RequestHost(ctx context.Context, ask HostAsk) (HostAnswer, error) {
	if ask.Capability == "" {
		return HostAnswer{}, errors.New("host ask: no capability named")
	}
	if s.broker == nil {
		// A server built without a broker cannot ask anybody. Same shape of
		// answer as no client at all, because it is the same fact from the
		// caller's side: there is nobody to ask.
		return HostAnswer{}, noHostErrFor(ask.Capability)
	}
	params := hostRequestParams{
		Capability: ask.Capability,
		URL:        ask.URL,
		Title:      ask.Title,
		Body:       ask.Body,
		SessionID:  ask.SessionID,
	}
	if ask.Capability == HostCapBadge {
		// Sent only where it means something. A badge of zero CLEARS the
		// badge, so the field cannot be omitempty on the wire and is a
		// pointer here instead.
		count := ask.Count
		params.Count = &count
	}
	var body hostAnswerBody
	if err := s.broker.Request(ctx, hostKind(ask.Capability), params, &body); err != nil {
		return HostAnswer{}, err
	}
	// A conversion, not a field-by-field copy: the two types are the same
	// shape wearing different tags — one is the wire body, one is what a
	// caller reads — and the compiler is what keeps them so.
	return HostAnswer(body), nil
}

// AttentionActivation is told that a person activated a desktop notification
// a client presented. The transport does not act on the click itself: what
// happens next — which window is raised, which pane is focused — is the
// composition root's, and it is bound here as behaviour rather than as a
// destination (the same shape as notify.HostHolder's).
type AttentionActivation interface {
	// Activated reports one click. It runs on a control task goroutine, so
	// it may block on further asks; ctx is that task's.
	Activated(ctx context.Context, sessionID string)
}

// attentionActivationHolder is the transport's mutable activation seam: the
// mutex and the sink it guards, read per call the way the dialog and
// url-opener holders are.
type attentionActivationHolder struct {
	mu   *sync.RWMutex
	sink *AttentionActivation
}

func (h *attentionActivationHolder) get() AttentionActivation {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return *h.sink
}

// SetAttentionActivation binds what happens when a person clicks a banner.
// Wired at the composition root before Start, so no click can observe the
// unset state; a server that never binds one logs the click and does nothing,
// which is the honest degrade for a host with no notification surface at all.
func (s *WSServer) SetAttentionActivation(sink AttentionActivation) {
	s.attentionActivationMu.Lock()
	defer s.attentionActivationMu.Unlock()
	s.attentionActivation = sink
}

// clientHostHandlers answers the client-host ingress: the resolution of a
// pending ask, and the notification that a banner was clicked.
type clientHostHandlers struct {
	broker     *Broker
	activation *attentionActivationHolder
	log        log.Logger
	r          Responder
}

// clientHostSpecs declares the client-host ingress.
//
// host.resolved is INGRESS-CRITICAL and the reason is ADR-0026 §2's, exactly:
// the asking task blocks on the answer while holding a lane permit, so an
// answer queued behind a full lane would deadlock the ask against the very
// work the client is unblocking. Same disposition as every other resolver.
//
// host.attentionActivated is NOT: acting on a click issues further asks and
// blocks, which is precisely what may never happen on the read loop.
func (s *WSServer) clientHostSpecs(immediate control.ImmediateSubmission, lane control.Submission) []methodSpec {
	holder := &attentionActivationHolder{mu: &s.attentionActivationMu, sink: &s.attentionActivation}
	return []methodSpec{
		reg(immediate, "host.resolved", params(validateHostResolvedRaw),
			func(w *wsConn, _ *connState, r Responder) handlerFunc {
				h := clientHostHandlers{broker: s.broker, activation: holder, log: s.log, r: r}
				return func(_ context.Context, req jsonrpcRequest) {
					h.handleHostResolved(w, req)
				}
			}),
		reg(lane, "host.attentionActivated", params(validateHostAttentionActivatedRaw),
			func(_ *wsConn, _ *connState, r Responder) handlerFunc {
				h := clientHostHandlers{broker: s.broker, activation: holder, log: s.log, r: r}
				return h.handleAttentionActivated
			}),
	}
}

// handleHostResolved hands one answer to the broker. The broker owns
// correlation, the recipient check and the consume; a refusal leaves the
// pending ask alive for a corrected retry.
func (h clientHostHandlers) handleHostResolved(w *wsConn, req jsonrpcRequest) {
	if h.broker == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Unknown request id"})
		return
	}
	if perr := h.broker.Resolve("host.resolved", req.Params, w); perr.Code != 0 {
		_ = h.r.TryError(req.ID, perr)
		return
	}
	_ = h.r.TryResult(req.ID, json.RawMessage(`{}`))
}

// handleAttentionActivated passes one banner click to the composition root.
// It is a notification, so there is no response to write; the client is told
// nothing about where the focus landed, because it is not the client's
// decision (AD-3).
func (h clientHostHandlers) handleAttentionActivated(ctx context.Context, req jsonrpcRequest) {
	var p hostAttentionActivatedParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.SessionID == "" {
		// The validator already refused this shape on the ingress; a
		// notification has no response to carry a second refusal.
		return
	}
	sink := h.activation.get()
	if sink == nil {
		// Said out loud: a person clicked a banner and nothing moved.
		h.log.Debug("notification click dropped: no activation sink is wired", "session", p.SessionID)
		return
	}
	sink.Activated(ctx, p.SessionID)
}
