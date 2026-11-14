package harnesstest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
)

type testToolSession struct {
	ending      chan struct{}
	gate        chan struct{}
	once        sync.Once
	releaseOnce sync.Once
	pending     atomic.Bool
	retired     atomic.Bool
	returned    atomic.Bool
	onReturn    func()
}

func newTestToolSession(block bool) *testToolSession {
	s := &testToolSession{ending: make(chan struct{}), gate: make(chan struct{})}
	if !block {
		s.release()
	}
	return s
}

func (s *testToolSession) factory(context.Context, harness.SessionInfo) (harness.ToolSession, harness.ToolBridge, error) {
	return s, harness.ToolBridge{}, nil
}
func (s *testToolSession) BeginTurn(harness.TurnAuthority) error { return nil }
func (s *testToolSession) EndTurn(ctx context.Context) error {
	s.once.Do(func() { close(s.ending) })
	select {
	case <-s.gate:
		s.returned.Store(true)
		if s.onReturn != nil {
			s.onReturn()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *testToolSession) Pending() bool                { return s.pending.Load() }
func (s *testToolSession) Retire(context.Context) error { s.retired.Store(true); return nil }
func (s *testToolSession) release()                     { s.releaseOnce.Do(func() { close(s.gate) }) }

type transitions struct {
	mu      sync.Mutex
	events  []harness.Transition
	changed chan struct{}
}

func newTransitions() *transitions { return &transitions{changed: make(chan struct{})} }
func (tr *transitions) observe(ev harness.Transition) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.events = append(tr.events, ev)
	close(tr.changed)
	tr.changed = make(chan struct{})
}

func (tr *transitions) countLocked(kind string) int {
	count := 0
	for _, ev := range tr.events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

func (tr *transitions) wait(t *testing.T, kind string, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		tr.mu.Lock()
		n, changed := tr.countLocked(kind), tr.changed
		tr.mu.Unlock()
		if n >= count {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("waiting for %s occurrence %d", kind, count)
		}
	}
}

func (tr *transitions) assertCount(t *testing.T, kind string, want int) {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if n := tr.countLocked(kind); n != want {
		t.Fatalf("%s count %d, want %d; trace: %+v", kind, n, want, tr.events)
	}
}

func (tr *transitions) assertBefore(t *testing.T, first string, firstN int, second string, secondN int) {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	position := func(kind string, n int) int {
		for i, ev := range tr.events {
			if ev.Kind == kind {
				n--
				if n == 0 {
					return i
				}
			}
		}
		return -1
	}
	a, b := position(first, firstN), position(second, secondN)
	if a < 0 || b < 0 || a >= b {
		t.Fatalf("want %s[%d] before %s[%d]; trace: %+v", first, firstN, second, secondN, tr.events)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, reason string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("waiting for %s", reason)
	}
}

func assertSuccessfulEnd(t *testing.T, events []harness.Event) {
	t.Helper()
	if len(events) != 1 || events[0].Kind != harness.EventEnd {
		t.Fatalf("terminal events: %+v", events)
	}
}
