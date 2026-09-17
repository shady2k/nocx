package session_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// silentRawProcess is a local-PTY-shaped Process (it answers the owner's raw
// read seam) whose readiness wait ENDS when it is closed but whose drain
// never reports that close: RawReadUntilAgain answers "nothing right now"
// before and after alike. That is the window nocx-mrfe5 hung in on macOS,
// built deterministically instead of raced for — the readiness goroutine
// has returned and will signal nothing again, while the last drain the owner
// ran saw EAGAIN rather than the closed file.
//
// No SignalProcessGroup, on purpose: a Process that cannot be asked to end
// gets a stop deadline of "now" (owner.stop), so Close reaches the forced
// proc.Close at once and nothing here waits out a grace period.
type silentRawProcess struct {
	closeOnce sync.Once
	closed    chan struct{}
	waiting   chan struct{}
	waitOnce  sync.Once
}

func newSilentRawProcess() *silentRawProcess {
	return &silentRawProcess{closed: make(chan struct{}), waiting: make(chan struct{})}
}

func (p *silentRawProcess) WaitReadable(ctx context.Context) error {
	p.waitOnce.Do(func() { close(p.waiting) })
	select {
	case <-p.closed:
		return os.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *silentRawProcess) RawReadUntilAgain([]byte, func([]byte)) (bool, error) {
	return false, nil
}

func (p *silentRawProcess) Read([]byte) (int, error)    { <-p.closed; return 0, os.ErrClosed }
func (p *silentRawProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *silentRawProcess) Close() error {
	p.closeOnce.Do(func() { close(p.closed) })
	return nil
}

func (p *silentRawProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (p *silentRawProcess) Done() <-chan struct{}                { return p.closed }
func (p *silentRawProcess) WaitErr() (error, bool)               { return nil, false }
func (p *silentRawProcess) Pid() int                             { return 4242 }
func (p *silentRawProcess) Shell() string                        { return "/bin/silent" }
func (p *silentRawProcess) ForegroundProcessGroup() (int, error) { return 0, nil }

type silentRawSpawner struct {
	mu   sync.Mutex
	proc *silentRawProcess
}

func (s *silentRawSpawner) Spawn(session.SpawnRequest) (session.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proc = newSilentRawProcess()
	return s.proc, nil
}

// TestServiceCloseEndsWhenTheReadinessWaitEndsWithoutAnEOF is the helper half
// of nocx-mrfe5: Service.Close must not depend on a readiness signal that
// nothing will ever send. The owner used to learn that the read side was
// over only through a drain it ran on a readiness signal; a readiness
// goroutine that returned — because the pty was closed under it — sent
// nothing, so the owner sat in its select forever and Close with it.
//
// Completion is watched on Close's own return; the timer is a failure
// watchdog, not the passing condition.
func TestServiceCloseEndsWhenTheReadinessWaitEndsWithoutAnEOF(t *testing.T) {
	spawner := &silentRawSpawner{}
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    spawner,
		Log:        discardLog(),
	})
	release := bindTo(svc, newSink())
	defer release()

	call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24})
	spawner.mu.Lock()
	proc := spawner.proc
	spawner.mu.Unlock()
	<-proc.waiting

	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Service.Close never returned: the owner is still waiting on a readiness wait that has already ended")
	}
}
