package app

// The durable record's own honesty, after the nocx-xn63t.1 review's blocker
// 4. The checkout record is the sweep's judgement input: a creation row that
// failed makes a checkout invisible to cleanup forever, and a last-used
// stamp that failed leaves an old time — a clean checkout would be removed
// earlier than the person's period says. Both failures were warning-only.
// Two things change, and each has its test:
//
//   - the ageing is safe when the record is untrustworthy: after a refused
//     write, the sweep trusts no stamp and ages nothing (the sticky hold is
//     the safe direction — a stamp that may be stale is never a reason to
//     remove);
//   - the failure is visible in the product through the same status surface
//     the stub store raises at composition — a soft degrade the Settings
//     notice shows, not only a log line (AGENTS.md).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/transport"
)

// failingRows wraps the real rows store and fails the writes the record
// makes, on demand — the store-side stand-in for a record refusing a write.
type failingRows struct {
	content.WorkerCheckoutRepository
	putErr   error
	touchErr error
}

func (r *failingRows) Put(ctx context.Context, co content.WorkerCheckout) error {
	if r.putErr != nil {
		return r.putErr
	}
	return r.WorkerCheckoutRepository.Put(ctx, co)
}

func (r *failingRows) Touch(ctx context.Context, path string, at int64) error {
	if r.touchErr != nil {
		return r.touchErr
	}
	return r.WorkerCheckoutRepository.Touch(ctx, path, at)
}

// Criterion, the ageing half: after the record refused the close path's
// last-used stamp, the sweep must not age the checkout by the stamp that
// failure left stale — an old row reads as expired, and removing it is
// removing earlier than the person's period says.
func TestASweepAfterARefusedRecordWriteTrustsNoStamp(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	stand.checkouts.rows = &failingRows{WorkerCheckoutRepository: stand.rows, touchErr: errors.New("the record refused the stamp")}
	if err := stand.checkouts.Touch(context.Background(), checkout, sweepNow); err == nil {
		t.Fatal("the failing store answered nil; the stand is not about the failure")
	}

	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())

	if checkoutGone(t, checkout) {
		t.Fatalf("the sweep aged the checkout at %q by a stamp the record had refused to move", checkout)
	}
}

// The paired half on an ordinary machine: with the writes succeeding, the
// same sweep removes the same aged checkout — the hold is about the
// record's trustworthiness, never about the checkout.
func TestASweepWithAnHonestRecordStillAges(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)
	ageCheckout(t, stand, key, checkout, sweepNow.Add(-31*24*time.Hour))

	if err := stand.checkouts.Touch(context.Background(), checkout, sweepNow.Add(-31*24*time.Hour)); err != nil {
		t.Fatalf("touch: %v", err)
	}

	newSweeper(stand, 30*24*time.Hour).RunOnce(context.Background())
	if !checkoutGone(t, checkout) {
		t.Fatalf("the checkout at %q survived a sweep with an honest record behind it", checkout)
	}
}

// recordingStatus stands in for the transport status: it records the raise
// a record write failure must make, so the product-visible half is
// asserted without a socket.
type recordingStatus struct {
	reasons []transport.CheckoutSweepDegradeReason
	details []string
}

func (s *recordingStatus) RaiseUnavailable(reason transport.CheckoutSweepDegradeReason, detail string) {
	s.reasons = append(s.reasons, reason)
	s.details = append(s.details, detail)
}

// Criterion, the visibility half: a record write failing at runtime reaches
// the same status surface the stub store raises at composition — a soft
// degrade visible in the product, not only in a log (AGENTS.md). Both write
// kinds the review names are held: the creation row and the last-used stamp.
func TestARefusedRecordWriteIsRaisedOnTheSweepStatus(t *testing.T) {
	stand := newCheckoutStand(t)
	status := &recordingStatus{}
	stand.checkouts.sweepStatus = status
	stand.checkouts.rows = &failingRows{
		WorkerCheckoutRepository: stand.rows,
		putErr:                   errors.New("the record refused the creation row"),
		touchErr:                 errors.New("the record refused the stamp"),
	}

	// THE CREATION ROW: a spawn's checkout exists but the record refused
	// to name it — invisible to cleanup forever, so the surface says so.
	_, lg := logtest.New(t)
	stand.checkouts.recordCreated(context.Background(), lg, &worktreeUndo{
		repoKey: "k", path: "/x", branch: "feat/x", base: "h", created: true,
	}, "worker-1", "t")
	if len(status.reasons) != 1 {
		t.Fatalf("raises after the refused creation row = %v, want one", status.reasons)
	}
	if status.reasons[0] != transport.CheckoutSweepDegradeRecordWrites {
		t.Fatalf("reason = %q, want %q", status.reasons[0], transport.CheckoutSweepDegradeRecordWrites)
	}
	if status.details[0] == "" {
		t.Fatal("detail is empty; the person reading the notice gets no why")
	}

	// THE LAST-USED STAMP: the close path's touch, refused — the old time
	// it leaves is what the ageing hold above stands on.
	if err := stand.checkouts.Touch(context.Background(), "/x", sweepNow); err == nil {
		t.Fatal("the failing store answered nil; the stand is not about the failure")
	}
	if len(status.reasons) != 2 || status.reasons[1] != transport.CheckoutSweepDegradeRecordWrites {
		t.Fatalf("raises after the refused stamp = %v, want the second recordWrites", status.reasons)
	}
}
