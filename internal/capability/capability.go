// Package capability is the typed, scoped domain-operation layer for the
// JSON-RPC control plane.
//
// The problem it solves: the transport is about to run ~86 control methods
// concurrently off the WebSocket read loop (internal/transport/control).
// "The stores are mutex-guarded, so this is not a data race" is true and
// beside the point: a mutex protects memory, not an invariant spanning two
// lock acquisitions or two stores. This package makes the declaration of
// what a handler may touch the ONLY way to touch it.
//
// # The model
//
// A handler is constructed with exactly one Operation — a typed
// ConfigOperation, VaultOperation, SessionOperation, … — and cannot obtain
// a store nobody gave it. An Operation owns access and exclusion together:
//
//   - Run acquires the operation's conflict gates (a control.Admission,
//     composed in the canonical order below), then calls the callback with
//     a domain SERVICE. The service lives inside the callback so it cannot
//     safely escape: every service method checks the operation's guard and
//     fails with ErrOperationInactive when called outside every in-flight
//     Run, so a captured handle cannot be carried out of the operation it
//     was issued for.
//   - The gates are WAITING (control.NewWaitingSemaphore): a gate held by
//     another operation makes the new operation WAIT (bounded by a wait
//     timeout and a queue-depth bound on the gate) rather than refuse. A
//     domain conflict is a serialisation point, not an overload — the
//     sequential client whose previous response left the permit held for a
//     moment must never be told the control plane is busy. Only exhausting
//     the bound is a refusal, and the transport maps that refusal to the
//     control.saturated wire error (internal/transport/ws_saturation.go);
//     a handler surfaces that. The wait happens inside Run, on the task
//     goroutine, and the composite acquires the conflict gates BEFORE the
//     execution lane (canonical order), so waiting conflict work never
//     occupies a worker permit.
//
// # Canonical acquisition order
//
// Every operation that spans domains acquires its gates in one order:
//
//	config, vault, content, session, git, filesystem, api
//
// The order is fixed here and enforced by the constructors, which take the
// domain gates as separate parameters and compose them in this order. Two
// operations whose resource sets overlap therefore cannot acquire in
// opposite orders; with the current non-blocking gates a deadlock is
// structurally impossible, and the order keeps it impossible if a blocking
// admission is ever introduced.
//
// # Conservative grain
//
// The initial operations take the whole domain where the conflict set is
// not yet confidently known (the brief's rule: "where the conflict set is
// not yet confidently known, take the whole domain"). Concretely:
//
//   - the config domain (profiles + groups + settings) resolves vault row
//     handles on its write paths (ADR-0017) and its secret-class settings
//     are vault-backed, so ConfigOperation holds [config, vault];
//   - the vault-secret domain computes its inventory inputs from profile
//     reads, so SecretOperation holds [config, vault];
//   - the API-testing import writes secret VALUES through the binding store
//     (design §8.1), which is vault-backed, so APIImportOperation holds
//     [vault, api];
//   - everything else holds its own domain's gate.
//
// Grain is a property of the operation implementation, refinable later
// without touching a single handler.
//
// # Read policy (stated per domain, per the brief)
//
//   - config: reads PARTICIPATE in the config gate. The profile/group
//     store gives a coherent single-file snapshot, but a config read that
//     spans profiles + settings (export.configExport, vault inventory
//     inputs) could otherwise observe two stores at different generations;
//     the gate keeps multi-store readers coherent.
//   - vault: reads participate in the vault gate. The vault is one
//     mutex-guarded store, but lifecycle ops (seal, reset) and secret ops
//     share the store; a seal between two secret operations is a conflict
//     the gate excludes conservatively.
//   - content, session, git, filesystem: reads participate in their
//     domain's gate. Each is one store; the conservative posture is one
//     gate per domain until per-id grain exists.
//   - api: reads participate in the api gate, and api.request.send holds it
//     only long enough to snapshot the request — never across the dial
//     (api.go).
//
// # Refinements deliberately deferred
//
//   - per-session / per-binding gate grain (session:<id>, git-binding:<id>,
//     filesystem-binding:<id>) — the current gates are whole-domain;
//   - splitting config reads out of the [config, vault] pair once the row
//     translation is measured.
//
// Both are implementation changes inside this package; no handler changes.
package capability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
)

// Domain gate names. They label the gates for metrics only
// (control.Admission.Name); no implementation may branch on them (AD-8).
const (
	GateConfig     = "config"
	GateVault      = "vault"
	GateContent    = "content"
	GateSession    = "session"
	GateGit        = "git"
	GateFilesystem = "filesystem"
	// GateAPI is the API-testing collection's own domain (api.go). It is
	// NOT the config gate: snippets, notes and UI state hold that one
	// because each is a document under the profile directory that
	// backup/restore also writes, and a collection is an arbitrary folder
	// the user chose (design §6.1) which nothing else in the app touches.
	GateAPI = "api"
)

// CanonicalOrder is the order every multi-domain operation acquires its
// gates in. Keep it in sync with the constructors in this package; a
// constructor that composes gates in a different order is a deadlock
// waiting for a blocking admission.
var CanonicalOrder = []string{GateConfig, GateVault, GateContent, GateSession, GateGit, GateFilesystem, GateAPI}

// Gate returns a fresh domain gate with the given capacity. The composition
// root builds one gate per domain and passes the same gate to every
// operation that touches that domain, so operations on the same domain
// exclude each other. Capacity 1 is the conservative whole-domain exclusion;
// a larger capacity admits more concurrent operations of that domain at the
// cost of weaker exclusion.
//
// The gate WAITS (bounded) for capacity instead of refusing instantly: a
// conflicting operation is a serialisation point, not an overload — the
// second request may proceed once the first releases, and the very next
// request of a sequential client must never be answered "control plane
// busy" because the response to its previous request left the permit still
// held for a moment. maxQueue bounds how many requests may wait on the
// gate (beyond it, refusal is instant) and waitTimeout bounds how long a
// request waits (beyond it, refusal). Only exhausting a bound is a refusal.
// The composition root supplies both; the defaults exist for direct use
// (tests) and are deliberately generous.
func Gate(domain string, capacity, maxQueue int, waitTimeout time.Duration) control.Admission {
	return control.NewWaitingSemaphore(domain, capacity, maxQueue, waitTimeout)
}

// ErrOperationInactive is returned by a domain service method called
// outside every in-flight Run of its operation — the escaped-handle
// failure. A service captured by a callback is only usable while the
// operation is running; after the last Run returns, the next call fails
// with this error instead of touching the store without the exclusion the
// operation exists to provide.
var ErrOperationInactive = errors.New("capability: service used outside its operation")

// RefusedError is returned by Run when the operation's conflict gates
// refused the work. It carries the control.Rejection the transport already
// maps to the control.saturated wire error.
type RefusedError struct {
	Rejection control.Rejection
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("capability %s refused: %s", e.Rejection.Scope, e.Rejection.Reason)
}

// IsRefused reports whether err is a gate refusal (a *RefusedError).
func IsRefused(err error) bool {
	var re *RefusedError
	return errors.As(err, &re)
}

// guard is the liveness link between an operation and the service it hands
// out. Run increments the active count for the callback's duration; every
// service method checks it. The count (rather than a boolean) is what makes
// the same operation safe under concurrent Runs: the handler for a method
// is a single instance, and the bounded submission may invoke it
// concurrently, so two in-flight Runs of one operation must not invalidate
// each other.
type guard struct {
	mu     sync.Mutex
	active int
}

// begin marks one in-flight Run. Called with the permit already held.
func (g *guard) begin() {
	g.mu.Lock()
	g.active++
	g.mu.Unlock()
}

// end releases one in-flight Run. Called before the permit is released.
func (g *guard) end() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}

// check is the error-returning form of the liveness test.
func (g *guard) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 {
		return nil
	}
	return ErrOperationInactive
}

// ok is the boolean form, for service methods that cannot return an error
// (vault.Seal, vault.Activity): an escaped call silently does nothing
// rather than touching the store outside the exclusion.
func (g *guard) ok() bool {
	return g.check() == nil
}

// operation is the shared acquire-run-release core every domain operation
// embeds. It is the ONE place the exclusion is enforced; a domain operation
// type is a typed alias over it with a fixed service type.
type operation[S any] struct {
	admission control.Admission
	guard     *guard
	service   S
}

// Run acquires the operation's gates (one composite admission, built in
// the canonical order), marks the guard active for the callback's
// duration, and releases everything on exit. A gate that refuses the work
// returns a *RefusedError without running the callback.
func (op *operation[S]) Run(ctx context.Context, fn func(context.Context, S) error) error {
	permit, rej := op.admission.TryAcquire(ctx)
	if rej != nil {
		return &RefusedError{Rejection: *rej}
	}
	// Release the permit only after the guard has gone cold: a service
	// that escaped the callback must not observe a live guard in the
	// window between the gate freeing and the callback ending.
	defer permit.Release()
	defer op.guard.end()
	op.guard.begin()
	return fn(ctx, op.service)
}
