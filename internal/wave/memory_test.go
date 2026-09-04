package wave

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// newParticipant is one prepared worker of the shared test wave.
func newParticipant(id ParticipantID) Participant {
	return Participant{
		ID:           id,
		Wave:         testWave,
		Role:         RoleWorker,
		State:        StatePrepared,
		Task:         "read AGENTS.md and report",
		RegisteredAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}
}

// newSeededStore is an empty record with the wave every test here needs.
func newSeededStore(t *testing.T) *MemoryStore {
	t.Helper()
	s := NewMemoryStore()
	if err := s.EnsureWave(context.Background(), testWave, coordSession); err != nil {
		t.Fatalf("ensure wave: %v", err)
	}
	return s
}

// What a reader is handed is what the writer committed — the whole
// participant, including the incarnation every later fact is compared
// against, and not just its id.
func TestAParticipantIsReadBackAsItWasWritten(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	p := newParticipant("p-1")
	if err := s.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit prepared: %v", err)
	}
	if err := s.MarkLive(ctx, p.ID, testLiveness()); err != nil {
		t.Fatalf("mark live: %v", err)
	}

	got, err := s.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != StateLive {
		t.Fatalf("state = %q, want %q", got.State, StateLive)
	}
	if got.Task != p.Task {
		t.Fatalf("task = %q, want %q", got.Task, p.Task)
	}
	if got.Liveness != testLiveness() {
		t.Fatalf("liveness = %+v, want %+v", got.Liveness, testLiveness())
	}
	if got.Wave != testWave || got.Role != RoleWorker || !got.RegisteredAt.Equal(p.RegisteredAt) {
		t.Fatalf("participant = %+v, want the one that was committed", got)
	}
}

// A caller holds a COPY. The two terminal facts are pointers, and a record
// that handed out the pointer it stores would let any reader rewrite a fact
// only the two admitted sources may write.
func TestAReaderCannotRewriteTheRecordThroughWhatItWasHanded(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	p := newParticipant("p-1")
	if err := s.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := s.RecordDeclaration(ctx, p.ID, Declaration{OK: true, Summary: "read it"}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	handed, err := s.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	handed.Declared.OK = false
	handed.Declared.Summary = "rewritten from outside"

	got, err := s.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.Declared.OK || got.Declared.Summary != "read it" {
		t.Fatalf("declaration = %+v: a reader rewrote a fact it was only handed", *got.Declared)
	}
}

// The two terminal facts are two independent facts and are held
// independently, because a declaration with no exit is an ordinary state and
// so is the reverse.
func TestTheTwoTerminalFactsAreHeldIndependently(t *testing.T) {
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_100_000).UTC()

	t.Run("a declaration alone", func(t *testing.T) {
		s := newSeededStore(t)
		p := newParticipant("p-decl")
		if err := s.CommitPrepared(ctx, p); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if _, err := s.RecordDeclaration(ctx, p.ID, Declaration{OK: true, Summary: "read it", At: at}); err != nil {
			t.Fatalf("declare: %v", err)
		}
		got, err := s.Participant(ctx, p.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if got.Declared == nil {
			t.Fatalf("declaration lost")
		}
		if !got.Declared.OK || got.Declared.Summary != "read it" || !got.Declared.At.Equal(at) {
			t.Fatalf("declaration = %+v", *got.Declared)
		}
		if got.Exited != nil {
			t.Fatalf("an exit was invented: %+v", *got.Exited)
		}
	})

	t.Run("an exit alone", func(t *testing.T) {
		s := newSeededStore(t)
		p := newParticipant("p-exit")
		if err := s.CommitPrepared(ctx, p); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if _, err := s.RecordExit(ctx, p.ID, Exit{Cause: "signalled", Code: 9, At: at}); err != nil {
			t.Fatalf("exit: %v", err)
		}
		got, err := s.Participant(ctx, p.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if got.Exited == nil {
			t.Fatalf("exit lost")
		}
		if got.Exited.Cause != "signalled" || got.Exited.Code != 9 || !got.Exited.At.Equal(at) {
			t.Fatalf("exit = %+v", *got.Exited)
		}
		if got.Declared != nil {
			t.Fatalf("a declaration was invented: %+v", *got.Declared)
		}
	})
}

// A terminal record is never re-terminalized, so a compensation that runs
// twice cannot turn a completed participant into an interrupted one.
func TestTerminalizeDoesNotOverwriteAnEstablishedTerminalState(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	p := newParticipant("p-1")
	if err := s.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Terminalize(ctx, p.ID, StateCompleted); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	if err := s.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
		t.Fatalf("second terminalize: %v", err)
	}
	got, err := s.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != StateCompleted {
		t.Fatalf("state = %q: a second terminalize overwrote an established one", got.State)
	}
}

// A non-terminal state is not a terminal one, and the record says so rather
// than writing it: a legal value used for the wrong thing.
func TestTerminalizeRefusesANonTerminalState(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	p := newParticipant("p-1")
	if err := s.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Terminalize(ctx, p.ID, StateLive); err == nil {
		t.Fatalf("terminalizing to live was accepted")
	}
	got, err := s.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != StatePrepared {
		t.Fatalf("state = %q, want it untouched at %q", got.State, StatePrepared)
	}
}

// MarkLive names the state it expects, so a record a compensation already
// closed is not resurrected by a late enrolment.
func TestMarkLiveRefusesARecordSomethingElseClosed(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	p := newParticipant("p-1")
	if err := s.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Terminalize(ctx, p.ID, StateInterrupted); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	if err := s.MarkLive(ctx, p.ID, testLiveness()); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("mark live err = %v, want ErrNoSuchParticipant", err)
	}
	got, err := s.Participant(ctx, p.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.State != StateInterrupted {
		t.Fatalf("state = %q: a late enrolment resurrected a closed record", got.State)
	}
}

// D3: the coordinator asks its SESSION and is told about its workers by name
// — including one whose control it has lost, because membership is not
// delegation and a taken-over worker is still in the wave. The coordinator's
// own participant row is not one of its holdings.
func TestHoldingsAreAnsweredBySessionAndSurviveALostDelegation(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	p := newParticipant("p-1")
	if err := s.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	itself := newParticipant("p-coordinator")
	itself.Role = RoleCoordinator
	if err := s.CommitPrepared(ctx, itself); err != nil {
		t.Fatalf("commit the coordinator: %v", err)
	}
	elsewhere := newParticipant("p-someone-elses")
	elsewhere.Wave = ID("wave-2")
	if err := s.EnsureWave(ctx, "wave-2", "sess-another-coordinator"); err != nil {
		t.Fatalf("ensure the other wave: %v", err)
	}
	if err := s.CommitPrepared(ctx, elsewhere); err != nil {
		t.Fatalf("commit elsewhere: %v", err)
	}
	if err := s.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession,
		Participant:       p.ID,
		Epoch:             7,
		CreatedByRunID:    "run-42",
		Effects:           DefaultBundle(),
		State:             DelegationActive,
	}); err != nil {
		t.Fatalf("put delegation: %v", err)
	}
	// The human takes over the pane. Control is suspended; membership is not.
	if err := s.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession,
		Participant:       p.ID,
		Epoch:             7,
		Effects:           DefaultBundle(),
		State:             DelegationInputSuspended,
	}); err != nil {
		t.Fatalf("suspend delegation: %v", err)
	}

	held, err := s.HeldBy(ctx, coordSession)
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 1 || held[0].ID != p.ID {
		t.Fatalf("held = %v, want exactly %q — a takeover removed a worker from its own wave", held, p.ID)
	}
}

// The open set is what the reservation counts, and a terminal participant is
// not in it.
func TestOnlyOpenParticipantsAreListed(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	for _, tc := range []struct {
		id    ParticipantID
		state State
	}{
		{"p-prepared", StatePrepared},
		{"p-live", StateLive},
		{"p-done", StateCompleted},
		{"p-gone", StateAbandoned},
	} {
		if err := s.CommitPrepared(ctx, newParticipant(tc.id)); err != nil {
			t.Fatalf("commit %q: %v", tc.id, err)
		}
		switch tc.state {
		case StateLive:
			if err := s.MarkLive(ctx, tc.id, testLiveness()); err != nil {
				t.Fatalf("mark live %q: %v", tc.id, err)
			}
		case StateCompleted, StateAbandoned:
			if err := s.Terminalize(ctx, tc.id, tc.state); err != nil {
				t.Fatalf("terminalize %q: %v", tc.id, err)
			}
		}
	}

	// And an open participant of ANOTHER wave is not this wave's: the
	// reservation counts what one coordinator started, so a second wave's
	// worker counting against it would refuse a spawn nobody had made.
	if err := s.EnsureWave(ctx, "wave-2", "sess-another-coordinator"); err != nil {
		t.Fatalf("ensure the other wave: %v", err)
	}
	elsewhere := newParticipant("p-someone-elses")
	elsewhere.Wave = ID("wave-2")
	if err := s.CommitPrepared(ctx, elsewhere); err != nil {
		t.Fatalf("commit elsewhere: %v", err)
	}

	got, err := s.NonTerminal(ctx, testWave)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d participants, want the 2 of this wave that are open: %v", len(got), got)
	}
	for _, p := range got {
		if p.State.Terminal() {
			t.Fatalf("terminal participant %q listed as open", p.ID)
		}
		if p.Wave != testWave {
			t.Fatalf("participant %q of wave %q listed under %q", p.ID, p.Wave, testWave)
		}
	}
}

// The coordinator session of a wave is its identity: a second spawn into a
// wave that exists must not move every participant to a controller that never
// spawned them.
func TestASecondEnsureDoesNotMoveAWaveToAnotherCoordinator(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	if err := s.EnsureWave(ctx, testWave, "sess-someone-else"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	got, err := s.CoordinatorSession(ctx, testWave)
	if err != nil {
		t.Fatalf("coordinator session: %v", err)
	}
	if got != coordSession {
		t.Fatalf("coordinator = %q, want %q — a second spawn reassigned the wave", got, coordSession)
	}
}

// What the record does not hold is refused BY NAME, never answered with a
// zero value: a participant with no delegation is addressable and not
// controllable, which is a real state, and a caller told "no effects" would
// refuse for the wrong reason.
func TestWhatTheRecordDoesNotHoldIsRefusedByName(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	if err := s.CommitPrepared(ctx, newParticipant("p-1")); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if _, err := s.Participant(ctx, "p-nobody"); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("participant err = %v, want ErrNoSuchParticipant", err)
	}
	if _, err := s.CoordinatorSession(ctx, "wave-nobody"); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("coordinator session err = %v, want ErrNoSuchParticipant", err)
	}
	if _, err := s.Delegation(ctx, "p-1"); !errors.Is(err, ErrNotDelegated) {
		t.Fatalf("delegation err = %v, want ErrNotDelegated", err)
	}
}

// The delegation is read back as it was put, and a second put REPLACES the
// effects rather than adding to them.
func TestADelegationIsReadBackAndASecondPutReplacesIt(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	if err := s.CommitPrepared(ctx, newParticipant("p-1")); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession, Participant: "p-1", Epoch: 7,
		CreatedByRunID: "run-42", Effects: DefaultBundle(), State: DelegationActive,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Delegation(ctx, "p-1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ControllerSession != coordSession || got.Epoch != 7 || got.CreatedByRunID != "run-42" {
		t.Fatalf("delegation = %+v", got)
	}
	if !got.Permits(EffectClose) || got.Permits(EffectDelegateFurther) {
		t.Fatalf("effects = %v, want the default bundle", got.Effects)
	}

	if perr := s.PutDelegation(ctx, Delegation{
		ControllerSession: coordSession, Participant: "p-1", Epoch: 7,
		Effects: []Effect{EffectObserve}, State: DelegationActive,
	}); perr != nil {
		t.Fatalf("second put: %v", perr)
	}
	got, err = s.Delegation(ctx, "p-1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got.Effects) != 1 || got.Effects[0] != EffectObserve {
		t.Fatalf("effects = %v: a second put added to the first rather than replacing it", got.Effects)
	}
}

// ── the mailbox ───────────────────────────────────────────────────────────

// commit is one message into a mailbox, with the test failed if it did not
// land.
func commitTo(t *testing.T, s *MemoryStore, to, from ReaderID, body string) Message {
	t.Helper()
	m, err := s.Commit(context.Background(), Message{
		Wave: testWave, Recipient: to, Sender: from, Body: body,
		CommittedAt: time.UnixMilli(1_700_000_200_000).UTC(),
	})
	if err != nil {
		t.Fatalf("commit %q: %v", body, err)
	}
	return m
}

// A read takes NOTHING and cursors are per reader: two readers see the same
// mailbox whole, and neither read moves anything the other depends on. This
// is the property a queue would not have.
func TestAMailboxReadTakesNothingAndCursorsArePerReader(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	first := commitTo(t, s, "p-1", ReaderID(coordSession), "one")
	second := commitTo(t, s, "p-1", ReaderID(coordSession), "two")
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seqs = %d, %d, want 1, 2 minted by the record", first.Seq, second.Seq)
	}
	if first.ID == "" || first.ID == second.ID {
		t.Fatalf("message ids = %q, %q, want two distinct minted ids", first.ID, second.ID)
	}
	if other := commitTo(t, s, "p-2", ReaderID(coordSession), "one"); other.Seq != 1 {
		t.Fatalf("seq = %d: a sequence is per mailbox, not global", other.Seq)
	}

	// The recipient reads and acknowledges everything.
	got, err := s.Since(ctx, "p-1", 0, MaxFetch)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d messages, want 2", len(got))
	}
	if aerr := s.AdvanceCursor(ctx, Cursor{Mailbox: "p-1", Reader: "p-1", Fetched: 2, Acted: 2}); aerr != nil {
		t.Fatalf("advance: %v", aerr)
	}

	// A second reader of the same mailbox still sees both, and its own
	// cursor starts at nothing.
	watcher, err := s.Cursor(ctx, "p-1", ReaderID(coordSession))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if watcher.Fetched != 0 || watcher.Acted != 0 {
		t.Fatalf("a reader that never looked has cursor %+v, want zero", watcher)
	}
	again, err := s.Since(ctx, "p-1", watcher.Fetched, MaxFetch)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("the second reader saw %d messages, want the 2 the first one read", len(again))
	}

	// And the first reader's own position survived the second one's read.
	mine, err := s.Cursor(ctx, "p-1", "p-1")
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if mine.Fetched != 2 || mine.Acted != 2 {
		t.Fatalf("cursor = %+v, want fetched 2 and acted 2", mine)
	}
}

// A fetch is bounded and a bounded fetch is still in order.
func TestAFetchIsBoundedAndOrdered(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	for i := 0; i < 5; i++ {
		commitTo(t, s, "p-1", ReaderID(coordSession), fmt.Sprintf("m-%d", i))
	}
	got, err := s.Since(ctx, "p-1", 1, 2)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("page = %v, want seqs 2 and 3", got)
	}
}

// A cursor never moves backwards: a mark that went back would hand a reader a
// message it has already acted on.
func TestACursorNeverMovesBackwards(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	commitTo(t, s, "p-1", ReaderID(coordSession), "one")
	commitTo(t, s, "p-1", ReaderID(coordSession), "two")
	if err := s.AdvanceCursor(ctx, Cursor{Mailbox: "p-1", Reader: "p-1", Fetched: 2, Acted: 2}); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := s.AdvanceCursor(ctx, Cursor{Mailbox: "p-1", Reader: "p-1", Fetched: 1, Acted: 0}); err != nil {
		t.Fatalf("second advance: %v", err)
	}
	got, err := s.Cursor(ctx, "p-1", "p-1")
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if got.Fetched != 2 || got.Acted != 2 {
		t.Fatalf("cursor = %+v: a mark moved backwards", got)
	}
}

// Undelivered asks the RECIPIENT's own cursor and nobody else's: another
// reader having looked says nothing about whether the mail was delivered.
func TestUndeliveredAsksTheRecipientsOwnCursor(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	commitTo(t, s, "p-1", ReaderID(coordSession), "one")
	commitTo(t, s, "p-2", ReaderID(coordSession), "two")

	// Somebody who is not the recipient reads p-1's mailbox whole.
	if err := s.AdvanceCursor(ctx, Cursor{Mailbox: "p-1", Reader: ReaderID(coordSession), Fetched: 1, Acted: 1}); err != nil {
		t.Fatalf("advance: %v", err)
	}
	open, err := s.Undelivered(ctx, testWave)
	if err != nil {
		t.Fatalf("undelivered: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("undelivered = %v, want both — another reader looking is not a delivery", open)
	}

	// The recipient itself fetches, and only its own mail becomes delivered.
	if aerr := s.AdvanceCursor(ctx, Cursor{Mailbox: "p-1", Reader: "p-1", Fetched: 1}); aerr != nil {
		t.Fatalf("advance: %v", aerr)
	}
	open, err = s.Undelivered(ctx, testWave)
	if err != nil {
		t.Fatalf("undelivered: %v", err)
	}
	if len(open) != 1 || open[0].Recipient != "p-2" {
		t.Fatalf("undelivered = %v, want only p-2's", open)
	}
}

// The record is reached from several goroutines at once — the supervisor
// reporting an exit, the enrolment rendezvous marking a participant live, and
// the coordinator's own calls are each on their own — so it is safe under
// concurrent use, which `-race` is what proves.
func TestTheRecordIsSafeUnderConcurrentUse(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)
	const n = 16

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		id := ParticipantID(fmt.Sprintf("p-%d", i))
		if err := s.CommitPrepared(ctx, newParticipant(id)); err != nil {
			t.Fatalf("commit %q: %v", id, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.MarkLive(ctx, id, testLiveness()); err != nil {
				t.Errorf("mark live %q: %v", id, err)
			}
			if _, err := s.RecordDeclaration(ctx, id, Declaration{OK: true, Summary: "done"}); err != nil {
				t.Errorf("declare %q: %v", id, err)
			}
			if _, err := s.Commit(ctx, Message{Wave: testWave, Recipient: "p-0", Sender: ReaderID(id), Body: "hello"}); err != nil {
				t.Errorf("commit a message from %q: %v", id, err)
			}
			if err := s.AdvanceCursor(ctx, Cursor{Mailbox: "p-0", Reader: ReaderID(id), Fetched: 1}); err != nil {
				t.Errorf("advance %q: %v", id, err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.NonTerminal(ctx, testWave); err != nil {
				t.Errorf("non terminal: %v", err)
			}
			if _, err := s.HeldBy(ctx, coordSession); err != nil {
				t.Errorf("held by: %v", err)
			}
			if _, err := s.Since(ctx, "p-0", 0, MaxFetch); err != nil {
				t.Errorf("since: %v", err)
			}
			if _, err := s.Undelivered(ctx, testWave); err != nil {
				t.Errorf("undelivered: %v", err)
			}
		}()
	}
	wg.Wait()

	// Every message landed on its own sequence: two writers must never be
	// given the same position in one mailbox.
	got, err := s.Since(ctx, "p-0", 0, n+1)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != n {
		t.Fatalf("mailbox holds %d messages, want %d", len(got), n)
	}
	seen := map[int64]bool{}
	for _, m := range got {
		if seen[m.Seq] {
			t.Fatalf("two messages share seq %d", m.Seq)
		}
		seen[m.Seq] = true
	}
}
