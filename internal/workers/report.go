package workers

// A worker says what it has done, or asks for an answer (nocx-luqz9.4; design
// §4.2, §4.3, §5.1; ADR-0070 decision 1).
//
// # A claim, and the two halves that never merge
//
// This is the FIRST of ADR-0070's two ways nocx learns about a worker, and it
// is deliberately not the other one. The worker SAYS, and what it says is a
// claim about itself that nothing here checks; nocx SEES, and what it sees is a
// screen classification placed in the same mailbox by observed.go. They never
// merge, because they answer different questions, and the record keeps no
// verdict from either: a `done` moves no state, and the idle that follows it is
// placed rather than suppressed. A misread screen costs a coordinator a wake;
// it can never record a completion, because nothing here records one.
//
// # The recipient is not a field, and that is the design
//
// A report is something a worker says ABOUT ITSELF, so there is nowhere for a
// recipient to go: the box it lands in is read off the record through the
// reporting participant's own id, and a worker therefore cannot EXPRESS "write
// to that coordinator" or "write to another worker" at all. That is A9's rule
// as a property of the type rather than a check somebody has to remember to
// write, and the same rule holds one level up — the capability a worker's run
// holds names its own participant, so the id is not an argument either.
//
// # Writing to one's own coordinator is not a new authority
//
// The effect is OBSERVE, for workers.say's reason read from the other end:
// leaving a message in a box reaches nobody's keyboard, answers no modal, and
// starts and ends nothing. A worker that may say where it has got to is not a
// worker that may do anything else.

import (
	"context"
	"errors"
	"fmt"
)

// Report is what a worker says about itself (CONTEXT.md; design §4.2): a kind,
// its own words, and — for a checkpoint — the two optional extras the mesh
// design names.
type Report struct {
	// Kind is what the worker is reporting: KindDone, KindQuestion or
	// KindProgress. The other values a mailbox knows are refused rather than
	// stored, because a row that said `observation` about something a tool
	// sent would be a row lying about its own producer.
	Kind MessageKind
	// Text is the worker's own words. It is UNTRUSTED by every reader and is
	// typed into no pane: nocx writes a pointer line to the coordinator and
	// never a word of this (design §5.2), because a model's text typed into
	// another agent's input region is prompt injection performed by us.
	Text string
	// Estimate is the worker's own approximate percentage (mesh design P2),
	// and it is the worker's own: nothing corroborates it, nothing reduces it
	// against a measured row, and it is allowed to sit at the same number for
	// a long time. A POINTER, because zero is a report — "I estimate nothing
	// is done" — and not the absence of one.
	Estimate *int
	// Artifact is something the coordinator can check against what nocx
	// already owns (mesh design P3): a commit, a path, a test run. It is not
	// verified here and is not meant to be — the design makes it a gradient
	// (a checkpoint naming something checkable is worth more than one that
	// does not) rather than a requirement, so empty is ordinary.
	Artifact string
}

// MaxEstimate is the top of the range an estimate may name.
//
// It is a percentage and therefore bounded, and the bound is declared HERE as
// well as in the contract because a row that carried 150 would violate the
// schema of the NEXT call that read it — the mailbox read — rather than of the
// one that wrote it, which is the worst place for a bad row to be noticed.
const MaxEstimate = 100

var (
	// ErrNotAReport means the value could not have come from a worker's
	// reporting tool: a kind that is not one of the three, a checkpoint's
	// extras on something that is not a checkpoint, or an estimate outside
	// its range. It is a refusal and never a coercion — storing what a caller
	// sent would put a row in a mailbox that the contract of the call reading
	// it next would reject.
	ErrNotAReport = errors.New("worker: this is not a report a worker's tool can send")
	// ErrReportNotRecorded means the mailbox refused the row. It is split
	// from every other failure here because it is the one fact a worker can
	// act on: nothing was recorded, and saying it again is the ordinary path
	// rather than a repeat of something that was refused.
	ErrReportNotRecorded = errors.New("worker: the report was not recorded in its coordinator's mailbox")
	// ErrNoCoordinator means the record holds no coordinator for this worker,
	// so there is no box a report could reach. Distinct from an empty box: a
	// box nobody has written to is ordinary and readable, while this is a
	// worker nothing addresses — the record's own inconsistency, not the
	// worker's.
	ErrNoCoordinator = errors.New("worker: nobody coordinates this worker")
)

// Report commits one worker's report into its coordinator's mailbox.
//
// It answers with the committed row and it HOLDS NOTHING. A question is not a
// request that waits for a reply — it is a report that says it needs one, and
// the answer arrives as the worker's next message (ADR-0070's "why not a
// blocking ask") — so there is no timeout to configure, no state to keep, and
// no clock in this file at all. A second question is committed rather than
// parked behind the first, which is the observable half of that sentence.
//
// The two ends of the row are both read off the record: the sender is the
// participant id the caller holds, and the recipient is the coordinator of the
// worker that participant belongs to. Nothing in the call points either
// somewhere else, so "a worker cannot reach another coordinator's mailbox" is a
// property of the shape rather than of a membership check that a later edit
// could weaken.
//
// A report from a participant that has already ENDED is accepted, deliberately.
// It moves nothing, so there is no state to resurrect, and the alternative —
// losing the last thing a worker said to the call that raced its exit — trades
// a fact for tidiness in the one direction that is never recoverable.
func (r *Registrar) Report(ctx context.Context, id ParticipantID, rep Report) (Message, error) {
	if err := checkReport(rep); err != nil {
		return Message{}, err
	}
	cur, err := r.store.Participant(ctx, id)
	if err != nil {
		return Message{}, fmt.Errorf("worker: report: %w", err)
	}
	// WHERE its mail goes is the record's answer and not the caller's, read at
	// the moment of the report rather than remembered from registration — the
	// same call the observation path makes, so one function owns "which box
	// does this worker's mail belong in".
	coordinator, err := r.coordinatorOf(ctx, cur.Group)
	if err != nil {
		return Message{}, fmt.Errorf("worker: report from %q: %w", id, err)
	}
	m, err := r.store.Commit(ctx, Message{
		Group: cur.Group, Recipient: coordinator, Sender: ReaderID(id),
		Kind: rep.Kind, Body: rep.Text,
		Estimate: rep.Estimate, Artifact: rep.Artifact,
		CommittedAt: r.now(),
	})
	if err != nil {
		// NOT LOST AND NOT CLAIMED. The row is not in the box, so the caller
		// is told so rather than handed the row it would have had: a worker
		// that believed a report it never made would correct a coordinator
		// about a fact that does not exist.
		return Message{}, fmt.Errorf("%w: %w", ErrReportNotRecorded, err)
	}
	// The mail is in the box, so the coordinator may now be woken about it —
	// and only now, exactly as placeObservation does it. Telling the wake
	// before the write would announce a row a reader cannot find, and the
	// checkpoint's own kind is what keeps it out of the count (§5.1, P4).
	r.wake.Arrived(ctx, coordinator)
	return m, nil
}

// checkReport refuses a report no worker's tool could have produced.
//
// IT IS HERE AND NOT ONLY IN THE SCHEMA, and the reason is the same one every
// other seam in this package gives: the store is reachable without the schema —
// an in-process caller, a test, a carrier that has not been written yet — and
// the cost of a bad row is paid by the NEXT call that reads the mailbox, not by
// the one that wrote it. The params contract refuses all of this first, so the
// endpoint's caller never sees these; what they protect is the record.
func checkReport(rep Report) error {
	if !oneOfReportKinds(rep.Kind) {
		return fmt.Errorf("worker: report kind %q is not one of %v: %w", rep.Kind, reportKinds, ErrNotAReport)
	}
	// The extras belong to a CHECKPOINT (P2, P3). Refused rather than dropped
	// on a report of another kind: a worker that sent an estimate with a
	// `done` meant something by it, and silently discarding what somebody
	// said is the soft degrade visible nowhere.
	if rep.Kind != KindProgress && (rep.Estimate != nil || rep.Artifact != "") {
		return fmt.Errorf("worker: a %s report carries no checkpoint extras: %w", rep.Kind, ErrNotAReport)
	}
	if rep.Estimate != nil && (*rep.Estimate < 0 || *rep.Estimate > MaxEstimate) {
		return fmt.Errorf("worker: estimate %d is outside 0..%d: %w", *rep.Estimate, MaxEstimate, ErrNotAReport)
	}
	// The two refusals Say already owns, shared rather than restated: an empty
	// row costs a reader a fetch and tells it nothing, and a body past
	// MaxMessageBytes is how an encrypted store is filled from inside an
	// agent's turn.
	switch {
	case rep.Text == "":
		return ErrEmptyMessage
	case len(rep.Text) > MaxMessageBytes:
		return fmt.Errorf("worker: %d bytes exceeds %d: %w",
			len(rep.Text), MaxMessageBytes, ErrMessageTooLarge)
	}
	return nil
}

// oneOfReportKinds answers whether a kind is one a worker's tool may report.
func oneOfReportKinds(kind MessageKind) bool {
	for _, k := range reportKinds {
		if kind == k {
			return true
		}
	}
	return false
}
