package control

// The third-Admission AD-8 proof, in the package's own test namespace:
// a new resource class is added by defining another Admission and wiring it
// through the constructors, with zero edits to the package's production
// source.
//
// It lives in an internal test file because the Admission interface is
// satisfied outside this package all the time (the capability layer builds
// store plans from it), and this is where the proof that the package needs no
// edit to accept a new kind belongs.

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

type oneShotPermit struct{}

func (oneShotPermit) Release() {}

func TestThirdAdmissionDefinedInTestFileIsUsedUnchanged(t *testing.T) {
	// The foreign admission composes with the package's own, and when it
	// refuses the permit acquired before it is released.
	sem := NewWaitingSemaphore("exec", 1, time.Second)
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
