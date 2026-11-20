// Package farhelper owns one fact per connection: whether the nocx helper on
// the far host is reachable, and if not, why and what a person can do about it.
//
// # Why it exists, and what it owns
//
// ADR-727g63tr decision 2: the availability of a far helper is a fact of the
// CONNECTION, not of a pane, a panel or an open. Git, Files and Ports all ask
// the same connection the same question, and a second owner of that question
// would answer it differently the first time a panel asked a moment late. So
// this package decides once per connection generation and decision revision,
// keeps the decision, and hands out a client; it publishes nothing itself.
//
// # What decides, and what it may reach
//
// The decision is mode, recorded consent, platform, install, dial and hello, in
// that order (design §5.10, lifecycle scenarios 2-7 and 10). The remote steps
// are interfaces this package declares — Probe, Install, Dial — and the
// composition root implements them over the local helper and the install
// machinery it already has. This package therefore imports no ssh client and
// no transport; the dependency test beside the tests refuses those edges, and
// only these packages are allowed: hostconn, helper/client, helper/proto,
// hostmode, consent and log.
//
// # The two fences
//
// Every remote result is tagged with the connection GENERATION it was started
// under and with the DECISION REVISION (mode or consent) current at the time.
// Before any remote effect the inputs are read again, and before publishing a
// successful hello they are read again; a result tagged with an older pair is
// dropped. A transport generation is replaced on a new link; the revision moves
// when the host's mode or the machine's consent changes, which is what makes a
// mode change during an install cancel the work and discard its late answer.
//
// # What it deliberately does not do
//
// It never asks a person. The connect-time ask (ADR-0069, ADR-0105) is the open
// path's, and this package only reads the recorded answers. A connection with no
// recorded consent is absent(consent-pending). It never installs on a host whose
// mode is not helper, and never installs before a shell exists on the connection
// or while the transport grades itself slow (C9).
package farhelper

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
)

// State is the helper axis as a surface draws it. It is the backend's fold of
// the transport and this package's own state (ADR-727g63tr decision 2), never a
// value a renderer derives.
type State string

const (
	// StateStarting is a connection whose decision is still running.
	StateStarting State = "starting"
	// StateServing is a reachable far helper.
	StateServing State = "serving"
	// StateUnavailable is a decision that will not change without a person or
	// a new connection: a mode, an absent consent, a platform, a failed
	// install, a refused exec, a version mismatch, or a helper that was
	// serving and is gone.
	StateUnavailable State = "unavailable"
	// StatePaused is an axis whose transport is not connected. An absent one
	// stays unavailable through the pause: the decision did not change.
	StatePaused State = "paused"
)

// Reason is the closed set of reasons a surface may be told. It is the wire's
// vocabulary (ADR-727g63tr decision 2) and the one this package decides.
type Reason string

const (
	ReasonModeRaw             Reason = "mode-raw"
	ReasonModeScript          Reason = "mode-script"
	ReasonConsentPending      Reason = "consent-pending"
	ReasonConsentDenied       Reason = "consent-denied"
	ReasonUnsupportedPlatform Reason = "unsupported-platform"
	ReasonInstallFailed       Reason = "install-failed"
	ReasonExecForbidden       Reason = "exec-forbidden"
	ReasonVersionMismatch     Reason = "version-mismatch"
	ReasonHelperLost          Reason = "helper-lost"
	ReasonConnection          Reason = "connection"
)

// Action is the one thing a surface may offer. It never installs or accepts
// anything (ADR-0068): host settings opens the host record's mode, refresh
// redials a helper that is already installed.
type Action string

const (
	ActionNone         Action = "none"
	ActionHostSettings Action = "host-settings"
	ActionRefresh      Action = "refresh"
)

// Status is the published axis: {state, reason, action}. Reason is empty while
// serving or starting, and ActionNone is the value both carry.
type Status struct {
	State  State
	Reason Reason
	Action Action
}

// Unavailable is the typed refusal Acquire answers when the axis is not
// serving. A caller reaches it with errors.As, which is the whole reason it is
// a type rather than a formatted error: the refusal carries the reason and the
// action the surface would draw.
type Unavailable struct {
	Reason Reason
	Action Action
}

func (e *Unavailable) Error() string {
	if e.Action == "" || e.Action == ActionNone {
		return "far helper unavailable: " + string(e.Reason)
	}
	return fmt.Sprintf("far helper unavailable: %s (%s)", e.Reason, e.Action)
}

// The errors the remote seams answer with. They are this package's own because
// the composition root's probe and dial already have their own sentinels, and
// translating them at the adapter is one place rather than a classification
// this package would have to grow for each of them.
var (
	ErrUnsupportedPlatform = errors.New("farhelper: no helper is built for this platform")
	ErrExecForbidden       = errors.New("farhelper: the host refused the helper exec")
	ErrVersionMismatch     = errors.New("farhelper: the helper answered a different version")
)

// Platform is the far host's kernel and machine, in Go's vocabulary. It is
// declared here rather than imported from deploy so this package's import list
// stays the one the design names.
type Platform struct {
	GOOS   string
	GOARCH string
}

// Installed is a completed install of the far helper: the directory that names
// the machine's install and the content hash that names the build.
type Installed struct {
	Dir        string
	Generation string
}

// Target is one resolved destination, plus the connection identity the work is
// fenced by. The destination is resolved by the coordinator before it reaches
// here: this package reads no ssh config and holds no credential.
type Target struct {
	ConnectionID       string
	Destination        proto.SSHDestination
	HostKeyFingerprint string
	Generation         uint64
}

// Connection is one observation of a connection, as this package needs it.
//
// It is the coordinator's own shape rather than the wire's, for the same reason
// hostconn.Facts is: a decision needs a state, a generation, a quality grade and
// a shell count together, and the wire's per-pane event does not carry all four
// (the `connections` op of stage A does, and the composition root adapts it).
//
// GENERATION IS THE TRANSPORT GENERATION, not the install hash. It is the fence
// a late result is dropped by; the install hash travels in Installed.
type Connection struct {
	// ID is the helper's connection id, derived from the resolved destination.
	ID string
	// Destination is the resolved destination the connection dials.
	Destination proto.SSHDestination
	// HostKeyFingerprint is the machine's host key, observed at dial time; it
	// keys the host's mode and the machine's consent.
	HostKeyFingerprint string
	// Generation is which of the transport's LINKS this observation is about,
	// as the helper reports it. A result tagged with an older generation is
	// dropped.
	Generation uint64
	// State is hostconn's own state name. Only Connected makes the axis able
	// to serve; every other state pauses it.
	State hostconn.Kind
	// Slow is the transport's quality grade. C9 forbids installing while it is
	// true.
	Slow bool
	// Shells is PlainShells: C9 gates writing the helper until a sound
	// transport has a shell. A completed install is read-only and may be used
	// without a shell.
	Shells int
	// Seq orders observations of one connection. Zero means the source has no
	// ordering to offer, and the observation is applied as the newest.
	Seq uint64
}

// Connections is the source of connection observations. The composition root
// supplies it: the local helper's `connections` answer is the table — a
// connection no pane rides included — and the same coordinator's
// connection-state stream keeps it current.
type Connections interface {
	// List answers the connections the local helper holds right now, panes or
	// no panes. A coordinator restart rebuilds its entries from this.
	List() []Connection
	// Watch reports every later observation. The current list is NOT replayed:
	// the registry subscribes before List. Callbacks run on a goroutine that
	// must not wait: only O(1) enqueue and context cancellation are permitted.
	Watch(fn func(Connection)) (stop func())
}

// Modes and Consents are the two stores a decision reads. Their Watch is called
// on every change, with no key: the registry re-reads the record it cares about
// and moves the decision revision only when that record changed.
type Modes interface {
	Lookup(fingerprint string) (hostmode.Mode, bool)
	// Watch callbacks must not wait; only O(1) enqueue and context cancellation.
	Watch(fn func()) (stop func())
}

type Consents interface {
	Lookup(fingerprint string) (consent.Answer, bool)
	// Watch callbacks must not wait; only O(1) enqueue and context cancellation.
	Watch(fn func()) (stop func())
}

// Prober asks the far host what it is. It is one bounded command and writes
// nothing.
type Prober interface {
	Probe(ctx context.Context, target Target) (Platform, error)
}

// Installer writes the helper artifact for the probed platform and answers
// where it landed and which build it is.
type Installer interface {
	// Find is read-only and is not gated by C9; only a required upload is gated.
	Find(ctx context.Context, target Target, platform Platform) (Installed, bool, error)
	Install(ctx context.Context, target Target, platform Platform) (Installed, error)
}

// Dialer opens a lane to the installed helper and completes the hello. A
// returned client is owned by this package from that moment: it is closed when
// the axis leaves serving, when the connection ends, and on Close.
type Dialer interface {
	Dial(ctx context.Context, target Target, installed Installed) (*client.Client, error)
}

// Config is everything a Registry needs. Every remote seam must be present; a
// composition that has none answers exec-forbidden rather than panicking.
type Config struct {
	Connections Connections
	Modes       Modes
	Consents    Consents
	Probe       Prober
	Install     Installer
	Dial        Dialer
	Clock       hostconn.Clock
	Log         log.Logger
}

// Registry is the per-connection table. Entries live for the connection's life:
// created when the connection is first reported, removed when it ends or fails,
// never on a pane's close.
type Registry struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	publishMu sync.Mutex
	mu        sync.Mutex
	entries   map[string]*entry
	watchers  map[uint64]func(string, Status)
	nextWatch uint64
	closed    bool

	stopConns    func()
	stopModes    func()
	stopConsents func()
}

// New builds the registry and starts deciding for the connections already
// present. It answers a working registry even with a nil source (a composition
// that feeds it later), and a nil remote seam makes every decision
// exec-forbidden rather than crashing a background goroutine.
func New(cfg Config) *Registry {
	if cfg.Clock == nil {
		cfg.Clock = hostconn.RealClock{}
	}
	if cfg.Log == nil {
		cfg.Log = log.NewSlogAdapter(nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registry{
		cfg:      cfg,
		ctx:      ctx,
		cancel:   cancel,
		entries:  make(map[string]*entry),
		watchers: make(map[uint64]func(string, Status)),
	}
	// SUBSCRIBE BEFORE THE LIST, so an observation racing the initial read is
	// delivered rather than lost. A duplicate is harmless: the entry applies
	// the same facts twice.
	if cfg.Connections != nil {
		r.stopConns = cfg.Connections.Watch(r.observe)
		for _, c := range cfg.Connections.List() {
			r.observe(c)
		}
	}
	if cfg.Modes != nil {
		r.stopModes = cfg.Modes.Watch(r.storeChanged)
	}
	if cfg.Consents != nil {
		r.stopConsents = cfg.Consents.Watch(r.storeChanged)
	}
	return r
}

// Close ends every decision and closes every client this registry owns.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	if r.stopConns != nil {
		r.stopConns()
	}
	if r.stopModes != nil {
		r.stopModes()
	}
	if r.stopConsents != nil {
		r.stopConsents()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	entries := make([]*entry, 0, len(r.entries))
	for id, e := range r.entries {
		entries = append(entries, e)
		delete(r.entries, id)
	}
	r.mu.Unlock()
	r.cancel()
	for _, e := range entries {
		e.close()
	}
}

// Watch registers fn for every published axis change. It answers the function
// that ends the subscription; calling that twice is harmless. The callback is
// made outside the registry's lock and must not block: it is the party that
// draws the axis, not a step in deciding it.
func (r *Registry) Watch(fn func(connectionID string, status Status)) (stop func()) {
	if r == nil || fn == nil {
		return func() {}
	}
	r.mu.Lock()
	r.nextWatch++
	id := r.nextWatch
	r.watchers[id] = fn
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.watchers, id)
			r.mu.Unlock()
		})
	}
}

// Axis answers the current status of one connection, and whether this registry
// knows it.
func (r *Registry) Axis(connectionID string) (Status, bool) {
	e := r.entry(connectionID)
	if e == nil {
		return Status{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statusLocked(), true
}

// Acquire answers a lease on the connection's far helper. It waits while the
// axis is starting, and otherwise answers the client with a context that ends
// when the axis leaves serving, or *Unavailable with the reason a surface would
// draw.
//
// It does not start anything: a caller never causes an install, an install is
// the background decision's. The caller's own context bounds the wait for a
// decision that is still starting.
func (r *Registry) Acquire(ctx context.Context, connectionID string) (*Lease, error) {
	e := r.entry(connectionID)
	if e == nil {
		return nil, &Unavailable{Reason: ReasonConnection, Action: ActionNone}
	}
	for {
		e.mu.Lock()
		if e.ctx.Err() != nil {
			e.mu.Unlock()
			return nil, &Unavailable{Reason: ReasonConnection, Action: ActionNone}
		}
		if e.frozen && e.phase != phaseAbsent {
			e.mu.Unlock()
			return nil, &Unavailable{Reason: ReasonHelperLost, Action: ActionNone}
		}
		switch e.phase {
		case phaseServing:
			if e.client != nil && e.servingCtx != nil {
				lease := &Lease{Client: e.client, Ctx: e.servingCtx}
				e.mu.Unlock()
				return lease, nil
			}
		case phaseAbsent:
			status := e.statusLocked()
			e.mu.Unlock()
			return nil, &Unavailable{Reason: status.Reason, Action: status.Action}
		case phaseBroken:
			status := e.statusLocked()
			e.mu.Unlock()
			return nil, &Unavailable{Reason: status.Reason, Action: status.Action}
		case phaseDeciding:
			if e.connectedLocked() {
				// Starting: a decision is running, and waiting is the honest
				// answer because the wire has no "starting" reason.
			} else {
				e.mu.Unlock()
				return nil, &Unavailable{Reason: ReasonConnection, Action: ActionNone}
			}
		}
		ready := e.ready
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.ctx.Done():
			return nil, &Unavailable{Reason: ReasonConnection, Action: ActionNone}
		case <-ready:
		}
	}
}

// Refresh redials the helper already installed on a connection that had one,
// and never installs. It is offered only for helper-lost; a call on an absent
// axis or on one with no install is a no-op, not an install.
func (r *Registry) Refresh(_ context.Context, connectionID string) error {
	e := r.entry(connectionID)
	if e == nil {
		return &Unavailable{Reason: ReasonConnection, Action: ActionNone}
	}
	e.refresh()
	return nil
}

// CloseFor ends every client this registry holds for the machine identified by
// its host-key fingerprint and refuses to dial again until FinishFor. It is
// D25's close-before-remove half: no helper may be running out of a directory
// being deleted, and a decision that is mid-install must not redial into one.
func (r *Registry) CloseFor(fingerprint string) {
	if fingerprint == "" {
		return
	}
	for _, e := range r.matching(fingerprint) {
		e.freeze()
	}
}

// FinishFor releases the frost CloseFor took. The next decision reads the mode
// and consent again, so an uninstall that revoked consent answers
// absent(consent-pending) rather than installing the helper it just removed.
func (r *Registry) FinishFor(fingerprint string) {
	if fingerprint == "" {
		return
	}
	for _, e := range r.matching(fingerprint) {
		e.thaw()
	}
}

func (r *Registry) matching(fingerprint string) []*entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.fingerprint() == fingerprint {
			out = append(out, e)
		}
	}
	return out
}

func (r *Registry) entry(connectionID string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[connectionID]
}

// observe applies one connection observation. An ended or failed connection
// removes the entry: its lifetime is the connection's, never a pane's.
func (r *Registry) observe(c Connection) {
	if c.ID == "" {
		return
	}
	if c.State == hostconn.Ended || c.State == hostconn.Failed {
		r.remove(c.ID)
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	e := r.entries[c.ID]
	if e == nil {
		e = newEntry(r, c)
		r.entries[c.ID] = e
		e.start()
		r.mu.Unlock()
		return
	}
	e.apply(c)
	r.mu.Unlock()
}

func (r *Registry) remove(connectionID string) {
	r.mu.Lock()
	e := r.entries[connectionID]
	delete(r.entries, connectionID)
	if e != nil {
		e.close()
	}
	r.mu.Unlock()
}

// storeChanged re-reads the mode and consent for every entry. The stores report
// "something changed", not which record: each entry decides whether the change
// is about its own machine, and only that one moves its revision.
func (r *Registry) storeChanged() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		e.inputsChanged()
	}
}

// decide runs one step of an entry's decision. It is a method on the registry
// because this is the seam the stage's own check names — `deadcode -whylive
// '…/internal/farhelper.(*Registry).decide'` must answer a path from main — and
// because a step is the registry's work, not a value the entry owns.
func (r *Registry) decide(e *entry) {
	e.advance()
}

func (r *Registry) publishEntry(e *entry, status Status) {
	// Delivery is serialized separately from map access. A replacement's
	// frame follows an already-authorized old callback, never precedes it.
	// Callbacks may read Axis/Acquire; no registry/state lock is held here.
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	r.mu.Lock()
	current := r.entries[e.id] == e
	// A retired actor may publish its final status only while the identity
	// remains absent. A replacement owns all subsequent frames for that ID.
	if status.State == StateUnavailable && e.ctx.Err() == nil && r.entries[e.id] == nil {
		e.mu.Lock()
		current = e.closed
		e.mu.Unlock()
	}
	r.mu.Unlock()
	if current {
		r.publish(e.id, status)
	}
}

func (r *Registry) publish(connectionID string, status Status) {
	r.mu.Lock()
	watchers := make([]func(string, Status), 0, len(r.watchers))
	for _, fn := range r.watchers {
		watchers = append(watchers, fn)
	}
	r.mu.Unlock()
	for _, fn := range watchers {
		fn(connectionID, status)
	}
}

// Lease is the far helper client and its serving lifetime. Ctx is cancelled the
// moment the axis leaves serving; the client is this registry's to close, so a
// holder must not close it.
type Lease struct {
	Client *client.Client
	Ctx    context.Context
}
