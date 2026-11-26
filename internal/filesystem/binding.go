package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/sessionid"
	"github.com/shady2k/nocx/internal/transfer"
)

// Binding is opaque outside this package: its provider is unexported, so
// there is no route to a filesystem that skips Acquire (spec §5.1, D1). The
// accessors expose only identity.
type Binding struct {
	id         string
	sessionID  sessionid.ID
	endpointID string // attestation; empty for local
	provider   Provider
	// sink is the binding's write half, or nil. It is nil exactly when the
	// provider did not implement Uploader, which is what makes rule R1 a
	// missing field rather than a condition a handler must remember to
	// check (design D7). Immutable: it is set at Register and read for the
	// binding's whole life.
	sink transfer.Sink
	// source is the binding's read-stream half, or nil — the same field
	// shape and the same rule, one direction over. It is nil exactly when
	// the provider did not implement Downloader, so a tab whose files
	// cannot be streamed refuses because it holds nothing to stream
	// through, not because a handler remembered to ask where the tab is.
	source transfer.Source
	// domain names the failure domain a path draws on, or nil. Immutable,
	// like sink and source: resolved once where the provider is still in
	// hand, and read for the binding's whole life.
	domain func(path string) Domain
	// axis and versioned are the two seams a helper-backed provider
	// contributes, captured beside sink, source and domain for the same
	// reason: at Register the concrete provider is still in hand and
	// afterwards it is not. Nil means the binding has none of it, which is
	// the local tab's answer and the SFTP tab's answer respectively.
	axis      func() (HelperAxis, bool)
	versioned VersionedFiles

	mu       sync.Mutex
	closed   bool
	inflight int // use-guard: Acquire takes it, release drops it, close waits on it
	// drained is closed once the binding is closed and its last use-guard is
	// gone. A channel rather than a sync.Cond because the wait is BOUNDED:
	// close waits at most closeDrainTimeout for a guard, then closes anyway.
	// A cond cannot carry a deadline, and an unbounded wait in a close path
	// is the shape that hangs shutdown (ADR-8eej3e66 §15).
	drained     chan struct{}
	drainClosed bool

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
// a running call — the one deliberate exception being Uploader, which holds
// it for the hand-off and not for the transfer that follows (D8, below).
type Handle interface {
	Root(ctx context.Context) (Root, error)
	List(ctx context.Context, path string, page Page) (Listing, error)
	Read(ctx context.Context, path string, maxBytes int64) (Content, error)
	Stat(ctx context.Context, path string) (Stat, error)
	Watch(ctx context.Context, paths []string) (WatchMode, error)
	// Canonical resolves a spelled path to the provider's canonical identity
	// of the object it names — symlinks followed, the provider's own rules
	// applied. It is the comparison a scope is enforced on, so a capability
	// asks the HANDLE rather than an os call: on a host behind the helper the
	// identity is the host's, and on this machine it is this one's, and the
	// capability must not be able to tell which it holds.
	//
	// It takes the use-guard like every other method, so a resolution never
	// reaches a provider under a closing binding.
	Canonical(ctx context.Context, path string) (string, error)
	// Uploader returns the binding's write half — the sink one transfer
	// runs on — or *ErrUploadUnsupported when the binding has none, which
	// is rule R1 expressed as a missing field rather than as a check
	// somebody must remember to perform. Like every other method it is
	// valid from Acquire until release and refuses afterwards.
	//
	// The Sink it hands back is deliberately DETACHED from the use-guard,
	// and that is design D8 rather than an oversight. Close waits for every
	// guard to drain, so a transfer that held one for its length would make
	// files.close and session teardown wait for as long as the upload runs
	// — and a teardown that cancels first and then waits, bounded, needs
	// "close regardless" to be something it can actually do. So the guard
	// covers obtaining the sink and nothing after it.
	//
	// What makes running unguarded safe is the lease underneath rather than
	// hope: closing the provider closes the SFTP session and client, which
	// unblocks a call already in flight and makes every later one report a
	// closed lease (internal/ssh/ssh_fsconn.go, Close). A write racing a
	// close therefore fails and names what it stranded; it never writes
	// through a torn-down lease, and it never holds the close open.
	Uploader() (transfer.Sink, error)
	// DomainKey names the failure domain a path of this binding draws on.
	// It never touches the filesystem: naming a mount must not stat it, or a
	// hung mount would hang the lookup that exists to contain it. A binding
	// whose provider could not name one answers a domain of its own, so two
	// bindings never share a bulkhead by accident.
	DomainKey(path string) Domain
	// HelperAxis reports the connection's helper axis as it stands — the same
	// reason a failed call answers with, from one source rather than two
	// (C28). A local tab has no helper axis and answers false.
	//
	// It takes the use-guard like every other method, so an axis is never
	// read through a closing binding; a released handle answers false, which
	// is the only refusal this signature can express and the same statement
	// the bool already makes — "there is no axis here for you".
	HelperAxis() (HelperAxis, bool)
	// Versioned returns the binding's conditional-write half, or
	// *ErrVersionedUnsupported when its provider contributed none. It is
	// Uploader's mirror in shape and in reasoning: the refusal is a missing
	// field rather than a check, and the guard covers the hand-off (D8) —
	// what the caller then runs on the returned value is a write of its own,
	// bounded by the helper's mutation budget, and closing the binding
	// closes the lease underneath it rather than waiting for it.
	Versioned() (VersionedFiles, error)
	// Downloader returns the binding's read-stream half — the source one
	// download runs on — or *ErrDownloadUnsupported when the binding has
	// none. It is Uploader's mirror in every respect that matters, and the
	// two comments above apply here unchanged: the refusal is a missing
	// field rather than a check, the guard covers the hand-off and not the
	// transfer that follows (D8), and what makes running unguarded safe is
	// the lease underneath — closing the provider closes the SFTP session
	// and client, so a read racing a close fails and reports how far it
	// got, and never reads through a torn-down lease.
	Downloader() (transfer.Source, error)
}

// HandleSource hands out a short-lived Handle, and it is the ONLY route a
// narrowed capability holds to a filesystem. A capability never holds a
// Provider and never holds a Handle for longer than one operation: it asks for
// one, does the work, and releases it, so a binding's lifetime is the call's
// and not the run's.
//
// Acquire performs the authorisation (Registry.Acquire re-checks ownership),
// and the returned release must run on every path — including every failure
// after Acquire returned, which is why callers defer it immediately.
//
// It is declared here rather than in the composition layer because the
// capabilities in this package (ScopedReader, ScopedEditor) are what hold it;
// the transport implements it over the same factory and registry the Files
// panel uses.
type HandleSource interface {
	Acquire(ctx context.Context) (Handle, func(), error)
}

// Caller is who is asking. filesystem declares it and transport satisfies it —
// the direction internal/discovery/discovery.go:113 already established, and
// the only one available: connState and wsConn are unexported in transport,
// and a filesystem that imported transport would point the dependency
// backwards. connState does not satisfy it as it stands (its method is has);
// transport adds an exported Owns(sessionid.ID) bool that forwards to it.
type Caller interface {
	Owns(sessionID sessionid.ID) bool
}

// HelperAxis is the connection's helper axis in this package's own vocabulary:
// the state it is in, why it is not serving when it is not, and the action a
// surface may offer. It is neutral on purpose (C30): internal/filesystem is
// linked into the artifact we deploy onto somebody else's host, so it may not
// reach internal/farhelper — and the panel must still be able to name the
// reason the connection published.
//
// The values are farhelper's closed vocabulary, carried as strings for exactly
// that reason. An empty Reason is "nothing to say yet" — a serving axis — and
// is not an error.
type HelperAxis struct {
	State  string
	Reason string
	Action string
}

// Version identifies the bytes a whole-file read returned, and is the fence a
// later conditional write compares against. Size and MtimeNs are the cheap
// fields; SHA256 is the one a rewrite of the same length inside one filesystem
// timestamp granule cannot fool.
type Version struct {
	Size    int64
	MtimeNs int64
	SHA256  string
}

// Expect is the condition a conditional write checks at the instant of the
// rename. Version says "this file, still exactly as I read it"; Absent says
// "nothing is at this name". They are alternatives, never a conjunction — a
// caller that asks for both has not decided what it wants, and the host
// refuses that request (bad_params) rather than picking one silently. Neither
// set means the write is unconditional.
type Expect struct {
	Version *Version
	Absent  bool
}

// VersionedFiles is the conditional write: read a file whole with the version
// of exactly those bytes, then install new bytes on the host only if the file
// still matches that version. The check is the HOST's, at the rename — a
// coordinator-side compare would leave the window it exists to close — and
// Commit either installs the bytes and answers the version they landed with,
// or refuses with a typed error (ErrChangedSinceRead, ErrExists) and installs
// nothing.
//
// ReadVersion refuses a file larger than maxBytes rather than returning a
// prefix: a version of a prefix is not the file's version, and a caller that
// got one would commit a fence describing bytes it never saw.
type VersionedFiles interface {
	ReadVersion(ctx context.Context, path string, maxBytes int64) (data []byte, version Version, err error)
	Commit(ctx context.Context, path string, data []byte, expect Expect) (Version, error)
}

// AxisProvider is the optional seam a provider contributes when the bindings
// it registers have a helper axis to report. The composition site asserts it
// where the concrete provider is still in hand, the same way it asserts
// Uploader and Downloader, and carries the method onto Capabilities.Axis.
type AxisProvider interface {
	HelperAxis() (HelperAxis, bool)
}

// VersionedProvider is the optional seam a provider contributes when it can
// serve VersionedFiles. A provider that does not implement it contributes no
// conditional write, and its binding refuses with *ErrVersionedUnsupported.
type VersionedProvider interface {
	Versioned() VersionedFiles
}

// Capabilities are the optional seams a provider contributed, resolved once
// at files.open where the concrete provider is still in hand and carried on
// the binding beside the endpoint attestation (design D7).
//
// A struct rather than a parameter per seam, and that is a decision about
// what happens next: a download seam arrived after the upload one, a
// directory upload will arrive after that, and each of them is the same kind
// of thing — a capability the provider either has or has not. Growing this
// struct leaves every call site that does not care untouched, where growing
// the parameter list makes each new seam a change to twenty-five of them.
//
// A nil field is not an omission: it IS rule R1 for that direction. A
// provider that cannot write contributes no Sink and its binding refuses to
// be uploaded to; a provider that cannot stream contributes no Source and
// its binding refuses to be downloaded from. Both shipped providers
// contribute both, so neither nil is a case in production — it is the shape
// that keeps the refusal expressible for the next provider that cannot.
type Capabilities struct {
	Sink   transfer.Sink
	Source transfer.Source
	// Axis reports the binding's helper axis as it stands WHEN ASKED, or
	// false for a binding that has no helper axis at all — a local tab. It
	// is a function rather than a captured status for the reason the axis
	// exists: it moves while the binding lives (a pause, a lost helper, a
	// reconnect), and a value sampled at files.open would leave the panel
	// drawing a state the connection had already left (C28, §5.10).
	Axis func() (HelperAxis, bool)
	// Versioned is the conditional-write seam, or nil. The nil is rule R1
	// in the write direction: a provider that cannot check a version
	// contributes nothing here and its binding refuses the conditional
	// write with *ErrVersionedUnsupported.
	Versioned VersionedFiles
	// Domain names the failure domain one of this binding's paths draws on,
	// or nil when the provider cannot say (a provider that implements
	// filesystem.DomainNamer supplies its own through the composition site).
	// A nil Domain is not "no domain": the binding then forms one domain of
	// its own, which is the conservative answer and never merges two
	// machines' work into one bulkhead.
	Domain func(path string) Domain
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
// caps are the optional seams the provider contributed. They are parameters
// rather than something derived here because Register takes a Provider: the
// assertions belong where the concrete provider is still in hand, beside the
// endpoint attestation (design D7).
func (r *Registry) Register(p Provider, sessionID sessionid.ID, endpointID string, caps Capabilities) (string, error) {
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
		sink:       caps.Sink,
		source:     caps.Source,
		domain:     caps.Domain,
		axis:       caps.Axis,
		versioned:  caps.Versioned,
		watches:    make(map[string]Watch),
		drained:    make(chan struct{}),
	}
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
// guards to drain, each bounded by closeDrainTimeout. Closing the terminal
// closes its bindings (spec §5.1): a binding is bounded by its session, never
// by a WebSocket, and an in-flight read is protected by the pooled reference
// the composition layer took, not by this call racing it.
//
// The bindings are drained CONCURRENTLY, and the whole set shares one bound
// in effect: a hung mount hangs every local binding at once, so closing ten
// of them one after another would spend the bound ten times over. The error
// is the one Registry.Close returns, joined across bindings, so a teardown
// that had to abandon a guard names it rather than staying silent
// (ADR-8eej3e66 §15: this call was the unbounded half of Stop).
func (r *Registry) CloseSession(sessionID sessionid.ID) error {
	r.mu.Lock()
	var doomed []*Binding
	for id, b := range r.bindings {
		if b.sessionID == sessionID {
			doomed = append(doomed, b)
			delete(r.bindings, id)
		}
	}
	r.mu.Unlock()
	errs := make([]error, len(doomed))
	var wg sync.WaitGroup
	for i, b := range doomed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = b.close()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// closeDrainTimeout bounds how long a closing binding waits for its
// use-guards. It is the same five seconds the transport's control drain uses,
// because it answers the same question: a dependency that ignores
// cancellation must not hold teardown open past a documented maximum
// (ADR-8eej3e66 §15).
const closeDrainTimeout = 5 * time.Second

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
	var errs []error
	b.mu.Lock()
	b.closed = true
	waiting := b.inflight
	if waiting == 0 && !b.drainClosed {
		b.drainClosed = true
		close(b.drained)
	}
	drained := b.drained
	b.mu.Unlock()
	if waiting > 0 {
		timer := time.NewTimer(closeDrainTimeout)
		defer timer.Stop()
		select {
		case <-drained:
		case <-timer.C:
			// The guard did not drain. Close anyway: closing an SFTP lease
			// is exactly what unblocks a call already inside it, and a
			// readdir on a hung mount cannot be unblocked by waiting for
			// it. Name what was abandoned rather than hanging teardown.
			errs = append(errs, &ErrCloseAbandoned{BindingID: b.id, Guards: waiting})
		}
	}
	b.watchMu.Lock()
	ws := b.watches
	b.watches = nil
	b.watchMu.Unlock()
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
	if b.closed && b.inflight == 0 && !b.drainClosed {
		b.drainClosed = true
		close(b.drained)
	}
	b.mu.Unlock()
}

// swapWatches replaces the binding's watch set atomically (spec §5.2): every
// ADDED path is established first; if any fails, only what this call
// established is closed and the existing set is untouched; only on full
// success are the DROPPED watches closed and the new set installed. Paths
// are a set — duplicates collapse.
//
// The set is DIFFED rather than rebuilt (nocx-8hdia.5). files.watch replaces
// the set rather than adding to it, and the client resends the whole set on
// every expand and every collapse, so rebuilding made a one-folder gesture
// re-establish the entire expanded tree. At the transport's 512-path ceiling
// that also meant briefly holding nearly twice the watch resources — which
// is precisely the moment an inotify or descriptor limit is reached, so the
// rebuild made the ceiling it was operating under harder to satisfy. A
// retained path keeps its existing Watch object: nothing is closed and
// nothing is created, so no window opens in which an event could be missed.
//
// The failure path is what diffing makes delicate and is asserted in the
// tests: `fresh` SHARES watch objects with the live set, so on failure only
// the newly established ones may be closed. Closing everything in `fresh`
// would tear down watches the caller still holds — turning a refused
// addition into a silent loss of every existing watch.
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
	// Only what THIS call established, so the failure path can close exactly
	// that and nothing the caller already had.
	established := make([]string, 0, len(unique))
	for _, p := range unique {
		if w, ok := b.watches[p]; ok {
			fresh[p] = w // retained: same live watch, no window
			continue
		}
		w, err := b.provider.Watch(ctx, p)
		if err != nil {
			for _, ep := range established {
				_ = fresh[ep].Close() // never leak a partially established set
			}
			return WatchMode{}, err
		}
		fresh[p] = w
		established = append(established, p)
	}
	old := b.watches
	b.watches = fresh
	for p, ow := range old {
		if _, retained := fresh[p]; retained {
			continue
		}
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

func (h *handle) Canonical(ctx context.Context, path string) (string, error) {
	drop, err := h.begin()
	if err != nil {
		return "", err
	}
	defer drop()
	return h.b.provider.Canonical(ctx, path)
}

func (h *handle) Stat(ctx context.Context, path string) (Stat, error) {
	drop, err := h.begin()
	if err != nil {
		return Stat{}, err
	}
	defer drop()
	return h.b.provider.Stat(ctx, path)
}

// DomainKey reports the path's failure domain without taking the use-guard:
// it reaches no provider call, and a lookup that could be refused by a
// closing binding would make a domain name depend on teardown timing.
func (h *handle) DomainKey(path string) Domain {
	if h.b.domain != nil {
		if d := h.b.domain(path); d.ID != "" {
			return d
		}
	}
	return Domain{Kind: "binding", ID: h.b.id}
}

func (h *handle) Watch(ctx context.Context, paths []string) (WatchMode, error) {
	drop, err := h.begin()
	if err != nil {
		return WatchMode{}, err
	}
	defer drop()
	return h.b.swapWatches(ctx, paths)
}

// Uploader hands back the binding's sink.
//
// The refusal is the upload design's rule R1 expressed as a nil field rather
// than as a condition somebody must remember to check: a binding whose
// provider cannot write never received a sink, so there is no check here to
// forget and no route by which such a tab could be written to. The provider
// is not touched on either path — neither a refusal nor a hand-off costs a
// round trip.
//
// The guard is taken and dropped around the hand-off only, which is the
// whole of D8 on this side: what the caller then runs on the sink is
// unguarded, and the interface comment says why that is safe.
func (h *handle) Uploader() (transfer.Sink, error) {
	drop, err := h.begin()
	if err != nil {
		return nil, err
	}
	defer drop()
	if h.b.sink == nil {
		return nil, &ErrUploadUnsupported{BindingID: h.b.id}
	}
	return h.b.sink, nil
}

// Downloader hands back the binding's source.
//
// It is Uploader's mirror line for line, and deliberately so: the refusal is
// rule R1 in the read direction expressed as a nil field, the provider is
// not touched on either path, and the guard is taken and dropped around the
// hand-off only — what the caller then runs on the source is unguarded,
// which is the whole of D8 on this side.
//
// The one thing that differs from the write direction is what running
// unguarded can cost, and it is smaller: a download creates nothing on the
// far host, so a read racing a binding close reports how far it got and can
// leave nothing behind to be named.
func (h *handle) Downloader() (transfer.Source, error) {
	drop, err := h.begin()
	if err != nil {
		return nil, err
	}
	defer drop()
	if h.b.source == nil {
		return nil, &ErrDownloadUnsupported{BindingID: h.b.id}
	}
	return h.b.source, nil
}

// HelperAxis reports the connection's helper axis as it stands.
//
// It reads the binding's live source rather than a value captured at
// files.open, which is the whole point of carrying a function: a paused axis
// that came back serving, or a connection that went away, must be reportable
// without reopening the panel (C28). The provider is not touched — an axis is
// a fact the registry holds, not a question to ask the host — so a surface can
// draw the reason without spending a round trip on a host that is not
// answering.
//
// The guard is taken for the same reason every other method takes it, and the
// refusal is `false`: this signature has no error to carry ErrHandleReleased,
// and "no axis here" is the same answer a local tab gives. A caller that must
// tell the two apart asks before it releases the handle, which is the contract
// the whole Handle surface states.
func (h *handle) HelperAxis() (HelperAxis, bool) {
	drop, err := h.begin()
	if err != nil {
		return HelperAxis{}, false
	}
	defer drop()
	if h.b.axis == nil {
		return HelperAxis{}, false
	}
	return h.b.axis()
}

// Versioned hands back the binding's conditional-write half.
//
// It is Uploader's mirror: the refusal is rule R1 expressed as a nil field
// rather than a condition somebody must remember to check, the provider is not
// touched on either path, and the use-guard covers the hand-off only (D8).
// What the caller runs on the returned value is a write of its own, bounded by
// the helper's mutation budget; closing the binding closes the lease underneath
// it, so a write racing a close fails and names what it stranded rather than
// writing through a torn-down lease.
func (h *handle) Versioned() (VersionedFiles, error) {
	drop, err := h.begin()
	if err != nil {
		return nil, err
	}
	defer drop()
	if h.b.versioned == nil {
		return nil, &ErrVersionedUnsupported{BindingID: h.b.id}
	}
	return h.b.versioned, nil
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
