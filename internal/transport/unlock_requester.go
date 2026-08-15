package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/transport/control"
)

// ── resolver ingress validators (the per-field sweep) ─────────────────────
//
// The two resolver RPCs are ingress-critical (they run on the read loop) and
// their params are tiny by budget (budgetTiny, 1 KiB), so the validators
// carry the per-field shape: the ask's request id and its closed outcome
// enum. The ask broker mints request ids as 16 hex chars; the id is checked
// for presence and bound, not shape — a stale id must still reach the
// handler's consume, whose "unknown request id" answer is the honest one for
// an ask that timed out or was dropped.

// validateUnlockResolvedRaw checks vault.unlockResolved. The outcome is a
// closed enum. The handler currently resolves an unknown outcome by ERRORING
// the pending ask; refusing it here instead means the pending ask waits for
// a corrected retry or its timeout — a broken renderer cannot turn a garbage
// outcome into a silent ask failure.
func validateUnlockResolvedRaw(raw json.RawMessage) string {
	var p struct {
		RequestID string `json:"requestId"`
		Outcome   string `json:"outcome"`
	}
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.RequestID == "" {
		return "requestId is required"
	}
	if utf8.RuneCountInString(p.RequestID) > maxIDRunes {
		return "requestId exceeds the id length bound"
	}
	switch p.Outcome {
	case "unsealed", "cancelled":
	default:
		return "outcome must be one of unsealed, cancelled"
	}
	return ""
}

// validatePasswordResolvedRaw checks connections.passwordResolved: the ask's
// request id, its closed outcome enum, and the submitted password. The
// password becomes the SSH answer — a credential, so it gets the same two
// rules the probe path applies to its key (ws_assistant.go): bounded, and no
// control character (a newline in a password would corrupt the auth
// exchange at the worst possible place). An empty submitted password stays
// accepted: the handler forwards it as-is, and the ssh layer is the one that
// decides what an empty answer means.
func validatePasswordResolvedRaw(raw json.RawMessage) string {
	var p struct {
		RequestID string `json:"requestId"`
		Outcome   string `json:"outcome"`
		Password  string `json:"password,omitempty"`
		Remember  bool   `json:"remember,omitempty"`
	}
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.RequestID == "" {
		return "requestId is required"
	}
	if utf8.RuneCountInString(p.RequestID) > maxIDRunes {
		return "requestId exceeds the id length bound"
	}
	switch p.Outcome {
	case "submitted", "cancelled":
	default:
		return "outcome must be one of submitted, cancelled"
	}
	if utf8.RuneCountInString(p.Password) > maxProbeKeyRunes {
		return "password exceeds the length bound"
	}
	if hasControlChars(p.Password) {
		return "password must not contain control characters"
	}
	return ""
}

// askBroker is the shared backend→renderer ask machinery: a pending
// registry keyed by server-assigned request id, a broadcast to every
// connected client, and a blocking wait for the resolution RPC. The vault
// unlock ask (UnlockRequester) and the connection-password ask
// (RequestConnectionPassword) are both thin specializations over it — one
// correlation mechanism, two meanings. A connection password is not the
// vault passphrase, so the two asks keep their own methods, params and
// error types; only the plumbing is shared.
type askBroker struct {
	mu      sync.Mutex
	pending map[string]*pendingAsk
}

// pendingAsk tracks one in-flight ask.
type pendingAsk struct {
	ch chan askResolution
}

// askResolution is one answer to a pending ask: either a result payload
// (the resolved answer) or an error (cancelled, no client, timeout,
// unknown outcome).
type askResolution struct {
	result json.RawMessage // nil when err != nil
	err    error
}

func (b *askBroker) init() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = make(map[string]*pendingAsk)
	}
}

// register mints a request id and records the pending ask. The returned
// channel receives exactly one askResolution (buffered, so the resolver
// never blocks on the waiter).
func (b *askBroker) register() (string, chan askResolution, error) {
	b.init()
	ridBytes := make([]byte, 8)
	if _, err := rand.Read(ridBytes); err != nil {
		return "", nil, fmt.Errorf("generate request id: %w", err)
	}
	rid := hex.EncodeToString(ridBytes)
	ch := make(chan askResolution, 1)
	b.mu.Lock()
	b.pending[rid] = &pendingAsk{ch: ch}
	b.mu.Unlock()
	return rid, ch, nil
}

// consume removes and returns the pending ask for rid. Returns ok=false
// for an unknown id — the renderer resolved something that was never
// asked, or asked twice (the second resolution is the error).
func (b *askBroker) consume(rid string) (*pendingAsk, bool) {
	b.init()
	b.mu.Lock()
	defer b.mu.Unlock()
	pa, ok := b.pending[rid]
	if ok {
		delete(b.pending, rid)
	}
	return pa, ok
}

// drop abandons the pending ask for rid (no client connected, context
// done) so a late resolution cannot wake a waiter nobody is listening to.
func (b *askBroker) drop(rid string) {
	b.init()
	b.mu.Lock()
	delete(b.pending, rid)
	b.mu.Unlock()
}

// broadcastAsk sends a notification to every connected client. noClientErr
// is the error to return when no renderer is attached — each ask names its
// own outcome ("unlock prompt" vs "connection password"), because the three
// outcomes of an ask must be distinguishable by their message.
func (s *WSServer) broadcastAsk(method string, params map[string]any, noClientErr error) error {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	if len(conns) == 0 {
		return noClientErr
	}

	// One enqueue per connection, never a blocking write: an ask to N
	// renderers costs N channel sends, not N write deadlines.
	for _, wc := range conns {
		_ = wc.TryNotify(method, mustMarshal(params))
	}
	return nil
}

// UnlockRequester lets backend code request a vault unlock from the user.
// A single method, behind an interface (AD-8), wired at the one composition
// root. Implemented by *WSServer: RequestUnlock sends a vault.unlockRequest
// notification to connected clients and blocks until one responds or the
// context is done.
type UnlockRequester interface {
	// RequestUnlock asks any connected renderer to show the vault unlock
	// dialog. The reason names why (e.g. "history needs the content key")
	// so the dialog can say what needs the unlock, not only that it is
	// locked. Blocks until a client responds via vault.unlockResolved, or
	// the context is done. Returns nil on success, or an error describing
	// why the unlock could not complete (no client connected, user
	// cancelled, timeout, etc.).
	RequestUnlock(ctx context.Context, reason string) error
}

// ErrNoClientConnected is returned by RequestUnlock when no renderer is
// attached to receive the notification.
var ErrNoClientConnected = errors.New("no client connected to show unlock prompt")

// ErrUnlockCancelled is returned by RequestUnlock when the user dismissed
// the unlock dialog without unlocking.
var ErrUnlockCancelled = errors.New("unlock cancelled by user")

// RequestUnlock sends a vault.unlockRequest notification to every connected
// client and blocks until one responds via vault.unlockResolved, or the
// context is done.
func (s *WSServer) RequestUnlock(ctx context.Context, reason string) error {
	rid, ch, err := s.asks.register()
	if err != nil {
		return err
	}

	if err := s.broadcastAsk("vault.unlockRequest", map[string]any{
		"requestId": rid,
		"reason":    reason,
	}, ErrNoClientConnected); err != nil {
		s.asks.drop(rid)
		return err
	}

	// Wait for a response or context done.
	select {
	case res := <-ch:
		return res.err
	case <-ctx.Done():
		s.asks.drop(rid)
		return ctx.Err()
	}
}

// askResolverHandlers answers the two resolver RPCs (vault.unlockResolved,
// connections.passwordResolved). They are ingress-critical: the asks they
// resolve (RequestUnlock, RequestConnectionPassword) block until the
// resolution arrives over the same socket the read loop consumes, so the
// resolvers run inline via the ImmediateSubmission and never queue behind
// the lane (registration.go). They hold the ask broker (transport-owned
// state — the migration map's "Ask machinery" row) and the Responder; no
// capability, no stores.
type askResolverHandlers struct {
	asks *askBroker
	r    Responder
}

// handleUnlockResolved handles the vault.unlockResolved RPC from the
// renderer: it looks up the pending request and signals its channel.
func (h askResolverHandlers) handleUnlockResolved(req jsonrpcRequest) {
	var params struct {
		RequestID string `json:"requestId"`
		Outcome   string `json:"outcome"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	pa, ok := h.asks.consume(params.RequestID)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Unknown request id"})
		return
	}

	switch params.Outcome {
	case "unsealed":
		pa.ch <- askResolution{}
	case "cancelled":
		pa.ch <- askResolution{err: ErrUnlockCancelled}
	default:
		pa.ch <- askResolution{err: fmt.Errorf("unlock resolved with unknown outcome: %q", params.Outcome)}
	}

	_ = h.r.TryResult(req.ID, json.RawMessage("{}"))
}

// askResolverSpecs declares the two resolver methods, both ingress-critical
// (ImmediateSubmission): a resolution must never wait for a lane permit.
func (s *WSServer) askResolverSpecs(immediate control.ImmediateSubmission) []methodSpec {
	return []methodSpec{
		reg(immediate, "vault.unlockResolved", params(validateUnlockResolvedRaw), func(w *wsConn, _ *connState, r Responder) handlerFunc {
			h := askResolverHandlers{asks: &s.asks, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleUnlockResolved(req) }
		}),
		reg(immediate, "connections.passwordResolved", params(validatePasswordResolvedRaw), func(w *wsConn, _ *connState, r Responder) handlerFunc {
			h := askResolverHandlers{asks: &s.asks, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePasswordResolved(req) }
		}),
	}
}
