package catalog

import (
	"fmt"
	"testing"
	"time"
)

// clock is a hand-wound wall clock: retention is a fact about elapsed time, so
// the test moves it rather than sleeping through the bound.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newClock() *clock { return &clock{at: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)} }

func TestAMutationThatNeverStartedAnswersNotStarted(t *testing.T) {
	s := newMutationStore(newClock().now)
	record, refusal := s.Begin("m-1", "settings.set")
	if record == nil {
		t.Fatalf("a fresh identity was refused: %+v", refusal)
	}
	record.NotStarted()
	got := s.Answer("m-1")
	if got.Answer != AnswerNotStarted || got.Operation != "settings.set" {
		t.Fatalf("answer = %+v, want not-started for settings.set", got)
	}
}

func TestAMutationThatRanAnswersDoneAndIsNotAcceptedAgain(t *testing.T) {
	s := newMutationStore(newClock().now)
	record, _ := s.Begin("m-1", "settings.set")
	record.Answered(AnsweredResult)
	record.Settled(OutcomeCompleted)
	if got := s.Answer("m-1"); got.Answer != AnswerDone {
		t.Fatalf("answer = %+v, want done", got)
	}
	again, refusal := s.Begin("m-1", "settings.set")
	if again != nil {
		t.Fatal("a mutation whose work ran must not be accepted a second time")
	}
	if refusal.Answer != AnswerDone || refusal.Operation != "settings.set" {
		t.Fatalf("replay refusal = %+v, want done for settings.set", refusal)
	}
}

func TestAMutationThatNeverStartedMayBeRetriedUnderItsOwnIdentity(t *testing.T) {
	s := newMutationStore(newClock().now)
	first, _ := s.Begin("m-1", "vault.unseal")
	// A sealed refusal is the one answer that means "nothing was touched", and
	// ADR-0032's unlock replay depends on it.
	first.Answered(AnsweredSealed)
	if got := s.Answer("m-1"); got.Answer != AnswerNotStarted {
		t.Fatalf("answer = %+v, want not-started", got)
	}
	second, refusal := s.Begin("m-1", "vault.unseal")
	if second == nil {
		t.Fatalf("the sanctioned retry was refused: %+v", refusal)
	}
	if got := s.Answer("m-1"); got.Answer != AnswerUnknown {
		t.Fatalf("the retry is running, answer = %+v, want outcome-unknown", got)
	}
	second.Answered(AnsweredResult)
	if got := s.Answer("m-1"); got.Answer != AnswerDone {
		t.Fatalf("answer after the retry = %+v, want done", got)
	}
}

// TestAReplacedAttemptCannotWriteTheRetryItSanctioned is the identity's
// window (nocx-3an1s.2.17): the record is the ATTEMPT's handle, not the
// identity's. An invocation that expired while queued records "not started",
// which sanctions the retry; the retry is admitted, and only then does the
// expired invocation reach its own end. That end answers its own caller and
// must change nothing — the retry is running, so a third invocation is refused,
// and the retry's answer is the one retained.
func TestAReplacedAttemptCannotWriteTheRetryItSanctioned(t *testing.T) {
	s := newMutationStore(newClock().now)
	first, _ := s.Begin("m-1", "vault.unseal")
	first.NotStarted()

	second, refusal := s.Begin("m-1", "vault.unseal")
	if second == nil {
		t.Fatalf("the sanctioned retry was refused: %+v", refusal)
	}
	if second == first {
		t.Fatal("the retry holds the replaced attempt's record; its answer is not its own")
	}

	// The expired attempt ends after the retry took the identity over.
	first.NotStarted()
	first.Settled(OutcomeNotStarted)

	if got := s.Answer("m-1"); got.Answer != AnswerUnknown {
		t.Fatalf("answer while the retry runs = %+v, want outcome-unknown", got)
	}
	third, refused := s.Begin("m-1", "vault.unseal")
	if third != nil {
		t.Fatal("a second invocation was admitted beside the retry")
	}
	if refused.Answer != AnswerUnknown || refused.Operation != "vault.unseal" {
		t.Fatalf("replay refusal = %+v, want outcome-unknown for vault.unseal", refused)
	}

	second.Answered(AnsweredResult)
	if got := s.Answer("m-1"); got.Answer != AnswerDone {
		t.Fatalf("answer after the retry answered = %+v, want done", got)
	}
}

func TestAWithdrawnOrExpiredMutationAnswersOutcomeUnknown(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeCancelled, OutcomeExpired} {
		s := newMutationStore(newClock().now)
		record, _ := s.Begin("m-1", "git.commit")
		record.Settled(outcome)
		// "Checking what happened": the work stopped without an answer, so the
		// effect cannot be claimed either way.
		if got := s.Answer("m-1"); got.Answer != AnswerUnknown {
			t.Fatalf("%s: answer = %+v, want outcome-unknown", outcome, got)
		}
	}
}

func TestAnAnswerArrivingAfterACancelIsKept(t *testing.T) {
	s := newMutationStore(newClock().now)
	record, _ := s.Begin("m-1", "git.commit")
	record.Settled(OutcomeCancelled)
	// The worker reached its store before the cancellation landed, and
	// answered. The record says otherwise now, exactly as §3 requires.
	record.Answered(AnsweredResult)
	if got := s.Answer("m-1"); got.Answer != AnswerDone {
		t.Fatalf("answer = %+v, want done", got)
	}
}

func TestARecordIsForgottenAfterTheRetentionBound(t *testing.T) {
	c := newClock()
	s := newMutationStore(c.now)
	record, _ := s.Begin("m-1", "settings.set")
	record.Answered(AnsweredResult)

	c.advance(time.Duration(RetentionMillis)*time.Millisecond - time.Millisecond)
	if got := s.Answer("m-1"); got.Answer != AnswerDone {
		t.Fatalf("just inside the bound, answer = %+v, want done", got)
	}
	c.advance(time.Millisecond)
	if got := s.Answer("m-1"); got.Answer != AnswerNoRecord {
		t.Fatalf("at the bound, answer = %+v, want no record", got)
	}
	// Expiry is the end of "checking what happened", and the identity starts
	// over: the backend can no longer tell a replay from a first attempt.
	again, _ := s.Begin("m-1", "settings.set")
	if again == nil {
		t.Fatal("an expired identity must start a fresh record")
	}
	if len(s.byKey) != 1 {
		t.Fatalf("the store holds %d records, want 1", len(s.byKey))
	}
}

func TestARunningMutationIsRetainedPastTheBoundAndAnswersUnknown(t *testing.T) {
	c := newClock()
	s := newMutationStore(c.now)
	record, _ := s.Begin("m-1", "git.commit")
	c.advance(time.Duration(RetentionMillis)*time.Millisecond + time.Hour)
	if got := s.Answer("m-1"); got.Answer != AnswerUnknown {
		t.Fatalf("a still-running mutation answers %+v, want outcome-unknown", got)
	}
	// Its own max bounds it; the store must not drop a live record on the
	// retention clock.
	record.Answered(AnsweredResult)
	if got := s.Answer("m-1"); got.Answer != AnswerDone {
		t.Fatalf("answer after the late result = %+v, want done", got)
	}
}

func TestAnUnknownIdentityAnswersNoRecord(t *testing.T) {
	s := newMutationStore(newClock().now)
	if got := s.Answer("never-seen"); got.Answer != AnswerNoRecord || got.Operation != "" {
		t.Fatalf("answer = %+v, want no record", got)
	}
}

func TestTheStoreIsBoundedAndDropsSettledRecordsFirst(t *testing.T) {
	s := newMutationStore(newClock().now)
	for i := 0; i < MaxRetained+8; i++ {
		record, _ := s.Begin(fmt.Sprintf("m-%d", i), "settings.set")
		record.Answered(AnsweredResult)
	}
	if len(s.byKey) > MaxRetained {
		t.Fatalf("the store holds %d records, want at most %d", len(s.byKey), MaxRetained)
	}
	if len(s.order) > MaxRetained*2 {
		t.Fatalf("the insertion order holds %d entries, want a bounded list", len(s.order))
	}
}

func TestTheRetentionIsAFiniteSourcedDeadline(t *testing.T) {
	if !Retention.valid() {
		t.Fatalf("the retention is not a finite, sourced deadline: %+v", Retention)
	}
	if RetentionMillis <= 0 {
		t.Fatalf("retention %d is not finite", RetentionMillis)
	}
}

func TestANilRecordTakesEveryWrite(t *testing.T) {
	var record *MutationRecord
	// A call with no identity has no record; dispatch must not branch on it.
	record.NotStarted()
	record.Answered(AnsweredError)
	record.Settled(OutcomeCompleted)
}
