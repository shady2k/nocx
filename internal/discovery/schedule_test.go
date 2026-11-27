package discovery

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/portsprobe"
)

type testLocalProvider struct {
	samples atomic.Int32
}

func (p *testLocalProvider) Sample(context.Context) portsprobe.Sample {
	p.samples.Add(1)
	return portsprobe.Sample{State: portsprobe.StateAvailable}
}

func TestSchedulerLocalCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &testLocalProvider{}
		s := NewScheduler(nil, log.NewSlogAdapter(nil), func(log.Logger) LocalProvider { return p })
		t.Cleanup(func() { _ = s.Close() })
		if !s.Activate(LocalTargetID, "localhost") {
			t.Fatal("local pane could not activate port discovery")
		}
		if got := s.Status(LocalTargetID).Sample.State; got != portsprobe.StatePending {
			t.Fatalf("before the settle sample state = %q", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := p.samples.Load(); got != 1 {
			t.Fatalf("samples after settle = %d, want 1", got)
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if got := p.samples.Load(); got != 1 {
			t.Fatalf("hidden pane sampled again: %d samples", got)
		}

		s.SetVisible(LocalTargetID, true)
		synctest.Wait()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := p.samples.Load(); got != 3 {
			t.Fatalf("samples after opening the panel and one periodic tick = %d, want 3", got)
		}
		s.SetPaused(LocalTargetID, true)
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if got := p.samples.Load(); got != 3 {
			t.Fatalf("paused pane sampled again: %d samples", got)
		}
		s.SampleNow(t.Context(), LocalTargetID)
		if got := p.samples.Load(); got != 4 {
			t.Fatalf("samples after a manual refresh while paused = %d, want 4", got)
		}

		s.LocalClosed()
		if got := s.Status(LocalTargetID); got.Host != "" || got.Sample.State != portsprobe.StatePending {
			t.Fatalf("closed local target retained state: %+v", got)
		}
	})
}

type testAxis struct {
	status farhelper.Status
	known  bool
	watch  func(string, farhelper.Status)
}

func (a *testAxis) Axis(string) (farhelper.Status, bool) { return a.status, a.known }
func (a *testAxis) Watch(fn func(string, farhelper.Status)) func() {
	a.watch = fn
	return func() { a.watch = nil }
}

func (a *testAxis) Acquire(context.Context, string) (*farhelper.Lease, error) {
	return nil, &farhelper.Unavailable{Reason: farhelper.ReasonHelperLost}
}

func TestSchedulerTracksTheHelperAxis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		axis := &testAxis{known: true, status: farhelper.Status{State: farhelper.StateStarting}}
		s := NewScheduler(axis, log.NewSlogAdapter(nil), nil)
		defer func() { _ = s.Close() }()
		const id = "connection"
		if s.Activate(id, "example.test") {
			t.Fatal("starting helper accepted a ports target")
		}

		axis.status.State = farhelper.StateServing
		axis.watch(id, axis.status)
		if got := s.Status(id); got.Host != "" {
			t.Fatalf("watch created a target without a panel request: %+v", got)
		}
		if !s.Activate(id, "example.test") {
			t.Fatal("serving helper refused a ports target")
		}
		s.SampleNow(t.Context(), id)
		if got := s.Status(id).Sample.State; got != portsprobe.StatePending {
			t.Fatalf("unavailable lease replaced the pending result: %s", got)
		}

		axis.status.State = farhelper.StateUnavailable
		axis.watch(id, axis.status)
		if s.Activate(id, "example.test") {
			t.Fatal("unavailable helper accepted a ports target")
		}
		axis.known = false
		axis.watch(id, axis.status)
		if got := s.Status(id); got.Host != "" {
			t.Fatalf("removed connection retained its target: %+v", got)
		}
	})
}
