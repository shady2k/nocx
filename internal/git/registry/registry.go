package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/session"
)

// Binding is opaque outside this package: its repo is unexported, so there is
// no route to a repository that skips Acquire (spec §5.1, D1). The accessors
// expose only identity.
type Binding struct {
	id        string
	sessionID session.ID
	repo      git.Repo

	mu       sync.Mutex
	cond     *sync.Cond
	closed   bool
	inflight int // use-guard: Acquire takes it, release drops it, close waits on it
}

// ID is the binding's opaque identity. It is what every wire call after
// git.open carries.
func (b *Binding) ID() string { return b.id }

// SessionID is the session the binding is bounded by (AD-9): a binding is
// bounded by its session, never by a WebSocket, and a live session keeps its
// bindings across a reconnect.
func (b *Binding) SessionID() session.ID { return b.sessionID }

// Handle is the only thing that can reach a repository. It is what Acquire
// returns, it holds the use-guard for its lifetime, and it is invalid after
// release: from Acquire until release every method is valid; after release —
// and once the binding's Close has begun — every method errors with
// ErrHandleReleased. Each method additionally holds its own guard for the
// duration of the provider call, so close can never reach the provider under
// a running call.
type Handle interface {
	Status(ctx context.Context) (git.Status, error)
	// EnvState is on the Handle, not only the Repo, because the transport
	// reaches repositories exclusively through Acquire — a handler cannot
	// ask a repo it was never given. The handle forwards to the bound
	// repo, guarding the call like every other method (nocx-69ey).
	EnvState() (git.EnvState, string)
	Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error)
	Log(ctx context.Context, max int) (git.Log, error)
	Stage(ctx context.Context, paths []string) (git.Status, error)
	Unstage(ctx context.Context, paths []string) (git.Status, error)
	StageAll(ctx context.Context) (git.Status, error)
	UnstageAll(ctx context.Context) (git.Status, error)
	Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error)
	HeadMessage(ctx context.Context) (git.HeadMessage, error)
	RemoteURL(ctx context.Context) (string, error)
}

// Caller is who is asking. registry declares it and transport satisfies it —
// the direction internal/filesystem already established, and the only one
// available: connState and wsConn are unexported in transport, and a package
// that imported transport would point the dependency backwards.
//
// internal/filesystem declares an identical interface; the registry
// deliberately does not import it. A consumer-declared interface is the Go
// idiom, and importing across feature packages would couple them permanently
// for the sake of one method signature (spec D15).
type Caller interface {
	Owns(sessionID session.ID) bool
}

// Registry maps bindingId → Binding (spec §5.1 "Bindings"). It is where a
// bound repository exists, and Acquire is the only route to one.
type Registry struct {
	mu       sync.Mutex
	bindings map[string]*Binding
}

// New creates an empty registry.
func New() *Registry { return &Registry{bindings: make(map[string]*Binding)} }

// Register binds a repo to a session and mints a fresh random id from
// crypto/rand, the way the per-launch capability token already is (ws.go,
// nocx-hl3): a binding id cannot be guessed or enumerated, and it is not a
// bearer token — Acquire re-checks ownership on every call. The composition
// layer calls this only after OpenOutcome.State is OpenOK; ownership of the
// returned Repo transfers to the registry here, and on failure the caller
// still owns it and must close it (spec §5.1, rules 1–3).
func (r *Registry) Register(repo git.Repo, sessionID session.ID) (string, error) {
	if repo == nil || isNilRepo(repo) {
		return "", errors.New("git: Register with nil repo")
	}
	id, err := newBindingID()
	if err != nil {
		return "", err
	}
	b := &Binding{
		id:        id,
		sessionID: sessionID,
		repo:      repo,
	}
	b.cond = sync.NewCond(&b.mu)
	r.mu.Lock()
	r.bindings[id] = b
	r.mu.Unlock()
	return id, nil
}

// isNilRepo reports whether p is an interface holding a typed nil — a
// nil *T. `p == nil` rejects only a nil interface; an interface holding a
// typed nil passes it, registers fine, and panics on first use or close.
func isNilRepo(p git.Repo) bool {
	v := reflect.ValueOf(p)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// Acquire returns a handle to the bound repo — and only after the caller
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

// Close removes the binding from the registry and closes it, waiting for
// every acquired handle to drain. This is the RPC-reachable path, so it
// surfaces close failures.
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
// guards to drain. Closing the terminal closes its bindings (spec §5.5): a
// binding is bounded by its session, never by a WebSocket, and an in-flight
// call is protected by the use-guard, not by this call racing it. It
// deliberately returns nothing: session teardown is fire-and-forget — the
// session itself is already dying and no caller awaits this — while
// Registry.Close, the RPC-reachable path, surfaces close failures.
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

// close drains the use-guard, then closes the provider, and returns its
// close error.
//
// The binding interval, stated with both ends (D18, and the lesson
// internal/vault bought): a binding is reachable from the moment Register
// returns until Close returns, and no Repo call is in flight after Close
// returns. Draining before closing the provider is what makes the second end
// true — a handle that is still valid keeps its repo for the whole of its
// last call, and the provider is closed only after the guard drained, so no
// handler can be mid-call on it.
//
// The consequence, named rather than left implicit: a mutation that has
// already begun completes against the repository it was authorised against,
// even if the shell has since moved. That is correct — the user pressed
// Commit on that repository's staged set — and D17 is what stops its result
// painting the repository they moved to.
func (b *Binding) close() error {
	b.mu.Lock()
	b.closed = true
	for b.inflight > 0 {
		b.cond.Wait()
	}
	b.mu.Unlock()
	return b.repo.Close()
}

// drop releases one use-guard. Called from handle.release.
func (b *Binding) drop() {
	b.mu.Lock()
	b.inflight--
	if b.inflight == 0 {
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

// handle is the live Handle. It is valid from Acquire until release and
// invalid after.
type handle struct {
	b *Binding

	mu       sync.Mutex
	released bool
}

func (h *handle) Status(ctx context.Context) (git.Status, error) {
	release, err := h.begin()
	if err != nil {
		return git.Status{}, err
	}
	defer release()
	return h.b.repo.Status(ctx)
}

func (h *handle) EnvState() (git.EnvState, string) {
	release, err := h.begin()
	if err != nil {
		// A released handle answers the conservative degraded, the same
		// way known() reports the pre-settle window: the panel is about
		// to hear unknownBinding anyway, and a "resolved" the binding
		// no longer stands behind is the lie D6 exists to prevent.
		return git.EnvDegraded, err.Error()
	}
	defer release()
	return h.b.repo.EnvState()
}

func (h *handle) Diff(ctx context.Context, path string, side git.Side, maxBytes int64) (git.Diff, error) {
	release, err := h.begin()
	if err != nil {
		return git.Diff{}, err
	}
	defer release()
	return h.b.repo.Diff(ctx, path, side, maxBytes)
}

func (h *handle) Log(ctx context.Context, max int) (git.Log, error) {
	release, err := h.begin()
	if err != nil {
		return git.Log{}, err
	}
	defer release()
	return h.b.repo.Log(ctx, max)
}

func (h *handle) Stage(ctx context.Context, paths []string) (git.Status, error) {
	release, err := h.begin()
	if err != nil {
		return git.Status{}, err
	}
	defer release()
	return h.b.repo.Stage(ctx, paths)
}

func (h *handle) Unstage(ctx context.Context, paths []string) (git.Status, error) {
	release, err := h.begin()
	if err != nil {
		return git.Status{}, err
	}
	defer release()
	return h.b.repo.Unstage(ctx, paths)
}

func (h *handle) StageAll(ctx context.Context) (git.Status, error) {
	release, err := h.begin()
	if err != nil {
		return git.Status{}, err
	}
	defer release()
	return h.b.repo.StageAll(ctx)
}

func (h *handle) UnstageAll(ctx context.Context) (git.Status, error) {
	release, err := h.begin()
	if err != nil {
		return git.Status{}, err
	}
	defer release()
	return h.b.repo.UnstageAll(ctx)
}

func (h *handle) Commit(ctx context.Context, msg string, amend bool) (git.CommitOutcome, error) {
	release, err := h.begin()
	if err != nil {
		return git.CommitOutcome{}, err
	}
	defer release()
	return h.b.repo.Commit(ctx, msg, amend)
}

func (h *handle) HeadMessage(ctx context.Context) (git.HeadMessage, error) {
	release, err := h.begin()
	if err != nil {
		return git.HeadMessage{}, err
	}
	defer release()
	return h.b.repo.HeadMessage(ctx)
}

func (h *handle) RemoteURL(ctx context.Context) (string, error) {
	release, err := h.begin()
	if err != nil {
		return "", err
	}
	defer release()
	return h.b.repo.RemoteURL(ctx)
}

// begin takes a use-guard for the duration of one call. The handle's own
// validity (not released) and the binding's (not closed) are both checked
// before the guard is taken, and the binding check sits in the same critical
// section as the increment. The returned drop is the binding's, not the
// handle's: each call counts its own guard, and close waits for all of them —
// the acquire's guard is released separately, by the handle's release func.
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
// shape as the per-launch capability token (ws.go, nocx-hl3). It is a
// package-level variable so tests can force the minting failure path of
// Register.
var newBindingID = func() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("git: mint binding id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
