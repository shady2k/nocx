// Package control is the scheduling contract for the JSON-RPC control plane.
//
// The JSON-RPC control plane must never run a slow method on the WebSocket
// read loop: a 30-second SSH probe would freeze every terminal tab on that
// socket. This package is the contract that lets that work run off the read
// loop. It is deliberately socket-free: it imports no websocket type and no
// internal/transport type, and is fully testable with no connection.
//
// The model, in one line: work is handed over through a Submission, which
// runs it on its own goroutine and never waits; an Admission is a resource
// the work takes on that goroutine, and Admissions compose in a fixed order
// so conflict work is acquired before execution capacity.
//
// AFTER ADR-8eej3e66 STAGE 8 NOTHING HERE REFUSES FOR CAPACITY. A busy
// resource makes a caller WAIT under its declared turn, and a wait that runs
// out answers "did not start". The remaining rejections are a wait that
// ended, the caller's own cancellation, and a resource that is unavailable
// until restart.
//
// Variation is expressed by the interface, never by a fork inside an
// implementation (AD-8): a new kind of resource is added by constructing
// another Admission and wiring it, with zero edits in this package. Name()
// exists for metrics only — no implementation may branch on it.
package control

import (
	"context"
)

// Admission is a bounded resource. TryAcquire never blocks: it either returns
// a Permit immediately or a *Rejection describing why not.
//
// After ADR-8eej3e66 stage 8 no admission refuses for CAPACITY: a resource
// that is being used makes the next caller WAIT under its declared turn
// (control.NewWaitingSemaphore, ResourceOrder, schedule.Keyed, schedule.
// Bulkhead, operation/domains), and a wait that runs out answers the
// declaration's "did not start". The remaining rejections are the ones a
// person can act on — a wait that ended, the caller's own cancellation, a
// store quarantined until restart — and they are classified by
// RejectionKind.
type Admission interface {
	// Name identifies the resource for metrics only. No implementation may
	// branch on it (AD-8).
	Name() string
	TryAcquire(context.Context) (Permit, *Rejection)
}

// Permit is a held slice of an Admission's capacity. Release returns it.
//
// Release MUST be idempotent: the second and later calls give nothing further
// back. This is part of the contract rather than a caller obligation because
// the failure it prevents is silent — a permit that returned more capacity
// than it held would let its Admission admit past its bound, and a bound that
// can be exceeded without anyone noticing is not a bound. Every implementation
// here enforces it with a sync.Once; one supplied from outside must too.
type Permit interface{ Release() }

// RejectionKind classifies why an admission did not grant its resource. Every
// value is something a person can be told; there is deliberately no capacity
// kind, because no admission refuses for capacity (ADR-8eej3e66 §7).
type RejectionKind string

const (
	// RejectionWaitExceeded is a bounded wait that ran out before the work
	// started. The declaration's answer is "did not start": nothing took
	// effect, and the same action may be asked again.
	RejectionWaitExceeded RejectionKind = "wait-exceeded"
	// RejectionCancelled is the caller's own context ending the wait.
	RejectionCancelled RejectionKind = "cancelled"
	// RejectionUnavailable is a resource that will not become available
	// without a restart — the quarantined store set of ADR-8eej3e66 §5.
	RejectionUnavailable RejectionKind = "unavailable"
)

// Rejection describes why TryAcquire or TrySubmit did not grant work. A nil
// *Rejection means success.
type Rejection struct {
	Kind   RejectionKind
	Reason string
	Scope  string
	// Holders names the requests that held the refusing admission at the
	// moment it refused (nocx-3an1s.1.8). It is empty for a refusal with no
	// held capacity — a closed submission, a missing key — and for an
	// admission that keeps no identity. Oldest first.
	Holders []Holder
}

// Unrestricted is an Admission that grants at once and holds nothing. It is
// the admission of an operation that orders on no resource and takes no
// execution capacity, which after ADR-8eej3e66 stage 8 is a real thing to
// declare rather than a placeholder: the work needs no exclusion and no
// permit, and saying so is what makes the absence of a gate visible.
func Unrestricted() Admission { return unrestricted{} }

type unrestricted struct{}

func (unrestricted) Name() string { return "unrestricted" }

func (unrestricted) TryAcquire(context.Context) (Permit, *Rejection) { return noPermit{}, nil }

// Submission is how work is handed over. It has one unconditional call site:
// the read loop submits, and the Submission decides whether the work runs now,
// later, or never.
type Submission interface {
	TrySubmit(context.Context, Task) *Rejection
}

// Shutdownable is implemented by submissions that own a worker lifecycle.
// Shutdown stops admission and lets already admitted work finish.
type Shutdownable interface {
	Shutdown()
}

// Task is a unit of control-plane work. Run is called with the context passed
// to TrySubmit and must return when that context is cancelled.
type Task struct {
	// Key is the authoritative ordering identity for a keyed submission.
	// Other submissions ignore it. Keyed submissions require a nonempty key;
	// the caller validates and extracts it before admission.
	Key string
	// Start is the one-step start-against-cancel decision. A worker that is
	// about to run a queued task asks it; false means the caller withdrew the
	// request while it waited and Run must not be called. nil means "always
	// start". It is supplied by the request owner, so admission can decide
	// without knowing what cancellation means for that request.
	Start func() bool
	Run   func(context.Context)
}

// startAllowed applies the one-step gate. It is called immediately before
// Run, in every submission's worker, so a queued task that was cancelled
// while waiting never starts.
func startAllowed(task Task) bool {
	return task.Start == nil || task.Start()
}
