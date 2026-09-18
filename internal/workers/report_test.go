package workers

// A worker reports, and the coordinator's mailbox is what it reaches
// (nocx-luqz9.4; design §4.2, §4.3, §5.1; mesh design P1–P7).
//
// WHAT IS UNDER TEST HERE is the record's half of one tool: a kind, the
// worker's own words, and — for a checkpoint — the two optional extras the
// mesh design names. The three properties every case below is an instance of
// are the design's, and they are the ones a plausible mistake would break:
//
//   - a report is APPENDED. Nothing it can say reaches the record's state, and
//     a later observation is placed beside it rather than instead of it.
//   - `progress` never wakes and never arms a retry (P4), while `done` and
//     `question` do (design §5.1). The kind is the whole of that decision.
//   - the recipient is not a parameter. A worker says something ABOUT ITSELF,
//     and the box it lands in is read off the record — so "reach another
//     coordinator's mailbox" is not expressible rather than merely refused.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// THE CRITERION. A worker's `done` with its text reaches its coordinator's
// mailbox exactly once, with the text intact, and an idle coordinator is woken
// for it exactly once — by the pointer line, which carries no word of the
// report.
func TestAWorkersDoneReportReachesItsCoordinatorsMailboxOnce(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	// The coordinator is idle with nothing to read, which is the state the
	// wake is allowed to type into and the state the report arrives in.
	s.says(t, ObservedIdle)
	if got := s.lines(); len(got) != 0 {
		t.Fatalf("nocx typed into a coordinator with nothing to read: %+v", got)
	}

	const text = "landed the migration and left the branch green"
	if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind: KindDone, Text: text,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	mail := s.mailed(t)
	if len(mail) != 1 {
		t.Fatalf("the coordinator's mailbox holds %d rows, want the worker's one report", len(mail))
	}
	row := mail[0]
	if row.Recipient != coordBox {
		t.Fatalf("the report was committed to %q, want the coordinator's own box %q", row.Recipient, coordBox)
	}
	if row.Sender != ReaderID(s.worker.ID) {
		t.Fatalf("the report's sender is %q, want the worker that reported %q", row.Sender, s.worker.ID)
	}
	if row.Kind != KindDone {
		t.Fatalf("the report's kind is %q, want %q", row.Kind, KindDone)
	}
	if row.Body != text {
		t.Fatalf("the report's text is\n  %q\nwant\n  %q", row.Body, text)
	}
	if row.Observed != nil {
		t.Fatalf("a report carries a state as well: %+v", row.Observed)
	}
	if row.Group != testGroup {
		t.Fatalf("the report names worker %q, want %q", row.Group, testGroup)
	}

	lines := s.lines()
	if len(lines) != 1 {
		t.Fatalf("the coordinator was typed at %d times, want exactly 1: %+v", len(lines), lines)
	}
	const want = "nocx: you have 1 new messages from your workers. Call workers.inbox."
	if lines[0].text != want {
		t.Fatalf("the wake line is\n  %q\nwant\n  %q", lines[0].text, want)
	}
	if got := s.attempts(); len(got) != 1 {
		t.Fatalf("the wake was asked to type %d times, want once for one report: %+v", len(got), got)
	}
	if got := s.notices(); len(got) != 0 {
		t.Fatalf("the human was told about a report that had just arrived: %+v", got)
	}
}

// A `question` is the same act as a `done` with a different kind, and the
// difference the design rests on is that the CALL DOES NOT WAIT (ADR-0070's
// "why not a blocking ask"): the answer arrives as the worker's next message,
// so there is nothing for this call to hold.
//
// "Holds nothing" is asserted on the FACTS and never against a clock (AGENTS.md:
// no test may depend on a duration). Three of them, and each is false of a call
// that waits for its answer: the first question answers with a committed row
// BEFORE anything has read it; the recipient's cursor is still zero afterwards,
// so nothing was delivered to make that commit possible; and a second question
// is committed past the first rather than parked behind it.
func TestAQuestionReportIsCommittedAndHoldsNothingOpen(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	s.says(t, ObservedIdle)

	first, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind: KindQuestion, Text: "which branch should the migration land on?",
	})
	if err != nil {
		t.Fatalf("first question: %v", err)
	}
	if first.Seq == 0 || first.ID == "" {
		t.Fatalf("the question answered with no committed row: %+v", first)
	}
	// Nothing has read it: the row is in the box, which is what makes this a
	// commit rather than a delivery.
	if mail := s.mailed(t); len(mail) != 1 || mail[0].Kind != KindQuestion {
		t.Fatalf("mailbox after one question = %+v, want the one row", mail)
	}
	// AND NOTHING WAS DELIVERED, read from the cursor rather than inferred: a
	// call that waited for its answer could only answer after somebody took the
	// row, so a non-zero Fetched here is the shape of a blocking ask.
	cursor, err := s.harness.store.Cursor(s.ctx, coordBox, coordBox)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor.Fetched != 0 {
		t.Fatalf("the coordinator's cursor is at %d after a question it has not read, "+
			"so the call waited for a delivery rather than committing", cursor.Fetched)
	}

	second, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind: KindQuestion, Text: "and should I delete the old one after?",
	})
	if err != nil {
		t.Fatalf("second question: %v", err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("the second question landed at %d, want it past the first's %d", second.Seq, first.Seq)
	}
	mail := s.mailed(t)
	if len(mail) != 2 || mail[0].Kind != KindQuestion || mail[1].Kind != KindQuestion {
		t.Fatalf("mailbox after two questions = %+v, want both, in order", mail)
	}

	// A question wakes the coordinator (design §5.1) and arms no retry while it
	// is the line that was already typed: one batch, one line.
	if lines := s.lines(); len(lines) != 1 {
		t.Fatalf("two questions in one batch produced %d lines, want 1: %+v", len(lines), lines)
	}
	if armed := s.harness.alarms.running(); armed != 1 {
		t.Fatalf("alarms armed = %d, want the one pause that follows a delivered line", armed)
	}
	if got := s.notices(); len(got) != 0 {
		t.Fatalf("the human was told before the pause elapsed: %+v", got)
	}
}

// P4, and the acceptance criterion that names it twice: a checkpoint reaches
// the mailbox, wakes NOBODY, and arms no retry. The report is read at the
// coordinator's next call, which is the whole of what it is for.
func TestACheckpointWakesNobodyAndArmsNoRetry(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	s.says(t, ObservedIdle)

	if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind: KindProgress, Text: "the schema is written, the store is next",
	}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	mail := s.mailed(t)
	if len(mail) != 1 || mail[0].Kind != KindProgress {
		t.Fatalf("the checkpoint did not reach the mailbox: %+v", mail)
	}
	if lines := s.lines(); len(lines) != 0 {
		t.Fatalf("a checkpoint woke the coordinator: %+v", lines)
	}
	if attempts := s.attempts(); len(attempts) != 0 {
		t.Fatalf("a checkpoint was counted as an attempt to wake: %+v", attempts)
	}
	if armed := s.harness.alarms.running(); armed != 0 {
		t.Fatalf("a checkpoint armed %d retries, want none (P4)", armed)
	}
	if got := s.notices(); len(got) != 0 {
		t.Fatalf("a checkpoint raised a human notice: %+v", got)
	}
	// And it stays unread: nothing was delivered, so nothing was cleared.
	if got := s.read(t); len(got.Messages) != 1 {
		t.Fatalf("the coordinator's read was handed %d rows, want the checkpoint still waiting", len(got.Messages))
	}
}

// P1 read as an acceptance criterion: a checkpoint is APPENDED. Two of them are
// both there, in order, and neither overwrites the other.
func TestTwoCheckpointsAreBothKeptInOrder(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	const (
		first  = "half the store is migrated"
		second = "the whole store is migrated"
	)
	for _, text := range []string{first, second} {
		if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
			Kind: KindProgress, Text: text,
		}); err != nil {
			t.Fatalf("checkpoint %q: %v", text, err)
		}
	}
	mail := s.mailed(t)
	if len(mail) != 2 {
		t.Fatalf("the mailbox holds %d checkpoints, want both", len(mail))
	}
	if mail[0].Body != first || mail[1].Body != second {
		t.Fatalf("checkpoints = %q then %q, want %q then %q",
			mail[0].Body, mail[1].Body, first, second)
	}
	if mail[1].Seq <= mail[0].Seq {
		t.Fatalf("positions %d then %d, want the second later", mail[0].Seq, mail[1].Seq)
	}
	// Neither replaced the other, which is what "append-only" means here: the
	// row the first report created is still the row it created.
	if mail[0].Kind != KindProgress || mail[0].Sender != ReaderID(s.worker.ID) {
		t.Fatalf("the first checkpoint was rewritten: %+v", mail[0])
	}
}

// P2 and P3: a checkpoint MAY carry the worker's own approximate estimate and
// a reference the coordinator can corroborate, and the row is where both
// arrive — a field the tool accepts and the coordinator can never read would
// be a soft degrade visible nowhere.
func TestACheckpointCarriesTheEstimateAndArtifactItWasGiven(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	estimate := 40
	if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind:     KindProgress,
		Text:     "half the store is migrated",
		Estimate: &estimate,
		Artifact: "commit 4f2a1c9",
	}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	mail := s.mailed(t)
	if len(mail) != 1 {
		t.Fatalf("the checkpoint did not reach the mailbox: %+v", mail)
	}
	if mail[0].Estimate == nil || *mail[0].Estimate != estimate {
		t.Fatalf("the checkpoint's estimate is %v, want %d", mail[0].Estimate, estimate)
	}
	if mail[0].Artifact != "commit 4f2a1c9" {
		t.Fatalf("the checkpoint's artifact is %q, want the reference the worker named", mail[0].Artifact)
	}

	// An estimate of zero is a value and not an absence, which is the whole
	// reason the field is a pointer: "I have started and I have no number" and
	// "I estimate nothing is done" are two different reports.
	zero := 0
	if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind: KindProgress, Text: "starting now", Estimate: &zero,
	}); err != nil {
		t.Fatalf("checkpoint with a zero estimate: %v", err)
	}
	if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
		Kind: KindProgress, Text: "still going",
	}); err != nil {
		t.Fatalf("checkpoint with no estimate: %v", err)
	}
	mail = s.mailed(t)
	if len(mail) != 3 {
		t.Fatalf("mailbox holds %d rows, want three checkpoints", len(mail))
	}
	if mail[1].Estimate == nil || *mail[1].Estimate != 0 {
		t.Fatalf("the zero estimate arrived as %v, want 0", mail[1].Estimate)
	}
	if mail[2].Estimate != nil {
		t.Fatalf("a checkpoint that gave no estimate arrived with %v", *mail[2].Estimate)
	}
}

// The recipient is not something a worker can name. A report lands in the box
// of the coordinator that holds the reporting worker and NOWHERE ELSE — not
// another coordinator's, not another worker's, and not its own.
//
// The assertion is over a record holding two workers under two coordinators,
// because a test with one worker could not tell "the recipient is derived" from
// "the recipient is the only box there is".
func TestAReportLandsOnlyInItsOwnCoordinatorsMailbox(t *testing.T) {
	h := newHarnessBound(t, 4)
	ctx := context.Background()
	mine := mustRegister(t, h)
	other, err := h.reg.Register(ctx, RegisterRequest{
		Group: ID("worker-2"), CoordinatorSession: "sess-other-coordinator",
		Role: RoleWorker, Task: "somebody else's work", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register the second worker: %v", err)
	}

	if _, err := h.reg.Report(ctx, mine.ID, Report{Kind: KindDone, Text: "mine is done"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if _, err := h.reg.Report(ctx, other.Participant.ID, Report{Kind: KindDone, Text: "theirs is done"}); err != nil {
		t.Fatalf("report from the second worker: %v", err)
	}

	mineBox := h.store.mailbox(t, coordBox)
	if len(mineBox) != 1 || mineBox[0].Body != "mine is done" {
		t.Fatalf("my coordinator's box = %+v, want exactly my report", mineBox)
	}
	theirBox := h.store.mailbox(t, ReaderID("sess-other-coordinator"))
	if len(theirBox) != 1 || theirBox[0].Body != "theirs is done" {
		t.Fatalf("the other coordinator's box = %+v, want exactly its own worker's report", theirBox)
	}
	// And neither worker's OWN box was written to: a report is something a
	// worker says, not something it leaves itself.
	for _, box := range []ReaderID{ReaderID(mine.ID), ReaderID(other.Participant.ID)} {
		if got := h.store.mailbox(t, box); len(got) != 0 {
			t.Fatalf("a worker's own box %q holds %+v, want nothing", box, got)
		}
	}
}

// Criterion 7, at the record: a report is a CLAIM and moves nothing. The
// participant is byte-for-byte what it was, and the observed idle that follows
// a `done` is still placed — the two facts are different facts, and a report
// that swallowed the observation would be the record agreeing with the worker
// about something only nocx can witness.
func TestAReportIsNotARecordFact(t *testing.T) {
	s := newWakeStand(t, 3*time.Second, 3)
	before, ok := s.harness.store.read(t, s.worker.ID)
	if !ok {
		t.Fatal("the worker is not in the record at all")
	}

	// A report of each kind, including the one that would be tempting to read
	// as a completion.
	for _, kind := range []MessageKind{KindDone, KindQuestion, KindProgress} {
		if _, err := s.harness.reg.Report(s.ctx, s.worker.ID, Report{
			Kind: kind, Text: "reported " + string(kind),
		}); err != nil {
			t.Fatalf("report %s: %v", kind, err)
		}
	}
	after, ok := s.harness.store.read(t, s.worker.ID)
	if !ok {
		t.Fatal("the worker left the record")
	}
	if after.State != before.State {
		t.Fatalf("three reports moved the record from %q to %q", before.State, after.State)
	}
	if after.Declared != nil || before.Declared != nil {
		t.Fatalf("a report wrote a declaration: before %+v, after %+v", before.Declared, after.Declared)
	}
	if after.Exited != nil || before.Exited != nil {
		t.Fatalf("a report wrote an exit: before %+v, after %+v", before.Exited, after.Exited)
	}

	// The observation that follows the `done` still arrives, and it arrives
	// BESIDE the reports: nothing was overwritten, and nothing suppressed it.
	s.settles(t, s.worker, ObservedIdle)
	mail := s.mailed(t)
	if len(mail) != 4 {
		t.Fatalf("the mailbox holds %d rows, want the three reports and the idle: %+v", len(mail), mail)
	}
	last := mail[len(mail)-1]
	if last.Observed == nil || last.Observed.State != ObservedIdle {
		t.Fatalf("the idle that followed three reports never arrived: %+v", last)
	}
	if last.Seq <= mail[2].Seq {
		t.Fatalf("the observation landed at %d, before a report at %d", last.Seq, mail[2].Seq)
	}
}

// Criterion 8: the mailbox refuses the row, and the caller is told. An answer
// that said "recorded" for a row that is not there is the failure this asserts
// against, and so is a first failure that poisons every report after it.
func TestAFailedMailboxWriteIsReportedAndTheNextReportWorks(t *testing.T) {
	h := newHarnessBound(t, 4)
	ctx := context.Background()
	p := mustRegister(t, h)

	h.store.setFault("commit", 1)
	if _, err := h.reg.Report(ctx, p.ID, Report{Kind: KindDone, Text: "this one is lost"}); err == nil {
		t.Fatal("a report whose mailbox write failed answered as though it had been recorded")
	} else if !errors.Is(err, ErrReportNotRecorded) {
		t.Fatalf("report error = %v, want it to name ErrReportNotRecorded", err)
	}
	if got := h.store.mailbox(t, coordBox); len(got) != 0 {
		t.Fatalf("a failed write left %+v in the mailbox", got)
	}

	// The failure is not held: the next report is the ordinary path, which is
	// what makes "say it again" the honest instruction for the arm above.
	if _, err := h.reg.Report(ctx, p.ID, Report{Kind: KindDone, Text: "this one lands"}); err != nil {
		t.Fatalf("the report after a failed one: %v", err)
	}
	got := h.store.mailbox(t, coordBox)
	if len(got) != 1 || got[0].Body != "this one lands" {
		t.Fatalf("the mailbox holds %+v, want the report that followed the failure", got)
	}
}

// The record is the owner of what a report IS, so it refuses a shape a worker's
// tool could not have produced: a kind that is not one of the three, a
// checkpoint's extras on something that is not a checkpoint, and an estimate
// outside the range the contract declares.
//
// These are unreachable through the tool (the params schema refuses each first),
// and they are refused here anyway for the reason every other seam in this
// package states: the store is reachable without the schema — an in-process
// caller, a test, a future carrier — and a row that violates its own contract
// would break the NEXT call that reads it, not this one.
func TestAReportRefusesWhatAWorkersToolCouldNotSend(t *testing.T) {
	h := newHarnessBound(t, 4)
	ctx := context.Background()
	p := mustRegister(t, h)
	tooFew, tooMany := -1, 101

	cases := []struct {
		name string
		rep  Report
		want error
	}{
		{"an unknown kind", Report{Kind: KindObservation, Text: "a state is not a report"}, ErrNotAReport},
		{"an empty kind", Report{Text: "nobody said what this is"}, ErrNotAReport},
		{"no text", Report{Kind: KindDone}, ErrEmptyMessage},
		{"a text past the mailbox's bound", Report{Kind: KindDone, Text: string(make([]byte, MaxMessageBytes+1))}, ErrMessageTooLarge},
		{"an estimate on a done", Report{Kind: KindDone, Text: "finished", Estimate: &tooFew}, ErrNotAReport},
		{"an artifact on a question", Report{Kind: KindQuestion, Text: "may I?", Artifact: "commit 1"}, ErrNotAReport},
		{"an estimate below zero", Report{Kind: KindProgress, Text: "going", Estimate: &tooFew}, ErrNotAReport},
		{"an estimate past a hundred", Report{Kind: KindProgress, Text: "going", Estimate: &tooMany}, ErrNotAReport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.reg.Report(ctx, p.ID, tc.rep); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if got := h.store.mailbox(t, coordBox); len(got) != 0 {
		t.Fatalf("a refused report was committed anyway: %+v", got)
	}
	// The three kinds a worker MAY report, each with what belongs to it, all
	// pass — the refusal above is not a wall in front of the ordinary path.
	hundred := 100
	for _, rep := range []Report{
		{Kind: KindDone, Text: "finished"},
		{Kind: KindQuestion, Text: "may I?"},
		{Kind: KindProgress, Text: "going", Estimate: &hundred, Artifact: "commit 1"},
	} {
		if _, err := h.reg.Report(ctx, p.ID, rep); err != nil {
			t.Fatalf("a legitimate %s report was refused: %v", rep.Kind, err)
		}
	}
	if got := h.store.mailbox(t, coordBox); len(got) != 3 {
		t.Fatalf("the mailbox holds %d rows, want the three legitimate reports", len(got))
	}
}
