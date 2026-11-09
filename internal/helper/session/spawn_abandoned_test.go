package session

// A SPAWN WHOSE CALLER STOPPED WAITING ENDS ITS SESSION (nocx-ouru5.1.5).
//
// The helper is the only party that both makes a session and can tell that the
// answer carrying its id had nowhere to go. Everything else — the sweep, the
// coordinator's own rollback — needs the id that answer would have carried. So
// the rule is stated and proved here, against this service alone: a request
// context that has ended by the time the spawn returns means the caller
// withdrew (the renderer's rpc.cancel, a far route's answer bound, a transport
// the host abandoned), and the reservation goes back before this call answers.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// gatedProcess is a shell that stays alive at a prompt: nothing is read from it
// and nothing writes to it, which is exactly the session nobody can ever close.
type gatedProcess struct {
	reads  *io.PipeReader
	writes *io.PipeWriter
	done   chan struct{}
	once   sync.Once
}

func newGatedProcess() *gatedProcess {
	r, w := io.Pipe()
	return &gatedProcess{reads: r, writes: w, done: make(chan struct{})}
}

func (p *gatedProcess) Read(b []byte) (int, error)  { return p.reads.Read(b) }
func (p *gatedProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *gatedProcess) Close() error {
	p.once.Do(func() {
		close(p.done)
		_ = p.writes.Close()
		_ = p.reads.Close()
	})
	return nil
}

func (p *gatedProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (p *gatedProcess) Done() <-chan struct{}                { return p.done }
func (p *gatedProcess) WaitErr() (error, bool)               { return nil, false }
func (p *gatedProcess) Pid() int                             { return 7 }
func (p *gatedProcess) Shell() string                        { return "/bin/sh" }
func (p *gatedProcess) ForegroundProcessGroup() (int, error) { return 7, nil }

// gatedSpawner holds every spawn inside the service until the test releases it,
// after saying it got there. It is the slow shell a person gives up on.
type gatedSpawner struct {
	entered chan struct{}
	release chan struct{}

	mu     sync.Mutex
	spawns []string
}

func (s *gatedSpawner) Spawn(req SpawnRequest) (Process, error) {
	s.mu.Lock()
	s.spawns = append(s.spawns, req.SessionID)
	s.mu.Unlock()
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return newGatedProcess(), nil
}

func (s *gatedSpawner) SpawnSSH(_ context.Context, req SSHSpawnRequest) (Process, error) {
	s.mu.Lock()
	s.spawns = append(s.spawns, req.SessionID)
	s.mu.Unlock()
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return newGatedProcess(), nil
}

func (s *gatedSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.spawns)
}

// TestASpawnWhoseCallerWithdrewLeavesNoReservation is the acceptance: the
// session that would have held a window, a row buffer and a lifecycle window
// for the life of the daemon is ended at the moment the caller is known to be
// gone, and the key it was claimed under is free for the retry that a
// coordinator still wants.
func TestASpawnWhoseCallerWithdrewLeavesNoReservation(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   string
		ssh  bool
	}{
		{name: "spawn", op: proto.OpSpawn},
		{name: "spawn-ssh", op: proto.OpSpawnSSH, ssh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawner := &gatedSpawner{entered: make(chan struct{}, 1), release: make(chan struct{})}
			svc := New(Options{Spawner: spawner, SSHSpawner: spawner})
			t.Cleanup(svc.Close)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type answer struct {
				value any
				err   error
			}
			answered := make(chan answer, 1)
			go func() {
				params := any(proto.SpawnParams{IdempotencyKey: "withdrawn"})
				if tc.ssh {
					params = proto.SSHSpawnParams{
						IdempotencyKey: "withdrawn",
						Destination: proto.SSHDestination{
							Host: "example.com", Port: 22, User: "test",
							Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive},
						},
					}
				}
				value, err := svc.Call(ctx, tc.op, mustJSONTest(t, params))
				answered <- answer{value: value, err: err}
			}()

			select {
			case <-spawner.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the spawner was never asked for a shell")
			}
			// THE CALLER GIVES UP: the request context ends while the shell is
			// still being made. The fork is not recallable, so the session
			// exists on the other side of this release.
			cancel()
			close(spawner.release)

			got := <-answered
			if got.err == nil {
				t.Fatalf("a withdrawn spawn answered with %T, want the caller's own failure", got.value)
			}
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("a withdrawn spawn failed with %v, want the caller's cancellation reported", got.err)
			}
			if used := svc.WindowBytesInUse(); used != 0 {
				t.Fatalf("the withdrawn spawn kept %d bytes reserved, want its reservation back", used)
			}
			assertNoInventory(t, svc, "a withdrawn spawn")

			// AND THE CLAIM IS FREE. A coordinator whose connection was lost
			// mid-spawn asks again with the same idempotency key, and the
			// answer it deserves is a fresh shell — not the id of a session
			// that was just ended.
			spawner.mu.Lock()
			spawner.release = make(chan struct{})
			close(spawner.release)
			spawner.mu.Unlock()
			params := any(proto.SpawnParams{IdempotencyKey: "withdrawn"})
			if tc.ssh {
				params = proto.SSHSpawnParams{
					IdempotencyKey: "withdrawn",
					Destination: proto.SSHDestination{
						Host: "example.com", Port: 22, User: "test",
						Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive},
					},
				}
			}
			retried, err := svc.Call(context.Background(), tc.op, mustJSONTest(t, params))
			if err != nil {
				t.Fatalf("the retry of a withdrawn spawn after the claim was freed: %v", err)
			}
			entry, ok := retried.(proto.SpawnResult)
			if !ok || entry.Entry.Session.Session == "" {
				t.Fatalf("the retry answered %T, want a spawned session", retried)
			}
			if spawner.count() != 2 {
				t.Fatalf("the retry forked %d shells, want a second one: a withdrawn spawn's session must not be reused",
					spawner.count())
			}
			if used := svc.WindowBytesInUse(); used == 0 {
				t.Fatal("the retried spawn reserved nothing")
			}
		})
	}
}
