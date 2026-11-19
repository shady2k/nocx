package hostconn

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdmitRefusalLeavesWarmSupervisorUntouched(t *testing.T) {
	s, l, link := closingStand(t)
	type contextKey struct{}
	original := context.WithValue(context.Background(), contextKey{}, "holder A")
	s.plan = Plan{Material: "A", ProbeInterval: time.Minute}
	s.planContext = original
	l.probeEvery.Store(int64(time.Minute))
	before := s.state
	refused := errors.New("caller refused")
	p := Plan{
		Material: "B", Holds: HoldsPlainShell, ProbeInterval: time.Second,
		Admit: func(got Link) error {
			if got != link {
				t.Fatal("admission inspected another transport")
			}
			return refused
		},
	}
	lease, err := s.Acquire(context.Background(), p)
	if lease != nil || !errors.Is(err, refused) {
		t.Fatalf("Acquire = %v, %v, want refusal", lease, err)
	}
	if s.plan.Material != "A" || s.planContext != original {
		t.Fatal("refused caller replaced the holder's reconnect plan")
	}
	if s.state != before || l.holders != 1 || l.plainShells != 0 || l.interval() != time.Minute || s.live != l || link.closeCount() != 0 {
		t.Fatalf("refusal changed supervisor: state %v holders %d shells %d cadence %s closes %d", s.state, l.holders, l.plainShells, l.interval(), link.closeCount())
	}
}

type admissionCarrier struct {
	link Link
	err  error
}

func (admissionCarrier) Name() string                                          { return "admission" }
func (admissionCarrier) Capabilities() Capabilities                            { return Capabilities{} }
func (c admissionCarrier) Dial(context.Context, Plan, EventSink) (Link, error) { return c.link, c.err }

func TestAdmitColdDialFailurePreservesHeldPlan(t *testing.T) {
	s, l, _ := closingStand(t)
	s.live = nil
	s.state = State{Kind: Reconnecting}
	type contextKey struct{}
	original := context.WithValue(context.Background(), contextKey{}, "A")
	s.plan, s.planContext = Plan{Material: "A"}, original
	refused := errors.New("handshake refused")
	s.carrier = admissionCarrier{err: refused}
	lease, err := s.Acquire(context.Background(), Plan{Material: "B"})
	if lease != nil || !errors.Is(err, refused) {
		t.Fatalf("Acquire = %v, %v", lease, err)
	}
	if s.plan.Material != "A" || s.planContext != original || s.live != nil || l.holders != 1 {
		t.Fatal("cold refused dial replaced the retained plan or holders")
	}
}

func TestAdmitFreshRefusalDoesNotIssueLease(t *testing.T) {
	s, _, _ := closingStand(t)
	s.live = nil
	link := &stillLink{}
	s.carrier = admissionCarrier{link: link}
	s.plan = Plan{Material: "A"}
	before := s.state
	refused := errors.New("caller refused")
	lease, err := s.Acquire(context.Background(), Plan{Material: "B", Admit: func(Link) error { return refused }})
	if lease != nil || !errors.Is(err, refused) {
		t.Fatalf("Acquire = %v, %v", lease, err)
	}
	if s.plan.Material != "A" || s.live != nil {
		t.Fatal("refused caller installed a live connection or reconnect plan")
	}
	if s.state != before {
		t.Fatalf("refusal changed state from %v to %v", before, s.state)
	}
	if link.closeCount() != 1 {
		t.Fatalf("unowned fresh link closes = %d, want 1", link.closeCount())
	}
}

func TestAdmitSuccessfulWarmLeaseOwnsReconnectPlan(t *testing.T) {
	s, l, link := closingStand(t)
	s.plan = Plan{Material: "A"}
	checked := false
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "B")
	lease, err := s.Acquire(ctx, Plan{Material: "B", Holds: HoldsPlainShell, ProbeInterval: time.Second, Admit: func(got Link) error {
		checked = got == link && l.holders == 1 && l.plainShells == 0 && s.plan.Material == "A"
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !checked || s.plan.Material != "B" || s.planContext == nil || l.holders != 2 || l.plainShells != 1 || l.interval() != time.Second {
		t.Fatal("successful admission did not precede holder accounting and plan ownership")
	}
	lease.Release()
}
