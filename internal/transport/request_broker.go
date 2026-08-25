package transport

// The server→client request broker (nocx-e2j1z): a generic mechanism for a
// backend caller to ask the renderer to do something and receive the answer,
// without re-implementing id minting, correlation, timeouts or ingress
// validation. The pattern it generalises is already written twice in this
// repo — vault.unlockRequest/unlockResolved and
// connections.passwordRequest/passwordResolved, brokered in
// unlock_requester.go — and §2.4 of the agent-tools design
// (.internal/specs/2026-08-16-agent-tools-design.md) makes this the third
// time as the mechanism rather than a third copy: JSON-RPC 2.0 is
// bidirectional by construction, and LSP is the precedent AD-1 names for
// choosing it.
//
// The broker is deliberately standalone: it owns the request lifecycle and
// nothing else. A transport wires it with four seams, so the mechanism is
// provable without production integration and the renderer half of the wire
// contract lands with the first real effect (a later bead):
//
//   - a Conns function (constructor-injected) that snapshots the renderer
//     connections currently attached, and a Deliver function that sends one
//     request notification to one connection and reports when the delivery
//     failed — the WSServer's connection set and per-connection enqueue in
//     production, a per-connection write in a test;
//   - Resolve, the read-loop ingress a transport's dispatch calls with a
//     resolution RPC's method, params and the connection that answered;
//   - ConnectionLost, the lifecycle signal a transport calls when a
//     connection dies.
//
// The delivery order is load-bearing: Request arms the pending request with
// its snapshot recipients BEFORE a single notification is delivered, so the
// moment the renderer can learn a request id, the request is already
// resolvable — a fast renderer's answer can never be refused as unknown
// because the broker had not finished registering.
//
// The two existing brokers are NOT rewritten here; request_broker_test.go
// proves the mechanism can express one of them.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"
)

// Conn is the broker's identity for one renderer connection. The transport
// hands the broker the same handle it uses for connection teardown, so a
// dead connection is recognisable in every pending request's recipient set.
//
// The handle MUST be comparable (a pointer): the broker stores recipients in
// a map keyed by Conn, and an unhashable value would panic at the insert.
// Request refuses a non-comparable recipient at the seam — a transport error,
// never a panic — and Resolve and ConnectionLost guard their lookups the
// same way, so an inconsistent transport cannot crash the read loop either.
type Conn any

// Conns snapshots the renderer connections currently attached. The broker
// calls it at the start of Request; the returned connections are the
// request's recipients — the only connections that can resolve it.
type Conns func() []Conn

// Deliver sends one request notification to one connection. It returns an
// error when the notification did not reach the renderer (a full or refused
// outbound queue, a connection closing mid-write): such a connection cannot
// answer the request, so the broker stops counting it as a recipient, and a
// request no delivery reaches is terminalized with ErrRequestUndelivered
// instead of waiting for a timeout that may not exist.
type Deliver func(conn Conn, method string, params json.RawMessage) error

// RequestKind declares one server→client request exchange: the notification
// method sent to the renderer, the RPC method the renderer answers with, and
// the meaning of a resolution. The meaning lives here, as data — how an
// outcome becomes a result payload or a terminal error — so a caller using
// the broker writes no request machinery at all.
//
// NotifyMethod and ResolveMethod are the only per-ask names; the renderer
// half of the wire contract for a real effect is decided by the bead that
// lands it, not by this mechanism.
type RequestKind struct {
	// NotifyMethod is the server→client notification that carries the
	// request (with requestId merged into its params), e.g.
	// "vault.unlockRequest".
	NotifyMethod string
	// ResolveMethod is the client→server RPC the renderer answers with,
	// e.g. "vault.unlockResolved". The transport's dispatch routes this
	// method's frames to Broker.Resolve, and the broker binds a resolution
	// to it: taking one kind's request id and submitting it through another
	// kind's *Resolved method never consumes the request.
	ResolveMethod string
	// NoClientErr is returned by Request when no renderer is attached to
	// receive the notification. Each ask names its own outcome, the way
	// ErrNoClientConnected and ErrPasswordNoClientConnected already do.
	// Required.
	NoClientErr error
	// Timeout bounds how long Request waits for the resolution. Zero waits
	// on the caller's context alone — but a request that can never be
	// answered also ends at the death of its recipients (ConnectionLost) or
	// when no delivery reaches any of them (ErrRequestUndelivered), so a
	// caller can never hang behind a silent renderer.
	Timeout time.Duration
	// MaxResolutionBytes bounds one resolution RPC's params FOR THIS KIND.
	// Zero means the mechanism default (maxResolutionBytes, 1 KiB — the
	// bound the two existing closed-outcome resolvers run under). A kind
	// whose resolution legitimately carries a payload (readScreen's frame)
	// declares its own bound here, and the broker enforces it on the
	// read-loop ingress, before the pending request is consumed — the
	// renderer cannot make the broker ingest an unbounded answer by
	// volume.
	MaxResolutionBytes int
	// Validate is the per-field shape check on a resolution's params,
	// applied on the read-loop ingress before the pending request is
	// consumed (the discipline validateUnlockResolvedRaw already applies to
	// the two existing resolvers). It may be nil when the envelope — the
	// requestId and the size bound — is all there is to check.
	Validate func(raw json.RawMessage) string
	// Resolve maps an accepted resolution's params to the result payload
	// Request decodes into the caller's typed result, or to a terminal
	// error (the ask's cancelled outcome, say). Required.
	Resolve func(raw json.RawMessage) (json.RawMessage, error)
}

// resolutionBound is the size bound this kind's resolutions are held to:
// the kind's own declared bound, or the mechanism default when the kind
// declares none.
func (k RequestKind) resolutionBound() int {
	if k.MaxResolutionBytes > 0 {
		return k.MaxResolutionBytes
	}
	return maxResolutionBytes
}

// ErrRequestTimedOut is returned by Request when the kind's Timeout elapses
// before the renderer resolves. One of the terminal reasons a request can
// end; the pending id is dropped, so a late resolution is answered with
// "Unknown request id".
var ErrRequestTimedOut = errors.New("request timed out waiting for the renderer")

// ErrRequestDisconnected is returned by Request when every connection that
// received the request notification has died without answering — including
// a connection that died between the recipient snapshot and the request
// being armed, which could never have answered. The request can never be
// resolved, so it is terminalized rather than left to hang its caller until
// a timeout that may be far away or absent.
var ErrRequestDisconnected = errors.New("renderer disconnected before answering the request")

// ErrRequestUndelivered is returned by Request when the request notification
// could not be delivered to any renderer: connections were attached, but
// every delivery failed (a full or refused outbound queue, connections
// closing mid-write). Distinct from the kind's no-client error (no renderer
// at all) and from ErrRequestDisconnected (a renderer that received the
// notification died).
var ErrRequestUndelivered = errors.New("request notification could not be delivered to any renderer")

// maxResolutionBytes bounds one resolution RPC's params for a kind that
// declares no bound of its own. A resolution answers a request minted
// moments ago; its payload is a closed outcome and at most a small result.
// The universal maxParamsBytes is the floor every control method gets; a
// resolution is bounded far tighter (the budgetTiny, 1 KiB, the two existing
// resolvers already run under), so a renderer cannot make the broker ingest
// an unbounded answer by volume. A kind whose resolution legitimately
// carries a payload (readScreen's frame) declares a larger bound on its
// RequestKind; the broker learns it at register and enforces it per method.
const maxResolutionBytes = 1 << 10 // 1 KiB

// Broker coordinates server→client requests. It is safe for concurrent use:
// several requests may be in flight at once, each keyed by its own minted
// request id.
type Broker struct {
	conns   Conns
	deliver Deliver
	mu      sync.Mutex
	pending map[string]*pendingRequest
	// methodBounds records the largest resolution size bound declared by a
	// registered request, keyed by its ResolveMethod. The size check in
	// Resolve reads it BEFORE the pending lookup, so an oversized envelope
	// is refused at the cheapest point — no request ever needs to be
	// pending for the bound to hold. A method that never registered a
	// request falls back to the mechanism default.
	methodBounds map[string]int
}

// pendingRequest tracks one in-flight request. It is resolvable from before
// the notification is delivered (its id is registered and it is armed under
// the broker's lock first) until its result, its timeout, its undeliverable
// state, or the death of every recipient — the span, stated with both ends.
type pendingRequest struct {
	kind       RequestKind
	ch         chan resolution
	recipients map[Conn]struct{} // nil until armed
	lost       map[Conn]struct{} // recipients whose death landed before the arm
}

// resolution is one answer to a pending request: either a result payload
// (the resolved answer, decoded into the caller's typed result by Request)
// or an error (the ask's cancelled outcome, a timeout, an undelivered or
// disconnected request).
type resolution struct {
	result json.RawMessage // nil when err != nil
	err    error
}

// NewBroker constructs the mechanism with its delivery seams. conns and
// deliver must not be nil: a broker that cannot reach a renderer cannot
// answer anything, and discovering that at the first Request would be a
// panic in a caller's goroutine for a wiring bug that belongs at
// construction.
func NewBroker(conns Conns, deliver Deliver) *Broker {
	if conns == nil || deliver == nil {
		panic("nocx: request broker requires conns and deliver seams")
	}
	return &Broker{conns: conns, deliver: deliver, pending: make(map[string]*pendingRequest), methodBounds: make(map[string]int)}
}

// Request issues one server→client request. It mints a request id, merges it
// into params, snapshots the current renderer connections, arms the pending
// request with those recipients, delivers the notification to each, and
// waits for the resolution RPC — until the result, the kind's Timeout, the
// caller's context, or the death or undeliverability of every recipient.
// result must be non-nil; the resolution's payload is decoded into it.
//
// Arming precedes delivery: the request is resolvable from before the
// notification, so the renderer can never answer faster than the broker can
// correlate. A caller whose context is already cancelled performs no
// delivery at all; a context cancelled after delivery still terminalizes the
// pending request through the wait below.
func (b *Broker) Request(ctx context.Context, kind RequestKind, params any, result any) error {
	// A caller that is already terminal must not cause a delivery: the
	// renderer would be asked to perform an effect for a request nobody is
	// waiting for. Cancellation after this point is observed by the select
	// below, which drops the pending request and returns ctx.Err().
	if err := ctx.Err(); err != nil {
		return err
	}
	if kind.Resolve == nil {
		return fmt.Errorf("request kind %q has no resolve mapping", kind.NotifyMethod)
	}
	if kind.NoClientErr == nil {
		return fmt.Errorf("request kind %q has no no-client error", kind.NotifyMethod)
	}
	if result == nil {
		return errors.New("request result must be a non-nil pointer")
	}

	rid, ch, err := b.register(kind)
	if err != nil {
		return err
	}
	payload, err := marshalWithRequestID(rid, params)
	if err != nil {
		b.drop(rid)
		return fmt.Errorf("marshal request params: %w", err)
	}

	// The recipient snapshot is taken after the id exists but before any
	// delivery, and the request is armed with the connections the
	// notification will reach — never a subset, never a stale superset that
	// outlives a death.
	recipients := b.conns()
	if len(recipients) == 0 {
		b.drop(rid)
		return kind.NoClientErr
	}
	// The handles must be comparable: the broker stores recipients in a map
	// keyed by Conn, and an unhashable value would panic at the insert. A
	// transport handing over a slice or a map is a wiring bug, and it
	// surfaces as a request error here, not as a panic inside arm.
	for _, c := range recipients {
		if !comparableConn(c) {
			b.drop(rid)
			return fmt.Errorf("request recipient %T is not comparable: the transport must hand the broker comparable connection handles", c)
		}
	}

	b.arm(rid, recipients)
	for _, c := range recipients {
		if err := b.deliver(c, kind.NotifyMethod, payload); err != nil {
			// The notification never reached this connection, so it cannot
			// answer: it stops being a recipient. When no delivery lands at
			// all, the last prune terminalizes the request with
			// ErrRequestUndelivered.
			b.pruneRecipient(rid, c)
		}
	}

	var timerC <-chan time.Time
	if kind.Timeout > 0 {
		timer := time.NewTimer(kind.Timeout)
		defer timer.Stop()
		timerC = timer.C
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if err := json.Unmarshal(res.result, result); err != nil {
			return fmt.Errorf("decode request result: %w", err)
		}
		return nil
	case <-timerC:
		b.drop(rid)
		return ErrRequestTimedOut
	case <-ctx.Done():
		b.drop(rid)
		return ctx.Err()
	}
}

// Pending reports how many requests are currently in flight. A test's
// assertion that terminalization leaves none behind.
func (b *Broker) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// Resolve handles one resolution RPC on the read-loop ingress. The
// transport's dispatch calls it with the RPC's method, its raw params and
// the connection that answered. A returned RPCError with a non-zero Code
// means the RPC is refused and the pending request is untouched — a
// corrected retry can still resolve it, and a broken renderer cannot turn a
// garbage outcome into a silent ask failure. A zero Code means the
// resolution was accepted and the pending request has been signalled with
// its result or its mapped error.
//
// The envelope's requestId is read first (presence and bound — the
// discipline already applied to renderer params). The SIZE bound is
// per-kind, learned from the requests registered for this resolution
// method, and applied BEFORE the lookup — an oversized envelope is refused
// at the cheapest point whether or not its id is live: a kind whose
// resolution carries a payload (readScreen's frame) declares its own bound
// at register, while the closed-outcome asks stay at the tight mechanism
// default. A request that was never minted, already resolved, timed out,
// resolved through a different method than its kind declared, or answered
// by a connection that never received the notification is all answered the
// same honest way: "Unknown request id", and nothing is resolved.
func (b *Broker) Resolve(method string, params json.RawMessage, conn Conn) RPCError {
	var env struct {
		RequestID string `json:"requestId"`
	}
	if msg := decodeParams(params, &env); msg != "" {
		return RPCError{Code: -32602, Message: msg}
	}
	if env.RequestID == "" {
		return RPCError{Code: -32602, Message: "requestId is required"}
	}
	if utf8.RuneCountInString(env.RequestID) > maxIDRunes {
		return RPCError{Code: -32602, Message: "requestId exceeds the id length bound"}
	}
	if !comparableConn(conn) {
		// Such a connection could never have been armed as a recipient (the
		// seam refused it), and a lookup keyed by it would panic. The honest
		// answer is the same as for a connection that never saw the
		// request.
		return RPCError{Code: -32602, Message: "Unknown request id"}
	}

	b.mu.Lock()
	if len(params) > b.boundFor(method) {
		// The method's declared bound: the largest a registered request for
		// this resolution method declared. Refused before the lookup, so a
		// broken renderer cannot make the read loop spend real work on an
		// envelope the request's own kind would reject.
		b.mu.Unlock()
		return RPCError{Code: -32602, Message: "resolution params exceed the size bound"}
	}
	p, ok := b.pending[env.RequestID]
	if !ok {
		b.mu.Unlock()
		return RPCError{Code: -32602, Message: "Unknown request id"}
	}
	if method != p.kind.ResolveMethod {
		// A resolution is bound to the RPC method its kind declared: taking
		// one kind's request id and submitting it through another kind's
		// *Resolved method must not consume it. Same answer as an unknown
		// id, deliberately: whether the id is live is not for this caller.
		b.mu.Unlock()
		return RPCError{Code: -32602, Message: "Unknown request id"}
	}
	if _, ok := p.recipients[conn]; !ok {
		// The answering connection never received the notification, so it
		// cannot know what it is resolving. The same answer as an unknown
		// id, deliberately: whether the id is live is not for this caller.
		b.mu.Unlock()
		return RPCError{Code: -32602, Message: "Unknown request id"}
	}
	// The kind's per-field check runs under the lock, before the consume:
	// a refused resolution must leave the pending request in place for a
	// corrected retry or its timeout. Validators are pure field checks —
	// bounded, non-blocking — so holding the lock for them is safe.
	if p.kind.Validate != nil {
		if msg := p.kind.Validate(params); msg != "" {
			b.mu.Unlock()
			return RPCError{Code: -32602, Message: msg}
		}
	}
	delete(b.pending, env.RequestID)
	b.mu.Unlock()

	// The outcome mapping is the request's own meaning and runs outside the
	// lock: at most one goroutine signals p.ch (the consume above removed
	// the pending, and the channel is buffered), so the send never blocks.
	result, err := p.kind.Resolve(params)
	if err != nil {
		p.ch <- resolution{err: err}
	} else {
		p.ch <- resolution{result: result}
	}
	return RPCError{}
}

// ConnectionLost is the lifecycle signal: the transport calls it when a
// connection dies. Every pending request that counted the connection as a
// recipient loses a possible answerer; a request with no surviving recipient
// can never be resolved — nobody who saw the notification is alive — so it
// is terminalized with ErrRequestDisconnected. Other connections, and the
// requests they can still answer, are untouched.
//
// A pending request that is registered but not yet armed (its death landed
// between the recipient snapshot and the arm) cannot lose a recipient it
// does not have yet — but it must not be armed with the dead connection
// either, or it could wait forever without a timeout. So the death is
// recorded on the pending request and arm skips it, terminalizing the
// request when no snapshot recipient survived.
func (b *Broker) ConnectionLost(conn Conn) {
	if !comparableConn(conn) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for rid, p := range b.pending {
		if p.recipients == nil {
			// Registered but not yet armed: the death landed between the
			// recipient snapshot and the arm.
			if p.lost == nil {
				p.lost = make(map[Conn]struct{})
			}
			p.lost[conn] = struct{}{}
			continue
		}
		if _, ok := p.recipients[conn]; !ok {
			continue
		}
		delete(p.recipients, conn)
		if len(p.recipients) == 0 {
			delete(b.pending, rid)
			p.ch <- resolution{err: ErrRequestDisconnected}
		}
	}
}

// register mints a request id and records the pending request. The returned
// channel receives exactly one resolution (buffered, so the resolver never
// blocks on the waiter). The kind's resolution size bound is recorded for
// its method here — the pre-lookup size check of Resolve reads it, so an
// oversized envelope is refused before the pending map is even consulted.
func (b *Broker) register(kind RequestKind) (string, chan resolution, error) {
	ridBytes := make([]byte, 8)
	if _, err := rand.Read(ridBytes); err != nil {
		return "", nil, fmt.Errorf("generate request id: %w", err)
	}
	rid := hex.EncodeToString(ridBytes)
	ch := make(chan resolution, 1)
	b.mu.Lock()
	b.pending[rid] = &pendingRequest{kind: kind, ch: ch}
	if kind.ResolveMethod != "" {
		if bound := kind.resolutionBound(); bound > b.methodBounds[kind.ResolveMethod] {
			b.methodBounds[kind.ResolveMethod] = bound
		}
	}
	b.mu.Unlock()
	return rid, ch, nil
}

// boundFor is the size bound Resolve applies to one resolution method's
// envelopes: the largest bound any registered request for the method
// declared, or the mechanism default for a method no request was ever
// registered for.
func (b *Broker) boundFor(method string) int {
	if bound, ok := b.methodBounds[method]; ok {
		return bound
	}
	return maxResolutionBytes
}

// arm records which connections received the notification. It is called
// before any delivery, so from the moment a notification is on the wire the
// request is resolvable by its recipients. A connection whose death was
// reported between the snapshot and this arm (pendingRequest.lost) is not
// armed: it cannot answer, and arming it would leave the request waiting on
// a dead connection with nothing left to terminalize it. When no snapshot
// recipient survived, the request is terminalized here with
// ErrRequestDisconnected.
func (b *Broker) arm(rid string, recipients []Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[rid]
	if !ok {
		return
	}
	p.recipients = make(map[Conn]struct{}, len(recipients))
	for _, c := range recipients {
		if _, dead := p.lost[c]; dead {
			continue // died between the snapshot and this arm
		}
		p.recipients[c] = struct{}{}
	}
	p.lost = nil // consumed by this arm
	if len(p.recipients) == 0 {
		// Every snapshot recipient died before the arm: nobody can answer,
		// so terminalize rather than leave the caller hanging without a
		// timeout.
		delete(b.pending, rid)
		p.ch <- resolution{err: ErrRequestDisconnected}
	}
}

// pruneRecipient removes one connection from a pending request's recipients
// after its delivery failed: it cannot answer what it never received. When
// the last recipient is pruned, the request is terminalized with
// ErrRequestUndelivered. A no-op when the pending is gone (a resolution won
// the race) or the connection is not a recipient.
func (b *Broker) pruneRecipient(rid string, conn Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[rid]
	if !ok {
		return
	}
	if _, ok := p.recipients[conn]; !ok {
		return
	}
	delete(p.recipients, conn)
	if len(p.recipients) == 0 {
		delete(b.pending, rid)
		p.ch <- resolution{err: ErrRequestUndelivered}
	}
}

// drop abandons the pending request for rid (no recipient reached, context
// done, timeout) so a late resolution cannot wake a waiter nobody is
// listening to.
func (b *Broker) drop(rid string) {
	b.mu.Lock()
	delete(b.pending, rid)
	b.mu.Unlock()
}

// comparableConn reports whether c can be a map key — the contract every
// Conn the broker stores must satisfy. Request refuses a non-comparable
// recipient at the seam; Resolve and ConnectionLost guard their lookups the
// same way so an inconsistent transport cannot crash the read loop with a
// panic inside the map.
func comparableConn(c Conn) bool {
	if c == nil {
		return false
	}
	return reflect.TypeOf(c).Comparable()
}

// marshalWithRequestID merges the minted request id into the caller's params
// object. The id is the correlation handle the renderer echoes back; every
// request notification carries exactly one, and the resolution is bound to
// it rather than to anything a renderer could confuse.
func marshalWithRequestID(rid string, params any) (json.RawMessage, error) {
	var m map[string]any
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, err
			}
		}
	}
	if m == nil {
		m = make(map[string]any)
	}
	m["requestId"] = rid
	return json.Marshal(m)
}
