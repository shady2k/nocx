// Package wave holds the record the backend keeps for a wave of agents: who
// is in it, what is known about each of them, and how a participant comes
// into existence without ever being forked before it is recorded.
//
// # Why the record is here and not in the agent
//
// The defect this exists for is a coordinator that forgets it has children.
// The cure is not a better protocol for the agent to follow, because an agent
// exists only while it takes a turn: anything that depends on an act the agent
// must remember is not an invariant. That holds against every upstream fix,
// including a durable-across-turns monitor, which still has to be armed.
//
// So supervision is the backend's (D1 of the 2026-08-24 orchestration
// mechanism design). The backend is a process without turns. It holds this
// record and it cannot forget. An earlier draft made supervision a LEASE the
// coordinator renews; renewal is an act, and a renewal is a poll by another
// name.
//
// # The three levers, and the fourth thing that is not one
//
// Only PROCESS EXIT and the participant's own DECLARATION may decide a
// participant's state (D9), beside what we launched it with. Process exit is
// ours because nocx owns the PTY; the declaration arrives over the
// authenticated channel of ADR-0024 decision 2. The backend's live grid
// (internal/panegrid) is a fourth source and is deliberately NOT one of these:
// it decides whether nocx may type into a pane and what the indicator shows,
// and nothing else. Nothing in this package reads a frame, and it imports no
// grid.
//
// # Membership is not delegation
//
// Two records, and neither implies the other (A1 of the wave authority
// model). MEMBERSHIP makes a participant addressable — it is what lets one
// participant reach another's mailbox. DELEGATION makes it controllable, and
// it is held by a controller SESSION rather than by a run, because a
// coordinator run ends while its workers live. A human taking over a worker's
// pane suspends the delegation's send-input and leaves membership untouched:
// the coordinator keeps seeing the worker and keeps being able to mail it, and
// loses only the right to type into it. Conflating the two is the defect this
// package is named against.
//
// # What this package does not claim
//
// A wave call carries NO authority the session does not already have (A12).
// Participant authenticity has no mechanism in this tree — there is no pidfd
// anywhere, start-time is read only remotely and only as a diagnostic, and a
// socketpair has no peer credential to stamp. An authority we cannot enforce
// is one we do not claim, so nothing here says "only the enrolled tree may
// speak for this participant".
package wave

import (
	"errors"
	"time"
)

// ID names a wave.
type ID string

// ParticipantID names one participant within one wave. It is minted by the
// backend before anything is forked, which is what makes an orphan
// discoverable rather than merely unlikely.
type ParticipantID string

// Role says what a participant is FOR. It is not an authority: what a
// coordinator may do to a worker is a Delegation, and a worker in a wave with
// no delegation over it is addressable and not controllable.
type Role string

const (
	// RoleCoordinator is the participant that dispatches work. There is
	// exactly one per wave in this slice (D15: one worker first, not three).
	RoleCoordinator Role = "coordinator"
	// RoleWorker is a participant that was given a task.
	RoleWorker Role = "worker"
)

// State is where a participant is in the interval this package defines.
//
// # The two terminal facts, and why neither alone reaches Completed
//
// A participant produces two facts at the end of its life, and they are
// independent: what it DECLARED it produced, and its PROCESS EXIT. The bead
// asks that neither alone terminalize it, and the reading this implements is
// the only one that does not leak a record:
//
//   - a declaration with no exit stays Live. The agent said it finished and is
//     still running; it may be given more work, and a record that called it
//     terminal would be describing a process that is still there.
//   - an exit with no declaration is terminal as Abandoned — deliberately NOT
//     Completed. Something is gone and it never said what it produced. A
//     coordinator reading Abandoned learns exactly that, which is the
//     fail-closed direction.
//   - both together reach Completed or Failed, and the DECLARATION's own
//     verdict decides which. This is the only path to Completed, which is the
//     claim the bead is making.
//
// This is the shape a transfer already uses: state comes from the result and
// its done, never from a progress sample, and a late sample resurrects
// nothing.
type State string

const (
	// StatePrepared is a committed record with nothing behind it yet. It is
	// deliberately reachable — it is the state the interval exists to make
	// discoverable, and it is what a fork that nobody recorded would not
	// have been. Nothing reads it as a running worker.
	StatePrepared State = "prepared"
	// StateLive is a participant whose enrolment arrived. Never entered
	// because a dispatch returned: dispatch is not delivery.
	StateLive State = "live"
	// StateCompleted is both facts present, with the declaration reporting
	// success.
	StateCompleted State = "completed"
	// StateFailed is both facts present, with the declaration reporting
	// failure. It is a participant that told us it did not succeed, which is
	// a different and better thing than one that told us nothing.
	StateFailed State = "failed"
	// StateAbandoned is a process exit with no declaration. Terminal, and
	// named so that it can never be misread as a completion.
	StateAbandoned State = "abandoned"
	// StateInterrupted is what the startup sweep writes over any
	// non-terminal participant after a backend restart. Not adopted: the
	// worker is gone with the backend that held it, and we could not prove a
	// process found at the far end was ours if it were not.
	StateInterrupted State = "interrupted"
)

// Terminal reports whether s is an end state. A terminal participant is no
// longer supervised and no longer holds a reservation.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateAbandoned, StateInterrupted:
		return true
	default:
		return false
	}
}

// Liveness is the full identity a piece of evidence is bound to, and every
// field is load-bearing.
//
// A bare attempt number attaches old evidence to a new incarnation: output
// offsets are per-session replay coordinates (AD-9), attempts restart, and a
// domain can be re-established under the same session. So an observation is
// admitted only when it matches on all of these, and a late one from a
// previous incarnation is refused rather than overwriting a current fact.
type Liveness struct {
	// BackendInstance is the backend that minted the record. A record whose
	// instance is not ours belongs to a process that is gone.
	BackendInstance string
	// SessionID is the backend-owned session identity (AD-7).
	SessionID string
	// Epoch distinguishes incarnations of one session.
	Epoch uint64
	// Lane is the authenticated lifecycle channel (ADR-0024) the evidence
	// arrived on.
	//
	// The design's list of what an observation is bound to says "lifecycle
	// DOMAIN" here, and this is the lane instead, deliberately. The enrolment
	// seam speaks in lanes — AgentEnroller.Enrol is handed one — and a lane
	// carries nested domains, so a domain written here would be one nothing
	// observed. A field populated by no carrier is worse than an absent one:
	// it compares equal to itself and reads as evidence. The domain arrives
	// with the DECLARATION carrier, which knows its own, and it lands in this
	// struct together with the code that compares it.
	Lane string
	// Attempt is the execution attempt (ADR-0020 §4).
	Attempt int
	// OutputOffset is the session-relative byte offset the evidence sits at.
	OutputOffset int64
}

// SameIncarnation reports whether other is evidence about the same live thing
// this record describes. It is the guard that makes a late observation from a
// replaced incarnation refusable rather than merely unlikely.
func (l Liveness) SameIncarnation(other Liveness) bool {
	return l.BackendInstance == other.BackendInstance &&
		l.SessionID == other.SessionID &&
		l.Epoch == other.Epoch &&
		l.Lane == other.Lane &&
		l.Attempt == other.Attempt
}

// Declaration is what a participant said about its own work, over the
// authenticated channel. It is one of the two facts permitted to decide state.
type Declaration struct {
	// OK is the participant's own verdict on its work.
	OK bool
	// Summary is what it says it produced. Free text from the participant:
	// it is content, never a commitment, and nothing derives authority from
	// it.
	Summary string
	// At is when the declaration was admitted by the backend, not a time the
	// participant chose. There is no clock shared with a participant, and a
	// time it supplied would be a value it could pick.
	At time.Time
}

// Exit is the process fact: the participant's process is gone, and this is how.
// It comes from the PTY nocx owns — never from the screen.
type Exit struct {
	// Cause distinguishes an ordinary exit from a signal or a lost channel.
	Cause string
	// Code is the exit status where Cause admits one.
	Code int
	// At is when the backend observed it.
	At time.Time
}

// Participant is one node of a wave.
type Participant struct {
	ID       ParticipantID
	Wave     ID
	Role     Role
	State    State
	Liveness Liveness
	// Task is what this participant was given to do, recorded at
	// registration so a restarted coordinator can be told by name what it
	// holds without reconstructing anything from a transcript.
	Task string
	// Declared is the participant's own terminal fact, or nil.
	Declared *Declaration
	// Exited is the process fact, or nil.
	Exited *Exit
	// RegisteredAt is when the record was committed — before any fork
	// attributable to it.
	RegisteredAt time.Time
}

// Effect is one thing a delegation permits its holder to do to a participant.
type Effect string

const (
	// EffectObserve is reading what the participant is doing. It survives a
	// human takeover, because severing a coordinator from its own worker
	// because a person helped it past a prompt is the cost that shape was
	// measured to have.
	EffectObserve Effect = "observe"
	// EffectReceiveEvents is being told when a fact about the participant
	// changes.
	EffectReceiveEvents Effect = "receive-events"
	// EffectSendInput is typing into the participant's pane. This is what a
	// human takeover suspends.
	EffectSendInput Effect = "send-input"
	// EffectClose is ending the participant.
	EffectClose Effect = "close"
	// EffectDelegateFurther is handing control onward. It is never in the
	// default bundle: transitive revocation arrives with the act axis, and
	// granting it by default would adopt that by the back door.
	EffectDelegateFurther Effect = "delegate-further"
)

// DefaultBundle is what a coordinator holds over a worker it spawned, and it
// deliberately omits EffectDelegateFurther.
func DefaultBundle() []Effect {
	return []Effect{EffectObserve, EffectReceiveEvents, EffectSendInput, EffectClose}
}

// DelegationState is the lifecycle of a delegation. The trigger set is closed
// (A4): human takeover of the pane suspends input; a change of authenticated
// authority context suspends scope; the participant going terminal expires it;
// the controller session ending, or an explicit human act, revokes it.
type DelegationState string

const (
	DelegationActive         DelegationState = "active"
	DelegationInputSuspended DelegationState = "input-suspended"
	DelegationScopeSuspended DelegationState = "scope-suspended"
	DelegationRevoked        DelegationState = "revoked"
	DelegationExpired        DelegationState = "expired"
)

// Permits reports whether a delegation in this state carries effect.
//
// The discriminator between the two suspensions is "did the authenticated
// authority context change", never "did a human touch the PTY" — which is why
// input-suspended keeps observe: a person helping their own worker past a
// prompt must not permanently sever the coordinator from it.
func (s DelegationState) Permits(e Effect) bool {
	switch s {
	case DelegationActive:
		return true
	case DelegationInputSuspended:
		return e != EffectSendInput
	default:
		return false
	}
}

// Delegation proves that a controller session may currently act on a
// participant. It is 2026-08-15 D8's record, adopted rather than re-derived: a
// wave-specific authority record would be a second vocabulary for one concept
// and would drift from the workspaces design the first time either moved.
type Delegation struct {
	// ControllerSession holds the authority. A session and not a run,
	// because a coordinator run ends while its workers live.
	ControllerSession string
	// Participant is what the authority is over.
	Participant ParticipantID
	// Epoch binds the delegation to an incarnation of the controller session.
	Epoch uint64
	// CreatedByRunID is provenance only. Lineage proves "A created B" and
	// confers nothing; this field is the same class and is never read to
	// decide whether an operation is allowed.
	CreatedByRunID string
	Effects        []Effect
	State          DelegationState
}

// Permits reports whether d currently carries e.
func (d Delegation) Permits(e Effect) bool {
	if !d.State.Permits(e) {
		return false
	}
	for _, have := range d.Effects {
		if have == e {
			return true
		}
	}
	return false
}

var (
	// ErrNoSuchParticipant is returned for an id the record does not hold.
	ErrNoSuchParticipant = errors.New("wave: no such participant")
	// ErrBoundExceeded means the wave already holds as many participants as
	// it may. The bound is checked BEFORE anything is forked, which is the
	// whole reason it is the first step: a bound checked after the fork
	// leaves an orphan every time it refuses.
	ErrBoundExceeded = errors.New("wave: participant bound exceeded")
	// ErrNotDelegated means the caller holds no delegation carrying the
	// effect it tried to use.
	ErrNotDelegated = errors.New("wave: no delegation carries that effect")
	// ErrStaleEvidence means an observation named an incarnation that is not
	// the one the record describes. It is a refusal and not a failure: it is
	// what stops a late fact from a replaced attempt overwriting a current
	// one.
	ErrStaleEvidence = errors.New("wave: evidence names another incarnation")
	// ErrRecordUnavailable means the durable half of the record is not
	// there — the encrypted store never opened. It is a REFUSAL and never a
	// degrade: a wave nobody can record is a wave nobody supervises, and
	// accepting a registration into nowhere is how an unaccounted agent gets
	// created by a bad start rather than by a bug.
	ErrRecordUnavailable = errors.New("wave: the record is unavailable")
	// ErrTerminal means a fact arrived about a participant whose record is
	// already closed. It is a refusal and not a failure: a late fact from a
	// process the record has already accounted for must not reopen it.
	ErrTerminal = errors.New("wave: participant is already terminal")
	// ErrEnrolmentNeverArrived means the launcher did not enrol within the
	// reservation's deadline. Failure is closed: the record is terminalized
	// and nothing goes on being addressed.
	ErrEnrolmentNeverArrived = errors.New("wave: enrolment never arrived")
)
