package workers

import "context"

// Store is where a worker's participants are held, and it is deliberately
// narrow: this package owns the SEMANTICS of a worker — the interval, the order
// that is the rollback, the reduction of two independent facts into one state
// — and a Store owns only what is true right now.
//
// It is an interface and not the map itself so the semantics can be tested
// against a double that fails on demand: every method is a boundary that can
// fail, and the order Register calls them in IS the rollback (see
// registrar.go). Nothing here is asked to be transactional across methods.
//
// The shipped implementation is MemoryStore, and nothing behind this interface
// is durable. Under the 2026-08-15 D5 a participant dies with the backend that
// spawned it, so the record's lifetime and its participants' lifetime coincide
// by construction, and a row that outlived them would describe nothing — see
// memory.go for the whole argument.
type Store interface {
	// EnsureGroup records a worker and the session that coordinates it, and does
	// nothing if it is already there. Idempotent rather than
	// create-once, because a coordinator does not open its worker: it spawns a
	// worker, and the worker is what that act implies. A create that failed the
	// second time would make the second spawn of a session an error for a
	// reason the caller could do nothing about.
	EnsureGroup(ctx context.Context, id ID, coordinatorSession string) error

	// NonTerminal lists the participants this worker holds that are not
	// finished. It is a list and not a count, because the callers ask two
	// different questions of one answer: step 1's reservation asks HOW MANY
	// before anything is forked, and a wait asks WHICH is still running. A
	// count beside a list would be two owners of "what is still open".
	NonTerminal(ctx context.Context, id ID) ([]Participant, error)

	// CommitPrepared enters the participant and its worker membership. It is
	// the opening end of the interval, and it happens before the spawn for
	// the reason the vault journal is written before the provider call: a
	// spawn that times out may still have forked, and a fork nobody recorded
	// is permanently undiscoverable.
	CommitPrepared(ctx context.Context, p Participant) error

	// MarkLive moves a prepared participant to live. It is called only on the
	// strength of an enrolment that arrived, never because a dispatch
	// returned. wt is the checkout the spawn made and the record now accepts:
	// from here on, the participant's worktree is the record's fact and its
	// undo is nobody's, because the compensation that would have removed it
	// is discharged the moment this write lands. Zero wt is the ordinary
	// spawn that shares its coordinator's checkout.
	MarkLive(ctx context.Context, id ParticipantID, l Liveness, wt Worktree) error

	// Terminalize writes a terminal state over a non-terminal one. A
	// compensation that itself fails leaves the record non-terminal and is
	// retried; a terminal state is never written for something that was not
	// established.
	Terminalize(ctx context.Context, id ParticipantID, s State) error

	// Closed records that the participant was ended by its COORDINATOR, and it
	// is the only write that may replace an exit.
	//
	// It runs after the closer returned, so the supervisor may already have
	// reported the exit this close caused — ending the session is what
	// produces it, and the two run on different goroutines with no order
	// between them. The record's answer to "why did this worker end" is the
	// coordinator's act, and the exit is its consequence, so closed wins that
	// race whichever way it falls. It is REFUSED over any other terminal
	// state: nocx's own compensation is not a coordinator's decision.
	Closed(ctx context.Context, id ParticipantID) error

	// RecordExit stores the process fact and returns the participant as it
	// then stands.
	RecordExit(ctx context.Context, id ParticipantID, e Exit) (Participant, error)

	// PutDelegation commits the controller session's authority over the
	// participant.
	PutDelegation(ctx context.Context, d Delegation) error

	// Delegation reads back the controller session's authority over a
	// participant. It is a READ and not a check: what an effect permits is
	// this package's semantics (Delegation.Permits), and a store that
	// answered "may this session close that worker" would be a second place
	// authority is decided.
	Delegation(ctx context.Context, id ParticipantID) (Delegation, error)

	// DelegationsBy lists every delegation whose ControllerSession is
	// sessionID — the downward edge a revocation needs and Resolve does not:
	// Resolve walks a chain UPWARD from a participant via
	// ParticipantBySession, but ending a session's own authority (it
	// terminalized, it closed, its admission retired) must also end
	// whatever IT controls, and there is no other route from "this session"
	// to "the delegations it holds". Order is unspecified; a revocation
	// walks every entry regardless.
	DelegationsBy(ctx context.Context, sessionID string) ([]Delegation, error)

	// Participant reads one back.
	Participant(ctx context.Context, id ParticipantID) (Participant, error)

	// CoordinatorSession answers who must judge a fact about this workers. It
	// is a read rather than a field on the participant because one worker has
	// one coordinator and copying it onto every participant row would be a
	// second place for it to be wrong. The backstop asks it at the moment a
	// fact enters, and never when its deadline fires: by then the worker may
	// hold nothing non-terminal, and the answer would be gone exactly when it
	// is needed.
	CoordinatorSession(ctx context.Context, id ID) (string, error)

	// ParticipantBySession names the participant running in one session, and
	// refuses a session that is nobody's. It is here rather than derived by a
	// caller because the only outside route to it is HeldBy, which needs a
	// COORDINATOR session — and the caller who needs this answer is the
	// worker, which has neither its own participant id (backend-owned, A9)
	// nor its coordinator's session.
	ParticipantBySession(ctx context.Context, sessionID string) (Participant, error)

	// Commit writes one message into a mailbox and stamps its Seq, which is
	// the store's to mint: a sequence a caller chose could collide, and the
	// order of a mailbox is the only thing a cursor can point at.
	Commit(ctx context.Context, m Message) (Message, error)

	// Since reads a page of one mailbox strictly after a sequence. It TAKES
	// NOTHING — no row is modified, no cursor moves — which is what lets two
	// readers read the same mailbox without either losing a message. limit
	// bounds the page; a caller is told separately whether more remains.
	Since(ctx context.Context, mailbox ReaderID, after int64, limit int) ([]Message, error)

	// Cursor reads one reader's position in one mailbox. A reader that has
	// never looked has a zero cursor, which is a position and not an error:
	// "I have seen nothing" is the ordinary starting state.
	Cursor(ctx context.Context, mailbox, reader ReaderID) (Cursor, error)

	// AdvanceCursor moves a reader's marks. It never moves either mark
	// BACKWARDS, because a cursor going backwards would hand out a message a
	// reader has already acted on, which is the duplicated-effect failure
	// §7.2 names.
	AdvanceCursor(ctx context.Context, c Cursor) error

	// Undelivered lists what a worker's mailboxes hold that their own
	// recipients have not fetched. It is the read behind
	// "committed-not-fetched is reported as itself and never as delivered",
	// and it asks the RECIPIENT's cursor: another reader having seen a
	// message says nothing about whether it was delivered.
	Undelivered(ctx context.Context, id ID) ([]Message, error)

	// HeldBy answers D3 — a coordinator asks what its SESSION holds and is
	// told by name. It is the session and not the run, because the run that
	// spawned the worker has ended by the time the question is asked; that
	// is the whole situation the question exists for.
	HeldBy(ctx context.Context, coordinatorSession string) ([]Participant, error)

	// HeldWorktrees answers which checkouts the record's non-terminal
	// participants hold, whichever session spawned them (nocx-xn63t.1.4).
	// The leftovers question is about a REPOSITORY, and a checkout is held
	// even while its own coordinator's session is not the one asking, so
	// the answer cannot be a HeldBy of one session. A participant without a
	// worktree — every spawn that shared its coordinator's checkout —
	// contributes nothing. Order is unspecified.
	HeldWorktrees(ctx context.Context) ([]Worktree, error)
}

// SpawnRequest is what Register asks the spawner for, and the participant id
// is already minted: the id exists before any connect, so a failed connect
// registers nothing.
type SpawnRequest struct {
	Participant ParticipantID
	Group       ID
	// CoordinatorSession is the session that ASKED, established by the
	// authorizer from the peer's process tree and never sent by a caller.
	//
	// It is carried here rather than left to the group id, which defaults to
	// it and may be set to anything else: the spawner needs the session to
	// open the participant's pane where the coordinator is standing
	// (nocx-ty5ks), and a group that a caller renamed would send the worker
	// somewhere nobody chose. Empty is legitimate — a registration with no
	// coordinator session behind it — and the spawner falls back to what it
	// did before this field existed.
	CoordinatorSession string
	Task               string
	// Command is the line the participant runs. It is carried rather than
	// derived because what makes an agent is the caller's business and not
	// this package's: nocx has no list of agents, and a record that decided
	// one would be the network manifest catalogue this design refused.
	Command string
	// Environment is where the worker runs. Spawning is the delegate effect
	// over the resource environment, permitted only into an environment the
	// run's own fence already names — reaching further is scope expansion.
	Environment string
	// Worktree is the checkout this spawn was asked to create, or nil when
	// the participant will share its coordinator's checkout. It is carried
	// as asked — Branch required, Base optional — and never resolved here:
	// resolving a base is a git-seam question, and the spawner owns that
	// seam.
	Worktree *WorktreeAsk
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

// TaskDelivery is what became of a participant's task at spawn (nocx-f545a.3).
//
// It is a fact about what nocx DID, reported beside the participant rather
// than written into its record: the reason a task was not typed is read off
// the pane's screen, and AD-6 forbids a screen reading from assigning status
// to a participant (ADR-0064 §4). So it travels with the registration that
// produced it, is answered to the caller once, and is kept nowhere.
//
// TWO WRITERS, AND THEY ANSWER TWO DIFFERENT QUESTIONS (nocx-luqz9.5). Typed
// and WaitingOn are the LAUNCHER's answer, read off the pane before a session
// has anything queued for it; BriefingQueued is the REGISTRAR's, and it is a
// fact about the queue rather than about the screen — whether the rules and the
// task were ever handed to one. The fields are disjoint, they are set at the
// two moments the two facts exist, and no call sets a field it did not observe.
type TaskDelivery struct {
	// Typed is true when the task was submitted into the participant's pane.
	Typed bool
	// WaitingOn names the pane state that kept the task from being typed — a
	// question the participant's agent is asking of its own — and is empty
	// whenever Typed is true, and when nothing was attempted at all.
	WaitingOn string
	// BriefingQueued is true when this participant's BRIEFING — the rules it
	// reports under, and then its task — reached the message queue
	// (nocx-luqz9.5, design §6), and false when nocx could not queue it at
	// all.
	//
	// FALSE IS A WHOLE ANSWER, and the one a coordinator has to act on: the
	// worker is live and has been told NOTHING — not its rules and not its
	// task, because they are one briefing and one call — and nothing further
	// will be delivered to it. The coordinator closes it and spawns another.
	// The alternative shape, a warning in a log, is the soft degrade AGENTS.md
	// refuses: a live worker that looks like a worker doing something.
	//
	// IT IS FALSE FOR A REGISTRATION THAT HAD NO TASK TOO, which is the same
	// fact read from the other end: a registration with nothing to brief hands
	// the queue nothing. workers.spawn's own schema requires a task, so a
	// coordinator never receives this false for a worker that had something to
	// be told.
	BriefingQueued bool
}

// TaskDeliverer is the optional half of Spawned: a launcher that attempted to
// deliver its task says what came of it. A Spawned that does not implement it
// attempted nothing, which is the zero TaskDelivery.
type TaskDeliverer interface {
	TaskDelivery() TaskDelivery
}

// WorktreeSource is the other optional half of Spawned: a launcher that
// created a checkout answers where it is — the resolved facts, never the ask.
// A Spawned that does not implement it created nothing, which is the zero
// Worktree. The record takes the answer at MarkLive, the moment it accepts
// the checkout's continued existence.
type WorktreeSource interface {
	WorktreeLocation() Worktree
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

// Closer ends a participant's process, and gives back the place it occupied.
// It is the far end of Close, and it is deliberately narrow: what it is handed
// is a participant the record has already decided may be ended, and what it
// does is end the session behind it and take its tab out of the window — the
// second half being what "closing a worker closes its tab" means for a
// participant whose process is already gone (nocx-xn63t.4.6). It writes no
// state IN THE RECORD and reports no verdict — the exit it causes arrives by
// the ordinary path.
type Closer interface {
	Close(ctx context.Context, p Participant) error
}

// TaskQueue hands a participant's BRIEFING — the rules it reports under, and
// then its task — to the queue that delivers it once its pane is free (design
// §9 Task 11 for the task, §6 for the rules, nocx-luqz9.5).
//
// ONE CALL AND NOT TWO. What a worker is told arrives in an ORDER — the rules
// first, so that a worker holding a task already knows how to report on it —
// and two calls would leave that order to whichever goroutine reached the queue
// first. One call also makes the failure whole: a queue that cannot take the
// briefing takes NEITHER half, so "the task was delivered and the rules were
// not" is not a state a caller can put this seam into (Briefing.Validate, and
// internal/app.paneMessages' own refusal of half a briefing).
//
// It is a seam for Closer's reason: the message queue, its authority checks
// and the pane it writes into are all the composition root's
// (internal/app.paneMessages), and this package knows only that a registration
// has something to hand it.
//
// Register calls this AFTER the delegation is committed and the participant
// is marked live (step 5/6 below) — never from within Spawner.Spawn (step
// 3), because EnqueueBriefing resolves the participant from its OWN session id
// (ParticipantBySession), and that mapping does not exist until MarkLive
// writes the participant's Liveness. A queue nobody wired in (nil) is the
// same absence case every other optional seam in this package's composition
// treats: nothing is enqueued, which only matters to a caller that never
// wired one in — production always does.
//
// A REFUSAL IS REPORTED, NOT LOGGED AWAY (nocx-luqz9.5, acceptance 4). The
// caller turns a non-nil error into TaskDelivery.BriefingQueued = false, which
// is how a coordinator learns that its worker is running and has been told
// nothing. It does NOT un-register the participant: the refusal is nocx's own
// failure to queue, not a reading of the pane, so ADR-0064 §4's asymmetry —
// a fact about delivery may not decide whether a participant exists — stands
// exactly as it did.
type TaskQueue interface {
	EnqueueBriefing(ctx context.Context, coordinatorSession string, participant Participant, briefing Briefing) error
}

// Supervisor is the watch that outlives the coordinator's turn. It is attached
// to a record that ALREADY EXISTS, which is what makes step 6 race-free: a
// process exiting between the mark and the attach is still observed, because
// the watcher finds an already-terminal process rather than missing a
// transition.
type Supervisor interface {
	Attach(ctx context.Context, p Participant) error
}
