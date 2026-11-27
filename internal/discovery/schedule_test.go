package discovery

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/client"
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
	status  farhelper.Status
	known   bool
	watch   func(string, farhelper.Status)
	acquire func(context.Context) (*farhelper.Lease, error)
}

func (a *testAxis) Axis(string) (farhelper.Status, bool) { return a.status, a.known }
func (a *testAxis) Watch(fn func(string, farhelper.Status)) func() {
	a.watch = fn
	return func() { a.watch = nil }
}

func (a *testAxis) Acquire(ctx context.Context, _ string) (*farhelper.Lease, error) {
	if a.acquire != nil {
		return a.acquire(ctx)
	}
	return nil, &farhelper.Unavailable{Reason: farhelper.ReasonHelperLost}
}

func TestSchedulerFarSampleErrors(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err            error
		leaveServing   bool
		state          portsprobe.State
		classification string
	}{
		{name: "connection lost", err: client.ErrLost, state: portsprobe.StateAvailable},
		{
			name: "deadline exceeded", err: context.DeadlineExceeded,
			state: portsprobe.StateFailedTransiently, classification: "probe timed out",
		},
		{
			name: "axis leaves serving during a sample", err: context.DeadlineExceeded,
			leaveServing: true, state: portsprobe.StateAvailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const id = "connection"
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				axis := &testAxis{
					known: true, status: farhelper.Status{State: farhelper.StateServing},
					acquire: func(context.Context) (*farhelper.Lease, error) {
						close(entered)
						<-release
						return nil, fmt.Errorf("far helper sample: %w", tc.err)
					},
				}
				s := NewScheduler(axis, log.NewSlogAdapter(nil), nil)
				defer func() { _ = s.Close() }()
				if !s.Activate(id, "example.test") {
					t.Fatal("serving helper refused a ports target")
				}
				s.SetPaused(id, true)
				lastGood := time.Now().Add(-time.Second)
				s.mu.Lock()
				s.targets[id].last = portsprobe.Sample{State: portsprobe.StateAvailable, Probe: "ss"}
				s.targets[id].lastGood = lastGood
				s.mu.Unlock()

				go func() {
					defer close(done)
					s.SampleNow(t.Context(), id)
				}()
				<-entered
				if tc.leaveServing {
					axis.status.State = farhelper.StateUnavailable
					axis.watch(id, axis.status)
				}
				close(release)
				<-done

				got := s.Status(id)
				if got.Sample.State != tc.state || got.Sample.Classification != tc.classification {
					t.Fatalf("sample = %+v, want %s (%q)", got.Sample, tc.state, tc.classification)
				}
				if tc.state == portsprobe.StateAvailable && got.Sample.Probe != "ss" {
					t.Fatalf("last good sample was replaced: %+v", got.Sample)
				}
				if !got.LastSampleAt.Equal(lastGood) {
					t.Fatalf("last good sample time = %v, want %v", got.LastSampleAt, lastGood)
				}
			})
		})
	}
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
