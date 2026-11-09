package session

// THE THREE RACES THE MERGED FIX LEFT (nocx-ouru5.1.9, review of PR #764).
//
// Every one of them is about the interval between a session being made and its
// answer being written, and each is proved here against the service alone or
// against the state it keeps; the host's half of the second one is proved in
// internal/helper/host.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// TestBothAnswersWrittenToNobodyEndTheSessionOnce is the first finding: the
// answer that names a session and the answer a RETRY is given are two different
// answers, and ownership has to be decided over the set of them. A retry that
// takes the session before either answer is written must not outlive the
// attempt whose answer nobody read: if BOTH answers reach nobody, the session
// is nobody's and its whole reservation must go back.
func TestBothAnswersWrittenToNobodyEndTheSessionOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   string
		ssh  bool
	}{
		{name: "spawn", op: proto.OpSpawn},
		{name: "spawn-ssh", op: proto.OpSpawnSSH, ssh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawner := &idleSpawner{}
			svc := New(Options{Spawner: spawner, SSHSpawner: spawner})
			t.Cleanup(svc.Close)

			firstValue, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "shared")))
			if err != nil {
				t.Fatalf("the first spawn: %v", err)
			}
			first := firstValue.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own

			// THE RETRY, handed the same session under the same key.
			retryValue, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "shared")))
			if err != nil {
				t.Fatalf("the retry: %v", err)
			}
			retry := retryValue.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own
			if retry.Entry.Session != first.Entry.Session {
				t.Fatalf("the retry was given %s, want the session the claim names, %s",
					retry.Entry.Session.Session, first.Entry.Session.Session)
			}
			if forks := spawner.count(); forks != 1 {
				t.Fatalf("the retry forked a second shell (%d spawns); a resolved claim is an answer", forks)
			}

			// NEITHER CALLER READ ITS ANSWER. The retry's answer is written
			// first and reaches nobody — but the first attempt's answer is
			// still owed, and a caller that has not been told yet may still be
			// told.
			svc.answer(t, tc.op, retry, true)
			if entries := inventoryOf(t, svc); len(entries) != 1 {
				t.Fatalf("the session was ended while another answer naming it was still to be written: %s",
					inventoryStringOf(entries))
			}
			if used := svc.WindowBytesInUse(); used == 0 {
				t.Fatal("the session's reservation was released while another answer naming it was still owed")
			}

			// AND NOW THE FIRST ONE TOO. Nobody was ever told the id, so the
			// session is nobody's: it ends, and the key is free.
			svc.answer(t, tc.op, first, true)
			if used := svc.WindowBytesInUse(); used != 0 {
				t.Fatalf("both answers reached nobody and %d bytes are still reserved", used)
			}
			assertNoInventory(t, svc, "two answers written to nobody")

			next, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "shared")))
			if err != nil {
				t.Fatalf("the next spawn after the key was freed: %v", err)
			}
			if next.(proto.SpawnResult).Entry.Session == first.Entry.Session { //nolint:errcheck // the shape is the service's own
				t.Fatal("the key answered with the session both answers reached nobody about")
			}
			if forks := spawner.count(); forks != 2 {
				t.Fatalf("the next spawn forked %d shells in total, want 2 (the ended one and its replacement)", forks)
			}
		})
	}
}

// TestADeliveredRetryKeepsTheSessionEvenIfTheFirstAnswerWasLost is the
// companion: one answer that reached somebody is enough. The session is that
// caller's, and the attempt whose answer vanished may not end it.
func TestADeliveredRetryKeepsTheSessionEvenIfTheFirstAnswerWasLost(t *testing.T) {
	spawner := &idleSpawner{}
	svc := New(Options{Spawner: spawner, SSHSpawner: spawner})
	t.Cleanup(svc.Close)

	firstValue, err := svc.Call(context.Background(), proto.OpSpawn, mustJSONTest(t, spawnParams(false, "shared")))
	if err != nil {
		t.Fatalf("the first spawn: %v", err)
	}
	first := firstValue.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own
	retryValue, err := svc.Call(context.Background(), proto.OpSpawn, mustJSONTest(t, spawnParams(false, "shared")))
	if err != nil {
		t.Fatalf("the retry: %v", err)
	}
	retry := retryValue.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own

	// The retry's caller read its answer; the first attempt's did not.
	svc.answer(t, proto.OpSpawn, retry, false)
	before := svc.WindowBytesInUse()
	svc.answer(t, proto.OpSpawn, first, true)
	if after := svc.WindowBytesInUse(); after != before {
		t.Fatalf("a session a retry was told about was released: %d bytes before, %d after", before, after)
	}
	entries := inventoryOf(t, svc)
	if len(entries) != 1 || entries[0].Session != first.Entry.Session {
		t.Fatalf("the delivered session is gone: %s", inventoryStringOf(entries))
	}
}

// closingProcess is a shell whose Close blocks until the test allows it to
// finish. It is not a ProcessGroupSignaller, so the session owner closes it at
// once rather than waiting out a tail — which is what puts the test inside the
// interval a retry must not cross.
type closingProcess struct {
	*idleProcess
	gate chan struct{}
	// closing says the shell has been asked to close and is not gone yet;
	// finished says it is gone. The gap between them is the interval a retry
	// must not cross.
	closing  chan struct{}
	finished chan struct{}
	mu       sync.Mutex
	started  bool
}

func newClosingProcess() *closingProcess {
	return &closingProcess{
		idleProcess: newIdleProcess(),
		gate:        make(chan struct{}),
		closing:     make(chan struct{}),
		finished:    make(chan struct{}),
	}
}

func (p *closingProcess) Close() error {
	p.mu.Lock()
	started := p.started
	p.started = true
	p.mu.Unlock()
	if !started {
		close(p.closing)
		<-p.gate
		err := p.idleProcess.Close()
		close(p.finished)
		return err
	}
	return p.idleProcess.Close()
}

// closingSpawner hands out closingProcess shells for the first spawn and idle
// ones after it, and records for each fork whether the shell before it had
// finished closing.
type closingSpawner struct {
	mu       sync.Mutex
	spawns   int
	first    *closingProcess
	afterEnd []bool
	forks    chan struct{}
}

func newClosingSpawner() *closingSpawner { return &closingSpawner{forks: make(chan struct{}, 4)} }

func (s *closingSpawner) Spawn(req SpawnRequest) (Process, error) {
	s.mu.Lock()
	s.spawns++
	n := s.spawns
	s.mu.Unlock()
	if n > 1 {
		// Only a SECOND fork is the event under test: the first one is what
		// created the shell the retry must wait for.
		select {
		case s.forks <- struct{}{}:
		default:
		}
	}
	if n == 1 {
		s.first = newClosingProcess()
		return s.first, nil
	}
	// A SECOND fork is only legal once the first shell has finished closing.
	closed := false
	if s.first != nil {
		select {
		case <-s.first.finished:
			closed = true
		default:
		}
		s.mu.Lock()
		s.afterEnd = append(s.afterEnd, closed)
		s.mu.Unlock()
	}
	return newIdleProcess(), nil
}

func (s *closingSpawner) SpawnSSH(_ context.Context, req SSHSpawnRequest) (Process, error) {
	return s.Spawn(SpawnRequest{SessionID: req.SessionID})
}

// TestARetryWaitsForTheShellItReplacesToFinishClosing is the third finding. The
// claim must not be offered for reuse while the shell it named is still being
// closed: a retry that took it there would fork a second live shell beside one
// that has not gone yet, and two live shells for one key is exactly what the
// claim exists to prevent.
//
// THE SEQUENCE IS FORCED. The release is held inside the old shell's Close, the
// retry asks for the same key while it is held, and the second fork records
// whether the first shell had finished closing at the moment it happened. The
// bounded wait below is a SEQUENCE POINT — it gives the unfixed service the
// chance to show the interleaving at all — and the assertion is the recorded
// order, not a duration.
func TestARetryWaitsForTheShellItReplacesToFinishClosing(t *testing.T) {
	spawner := newClosingSpawner()
	svc := New(Options{Spawner: spawner, SSHSpawner: spawner})
	t.Cleanup(svc.Close)

	value, err := svc.Call(context.Background(), proto.OpSpawn, mustJSONTest(t, spawnParams(false, "gate")))
	if err != nil {
		t.Fatalf("the first spawn: %v", err)
	}
	first := value.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own

	// THE FIRST ANSWER REACHED NOBODY, and the release is now inside the old
	// shell's Close, deliberately held open.
	released := make(chan struct{})
	go func() {
		svc.answer(t, proto.OpSpawn, first, true)
		close(released)
	}()
	select {
	case <-spawner.first.closing:
	case <-time.After(30 * time.Second):
		t.Fatal("the old shell was never asked to close")
	}

	// A RETRY, asking for the same key while the shell is still closing.
	type answer struct {
		id  proto.HostSessionID
		err error
	}
	retried := make(chan answer, 1)
	go func() {
		got, cerr := svc.Call(context.Background(), proto.OpSpawn, mustJSONTest(t, spawnParams(false, "gate")))
		if cerr != nil {
			retried <- answer{err: cerr}
			return
		}
		retried <- answer{id: got.(proto.SpawnResult).Entry.Session} //nolint:errcheck // the shape is the service's own
	}()

	// The sequence point: without the fix the retry forks here, while the old
	// shell is held inside Close.
	select {
	case <-spawner.forks:
	case <-time.After(2 * time.Second):
	}

	// Let the old shell finish closing, and let the retry finish with it.
	close(spawner.first.gate)
	<-released
	got := <-retried
	if got.err != nil {
		t.Fatalf("the retry waiting on the closing key: %v", got.err)
	}
	if got.id == first.Entry.Session {
		t.Fatalf("the retry was answered with the session that was being closed, %s", got.id.Session)
	}
	spawner.mu.Lock()
	afterEnd := append([]bool(nil), spawner.afterEnd...)
	spawner.mu.Unlock()
	if len(afterEnd) == 0 {
		t.Fatal("the retry never forked a shell of its own")
	}
	for i, closed := range afterEnd {
		if !closed {
			t.Fatalf("fork %d happened while the shell it replaces was still closing: two live shells for one key", i+2)
		}
	}
	if left := inventoryOf(t, svc); len(left) != 1 || left[0].Session != got.id {
		t.Fatalf("after the retry, the helper holds %s; want only the retry's session %s",
			inventoryStringOf(left), got.id.Session)
	}
}
