// Package control is the scheduling contract for the JSON-RPC control plane.
//
// Today internal/transport/ws.go runs ~86 control methods synchronously on
// the WebSocket read loop, so a slow method (a 30-second SSH probe) freezes
// every terminal tab on that socket. This package is the contract that lets
// that work run off the read loop under a bound. It is deliberately
// socket-free: it imports no websocket type and no internal/transport type,
// and is fully testable with no connection.
//
// The model, in one line: work is handed over through a Submission; a bounded
// Submission runs each task on its own goroutine only after acquiring a
// Permit from an Admission; Admissions compose, and conflict admission is
// acquired BEFORE the scarce execution permit (the order passed to
// NewComposite), so refused or waiting conflict work never occupies an active
// worker permit.
//
// Variation is expressed by the interface, never by a fork inside an
// implementation (AD-8): a new kind of resource is added by constructing
// another Admission and wiring it, with zero edits in this package. Name()
// exists for metrics only — no implementation may branch on it.
package control

import (
	"context"
	"time"
)

// Admission is a bounded resource. TryAcquire never blocks: it either returns
// a Permit immediately or a *Rejection describing why not.
type Admission interface {
	// Name identifies the resource for metrics only. No implementation may
	// branch on it (AD-8).
	Name() string
	TryAcquire(context.Context) (Permit, *Rejection)
}

// NonblockingAdmission is an Admission whose TryAcquire never blocks: it
// either returns a Permit immediately or a *Rejection. It is the only
// admission a bounded Submission may hold, because a Submission's TrySubmit
// is called by the read loop, which must never block on admission.
//
// The marker method is unexported, so only types in this package satisfy the
// interface: NewSemaphore and NewCompositeNonblocking produce values of it,
// and NewWaitingSemaphore deliberately does not. A waiting admission wired
// into a Submission is a compile error, not a runtime surprise (ADR-0026).
type NonblockingAdmission interface {
	Admission
	nonblocking()
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

// Rejection describes why TryAcquire or TrySubmit refused work. A nil
// *Rejection means success.
type Rejection struct {
	Reason     string
	Scope      string
	RetryAfter time.Duration
	// Holders names the requests that held the refusing admission at the
	// moment it refused (nocx-3an1s.1.8). It is empty for a refusal with no
	// held capacity — a closed submission, a missing key — and for an
	// admission that keeps no identity. Oldest first.
	Holders []Holder
}

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
