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
//     another operation makes the new operation WAIT, bounded by the turn its
//     catalogue entry declares (ADR-8eej3e66 §3), rather than refuse. A
//     domain conflict is a serialisation point, not an overload — the
//     sequential client whose previous response left the permit held for a
//     moment must never be told the control plane is busy. A wait that runs
//     out answers the declaration's own "did not start"; nothing refuses for
//     capacity. The wait happens inside Run, on the task goroutine.
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
// It governs the gates that remain. Migration stages 5–7 of ADR-8eej3e66
// removed git's gate (a per-checkout key and a per-mount bulkhead in
// internal/operation/schedule replace it) and the session, content and API
// gates (per-resource FIFOs, internal/transport/control.ResourceOrder). The
// store guard that replaced the config and vault gates keeps the same
// sequence inside its own plan: every declared barrier, then config, vault,
// content in that key order.
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
//   - the API-testing import writes only collection files and never touches
//     vault material, so APIImportOperation holds [api];
//   - everything else holds its own domain's gate.
//
// Grain is a property of the operation implementation, refinable later
// without touching a single handler.
//
// # Read policy (stated per domain, per the brief)
//
//   - config: reads PARTICIPATE in the store guard's shared plan for the
//     config store (stores.go: every barrier that protects it, then its own
//     key). The profile/group store gives a coherent single-file snapshot,
//     but a config read that spans profiles + settings (export.configExport,
//     vault inventory inputs) could otherwise observe two stores at
//     different generations; the plan keeps multi-store readers coherent and
//     lets a restore exclude them.
//   - vault: reads participate in the guard's shared plan for the vault. The
//     vault is one mutex-guarded store, but lifecycle ops (seal, reset) and
//     secret ops share the store; a seal between two secret operations is a
//     conflict the plan excludes conservatively.
//   - session and content: PER RESOURCE, not per domain (ADR-8eej3e66
//     section 5, migration stage 6). Their operations are constructed with a
//     control.ResourceOrder instead of a domain gate: it takes the keys the
//     invocation's declaration names — one session, the content store, the
//     layout collection — and a waiter on a different resource is not behind
//     anybody. The key set and the wait bound arrive in the context from the
//     dispatcher, which is the only layer that has both the declaration and
//     the validated params, and capability reads neither. Reads order like
//     writes: a read of one resource is a phase with the resource in it.
//     Content composes that FIFO with the store guard's shared barrier
//     (StoreGuard.SharedBarrier), barrier first, so a restore's exclusive
//     claim excludes its writers without the FIFO's grain returning to one
//     key for the whole store.
//   - git: PER CHECKOUT AND PER MOUNT (ADR-8eej3e66 section 5, migration
//     stage 5). A git operation orders on its checkout's key in
//     internal/operation/schedule, and its execution is bounded by the
//     mount's bulkhead; a staged read of one session inside git.open still
//     takes the session gate, which stage 6 left for the domains that stage
//     a registry lookup under it.
//   - filesystem: reads participate in the filesystem gate, and so does a
//     staged read of one session inside files.open. It is one store; the
//     conservative posture is one gate per domain until its stage migrates.
//   - api: reads order per open collection (a ResourceOrder keyed on the
//     collection handle), and api.request.send holds that key only long
//     enough to snapshot the request — never across the dial (api.go).
//
// # Refinements deliberately deferred
//
//   - per-binding gate grain for filesystem (filesystem-binding:<id>) — that
//     gate is whole-domain until its stage migrates; git took the
//     per-checkout key in stage 5;
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

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/transport/control"
)

// Domain gate names. They label the gates for metrics only
// (control.Admission.Name); no implementation may branch on them (AD-8).
const (
	GateConfig  = "config"
	GateVault   = "vault"
	GateContent = "content"
	GateSession = "session"
	GateGit     = "git"
	// GateFilesystem names the domain, and no gate is constructed for it
	// since stage 4 of ADR-8eej3e66: filesystem work is ordered per binding
	// and bounded per failure domain (internal/operation/domains). The name
	// stays because CanonicalOrder is a fixed order and a future barrier
	// would need the slot; a gate built here again would be the regression.
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
func Gate(domain string, capacity int, waitTimeout time.Duration) control.Admission {
	return control.NewWaitingSemaphore(domain, capacity, waitTimeout)
}

// UnlockAnswering marks an admission whose acquisition THE ANSWER TO A VAULT
// UNLOCK must itself make, and the declaration is the whole of it: an
// operation composed from such an admission marks its callback's context, and
// an operation-stance credential read that sees the mark fails rather than
// waiting for a dialog nobody can satisfy (credential/unlock_fence.go).
//
// Today that is exactly one admission — the vault's, because vault.unseal runs
// under it (transport.vaultSpecs) — and the composition root says so rather
// than this package inferring it from a domain name, which is metrics-only
// (AD-8). Two handlers have already raised an unanswerable unlock by holding
// this admission across a resolve (nocx-o3606, nocx-9fzkk); the mark is what
// stops the third.
//
// It takes an Admission rather than a domain name and a capacity because the
// vault's admission is not a plain domain gate any more: stage 7 of
// ADR-8eej3e66 replaced it with the store key and the barrier plan
// (stores.go), and the fence has to travel with that object unchanged. The
// question the marker answers — "does the answer to this unlock need the
// admission I am holding" — is a property of the admission, not of how it was
// constructed.
//
// The ssh dial legitimately resolves a key passphrase while holding no domain
// gate at all (openOperation's Dial phase), so it is untouched by the mark.
// The execution lane that used to bound it went in stage 8, which is what
// closed nocx-0dvf2: with no lane there is no capacity for a full window to
// hold against the unseal.
func UnlockAnswering(a control.Admission) control.Admission {
	return unlockAnsweringGate{Admission: a}
}

// unlockAnsweringGate is the marker wrapper. It is a type rather than a flag
// on the admission so the declaration travels with the gate through every
// composite it is put into, and so only this package can make one.
type unlockAnsweringGate struct {
	control.Admission
}

// answeringGateIn reports the name of the unlock-answering gate inside a
// (possibly composite) admission, if there is one. Asked once per operation,
// at construction.
func answeringGateIn(a control.Admission) (string, bool) {
	for _, part := range control.Parts(a) {
		if g, ok := part.(unlockAnsweringGate); ok {
			return g.Name(), true
		}
	}
	return "", false
}

// ErrOperationInactive is returned by a domain service method called
// outside every in-flight Run of its operation — the escaped-handle
// failure. A service captured by a callback is only usable while the
// operation is running; after the last Run returns, the next call fails
// with this error instead of touching the store without the exclusion the
// operation exists to provide.
var ErrOperationInactive = errors.New("capability: service used outside its operation")

// ErrOperationUnavailable is returned when an operation was constructed
// without its domain service. The operation remains present so callers can
// report an unavailable capability instead of dereferencing a typed nil.
var ErrOperationUnavailable = errors.New("capability: operation service unavailable")

// refused is the ONE place an admission refusal becomes the error a handler
// answers with, and the one place it is reported to the request's diagnostics
// record (nocx-3an1s.1.8). Two ownerships in one call because a second copy
// of "a gate refused" is where the scope a person is told about would drift
// from the scope the record kept.
func refused(ctx context.Context, rej *control.Rejection) error {
	control.ReportRefusal(ctx, *rej)
	if rej.Kind == control.RejectionUnavailable {
		return &UnavailableError{Scope: rej.Scope, Reason: rej.Reason}
	}
	return &RefusedError{Rejection: *rej}
}

// UnavailableError is returned by Run when the store an operation needs is
// quarantined: it will not serve until restart, so the answer is not the busy
// dialogue and not a retry (ADR-8eej3e66 §5). It is deliberately NOT a
// *RefusedError: the transport answers it as a wait that ended or a
// cancellation, and a store that is unavailable is not a wait.
type UnavailableError struct {
	// Scope names the store as "store:<name>".
	Scope string
	// Reason is the sentence a person reads. It names the store, says what
	// happened and says that a restart ends it.
	Reason string
	// Err is the cause, when there is one: a restore that quarantined the set
	// keeps its recovery failure reachable here.
	Err error
}

func (e *UnavailableError) Error() string { return e.Reason }

func (e *UnavailableError) Unwrap() error { return e.Err }

// RefusedError is returned by Run when an admission did not grant the work:
// a wait that ran out, the caller's cancellation, or a quarantined store.
// It carries the control.Rejection the transport answers.
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
	admission   control.Admission
	guard       *guard
	service     S
	disposition Disposition
	// answeringGate is the name of the unlock-answering gate this
	// operation's admission holds, empty when it holds none. Computed once
	// by newOperation, because the answer cannot change for the life of the
	// operation and Run must not walk a composite per call.
	answeringGate string
	// ordered reports that the admission holds per-resource keys. The
	// callback then runs inside that ordered scope, and a further ordered
	// acquisition from it is the nesting ADR-8eej3e66 section 5 forbids: it
	// would be two keys held in whatever order two handlers happened to
	// acquire them, which is a deadlock waiting for a slow dependency.
	ordered bool
}

// Disposition returns the operation's explicit assistant projection contract.
func (op *operation[S]) Disposition() Disposition {
	return op.disposition
}

// Run acquires the operation's gates (one composite admission, built in
// the canonical order), marks the guard active for the callback's
// duration, and releases everything on exit. A gate that refuses the work
// returns a *RefusedError without running the callback.
func (op *operation[S]) Run(ctx context.Context, fn func(context.Context, S) error) error {
	permit, rej := op.admission.TryAcquire(ctx)
	if rej != nil {
		return refused(ctx, rej)
	}
	// The callback's context says it holds the resources, so a nested
	// ordered acquisition inside it panics instead of deadlocking
	// (control.ResourceOrder.TryAcquire). Set after the permit exists: the
	// acquisition itself must not see the mark.
	if op.ordered {
		ctx = control.WithOrderHeld(ctx)
	}
	// Release the permit only after the guard has gone cold: a service
	// that escaped the callback must not observe a live guard in the
	// window between the gate freeing and the callback ending.
	defer permit.Release()
	defer op.guard.end()
	op.guard.begin()
	// THE CALLBACK'S CONTEXT SAYS WHAT IT IS INSIDE. An operation-stance
	// credential read waits for a person to answer the vault's unlock, and
	// the answer is a vault.unseal that needs this very gate; a read from in
	// here would show somebody a dialog and then refuse their Unlock. The
	// mark makes that a loud, immediate failure of the one request instead
	// (credential/unlock_fence.go). It is set only when the operation
	// actually holds such a gate, so the callbacks that legitimately resolve
	// material — the ssh dial holds the lane and no domain gate — are
	// untouched.
	if op.answeringGate != "" {
		ctx = credential.WithUnlockAnswerGate(ctx, op.answeringGate)
	}
	return fn(ctx, op.service)
}
