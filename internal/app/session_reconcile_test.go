package app

// The verdict half of restart reconciliation (nocx-k6p18.5), tested where the
// verdict is DECIDED. The store's half — what each verdict does to the rows —
// is internal/content/reconcile_test.go.
//
// The assertion that matters here is a negative and it is asserted PER FAILURE
// MODE rather than once: a refused connection, a timeout, a sealed vault and an
// unreachable host each leave the session unreconciled, and none of them
// produces `absent`. `absent` deletes a recording and closes a block, so a
// single wrong classification here is destroyed work; `unknown` costs a week of
// disk that the age bound then reclaims.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/vault"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingReconciler is the store's seam as a double: it remembers the
// judgements it was handed, so a test can assert on the VERDICT rather than on
// its consequences.
type recordingReconciler struct {
	pending   []content.PendingSession
	applied   []content.SessionJudgement
	sweptWith []time.Duration
	applyErr  error
}

func (r *recordingReconciler) Pending(context.Context) ([]content.PendingSession, error) {
	return r.pending, nil
}

func (r *recordingReconciler) Apply(_ context.Context, j content.SessionJudgement) error {
	r.applied = append(r.applied, j)
	return r.applyErr
}

func (r *recordingReconciler) SweepStale(_ context.Context, age time.Duration) (int, error) {
	r.sweptWith = append(r.sweptWith, age)
	return 0, nil
}

// stubInventory owns one id space and answers with whatever it was given.
type stubInventory struct {
	owns map[string]bool
	live map[string]struct{}
	err  error
}

func (s stubInventory) Owns(id string) bool { return s.owns[id] }
func (s stubInventory) LiveSessions(context.Context) (map[string]struct{}, error) {
	return s.live, s.err
}

// timeoutErr is a net.Error that times out, which is how a dial reports one.
type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }
func (timeoutErr) Temporary() bool {
	return true
}

var _ net.Error = timeoutErr{}

const aSession = "session-carried-over"

func onePending() []content.PendingSession {
	return []content.PendingSession{{SessionID: aSession, Since: time.Now().Add(-time.Hour)}}
}

// THE assertion, per failure mode. Every one of these is a real way for the
// ask to fail, and not one of them is an answer.
func TestNoFailureModeEverProducesAbsent(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		cause content.UnreconciledCause
	}{
		{"a refused connection", syscall.ECONNREFUSED, content.CauseConnectionRefused},
		{"a refused connection reported as prose", errors.New(
			"dial unix /run/nocx/helper.sock: connect: connection refused"), content.CauseConnectionRefused},
		{"a timeout", timeoutErr{}, content.CauseTimedOut},
		{"a deadline that passed", context.DeadlineExceeded, content.CauseTimedOut},
		{"a sealed vault", vault.ErrVaultSealed, content.CauseVaultSealed},
		{"a vault nobody is there to unlock", vault.ErrNoUnlockClient, content.CauseVaultSealed},
		{"an unreachable host", errors.New("ssh: no route to host"), content.CauseHostUnreachable},
		{"something this build cannot classify", errors.New("¯\\_(ツ)_/¯"), content.CauseHostUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingReconciler{pending: onePending()}
			inv := stubInventory{owns: map[string]bool{aSession: true}, err: tc.err}
			reconcileSessions(context.Background(), rec, []sessionInventory{inv}, time.Hour, quietLogger())

			if len(rec.applied) != 1 {
				t.Fatalf("judgements = %+v, want exactly one", rec.applied)
			}
			got := rec.applied[0]
			if got.Verdict == content.VerdictAbsent {
				t.Fatalf("%s produced ABSENT — a failure is not an answer, and absent deletes "+
					"the recording and closes the block", tc.name)
			}
			if got.Verdict != content.VerdictUnknown {
				t.Fatalf("verdict = %q, want unknown", got.Verdict)
			}
			if got.Cause != tc.cause {
				t.Fatalf("cause = %q, want %q — the product says WHY nobody could be asked",
					got.Cause, tc.cause)
			}
			if got.Detail == "" {
				t.Fatal("no detail was carried — a bug report needs the error's own words")
			}
		})
	}
}

// And the paired positive, without which the negative above is satisfiable by
// a function that always answers unknown: an inventory that owns the id and
// ANSWERS produces live for what it reports and absent for what it does not.
func TestAnInventoryThatAnswersProducesBothVerdicts(t *testing.T) {
	const gone = "session-the-host-does-not-hold"
	rec := &recordingReconciler{pending: []content.PendingSession{
		{SessionID: aSession}, {SessionID: gone},
	}}
	inv := stubInventory{
		owns: map[string]bool{aSession: true, gone: true},
		live: map[string]struct{}{aSession: {}},
	}
	reconcileSessions(context.Background(), rec, []sessionInventory{inv}, time.Hour, quietLogger())

	if len(rec.applied) != 2 {
		t.Fatalf("judgements = %+v, want one per session", rec.applied)
	}
	byID := map[string]content.SessionVerdict{}
	for _, j := range rec.applied {
		byID[j.SessionID] = j.Verdict
	}
	if byID[aSession] != content.VerdictLive {
		t.Fatalf("a session the host reports = %q, want live", byID[aSession])
	}
	if byID[gone] != content.VerdictAbsent {
		t.Fatalf("a session the host was asked about and does not hold = %q, want absent", byID[gone])
	}
}

// An inventory may only judge the id space it owns. Without this rule a helper
// asked about a session it never spawned answers "I do not hold that" — which
// is TRUE — and the answer deletes somebody else's live work.
func TestAnInventoryNeverJudgesAnIdSpaceItDoesNotOwn(t *testing.T) {
	rec := &recordingReconciler{pending: onePending()}
	inv := stubInventory{owns: map[string]bool{}, live: map[string]struct{}{}}
	reconcileSessions(context.Background(), rec, []sessionInventory{inv}, time.Hour, quietLogger())

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	if got := rec.applied[0]; got.Verdict != content.VerdictUnknown || got.Cause != content.CauseNoInventory {
		t.Fatalf("a session no inventory owns = %q/%q, want unknown/noInventory — an inventory that "+
			"does not own the id was asked anyway", got.Verdict, got.Cause)
	}
}

// The same with no inventories at all, which is what the composition root
// passes today, and the age bound still runs — because removing the startup
// delete without replacing the bound is what must never ship.
func TestWithNothingToAskEverySessionIsUnknownAndTheAgeBoundStillRuns(t *testing.T) {
	rec := &recordingReconciler{pending: onePending()}
	reconcileSessions(context.Background(), rec, nil, 42*time.Hour, quietLogger())

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictUnknown {
		t.Fatalf("judgements = %+v, want one unknown", rec.applied)
	}
	if len(rec.sweptWith) != 1 || rec.sweptWith[0] != 42*time.Hour {
		t.Fatalf("the age bound ran %v, want once at the configured age", rec.sweptWith)
	}
}

// A verdict that cannot be written is not a startup failure and does not
// consume the session: the pass logs it, goes on, and the next pass repeats it.
func TestAVerdictThatCannotBeWrittenLeavesThePassRunning(t *testing.T) {
	rec := &recordingReconciler{pending: onePending(), applyErr: errors.New("disk is on fire")}
	reconcileSessions(context.Background(), rec, nil, time.Hour, quietLogger())
	if len(rec.sweptWith) != 1 {
		t.Fatalf("the age bound did not run after a failed verdict: %v", rec.sweptWith)
	}
}
