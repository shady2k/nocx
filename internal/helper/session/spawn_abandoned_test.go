package session

// WHO MAY END A SPAWN'S SESSION, AND WHEN (nocx-ouru5.1.5).
//
// A spawn's session has exactly one name, and that name travels in the answer.
// Two facts decide who may end it afterwards, and this file proves both against
// the service alone:
//
//   - an answer written to NOBODY (the caller had withdrawn) leaves a session
//     nothing can ever name again, so the helper ends it — the reservation goes
//     back and the idempotency key is free for the retry that a coordinator
//     still wants;
//   - a session a RETRY was already handed is somebody's shell, so the spawn
//     that made it must not end it — even when its own answer reached nobody.
//
// The host decides the first fact (internal/helper/host's AnswerDelivery, at
// the instant the answer is written) and reports it here; the second is decided
// under this service's own mutex, in the same critical section that resolves
// and reads the claim.

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// idleProcess is a shell that stays alive at a prompt: nothing is read from it
// and nothing writes to it, which is exactly the session nobody can ever close.
type idleProcess struct {
	reads  *io.PipeReader
	writes *io.PipeWriter
	done   chan struct{}
	once   sync.Once
}

func newIdleProcess() *idleProcess {
	r, w := io.Pipe()
	return &idleProcess{reads: r, writes: w, done: make(chan struct{})}
}

func (p *idleProcess) Read(b []byte) (int, error)  { return p.reads.Read(b) }
func (p *idleProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *idleProcess) Close() error {
	p.once.Do(func() {
		close(p.done)
		_ = p.writes.Close()
		_ = p.reads.Close()
	})
	return nil
}

func (p *idleProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (p *idleProcess) Done() <-chan struct{}                { return p.done }
func (p *idleProcess) WaitErr() (error, bool)               { return nil, false }
func (p *idleProcess) Pid() int                             { return 7 }
func (p *idleProcess) Shell() string                        { return "/bin/sh" }
func (p *idleProcess) ForegroundProcessGroup() (int, error) { return 7, nil }

// idleSpawner answers every spawn with a prompt-sitting shell and counts them,
// so a test can say whether a retry forked or was handed the first one.
type idleSpawner struct {
	mu     sync.Mutex
	spawns []string
}

func (s *idleSpawner) Spawn(req SpawnRequest) (Process, error) {
	s.mu.Lock()
	s.spawns = append(s.spawns, req.SessionID)
	s.mu.Unlock()
	return newIdleProcess(), nil
}

func (s *idleSpawner) SpawnSSH(_ context.Context, req SSHSpawnRequest) (Process, error) {
	s.mu.Lock()
	s.spawns = append(s.spawns, req.SessionID)
	s.mu.Unlock()
	return newIdleProcess(), nil
}

func (s *idleSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.spawns)
}

// spawnParams is one spawn request of either op, under one idempotency key.
func spawnParams(ssh bool, key string) any {
	if !ssh {
		return proto.SpawnParams{IdempotencyKey: key}
	}
	return proto.SSHSpawnParams{
		IdempotencyKey: key,
		Destination: proto.SSHDestination{
			Host: "example.com", Port: 22, User: "test",
			Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive},
		},
	}
}

// answer ends a spawn the way the host ends one: the report of what became of
// the answer, and then the hook that carries it.
func (s *Service) answer(t *testing.T, op string, result any, withdrawn bool) {
	t.Helper()
	s.ResponseWritten(context.Background(), op, result, host.AnswerDelivery{Withdrawn: withdrawn})
}

// TestAnAnswerWrittenToNobodyEndsTheSpawnedSession is the withdrawal's own
// acceptance: the caller gave up before the frame went out, so the session that
// would have held a window, a row buffer and a lifecycle window for the life of
// the daemon is ended at that moment — and the key it was claimed under is free
// for the retry that a coordinator still wants.
func TestAnAnswerWrittenToNobodyEndsTheSpawnedSession(t *testing.T) {
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

			value, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "withdrawn")))
			if err != nil {
				t.Fatalf("spawn: %v", err)
			}
			spawned, ok := value.(proto.SpawnResult)
			if !ok || spawned.Entry.Session.Session == "" {
				t.Fatalf("spawn answered %T, want a spawned session", value)
			}
			if used := svc.WindowBytesInUse(); used == 0 {
				t.Fatalf("the spawn reserved nothing")
			}

			// THE HOST WRITES THE ANSWER TO NOBODY and says so.
			svc.answer(t, tc.op, spawned, true)

			if used := svc.WindowBytesInUse(); used != 0 {
				t.Fatalf("an answer written to nobody kept %d bytes reserved, want its reservation back", used)
			}
			assertNoInventory(t, svc, "an answer written to nobody")

			// AND THE CLAIM IS FREE: a coordinator whose connection was lost
			// mid-spawn asks again with the same idempotency key, and the answer
			// it deserves is a fresh shell — not the id of a session that was
			// just ended.
			retried, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "withdrawn")))
			if err != nil {
				t.Fatalf("the retry of a withdrawn spawn after the claim was freed: %v", err)
			}
			entry, ok := retried.(proto.SpawnResult)
			if !ok || entry.Entry.Session.Session == "" {
				t.Fatalf("the retry answered %T, want a spawned session", retried)
			}
			if entry.Entry.Session == spawned.Entry.Session {
				t.Fatalf("the retry was handed the ended session %s", spawned.Entry.Session.Session)
			}
			if spawner.count() != 2 {
				t.Fatalf("the retry forked %d shells, want a second one: a withdrawn spawn's session must not be reused",
					spawner.count())
			}
		})
	}
}

// TestAnAnswerAlreadyWrittenKeepsTheSession is the other side of the same
// decision: when the frame went out first, a caller may be holding the id, so
// the session stays exactly as it was.
func TestAnAnswerAlreadyWrittenKeepsTheSession(t *testing.T) {
	spawner := &idleSpawner{}
	svc := New(Options{Spawner: spawner, SSHSpawner: spawner})
	t.Cleanup(svc.Close)

	value, err := svc.Call(context.Background(), proto.OpSpawn, mustJSONTest(t, spawnParams(false, "delivered")))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	spawned := value.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own
	svc.answer(t, proto.OpSpawn, spawned, false)

	if used := svc.WindowBytesInUse(); used == 0 {
		t.Fatal("a delivered answer released the session it named")
	}
	if entries := inventoryOf(t, svc); len(entries) != 1 || entries[0].Session != spawned.Entry.Session {
		t.Fatalf("a delivered answer changed the inventory: %s", inventoryStringOf(entries))
	}
}

// TestASessionHandedToARetryIsNotEndedByItsOwnSpawn is the ownership rule
// (review round 2, refined by nocx-ouru5.1.9): a hand-over gives the retry an
// id but not yet a delivered answer, so when the first spawn's answer turns out
// to have reached nobody the session is not ended — the retry's own answer is
// still to be written and may be the one that reaches somebody (see
// spawn_races_test.go for both ends of that: delivered keeps it, lost ends it).
//
// The facts are decided under one mutex, which is what makes the order
// impossible to lose: either the retry's hand-over is counted first and the
// withdrawal finds an answer still owed, or the withdrawal is decided first and
// the claim is gone before the retry can read it.
func TestASessionHandedToARetryIsNotEndedByItsOwnSpawn(t *testing.T) {
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

			first, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "shared")))
			if err != nil {
				t.Fatalf("the first spawn: %v", err)
			}
			original := first.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own

			// THE RETRY, exactly as claimKey serves one that arrived while the
			// first was in flight: the claim is resolved, so it is handed the
			// existing session rather than forking.
			retried, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "shared")))
			if err != nil {
				t.Fatalf("the retry: %v", err)
			}
			again := retried.(proto.SpawnResult) //nolint:errcheck // the shape is the service's own
			if again.Entry.Session != original.Entry.Session {
				t.Fatalf("the retry was given %s, want the session the claim names, %s",
					again.Entry.Session.Session, original.Entry.Session.Session)
			}
			if spawner.count() != 1 {
				t.Fatalf("the retry forked a second shell (%d spawns); a resolved claim is an answer", spawner.count())
			}

			// Now the FIRST spawn's answer is found to have reached nobody. The
			// session is the retry's, so it must survive — with its whole
			// reservation.
			before := svc.WindowBytesInUse()
			svc.answer(t, tc.op, original, true)

			if after := svc.WindowBytesInUse(); after != before {
				t.Fatalf("the first spawn's withdrawal released a session a retry holds: %d bytes before, %d after", before, after)
			}
			entries := inventoryOf(t, svc)
			if len(entries) != 1 || entries[0].Session != original.Entry.Session {
				t.Fatalf("the session a retry was handed is gone: %s", inventoryStringOf(entries))
			}
			// And the claim still answers with it, so a third ask is still one
			// shell.
			third, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, spawnParams(tc.ssh, "shared")))
			if err != nil {
				t.Fatalf("the third ask: %v", err)
			}
			if third.(proto.SpawnResult).Entry.Session != original.Entry.Session { //nolint:errcheck // the shape is the service's own
				t.Fatal("the claim stopped naming the session a retry was handed")
			}
		})
	}
}

// inventoryOf reads what the service holds, typed.
func inventoryOf(t *testing.T, svc *Service) []proto.SessionEntry {
	t.Helper()
	value, err := svc.Call(context.Background(), proto.OpSessions, mustJSONTest(t, proto.SessionsParams{}))
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	result, ok := value.(proto.SessionsResult)
	if !ok {
		t.Fatalf("sessions answered %T, want proto.SessionsResult", value)
	}
	return result.Sessions
}

func inventoryStringOf(entries []proto.SessionEntry) string {
	out, err := json.Marshal(entries)
	if err != nil {
		return "<unprintable>"
	}
	return string(out)
}
