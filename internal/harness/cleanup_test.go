package harness

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLaunchCleanupOnSpawnFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		dir  string
	}{
		{name: "empty command"},
		{name: "missing executable", argv: []string{"/nocx-missing-agent"}},
		{name: "start failure", argv: []string{"/bin/sh"}, dir: "/nocx-missing-directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			cleanupErr := errors.New("cleanup failure")
			c, err := spawn(ctx, Base{}, Launch{Argv: tc.argv, Dir: tc.dir, Cleanup: func() error { calls.Add(1); return cleanupErr }})
			if c != nil || err == nil {
				t.Fatalf("spawn = %v, %v", c, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("cleanup calls = %d, want 1", calls.Load())
			}
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("cleanup error missing: %v", err)
			}
		})
	}
}

func TestLaunchCleanupAfterProcessTeardown(t *testing.T) {
	for _, dispose := range []bool{false, true} {
		name := "normal exit"
		script := "exit 0"
		if dispose {
			name = "disposed"
			script = "exec cat"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			var c *child
			cleanupErr := errors.New("cleanup failure")
			launch := Launch{Argv: []string{"/bin/sh", "-c", script}, Cleanup: func() error {
				calls.Add(1)
				if c.cmd.ProcessState == nil {
					return errors.New("cleanup ran before Wait")
				}
				return cleanupErr
			}}
			var err error
			c, err = spawn(ctx, Base{}, launch)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("cleanup ran while child was owned")
			}
			if dispose {
				err = disposeChild(c)
			} else {
				select {
				case <-c.observed:
				case <-ctx.Done():
					c.kill()
					t.Fatal("child did not exit")
				}
				c.finish()
				err = <-c.gone
			}
			if calls.Load() != 1 {
				t.Fatalf("cleanup calls = %d, want 1", calls.Load())
			}
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("cleanup error missing: %v", err)
			}
		})
	}
}

type cleanupStartupProtocol struct {
	shellProtocol
	entered chan struct{}
	release chan struct{}
	cleaned chan struct{}
}

func (p cleanupStartupProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	close(p.entered)
	<-p.release
	return Launch{Argv: []string{"/bin/sh", "-c", "exec cat"}, Cleanup: func() error { close(p.cleaned); return nil }}, nil
}

func TestLaunchCleanupAfterCancelledStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := cleanupStartupProtocol{entered: make(chan struct{}), release: make(chan struct{}), cleaned: make(chan struct{})}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
	case <-ctx.Done():
		t.Fatal("Start not entered")
	}
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = turn.Wait(ctx)
	close(p.release)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case <-p.cleaned:
	case <-ctx.Done():
		t.Fatal("late launch resource was not cleaned")
	}
}
