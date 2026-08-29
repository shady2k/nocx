package session

// Liveness (nocx-iarf9): what the backend currently believes about a session,
// as {liveness, livenessEpoch, observedAt} over alive | dead | unknown |
// interrupted.
//
// UNKNOWN IS THE POINT. A session on a host that has stopped answering is
// neither alive nor dead, and both of those renderings lie: "alive" invites
// the user to type into nothing, "dead" throws away work that is very likely
// still running. The tests below drive each transition and, above all, the one
// that had no way to be expressed before.
//
// The two halves of the vocabulary are reached differently, and that
// difference is the invariant: alive and unknown are ASSERTED by observers,
// dead and interrupted are DERIVED from the session's own end and may never be
// asserted by anyone.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

// livenessReg builds a registry whose ssh factory hands out a channel the test
// controls, so a remote session can be opened without a host.
func livenessReg(t *testing.T, ch *waitErrChannel) *Reg {
	t.Helper()
	r := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
	return r.WithSSHFactory(&livenessSSHFactory{ch: ch})
}

type livenessSSHFactory struct{ ch *waitErrChannel }

func (f *livenessSSHFactory) Connect(context.Context, string, ...ssh.ConnectOption) (ssh.Channel, error) {
	if f.ch != nil {
		return &livenessRemoteChannel{waitErrChannel: f.ch}, nil
	}
	return ssh.NewStubChannel(log.NewSlogAdapter(nil)), nil
}

// livenessRemoteChannel is the test's controllable channel wearing the one
// extra method ssh.Channel has over session.Channel, so a remote session can
// be opened over an end the test decides the timing of.
type livenessRemoteChannel struct {
	*waitErrChannel
}

func (c *livenessRemoteChannel) ShellIntegrationReason() ssh.RefusalReason {
	return ssh.ReasonNone
}

func openRemote(t *testing.T, r *Reg, host string) Session {
	t.Helper()
	s, err := r.Open(context.Background(), Config{
		Kind: KindRemote, Host: host, Cols: 80, Rows: 24,
		Remote: &ssh.ConnectConfig{User: "test"},
	})
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	return s
}

// A session that has just been opened is alive, and the projection is stamped:
// an epoch to order later observations against, and a time it was observed.
func TestLiveness_AFreshSessionIsAlive(t *testing.T) {
	r := newLineageReg(t)
	s := openRoot(t, r)

	st := s.Liveness()
	if st.Liveness != LivenessAlive {
		t.Errorf("liveness = %q, want %q", st.Liveness, LivenessAlive)
	}
	if st.Epoch == 0 {
		t.Error("livenessEpoch = 0: an unstamped projection cannot refuse a stale observation")
	}
	if st.ObservedAt.IsZero() {
		t.Error("observedAt is zero: a projection with no time cannot report its own freshness")
	}
}

// A shell that exited on its own is an AUTHORITATIVE TERMINAL EVENT, and that
// — and only that — is what dead means.
func TestLiveness_AuthoritativeExitIsDead(t *testing.T) {
	ch := &waitErrChannel{done: make(chan struct{})}
	s := sessionWithChannel(ch)

	ch.waitErr = nil
	ch.waitSet = true
	close(ch.done)

	if got := s.Liveness().Liveness; got != LivenessDead {
		t.Errorf("liveness = %q, want %q", got, LivenessDead)
	}
}

// A loss is not a death. The channel is gone and the backend cannot say how
// the session ended, so it says exactly that — the content ledger's word,
// reused rather than a second spelling invented (ledger.go:336).
func TestLiveness_LossIsInterruptedNotDead(t *testing.T) {
	ch := &waitErrChannel{done: make(chan struct{})}
	s := sessionWithChannel(ch)

	ch.waitErr = errors.New("ssh: connection lost")
	ch.waitSet = true
	close(ch.done)

	got := s.Liveness().Liveness
	if got == LivenessDead {
		t.Fatal("a loss reported as dead: the backend cannot know the session died, and saying so throws away work that may still be running")
	}
	if got != LivenessInterrupted {
		t.Errorf("liveness = %q, want %q", got, LivenessInterrupted)
	}
}

// A teardown that never let the process report is the same statement: no
// authoritative event, so no death.
func TestLiveness_UnrecordedTeardownIsInterrupted(t *testing.T) {
	ch := &waitErrChannel{done: make(chan struct{})}
	s := sessionWithChannel(ch)
	close(ch.done)

	if got := s.Liveness().Liveness; got != LivenessInterrupted {
		t.Errorf("liveness = %q, want %q", got, LivenessInterrupted)
	}
}

// THE CASE THE WHOLE STATE EXISTS FOR: the host stopped answering. Nothing has
// ended — the channel is open, no exit was reported — so the session is
// neither alive nor dead. It reads unknown, and it says when.
func TestLiveness_AHostThatStoppedRespondingIsUnknownNotDead(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")
	before := s.Liveness()

	r.observeHost("srv-01", ssh.Reachability{})

	st := s.Liveness()
	if st.Liveness == LivenessDead {
		t.Fatal("an unreachable host reported as dead")
	}
	if st.Liveness == LivenessAlive {
		t.Fatal("an unreachable host still reported as alive")
	}
	if st.Liveness != LivenessUnknown {
		t.Errorf("liveness = %q, want %q", st.Liveness, LivenessUnknown)
	}
	if st.Epoch <= before.Epoch {
		t.Errorf("livenessEpoch = %d, want greater than the previous %d", st.Epoch, before.Epoch)
	}
	select {
	case <-s.Done():
		t.Fatal("the session ended: this test proves nothing about a LIVE session on an unreachable host")
	default:
	}
}

// And back: a host that answers again returns the session to alive, at a fresh
// epoch. Unknown is a state we can leave, not a terminal one.
func TestLiveness_AHostThatAnswersAgainIsAliveAgain(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")

	r.observeHost("srv-01", ssh.Reachability{})
	unknownAt := s.Liveness()
	r.observeHost("srv-01", ssh.Reachability{Responsive: true})

	st := s.Liveness()
	if st.Liveness != LivenessAlive {
		t.Errorf("liveness = %q, want %q", st.Liveness, LivenessAlive)
	}
	if st.Epoch <= unknownAt.Epoch {
		t.Errorf("livenessEpoch = %d, want greater than %d", st.Epoch, unknownAt.Epoch)
	}
}

// A session on another host is untouched: an observation is about the host it
// names, and marking every session unknown because one machine went quiet
// would make the state worthless.
func TestLiveness_AnObservationDoesNotReachAnotherHost(t *testing.T) {
	r := livenessReg(t, nil)
	elsewhere := openRemote(t, r, "srv-02")
	local := openRoot(t, r)

	r.observeHost("srv-01", ssh.Reachability{})

	if got := elsewhere.Liveness().Liveness; got != LivenessAlive {
		t.Errorf("srv-02 session liveness = %q, want %q", got, LivenessAlive)
	}
	if got := local.Liveness().Liveness; got != LivenessAlive {
		t.Errorf("local session liveness = %q, want %q", got, LivenessAlive)
	}
}

// The epoch's whole job: a late observation carrying an older epoch does NOT
// overwrite a current record. Without this a delayed "alive" from before the
// host went quiet would revive a session nobody can reach.
func TestLiveness_ALateObservationDoesNotOverwriteACurrentRecord(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")
	r.observeHost("srv-01", ssh.Reachability{})
	current := s.Liveness()

	stale := Observation{Liveness: LivenessAlive, Epoch: current.Epoch - 1, ObservedAt: time.Now()}
	if r.Observe(refOf(s), stale) {
		t.Error("a stale observation was applied")
	}
	if got := s.Liveness(); got != current {
		t.Errorf("record = %+v after a stale observation, want %+v", got, current)
	}

	// An observation at the SAME epoch is not newer either: equal epochs
	// carry no ordering, and applying one would make the record depend on
	// arrival order, which is the thing the epoch replaces.
	same := Observation{Liveness: LivenessAlive, Epoch: current.Epoch, ObservedAt: time.Now()}
	if r.Observe(refOf(s), same) {
		t.Error("an observation at the current epoch was applied")
	}
}

// An observation is addressed to an INCARNATION, not to an id. One naming
// another instance, or another epoch of this id, describes a different session
// and must not touch this record — the same refusal the parent edge makes, by
// the same owner (SameIncarnation).
func TestLiveness_AnObservationForAnotherIncarnationIsRefused(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")
	current := s.Liveness()

	for name, ref := range map[string]Ref{
		"another instance": {ID: s.ID(), Identity: Identity{InstanceID: "ffffffffffffffffffffffffffffffff", Epoch: s.Identity().Epoch}},
		"another epoch":    {ID: s.ID(), Identity: Identity{InstanceID: s.Identity().InstanceID, Epoch: s.Identity().Epoch + 1}},
		"another session":  {ID: NewID(), Identity: s.Identity()},
	} {
		t.Run(name, func(t *testing.T) {
			obs := Observation{Liveness: LivenessUnknown, Epoch: current.Epoch + 10, ObservedAt: time.Now()}
			if r.Observe(ref, obs) {
				t.Error("an observation for a different incarnation was applied")
			}
			if got := s.Liveness(); got != current {
				t.Errorf("record = %+v, want %+v", got, current)
			}
		})
	}
}

// No observer may ASSERT an end. dead and interrupted are derived from the
// session's own terminal event and from nothing else — an observation claiming
// either is refused, whatever epoch it carries.
func TestLiveness_NoObservationMayAssertATerminalState(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")
	current := s.Liveness()

	for _, claim := range []Liveness{LivenessDead, LivenessInterrupted} {
		obs := Observation{Liveness: claim, Epoch: current.Epoch + 1, ObservedAt: time.Now()}
		if r.Observe(refOf(s), obs) {
			t.Errorf("an observation asserting %q was applied: only the session's own end may reach a terminal state", claim)
		}
		if got := s.Liveness().Liveness; got != current.Liveness {
			t.Errorf("liveness = %q after a %q claim, want %q", got, claim, current.Liveness)
		}
	}
}

// And a terminal state is final: once the session has ended, no observation
// revives it. The interval closes at the end of the session, not at the next
// thing anyone says about it.
func TestLiveness_ATerminalStateIsFinal(t *testing.T) {
	ch := &waitErrChannel{done: make(chan struct{})}
	r := livenessReg(t, ch)
	s := openRemote(t, r, "srv-01")

	ch.waitErr = errors.New("ssh: connection lost")
	ch.waitSet = true
	close(ch.done)
	terminal := s.Liveness()
	if terminal.Liveness != LivenessInterrupted {
		t.Fatalf("liveness = %q, want %q", terminal.Liveness, LivenessInterrupted)
	}

	obs := Observation{Liveness: LivenessAlive, Epoch: terminal.Epoch + 1, ObservedAt: time.Now()}
	if r.Observe(refOf(s), obs) {
		t.Error("an observation was applied to a session that had ended")
	}
	if got := s.Liveness(); got != terminal {
		t.Errorf("record = %+v, want the terminal %+v", got, terminal)
	}
}

// The projection tells its watcher when the VALUE changes, and only then: a
// keepalive that keeps succeeding must not publish a notification per tick.
func TestLiveness_TheObserverIsToldOnChangeAndNotOtherwise(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")

	var changes []Ref
	r.SetLivenessObserver(func(ref Ref) { changes = append(changes, ref) })

	r.observeHost("srv-01", ssh.Reachability{Responsive: true}) // already alive — no change
	if len(changes) != 0 {
		t.Errorf("%d notifications for an unchanged value, want 0", len(changes))
	}

	r.observeHost("srv-01", ssh.Reachability{})
	if len(changes) != 1 {
		t.Fatalf("%d notifications after alive→unknown, want 1", len(changes))
	}
	if changes[0].ID != s.ID() || changes[0].Identity != s.Identity() {
		t.Errorf("notified about %+v, want this session's own incarnation", changes[0])
	}

	r.observeHost("srv-01", ssh.Reachability{}) // still unknown — no second notification
	if len(changes) != 1 {
		t.Errorf("%d notifications for a repeated observation, want 1", len(changes))
	}
}

// A registry with no watcher is not a special case: the whole product runs
// this path before the composition root wires one, and an observation must not
// panic or be lost because nobody is listening.
func TestLiveness_ObservationsWorkWithNoObserverWired(t *testing.T) {
	r := livenessReg(t, nil)
	s := openRemote(t, r, "srv-01")

	r.observeHost("srv-01", ssh.Reachability{})

	if got := s.Liveness().Liveness; got != LivenessUnknown {
		t.Errorf("liveness = %q with no observer wired, want %q", got, LivenessUnknown)
	}
}
