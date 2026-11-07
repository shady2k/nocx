// Package control_test exercises the scheduling contract through its public
// surface only. A third Admission defined in a test file lives in
// third_admission_test.go (package control, internal).
package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

// --- helpers -------------------------------------------------------------

// waitAcquirable polls TryAcquire until it succeeds (releasing immediately),
// proving the admission has capacity again. Used to observe a permit release.
func waitAcquirable(t *testing.T, a control.Admission, what string) {
	t.Helper()
	waittest.WaitForTimeout(t, what, 2*time.Second, func() bool {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if p, rej := a.TryAcquire(ctx); rej == nil {
		p.Release()
		t.Fatalf("%s was acquirable, expected it to be held", what)
	}
}

// --- nothing refuses admission -------------------------------------------

// TestAsyncSubmissionNeverRefuses submits far more tasks than any worker pool
// this package ever had, each holding its goroutine until released, and
// requires every submit to be accepted: the read loop hands the work over and
// moves on (ADR-8eej3e66 §7, stage 8). What bounds the number is the credit
// window on the connection, not a submission.
func TestAsyncSubmissionNeverRefuses(t *testing.T) {
	var sub control.AsyncSubmission
	const tasks = 64
	release := make(chan struct{})
	var mu sync.Mutex
	running := 0
	for range tasks {
		if rej := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) {
			mu.Lock()
			running++
			mu.Unlock()
			<-release
		}}); rej != nil {
			t.Fatalf("submit %d refused: %+v", tasks, rej)
		}
	}
	// All of them run concurrently: none is queued behind a permit.
	waittest.WaitFor(t, "every submitted task running", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return running == tasks
	})
	close(release)
}

// A task that was withdrawn while it waited never runs: the one-step
// start-against-cancel decision is still the submission's.
func TestAsyncSubmissionNeverStartsAWithdrawnTask(t *testing.T) {
	var sub control.AsyncSubmission
	ran := make(chan struct{}, 1)
	if rej := sub.TrySubmit(context.Background(), control.Task{
		Start: func() bool { return false },
		Run:   func(context.Context) { ran <- struct{}{} },
	}); rej != nil {
		t.Fatalf("submit refused: %+v", rej)
	}
	select {
	case <-ran:
		t.Fatal("a withdrawn task was run")
	case <-time.After(50 * time.Millisecond):
	}
}

// --- permits are released on every exit path ------------------------------

func TestPermitSpanNormalReturn(t *testing.T) {
	sem := control.NewWaitingSemaphore("worker", 1, time.Second)
	p, rej := sem.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("acquire refused: %+v", rej)
	}
	mustNotAcquire(t, sem, "permit while the task is running")
	p.Release()
	waitAcquirable(t, sem, "permit after normal return")
}

func TestPermitReleasedOnContextCancellation(t *testing.T) {
	sem := control.NewWaitingSemaphore("worker", 1, time.Second)
	p, rej := sem.TryAcquire(context.Background())
	if rej != nil {
		t.Fatalf("acquire refused: %+v", rej)
	}
	mustNotAcquire(t, sem, "permit while the task waits on the context")
	p.Release()
	waitAcquirable(t, sem, "permit after context cancellation")
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
		return nil, &control.Rejection{Kind: control.RejectionUnavailable, Reason: "refused", Scope: r.name}
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
