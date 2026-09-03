package wave

import "context"

// Store is the durable half of the record, and it is deliberately narrow: this
// package owns the SEMANTICS of a wave and owns no rows. The rows live in the
// encrypted content store, because ADR-0043 puts one connection on it and AD-8
// puts one owner on a behaviour, so a second database beside it would be a
// second owner of "what nocx durably knows".
//
// Every method is a boundary that can fail, and the order Register calls them
// in IS the rollback (see registrar.go). Nothing here is asked to be
// transactional across methods; CommitPrepared is the one place a transaction
// is required, and it is required inside that single call.
type Store interface {
	// EnsureWave records a wave and the session that coordinates it, and does
	// nothing if it is already there. Idempotent rather than
	// create-once, because a coordinator does not open its wave: it spawns a
	// worker, and the wave is what that act implies. A create that failed the
	// second time would make the second spawn of a session an error for a
	// reason the caller could do nothing about.
	EnsureWave(ctx context.Context, id ID, coordinatorSession string) error

	// NonTerminal lists the participants this wave holds that are not
	// finished. It answers two questions with one row set, which is why it is
	// not a count: step 1's reservation asks HOW MANY before anything is
	// forked, and the startup sweep asks WHICH, so that a restart can
	// terminalize each by name. A count beside a list would be two owners of
	// "what is still open".
	NonTerminal(ctx context.Context, id ID) ([]Participant, error)

	// AllNonTerminal lists every open participant across every wave. It is
	// the sweep's question and it is deliberately not NonTerminal with a
	// magic empty id: a restart closes what this backend can no longer
	// judge, which is a property of the BACKEND and not of any one wave.
	AllNonTerminal(ctx context.Context) ([]Participant, error)

	// CommitPrepared writes the participant row, its wave membership and its
	// lineage edge in ONE transaction. It is the opening end of the interval,
	// and it happens before the spawn for the reason the vault journal is
	// written before the provider call: a spawn that times out may still have
	// forked, and a fork nobody recorded is permanently undiscoverable.
	CommitPrepared(ctx context.Context, p Participant) error

	// MarkLive moves a prepared participant to live. It is called only on the
	// strength of an enrolment that arrived, never because a dispatch
	// returned.
	MarkLive(ctx context.Context, id ParticipantID, l Liveness) error

	// Terminalize writes a terminal state. A compensation that itself fails
	// leaves the record non-terminal and is retried; a terminal state is
	// never written for something that was not established.
	Terminalize(ctx context.Context, id ParticipantID, s State) error

	// RecordDeclaration stores the participant's own terminal fact and
	// returns the participant as it then stands, so the caller reduces from
	// stored state rather than from what it believed was stored.
	RecordDeclaration(ctx context.Context, id ParticipantID, d Declaration) (Participant, error)

	// RecordExit stores the process fact and returns the participant as it
	// then stands.
	RecordExit(ctx context.Context, id ParticipantID, e Exit) (Participant, error)

	// PutDelegation commits the controller session's authority over the
	// participant.
	PutDelegation(ctx context.Context, d Delegation) error

	// Participant reads one back.
	Participant(ctx context.Context, id ParticipantID) (Participant, error)

	// HeldBy answers D3 — a restarted coordinator asks what its SESSION
	// holds and is told by name. It is the session and not the run, because
	// the run that spawned the worker has ended by the time the question is
	// asked; that is the whole situation the question exists for.
	HeldBy(ctx context.Context, coordinatorSession string) ([]Participant, error)
}

// SpawnRequest is what Register asks the spawner for, and the participant id
// is already minted: the id exists before any connect, so a failed connect
// registers nothing.
type SpawnRequest struct {
	Participant ParticipantID
	Wave        ID
	Task        string
	// Command is the line the participant runs. It is carried rather than
	// derived because what makes an agent is the caller's business and not
	// this package's: nocx has no list of agents, and a record that decided
	// one would be the network manifest catalogue this design refused.
	Command string
	// Environment is where the worker runs. Spawning is the delegate effect
	// over the resource environment, permitted only into an environment the
	// run's own fence already names — reaching further is scope expansion.
	Environment string
}

// Spawned is a launcher that has been forked. It is not yet a participant:
// nothing may be addressed until its enrolment arrives.
type Spawned interface {
	// Liveness is the incarnation the launcher was started under.
	Liveness() Liveness
	// Kill ends it. This is the compensation for every failure after the
	// fork, and it is available synchronously — which is why this procedure
	// needs no journal.
	Kill(ctx context.Context) error
}

// Spawner creates the session and starts the launcher inside it.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (Spawned, error)
}

// Enrolments is the far end of step 4: the launcher enrols BEFORE it execs the
// real agent, and refuses visibly if it cannot. An enrolment that never
// arrives is a closed failure — there is no unorchestrated agent to worry
// about, because the launcher never reached its exec.
type Enrolments interface {
	// Await blocks until the participant's launcher enrols, ctx is done, or
	// the enrolment is refused. It returns the incarnation the enrolment
	// arrived on, which is what MarkLive is bound to.
	Await(ctx context.Context, p ParticipantID) (Liveness, error)
	// Withdraw undoes an arrived enrolment. It is the compensation for a
	// failure after step 4.
	Withdraw(ctx context.Context, p ParticipantID) error
}

// Supervisor is the watch that outlives the coordinator's turn. It is attached
// to a record that ALREADY EXISTS, which is what makes step 6 race-free: a
// process exiting between the mark and the attach is still observed, because
// the watcher finds an already-terminal process rather than missing a
// transition.
type Supervisor interface {
	Attach(ctx context.Context, p Participant) error
}
