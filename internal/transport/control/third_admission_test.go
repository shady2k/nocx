package control

// The third-Admission AD-8 proof, in the package's own test namespace:
// a new resource class is added by defining another Admission and wiring it
// through the constructors, with zero edits to the package's production
// source.
//
// It lives in an internal test file because NonblockingAdmission's marker
// method is unexported by design (ADR-0026 item 3 of Enforcement): only this
// package may declare a submission-path admission, so a waiting admission
// cannot be wired into a Submission's TrySubmit from anywhere else — the
// miswiring is a compile error, not a convention. The package's internal
// tests are the one legitimate outside-implementer of the marker, which is
// why the proof runs here.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// oneShotAdmission is the THIRD Admission implementation, defined in this
// test file and wired through the package's own constructors with zero edits
// to its production source. It is the AD-8 proof: the executor can add a new
// kind of resource by constructing another Admission.
type oneShotAdmission struct {
	name string

	mu   sync.Mutex
	used bool
}

func (o *oneShotAdmission) Name() string { return o.name }

func (o *oneShotAdmission) TryAcquire(context.Context) (Permit, *Rejection) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.used {
		return nil, &Rejection{Reason: "already used", Scope: o.name}
	}
	o.used = true
	return oneShotPermit{}, nil
}

// nonblocking seals the interface: TryAcquire never blocks, so the admission
// is a legitimate part of a submission-path composite, exactly like the
// package's semaphore. No type outside this package can declare it.
func (o *oneShotAdmission) nonblocking() {}

type oneShotPermit struct{}

func (oneShotPermit) Release() {}

func TestThirdAdmissionDefinedInTestFileIsUsedUnchanged(t *testing.T) {
	sub := NewBoundedSubmission(&oneShotAdmission{name: "oneshot"})

	ran := make(chan struct{})
	if rej := sub.TrySubmit(context.Background(), Task{Run: func(context.Context) { close(ran) }}); rej != nil {
		t.Fatalf("first submit through the third admission was rejected: %+v", rej)
	}
	<-ran

	if rej := sub.TrySubmit(context.Background(), Task{Run: func(context.Context) { t.Error("must not run") }}); rej == nil {
		t.Fatal("second submit must be refused by the exhausted third admission")
	}

	// It also composes with the package's own semaphore: when the foreign
	// admission refuses, the semaphore permit acquired before it is released.
	sem := NewSemaphore("exec", 1)
	comp := NewComposite(&oneShotAdmission{name: "oneshot2"}, sem)
	if p, rej := comp.TryAcquire(context.Background()); rej != nil {
		t.Fatalf("first composite acquire refused: %+v", rej)
	} else {
		p.Release()
	}
	if _, rej := comp.TryAcquire(context.Background()); rej == nil {
		t.Fatal("composite acquired after the foreign admission was exhausted")
	}
	waitAcquirable(t, sem, "semaphore after a failed composite acquire")
}

// waitAcquirable polls TryAcquire until it succeeds (releasing immediately),
// proving the admission has capacity again. Local copy of the external
// suite's helper — package boundaries cannot share it.
func waitAcquirable(t *testing.T, a Admission, what string) {
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
