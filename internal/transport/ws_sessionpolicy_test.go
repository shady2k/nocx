package transport

import (
	"context"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

func TestSessionPolicyStore_ScopedToOneSession(t *testing.T) {
	s := newSessionPolicyStore()
	s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit)

	if got := s.For(session.ID("a"))[content.EffectObserve]; got != content.DecisionPermit {
		t.Fatalf("session a: got %q, want permit", got)
	}
	if _, ok := s.For(session.ID("b"))[content.EffectObserve]; ok {
		t.Fatal("session b saw session a's answer")
	}
}

func TestSessionPolicyStore_DropClearsTheSession(t *testing.T) {
	s := newSessionPolicyStore()
	s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit)
	s.Set(session.ID("a"), content.EffectMutateDestructive, content.DecisionRefuse)
	s.Drop(session.ID("a"))

	if got := s.For(session.ID("a")); len(got) != 0 {
		t.Fatalf("after drop: got %+v, want empty", got)
	}
}

// For hands out a COPY. The resolver reads the overlay with no lock held, so
// a map handed out under the read lock would be read after the lock is gone —
// and worse, a caller mutating it would edit the store from outside. Both are
// asserted here because "it returned a map with the right contents" is true of
// the aliasing version too.
func TestSessionPolicyStore_ForHandsOutACopy(t *testing.T) {
	s := newSessionPolicyStore()
	s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit)

	got := s.For(session.ID("a"))
	got[content.EffectMutateDestructive] = content.DecisionPermit
	delete(got, content.EffectObserve)

	live := s.For(session.ID("a"))
	if d := live[content.EffectObserve]; d != content.DecisionPermit {
		t.Fatalf("observe: got %q, want permit — the caller's edit reached the store", d)
	}
	if _, widened := live[content.EffectMutateDestructive]; widened {
		t.Fatal("mutate-destructive: the caller's insert reached the store")
	}
}

// An unknown session is an empty overlay, never nil: the resolver ranges over
// what it is given, and "absent" must read as "no override", never as a
// permit. Fail toward asking (the plan's global constraint).
func TestSessionPolicyStore_UnknownSessionIsEmptyNotNil(t *testing.T) {
	s := newSessionPolicyStore()

	got := s.For(session.ID("never-seen"))
	if got == nil {
		t.Fatal("got nil, want an empty overlay")
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestSessionPolicyStore_ConcurrentUse(t *testing.T) {
	s := newSessionPolicyStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit) }()
		go func() { defer wg.Done(); _ = s.For(session.ID("a")) }()
		go func() { defer wg.Done(); s.Drop(session.ID("a")) }()
	}
	wg.Wait()
}

// --- the teardown paths ---------------------------------------------------
//
// A session-scoped permission's whole promise is that it does not outlive its
// session, and the promise is kept by a call at EVERY teardown path. There are
// three in ws.go, the three that call gitSessionClosed: Stop (which passes a
// nil conn, because at shutdown nobody is attached), monitorExit (the shell
// ended on its own) and closeSession (the tab was closed by hand). A store
// dropped at one of them leaks the permission on the other two, and nothing
// would report it.
//
// One shared helper drives all three, rather than three hand-written tests: a
// path that clears the overlay by luck rather than by a call still passes a
// test written around that path, and differs under a shared one.

// serverWithLiveSession stands up a server with one open session and the
// receiver every teardown owner claims. The pty is the exit-test fake, so the
// session's Done() closes when the test says so and no process is spawned.
func serverWithLiveSession(t *testing.T) (*WSServer, session.Session, *exitFakePTY) {
	t.Helper()
	fake := newExitFakePTY()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, &exitFakePTYFactory{fake: fake})
	s := NewWSServer(logger, reg)

	sess, err := reg.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	// The receiver is what removeRx claims; both of the two teardown owners
	// go through it, so a session without one is not the production shape.
	s.getOrCreateRx(sess.ID())
	return s, sess, fake
}

// assertDroppedBy records an override for a live session, runs one teardown
// path, and fails unless the overlay is gone. The three paths are compared
// like for like: same store, same precondition, same assertion.
func assertDroppedBy(t *testing.T, teardown func(s *WSServer, sess session.Session, fake *exitFakePTY)) {
	t.Helper()
	s, sess, fake := serverWithLiveSession(t)
	sid := sess.ID()

	s.sessionPolicy.Set(sid, content.EffectObserve, content.DecisionPermit)
	if got := s.sessionPolicy.For(sid)[content.EffectObserve]; got != content.DecisionPermit {
		t.Fatalf("precondition: got %q, want permit before teardown", got)
	}

	teardown(s, sess, fake)

	if got := s.sessionPolicy.For(sid); len(got) != 0 {
		t.Fatalf("after teardown: got %+v, want empty — this path leaks the permission past its session", got)
	}
}

// ws.go, Stop: every session is closed at shutdown, with a nil conn because
// nobody is attached. The drop must not depend on there being a connection.
func TestSessionPolicy_DroppedByStop(t *testing.T) {
	assertDroppedBy(t, func(s *WSServer, _ session.Session, _ *exitFakePTY) {
		if err := s.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})
}

// ws.go, monitorExit: the shell exited on its own — the ordinary way a session
// ends, and the path that used to carry none of the teardown responsibilities.
func TestSessionPolicy_DroppedByMonitorExit(t *testing.T) {
	assertDroppedBy(t, func(s *WSServer, sess session.Session, fake *exitFakePTY) {
		rx := s.getRx(sess.ID())
		if rx == nil {
			t.Fatal("no receiver to tear down")
		}
		fake.recordWait(nil) // the channel dies; monitorExit's <-Done() returns
		s.monitorExit(rx, sess)
	})
}

// ws.go, closeSession: the tab was closed by hand.
func TestSessionPolicy_DroppedByCloseSession(t *testing.T) {
	assertDroppedBy(t, func(s *WSServer, sess session.Session, _ *exitFakePTY) {
		s.closeSession(sess.ID(), sess)
	})
}

// The spec's claim, asserted rather than assumed: a question still on screen
// when the session ends leaves no overlay behind. The pending approval is the
// state the ask path is in when the user closes the tab without answering —
// the store must not be holding anything for that session afterwards, so a
// later session cannot inherit it.
func TestSessionPolicy_PendingAskDiesWithItsSession(t *testing.T) {
	s, sess, _ := serverWithLiveSession(t)
	sid := sess.ID()

	s.sessionPolicy.Set(sid, content.EffectObserve, content.DecisionPermit)
	s.closeSession(sid, sess)

	if got := s.sessionPolicy.For(sid); len(got) != 0 {
		t.Fatalf("after close: got %+v, want empty", got)
	}
	// And the run grant minted for that id afterwards carries no trace of the
	// answer: the overlay is what "this session" meant, and it is gone.
	if got := s.sessionPolicy.For(sid)[content.EffectObserve]; got != "" {
		t.Fatalf("observe: got %q, want no override", got)
	}
}

// --- the resolver reads it ------------------------------------------------

// runGrantFor mints one ask run's authority, and the session's own answers are
// the third layer content.ResolvePolicy resolves (spec, "The session layer").
// Before this it passed nil, so "allow in this session" was a fact nothing
// read. The run grant's base scope is already the run's own session, so the
// overlay needs no scope of its own — it only moves the row's decision.
func TestRunGrantFor_AppliesTheSessionsOverlay(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	s := NewWSServer(logger, newRegWithStub(logger), WithAgentPolicy(askObserveStore(t)))

	const answered, other = "session-a", "session-b"

	// The global policy asks about observe, and that is what an unanswered
	// session gets.
	if d := s.runGrantFor(answered).Policy.DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("before the answer: got %q, want ask", d)
	}

	s.sessionPolicy.Set(session.ID(answered), content.EffectObserve, content.DecisionPermit)

	g := s.runGrantFor(answered)
	if d := g.Policy.DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("after the answer: got %q, want permit — the overlay is not being resolved", d)
	}
	// The overlay decides a row; it never re-scopes one. The run's own
	// session is still the base scope of every row.
	if !hasSessionScope(g.Scopes, answered) {
		t.Fatalf("scopes %+v: want the run's own session as a base scope", g.Scopes)
	}
	// And the answer reaches exactly one session.
	if d := s.runGrantFor(other).Policy.DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("another session: got %q, want ask — the overlay escaped its session", d)
	}
}

// An unwired policy still mints no grant: the overlay is a layer on top of a
// policy, never a policy of its own. Without this an "allow in this session"
// could be the whole authority a run carries.
func TestRunGrantFor_NoPolicyIsStillNoGrant(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	s := NewWSServer(logger, newRegWithStub(logger)) // no WithAgentPolicy

	s.sessionPolicy.Set(session.ID("session-a"), content.EffectObserve, content.DecisionPermit)

	if g := s.runGrantFor("session-a"); g != nil {
		t.Fatalf("got %+v, want no grant when no policy is wired", g)
	}
}

func hasSessionScope(scopes []content.GrantScope, sid string) bool {
	for _, sc := range scopes {
		if sc.Kind == content.ResourceSession && sc.ID == sid {
			return true
		}
	}
	return false
}
