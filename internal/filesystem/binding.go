package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// Binding is opaque outside this package: its provider is unexported, so
// there is no route to a filesystem that skips Acquire (spec §5.1, D1). The
// accessors expose only identity.
type Binding struct {
	id         string
	sessionID  session.ID
	endpointID string // attestation; empty for local
	provider   Provider
	// sink is the binding's write half, or nil. It is nil exactly when the
	// provider did not implement Uploader, which is what makes rule R1 a
	// missing field rather than a condition a handler must remember to
	// check (design D7). Immutable: it is set at Register and read for the
	// binding's whole life.
	sink transfer.Sink

	mu       sync.Mutex
	cond     *sync.Cond
	closed   bool
	inflight int // use-guard: Acquire takes it, release drops it, close waits on it

	watchMu sync.Mutex
	watches map[string]Watch // the binding's watch set, replaced whole (spec §5.2)
}

// ID is the binding's opaque identity. It is what every wire call after
// files.open carries.
func (b *Binding) ID() string { return b.id }

// EndpointID is the backend's attestation of the resolved destination (D4,
// D6); empty for local bindings, which is what makes files.reveal a
// local-only capability.
func (b *Binding) EndpointID() string { return b.endpointID }

// Handle is the only thing that can reach a filesystem. It is what Acquire
// returns, it holds the use-guard for its lifetime, and it is invalid after
// release: from Acquire until release every method is valid; after release —
// and once the binding's Close has begun — every method errors with
// ErrHandleReleased. Each method additionally holds its own guard for the
// duration of the provider call, so close can never reach the provider under
// a running call.
type Handle interface {
	Root(ctx context.Context) (Root, error)
	List(ctx context.Context, path string, page Page) (Listing, error)
	Read(ctx context.Context, path string, maxBytes int64) (Content, error)
	Watch(ctx context.Context, paths []string) (WatchMode, error)
	// Upload writes one file onto the machine this binding views, through
	// the binding's sink. A binding without one refuses with
	// *ErrUploadUnsupported (rule R1). progress is called with the running
	// byte total after each chunk that reached the far side and may be nil.
	//
	// The returned Outcome is meaningful even when the error is not nil:
	// a failed transfer names what it left behind, and an error and a
	// non-empty Outcome.Stranded are not alternatives.
	Upload(ctx context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error)
}

// Caller is who is asking. filesystem declares it and transport satisfies it —
// the direction internal/discovery/discovery.go:113 already established, and
// the only one available: connState and wsConn are unexported in transport,
// and a filesystem that imported transport would point the dependency
// backwards. connState does not satisfy it as it stands (its method is has);
// transport adds an exported Owns(session.ID) bool that forwards to it.
type Caller interface {
	Owns(sessionID session.ID) bool
}

// Registry maps bindingId → Binding (spec §5.1 "Bindings"). It is where a
// bound provider exists, and Acquire is the only route to one.
type Registry struct {
	mu       sync.Mutex
	bindings map[string]*Binding
}

// New creates an empty registry.
func New() *Registry { return &Registry{bindings: make(map[string]*Binding)} }

// Register binds a provider to a session and mints a fresh random id from
// crypto/rand, the way the per-launch capability token already is (ws.go,
// nocx-hl3): a binding id cannot be guessed or enumerated, and it is not a
// bearer token — Acquire re-checks ownership on every call. endpointID is
// the attestation; leave it empty for local. The composition layer
//
// sink is the binding's write half and is nil for a binding that cannot be
// written to — every local one, and any remote provider that did not
// implement Uploader. It is a parameter rather than something derived here
// because Register takes a Provider: the assertion belongs where the
// concrete provider is still in hand, beside the endpoint attestation
// (design D7).
func (r *Registry) Register(p Provider, sessionID session.ID, endpointID string, sink transfer.Sink) (string, error) {
	if p == nil || isNilProvider(p) {
		return "", errors.New("filesystem: Register with nil provider")
	}
	id, err := newBindingID()
	if err != nil {
		return "", err
	}
	b := &Binding{
		id:         id,
		sessionID:  sessionID,
		endpointID: endpointID,
		provider:   p,
		sink:       sink,
		watches:    make(map[string]Watch),
	}
	b.cond = sync.NewCond(&b.mu)
	r.mu.Lock()
	r.bindings[id] = b
	r.mu.Unlock()
	return id, nil
}

// isNilProvider reports whether p is an interface holding a typed nil — a
// nil *T. `p == nil` rejects only a nil interface; an interface holding a
// typed nil passes it, registers fine, and panics on first use or close.
func isNilProvider(p Provider) bool {
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// Acquire returns a handle to the bound provider — and only after the caller
// proves it Owns the binding's session (D15). The check lives inside the one
// call that also takes the use-guard, so a handler cannot forget it; the
// alternative D1 rejected — sessionId on every call, checked by every handler
// — is rejected precisely because there the check is copied N times and the
// Nth copy is the hole.
//
// The returned release func drops the use-guard and must run after the
// handler's calls (defer is correct). After it runs, every method on the
// handle errors with ErrHandleReleased. Close waits for the guard to drain.
func (r *Registry) Acquire(id string, c Caller) (Handle, func(), error) {
	r.mu.Lock()
	b, ok := r.bindings[id]
	if !ok {
		r.mu.Unlock()
		return nil, nil, &ErrUnknownBinding{ID: id}
	}
	if c == nil || !c.Owns(b.sessionID) {
		r.mu.Unlock()
		return nil, nil, &ErrNotOwned{ID: id, SessionID: b.sessionID}
	}
	b.mu.Lock()
	if b.closed {
		// Defensive: the registry removes the binding and close marks it
		// closed before draining, so an in-map binding is never closed
		// today; this guards the invariant against future closer shapes.
		b.mu.Unlock()
		r.mu.Unlock()
		return nil, nil, &ErrUnknownBinding{ID: id}
	}
	b.inflight++
	b.mu.Unlock()
	r.mu.Unlock()
	h := &handle{b: b}
	return h, h.release, nil
}

func (r *Registry) Close(id string) error {
	r.mu.Lock()
	b, ok := r.bindings[id]
	if !ok {
		r.mu.Unlock()
		return &ErrUnknownBinding{ID: id}
	}
	delete(r.bindings, id)
	r.mu.Unlock()
	return b.close()
}

// CloseSession closes every binding of a session and waits for all of their
// guards to drain. Closing the terminal closes its bindings (spec §5.1): a
// binding is bounded by its session, never by a WebSocket, and an in-flight
// read is protected by the pooled reference the composition layer took, not
// by this call racing it. It deliberately returns nothing: session teardown
// is fire-and-forget — the session itself is already dying and no caller
// awaits this — while Registry.Close, the RPC-reachable path, surfaces close
// failures.
func (r *Registry) CloseSession(sessionID session.ID) {
	r.mu.Lock()
	var doomed []*Binding
	for id, b := range r.bindings {
		if b.sessionID == sessionID {
			doomed = append(doomed, b)
			delete(r.bindings, id)
		}
	}
	r.mu.Unlock()
	for _, b := range doomed {
		_ = b.close()
	}
}

// close drains the use-guard, then tears down watches, then closes the
// provider, and returns every close error it collected.
//
// The order is the point. Draining before the watch teardown means a handle
// that is still valid keeps its watches for the whole of its last call, and
// a Watch call in flight installs its set before the teardown — the set that
// is closed is the final set, so nothing is closed underneath a live handle
// and nothing is installed after the cleanup and leaked. The provider is
// closed only after the guard drained, so no handler can be mid-call on it.
func (b *Binding) close() error {
	b.mu.Lock()
	b.closed = true
	for b.inflight > 0 {
		b.cond.Wait()
	}
	b.mu.Unlock()
	b.watchMu.Lock()
	ws := b.watches
	b.watches = nil
	b.watchMu.Unlock()
	var errs []error
	for _, w := range ws {
		if err := w.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := b.provider.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// drop releases one use-guard. Called from handle.release.
func (b *Binding) drop() {
	b.mu.Lock()
	b.inflight--
	b.cond.Broadcast()
	b.mu.Unlock()
}

// swapWatches replaces the binding's watch set atomically (spec §5.2): every
// path is established first; if any fails, the partial set is closed and the
// existing set is untouched; only on full success are the old watches closed
// and the new set installed. Paths are a set — duplicates collapse.
func (b *Binding) swapWatches(ctx context.Context, paths []string) (WatchMode, error) {
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		unique = append(unique, p)
	}
	b.watchMu.Lock()
	defer b.watchMu.Unlock()
	fresh := make(map[string]Watch, len(unique))
	for _, p := range unique {
		w, err := b.provider.Watch(ctx, p)
		if err != nil {
			for _, fw := range fresh {
				_ = fw.Close() // never leak a partially established set
			}
			return WatchMode{}, err
		}
		fresh[p] = w
	}
	old := b.watches
	b.watches = fresh
	for _, ow := range old {
		_ = ow.Close() // the swap already succeeded; a stale watch's close error cannot change that
	}
	return aggregateMode(fresh), nil
}

// aggregateMode describes a whole watch set: a degraded watch (polling with
// a reason) is the most alarming answer, designed polling (no reason, SFTP)
// comes next, and only an all-live set reports live (spec §5.5).
func aggregateMode(ws map[string]Watch) WatchMode {
	anyPolling := false
	for _, w := range ws {
		m := w.Mode()
		if m.Kind == WatchPolling {
			anyPolling = true
			if m.DegradedReason != "" {
				return m
			}
		}
	}
	if anyPolling {
		return WatchMode{Kind: WatchPolling}
	}
	return WatchMode{Kind: WatchLive}
}

// handle is the live Handle. It is valid from Acquire until release and
// invalid after.
type handle struct {
	mu       sync.Mutex
	released bool
	b        *Binding
}

// begin takes a use-guard for the duration of one call. The handle's own
// validity (not released) and the binding's (not closed) are both checked
// before the guard is taken, and the binding check sits in the same critical
// section as the increment.
//
// That placement is the fix for the window the original code left open: it
// checked `released`, unlocked, and then called the provider, so a racing
// release could drop the last guard underneath it and close could reach the
// provider mid-call. Here the check and the guard are one operation — either
// the call counts itself before close marks the binding closed (and close
// waits for it), or it sees the flag and refuses. The released flag is
// checked under the handle's own mutex first, so a method cannot start after
// its release func ran, which is the other end of the contract.
func (h *handle) begin() (func(), error) {
	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return nil, &ErrHandleReleased{}
	}
	h.mu.Unlock()
	b := h.b
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, &ErrHandleReleased{}
	}
	b.inflight++
	b.mu.Unlock()
	return b.drop, nil
}

func (h *handle) Root(ctx context.Context) (Root, error) {
	drop, err := h.begin()
	if err != nil {
		return Root{}, err
	}
	defer drop()
	return h.b.provider.Root(ctx)
}

func (h *handle) List(ctx context.Context, path string, page Page) (Listing, error) {
	drop, err := h.begin()
	if err != nil {
		return Listing{}, err
	}
	defer drop()
	return h.b.provider.List(ctx, path, page)
}

func (h *handle) Read(ctx context.Context, path string, maxBytes int64) (Content, error) {
	drop, err := h.begin()
	if err != nil {
		return Content{}, err
	}
	defer drop()
	return h.b.provider.Read(ctx, path, maxBytes)
}

func (h *handle) Watch(ctx context.Context, paths []string) (WatchMode, error) {
	drop, err := h.begin()
	if err != nil {
		return WatchMode{}, err
	}
	defer drop()
	return h.b.swapWatches(ctx, paths)
}

// Upload writes one file through the binding's sink.
//
// The refusal is the upload design's rule R1 expressed as a nil field rather
// than as a condition somebody must remember to check: a local binding never
// received a sink, so there is no check here to forget and no route by which
// a local tab could be written to. The provider is not touched on that path
// — a refusal costs no round trip.
func (h *handle) Upload(ctx context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	drop, err := h.begin()
	if err != nil {
		return transfer.Outcome{}, err
	}
	defer drop()
	if h.b.sink == nil {
		return transfer.Outcome{}, &ErrUploadUnsupported{BindingID: h.b.id}
	}
	return h.b.sink.Put(ctx, u, r, progress)
}

// release drops the use-guard. It is idempotent: the second call is a no-op,
// so a deferred release racing a manual one cannot double-decrement.
func (h *handle) release() {
	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return
	}
	h.released = true
	h.mu.Unlock()
	h.b.drop()
}

// newBindingID mints an unguessable binding id from crypto/rand, the same
// shape as the per-launch capability token (ws.go, nocx-hl3).
func newBindingID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("filesystem: mint binding id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
