// Package sessionruntime is the session runtime's state machine, written as an
// executable contract BEFORE it is an implementation (bead nocx-ygxjv.1,
// [ADR-0066]).
//
// # What is here and what is deliberately not
//
// These files declare the VOCABULARY: the states, the events, the errors and
// the [Runtime] interface an implementation must satisfy. They contain no
// functions, because there is nothing yet to run — nocx-ygxjv.2 builds the
// implementation, and the contract exists so that it cannot choose the model by
// accident, the way internal/lifecycle's kernel exists ahead of its transports.
//
// The MODEL — a reference implementation of [Runtime] with no I/O — and the
// SCHEDULES that judge it live in this package's _test.go files. When the real
// runtime arrives it implements the same interface and the same schedules run
// against it, which is the only way the contract can be evidence about the
// product rather than about itself.
//
// # Why the vocabulary is production and the model is not
//
// Not an aesthetic choice. A new production package with no caller fails the
// dead-code ratchet (AGENTS.md, "Is the code reachable?"), and a package with
// only _test.go files fails `go build ./...`, which the build-ci target runs.
// Types, constants and an interface are neither: `deadcode` answers "is this
// FUNCTION reachable from main", and there are no functions here to answer
// about. The model's own constructor and transitions are therefore in the test
// files, where they belong until something implements them for real.
//
// # Composition, not absorption
//
// The runtime does NOT own the authenticated lifecycle. internal/lifecycle
// already does — domains, epochs, capabilities, the sequence rule, the attempt
// model and logical completion are its, and ADR-0024 decisions 2, 3, 5, 6, 7
// and 8 are recorded against it. What the runtime owns is the RENDEZVOUS: the
// meeting of an authenticated completion with the render fence the emulator
// saw, which executes in the renderer today (frontend/src/scrollback/blocks.ts)
// and moves here because the emulator moved here (ADR-0066). So the lifecycle
// reaches this package through [AuthenticatedEvents], a port, and nothing in
// here authenticates anything.
//
// # The write boundary already has an owner, and this does not become a second
//
// internal/helper/session.hostSession.write holds its mutex from validating the
// writer, the attachment and proto.LeaseEpoch through the return of
// proc.Write, so a lease transition can never land between the check and the
// write. That lease's subject is a COORDINATOR ATTACHMENT: which connection may
// write at all. [ControlEpoch] here has a different subject — which PRINCIPAL,
// a person or an agent, is currently directing the session — and the two are
// nested rather than parallel. A write is executed only when it carries a live
// control epoch AND arrives over the attachment holding the carrier lease.
// Declaring both, and the relation, is the point: proto.LeaseEpoch cannot be
// declared the human/agent control epoch (today a person and an agent reach the
// PTY through one of them), and a second counter with the SAME subject would be
// the duplicate-owner defect AD-8 exists to prevent.
package sessionruntime
