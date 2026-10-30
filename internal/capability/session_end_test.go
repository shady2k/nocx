package capability_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

type heldSessionEndRegistry struct {
	session.Registry
	entered chan struct{}
	release chan struct{}
	failure error
}

func (r *heldSessionEndRegistry) PrepareEndSession(id session.ID) (session.Session, func() error, error) {
	sess, finish, err := r.Registry.PrepareEndSession(id)
	if err != nil {
		return nil, nil, err
	}
	return sess, func() error {
		close(r.entered)
		<-r.release
		return errors.Join(finish(), r.failure)
	}, nil
}

func TestSessionEndRetiresBeforeTeardownAndKeepsItsLaneUntilCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "teardown_failure"
		}
		t.Run(name, func(t *testing.T) {
			registry := newFakeSessionRegistry()
			registry.sessions["A"] = &fakeSession{id: "A"}
			registry.sessions["B"] = &fakeSession{id: "B"}
			reg := &heldSessionEndRegistry{Registry: registry, entered: make(chan struct{}), release: make(chan struct{})}
			if fail {
				reg.failure = errors.New("helper close failed")
			}
			sessionGate := control.NewSemaphore(capability.GateSession, 1)
			lane := control.NewSemaphore("control", 1)
			ops := capability.NewSessionOperations(sessionGate, lane, reg, nil)
			op, err := ops.ForSessionEnd("A")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				sess session.Session
				err  error
			}
			result := make(chan outcome, 1)
			go func() {
				sess, err := op.Run(ctx, func(retired session.Session) {
					if retired.ID() != "A" {
						t.Errorf("retired %q, want A", retired.ID())
					}
					if _, err := reg.Get("A"); err == nil {
						t.Error("before-teardown callback could still look up the retired session")
					}
				})
				result <- outcome{sess, err}
			}()
			<-reg.entered
			// Cancellation after retirement cannot skip process completion.
			cancel()
			permit, rej := sessionGate.TryAcquire(context.Background())
			if rej != nil {
				t.Error("slow teardown still holds the whole-session gate")
			} else {
				if _, err := reg.Get("B"); err != nil {
					t.Errorf("unrelated session disappeared: %v", err)
				}
				permit.Release()
			}
			if permit, rej := lane.TryAcquire(context.Background()); rej == nil {
				permit.Release()
				t.Error("teardown released its execution allowance before completion")
			}
			close(reg.release)
			got := <-result
			if got.sess == nil || got.sess.ID() != "A" || !errors.Is(got.err, reg.failure) {
				t.Fatalf("completion = (%v, %v), want retired A and %v", got.sess, got.err, reg.failure)
			}
			if permit, rej := lane.TryAcquire(context.Background()); rej != nil {
				t.Fatal("finished teardown leaked its execution allowance")
			} else {
				permit.Release()
			}
		})
	}
}

func TestSessionEndAdmissionRefusalLeavesTheSessionLive(t *testing.T) {
	reg := newFakeSessionRegistry()
	reg.sessions["A"] = &fakeSession{id: "A"}
	ops := capability.NewSessionOperations(control.NewSemaphore(capability.GateSession, 1), control.NewSemaphore("control", 0), reg, nil)
	op, err := ops.ForSessionEnd("A")
	if err != nil {
		t.Fatal(err)
	}
	got, err := op.Run(context.Background(), func(session.Session) { t.Error("refused close reached teardown preparation") })
	if got != nil || !capability.IsRefused(err) {
		t.Fatalf("saturated close = (%v, %v), want refusal before retirement", got, err)
	}
	if _, err := reg.Get("A"); err != nil {
		t.Fatalf("refused close removed the session: %v", err)
	}
}

// A counting admission makes cancellation's position in the operation visible:
// cancelled queued work must not acquire anything, and cancellation during a
// permit handoff must release what it acquired without retiring the identity.
type reviewCountingAdmission struct {
	control.Admission
	calls  int
	cancel context.CancelFunc
}

func (a *reviewCountingAdmission) TryAcquire(ctx context.Context) (control.Permit, *control.Rejection) {
	a.calls++
	permit, rejection := a.Admission.TryAcquire(ctx)
	if rejection == nil && a.cancel != nil {
		a.cancel()
	}
	return permit, rejection
}

func TestReviewSessionEndCancelledBeforeAdmissionLeavesSessionLive(t *testing.T) {
	reg := newFakeSessionRegistry()
	reg.sessions["A"] = &fakeSession{id: "A"}
	gate := &reviewCountingAdmission{Admission: control.NewSemaphore(capability.GateSession, 1)}
	lane := &reviewCountingAdmission{Admission: control.NewSemaphore("control", 1)}
	ops := capability.NewSessionOperations(gate, lane, reg, nil)
	op, err := ops.ForSessionEnd("A")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := op.Run(ctx, func(session.Session) { t.Error("cancelled close reached teardown") })
	if got != nil || !capability.IsRefused(err) {
		t.Errorf("cancelled close = (%v, %v), want refusal", got, err)
	}
	if gate.calls != 0 || lane.calls != 0 {
		t.Errorf("cancelled close acquired admissions: gate=%d lane=%d", gate.calls, lane.calls)
	}
	if _, err := reg.Get("A"); err != nil {
		t.Fatalf("cancelled close retired A: %v", err)
	}
}

func TestReviewSessionEndCancelledDuringAdmissionLeavesSessionLive(t *testing.T) {
	for _, cancelAt := range []string{"gate", "lane"} {
		t.Run(cancelAt, func(t *testing.T) {
			reg := newFakeSessionRegistry()
			reg.sessions["A"] = &fakeSession{id: "A"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate := &reviewCountingAdmission{Admission: control.NewSemaphore(capability.GateSession, 1)}
			lane := &reviewCountingAdmission{Admission: control.NewSemaphore("control", 1)}
			if cancelAt == "gate" {
				gate.cancel = cancel
			} else {
				lane.cancel = cancel
			}
			op, err := capability.NewSessionOperations(gate, lane, reg, nil).ForSessionEnd("A")
			if err != nil {
				t.Fatal(err)
			}
			got, err := op.Run(ctx, func(session.Session) { t.Error("cancelled close reached teardown") })
			if got != nil || !capability.IsRefused(err) {
				t.Errorf("cancelled close = (%v, %v), want refusal", got, err)
			}
			if cancelAt == "gate" && lane.calls != 0 {
				t.Error("close acquired execution lane after gate cancelled it")
			}
			if _, err := reg.Get("A"); err != nil {
				t.Errorf("cancelled close retired A: %v", err)
			}
			for _, admission := range []control.Admission{gate.Admission, lane.Admission} {
				permit, rejection := admission.TryAcquire(context.Background())
				if rejection != nil {
					t.Errorf("cancelled close leaked permit: %+v", rejection)
				} else {
					permit.Release()
				}
			}
		})
	}
}
