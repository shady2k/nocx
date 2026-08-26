// Package control_test exercises the scheduling contract through its public
// surface only. The third-Admission AD-8 proof lives in
// third_admission_test.go (package control, internal): the NonblockingAdmission
// marker is unexported by design (ADR-0026), so only the package itself — its
// internal tests included — may define a submission-path admission.
package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/testwait"
	"github.com/shady2k/nocx/internal/transport/control"
)

// --- helpers -------------------------------------------------------------

// waitAcquirable polls TryAcquire until it succeeds (releasing immediately),
// proving the admission has capacity again. Used to observe a permit release.
func waitAcquirable(t *testing.T, a control.Admission, what string) {
	t.Helper()
	testwait.WaitForTimeout(t, what, 2*time.Second, func() bool {
		p, rej := a.TryAcquire(context.Background())
		if rej != nil {
			return false
		}
		p.Release()
		return true
	})
}

// mustNotAcquire asserts the admission currently has no capacity — the
// "permit held" end of the acquire→release interval.
func mustNotAcquire(t *testing.T, a control.Admission, what string) {
	t.Helper()
	if p, rej := a.TryAcquire(context.Background()); rej == nil {
		p.Release()
		t.Fatalf("%s was acquirable, expected it to be held", what)
	}
}

// --- acceptance 1: TrySubmit refuses promptly, never blocks -----------------

func TestTrySubmitRejectsPromptlyWhenResourcesExhausted(t *testing.T) {
	sem := control.NewSemaphore("worker", 1)
	sub := control.NewBoundedSubmission(sem)

	release := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { <-release }}); rej != nil {
		t.Fatalf("first submit rejected: %+v", rej)
	}

	ran := make(chan struct{})
	result := make(chan *control.Rejection, 1)
	go func() {
		result <- sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { close(ran) }})
	}()

	select {
	case rej := <-result:
		if rej == nil {
			t.Fatal("TrySubmit succeeded while the only permit was held")
		}
		if rej.Reason == "" || rej.Scope == "" {
			t.Errorf("rejection must carry Reason and Scope, got %+v", rej)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("TrySubmit blocked while resources were exhausted — the read loop would freeze")
	}

	select {
	case <-ran:
		t.Fatal("a rejected task must never run")
	default:
	}

	close(release)
	waitAcquirable(t, sem, "permit after the first task finished")
}

// --- acceptance 2: saturating one resource leaves another untouched ----------

func TestSaturatingOneAdmissionLeavesAnotherUntouched(t *testing.T) {
	conflict := control.NewSemaphore("conflict", 1)
	exec := control.NewSemaphore("exec", 1)
	sub := control.NewBoundedSubmission(control.NewCompositeNonblocking(conflict, exec))

	// Saturate the conflict admission directly; the composite refuses at the
	// first gate and must never consume the execution permit.
	conflictPermit, rej := conflict.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("could not saturate conflict admission: %+v", rej)
	}
	if refused := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { t.Error("must not run") }}); refused == nil {
		t.Fatal("expected rejection while the conflict admission is saturated")
	}
	p, execRej := exec.TryAcquire(context.Background())
	if execRej != nil {
		t.Fatalf("execution admission was consumed by a composite acquire that refused at the conflict gate: %+v", execRej)
	}
	p.Release()
	conflictPermit.Release()

	// And the reverse: saturating the execution admission leaves the conflict
	// admission untouched. The composite acquires conflict first and must
	// release it again when the exec gate refuses.
	execPermit, rej := exec.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("could not saturate execution admission: %+v", rej)
	}
	if rej := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { t.Error("must not run") }}); rej == nil {
		t.Fatal("expected rejection while the execution admission is saturated")
	}
	if p, rej := conflict.TryAcquire(context.Background()); rej != nil {
		t.Fatalf("conflict admission was leaked by a composite acquire that refused at the exec gate: %+v", rej)
	} else {
		p.Release()
	}
	execPermit.Release()
}

// --- acceptance 4: permits are released on every exit path -------------------

func TestPermitSpanNormalReturn(t *testing.T) {
	sem := control.NewSemaphore("worker", 1)
	sub := control.NewBoundedSubmission(sem)

	release := make(chan struct{})
	done := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) {
		defer close(done)
		<-release
	}}); rej != nil {
		t.Fatalf("submit rejected: %+v", rej)
	}

	// First end of the interval: the permit is held from the moment TrySubmit
	// returns — TryAcquire happens synchronously, before the goroutine spawns.
	mustNotAcquire(t, sem, "permit while the task is running")

	close(release)
	<-done

	// Second end: the permit is released after the task returns.
	waitAcquirable(t, sem, "permit after normal return")
}

func TestPermitReleasedOnContextCancellation(t *testing.T) {
	sem := control.NewSemaphore("worker", 1)
	sub := control.NewBoundedSubmission(sem)

	ctx, cancel := context.WithCancel(context.Background())
	if rej := sub.TrySubmit(ctx, control.Task{Run: func(ctx context.Context) { <-ctx.Done() }}); rej != nil {
		t.Fatalf("submit rejected: %+v", rej)
	}
	mustNotAcquire(t, sem, "permit while the task waits on the context")
	cancel()
	waitAcquirable(t, sem, "permit after context cancellation")
}

// --- acceptance 5: conflicting work never starves unrelated work ---------------

func TestConflictingWorkDoesNotStarveUnrelatedWork(t *testing.T) {
	// One conflict slot, two workers. Conflict is acquired BEFORE the
	// execution permit (NewComposite order), so work refused at the conflict
	// gate never occupies a worker permit.
	conflict := control.NewSemaphore("conflict", 1)
	exec := control.NewSemaphore("exec", 2)
	sub := control.NewBoundedSubmission(control.NewCompositeNonblocking(conflict, exec))

	// Conflicting task A: takes the conflict slot and one worker, then blocks.
	releaseA := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { <-releaseA }}); rej != nil {
		t.Fatalf("first conflicting submit rejected: %+v", rej)
	}

	// Conflicting task B: refused at the conflict gate, promptly, without
	// consuming the second worker permit.
	ranB := make(chan struct{})
	result := make(chan *control.Rejection, 1)
	go func() {
		result <- sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { close(ranB) }})
	}()
	select {
	case rej := <-result:
		if rej == nil {
			t.Fatal("conflicting work must be refused while the conflict slot is held")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("conflicting TrySubmit blocked")
	}
	select {
	case <-ranB:
		t.Fatal("refused conflicting task must never run")
	default:
	}

	// Non-conflicting work needs only a worker permit. Because the refused B
	// never took one, a worker is still free and the work runs — while the
	// conflict slot stays occupied.
	done := make(chan struct{})
	unrelated := control.NewBoundedSubmission(exec)
	if rej := unrelated.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { close(done) }}); rej != nil {
		t.Fatalf("unrelated work was refused while only the conflict slot was held: %+v", rej)
	}
	<-done

	close(releaseA)
}

// --- constraint 4: the composite's order and release-on-failure are contracts --

// admissionLog is a shared, ordered record of acquire/release events across
// several recordingAdmissions, so the composite's global order is asserted.
type admissionLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *admissionLog) add(e string) {
	l.mu.Lock()
	l.entries = append(l.entries, e)
	l.mu.Unlock()
}

func (l *admissionLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

// recordingAdmission — another test-file Admission — records every acquire
// and release into a shared log.
type recordingAdmission struct {
	name  string
	allow bool
	log   *admissionLog
}

func (r *recordingAdmission) Name() string { return r.name }

func (r *recordingAdmission) TryAcquire(context.Context) (control.Permit, *control.Rejection) {
	r.log.add("acquire:" + r.name)
	if !r.allow {
		return nil, &control.Rejection{Reason: "refused", Scope: r.name}
	}
	return recordingPermit{r}, nil
}

type recordingPermit struct{ r *recordingAdmission }

func (p recordingPermit) Release() { p.r.log.add("release:" + p.r.name) }

func TestCompositeAcquiresConflictBeforeExecutionAndReleasesOnFailure(t *testing.T) {
	log := &admissionLog{}
	conflict := &recordingAdmission{name: "conflict", allow: true, log: log}
	exec := &recordingAdmission{name: "exec", allow: true, log: log}
	comp := control.NewComposite(conflict, exec)

	p, rej := comp.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("composite acquire refused: %+v", rej)
	}
	if got, want := log.snapshot(), []string{"acquire:conflict", "acquire:exec"}; !equal(got, want) {
		t.Fatalf("composite must acquire conflict before execution, got %v want %v", got, want)
	}
	p.Release()
	if got, want := log.snapshot(), []string{"acquire:conflict", "acquire:exec", "release:exec", "release:conflict"}; !equal(got, want) {
		t.Fatalf("composite must release in reverse order, got %v want %v", got, want)
	}

	// When a later gate refuses, every earlier permit is released.
	log2 := &admissionLog{}
	conflict2 := &recordingAdmission{name: "conflict2", allow: true, log: log2}
	exec2 := &recordingAdmission{name: "exec2", allow: false, log: log2}
	comp2 := control.NewComposite(conflict2, exec2)
	if _, rej := comp2.TryAcquire(context.Background()); rej == nil {
		t.Fatal("composite must refuse when a later admission refuses")
	}
	if got, want := log2.snapshot(), []string{"acquire:conflict2", "acquire:exec2", "release:conflict2"}; !equal(got, want) {
		t.Fatalf("composite must release earlier permits when a later gate refuses, got %v want %v", got, want)
	}
}

// --- ImmediateSubmission ------------------------------------------------------

func TestImmediateSubmissionRunsInline(t *testing.T) {
	var sub control.ImmediateSubmission
	ran := false
	if rej := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { ran = true }}); rej != nil {
		t.Fatalf("ImmediateSubmission rejected: %+v", rej)
	}
	if !ran {
		t.Fatal("ImmediateSubmission must run the task inline before returning")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
