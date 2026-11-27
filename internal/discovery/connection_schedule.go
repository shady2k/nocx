package discovery

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/portsprobe"
)

const (
	settleDelay    = time.Second
	sampleInterval = 10 * time.Second
)

// ConnectionRegistry owns the helper axes and their client lifetimes.
type ConnectionRegistry interface {
	Acquire(context.Context, string) (*farhelper.Lease, error)
	Axis(string) (farhelper.Status, bool)
	Watch(func(string, farhelper.Status)) func()
}

// LocalProvider reads the listeners of the machine the coordinator runs on.
type LocalProvider interface {
	Sample(context.Context) portsprobe.Sample
}

// ConnectionScheduler owns the sampling cadence for each far-helper
// connection and for the local machine.
type ConnectionScheduler struct {
	reg      ConnectionRegistry
	newLocal func(log.Logger) LocalProvider
	logger   log.Logger
	unwatch  func()

	mu      sync.Mutex
	targets map[string]*connectionTarget
	closed  bool
}

type connectionTarget struct {
	host  string
	local LocalProvider

	last     portsprobe.Sample
	lastGood time.Time
	paused   bool
	visible  bool

	// A run lasts from activation or a return to serving until the axis
	// leaves serving. A cancelled run cannot publish a late sample.
	run    context.Context
	cancel context.CancelFunc

	trigger chan context.Context // coalesces automatic samples
	done    chan struct{}
	slot    chan struct{} // shared by automatic and manual samples
	settle  *time.Timer
	period  *time.Timer
}

func NewConnectionScheduler(reg ConnectionRegistry, logger log.Logger, newLocal func(log.Logger) LocalProvider) *ConnectionScheduler {
	s := &ConnectionScheduler{
		reg: reg, logger: logger, newLocal: newLocal,
		targets: make(map[string]*connectionTarget),
	}
	if reg != nil {
		s.unwatch = reg.Watch(s.axisChanged)
	}
	return s
}

// Activate creates a target on first use. A remote target can be used only
// while that connection's helper serves panels.
func (s *ConnectionScheduler) Activate(id, host string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if id != LocalTargetID {
		if s.reg == nil {
			return false
		}
		axis, known := s.reg.Axis(id)
		if !known || axis.State != farhelper.StateServing {
			return false
		}
	}
	if t := s.targets[id]; t != nil {
		s.startLocked(t)
		return true
	}

	t := &connectionTarget{
		host: host, trigger: make(chan context.Context, 1),
		done: make(chan struct{}), slot: make(chan struct{}, 1),
	}
	if id == LocalTargetID && s.newLocal != nil {
		t.local = s.newLocal(s.logger)
	}
	s.targets[id] = t
	s.startLocked(t)
	go s.loop(id, t)
	return true
}

func (s *ConnectionScheduler) axisChanged(id string, axis farhelper.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.targets[id]
	if t == nil {
		return
	}
	_, known := s.reg.Axis(id)
	switch {
	case !known:
		s.removeLocked(id)
	case axis.State != farhelper.StateServing:
		t.stopRun()
	default:
		s.startLocked(t)
	}
}

func (s *ConnectionScheduler) startLocked(t *connectionTarget) {
	if t.run != nil {
		return
	}
	t.run, t.cancel = context.WithCancel(context.Background())
	t.last = portsprobe.Sample{State: portsprobe.StatePending}
	t.lastGood = time.Time{}
	if !t.paused {
		var timer *time.Timer
		timer = time.AfterFunc(settleDelay, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			// A stale callback cannot touch a replacement timer.
			if t.settle != timer {
				return
			}
			t.settle = nil
			t.nudge()
			s.armPeriodicLocked(t)
		})
		t.settle = timer
	}
}

// LocalClosed drops the local target when its last session closes. The
// registry tells us when to drop remote targets.
func (s *ConnectionScheduler) LocalClosed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(LocalTargetID)
}

func (s *ConnectionScheduler) removeLocked(id string) {
	if t := s.targets[id]; t != nil {
		delete(s.targets, id)
		t.stopRun()
		close(t.done)
	}
}

func (t *connectionTarget) stopRun() {
	t.stopTimers()
	if t.run != nil {
		t.cancel()
		t.run = nil
	}
}

// SetVisible controls periodic sampling for the visible Ports panel.
func (s *ConnectionScheduler) SetVisible(id string, visible bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.targets[id]; t != nil {
		t.visible = visible
		switch {
		case !visible:
			t.stopPeriodic()
		case t.run != nil && !t.paused && t.settle == nil:
			t.nudge()
			s.armPeriodicLocked(t)
		}
	}
}

// SetPaused suppresses automatic sampling until the user resumes it.
func (s *ConnectionScheduler) SetPaused(id string, paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.targets[id]; t != nil {
		t.paused = paused
		switch {
		case paused:
			t.stopTimers()
		case t.run != nil && t.settle == nil:
			t.nudge()
			s.armPeriodicLocked(t)
		}
	}
}

// SampleNow refreshes the target immediately, including while paused.
func (s *ConnectionScheduler) SampleNow(ctx context.Context, id string) {
	s.mu.Lock()
	t := s.targets[id]
	var run context.Context
	if t != nil {
		run = t.run
	}
	s.mu.Unlock()
	if run != nil {
		s.sample(ctx, id, t, run, false)
	}
}

type ConnectionTargetStatus struct {
	Host         string
	Sample       portsprobe.Sample
	Paused       bool
	Visible      bool
	LastSampleAt time.Time
}

func (s *ConnectionScheduler) Status(id string) ConnectionTargetStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.targets[id]; t != nil {
		return ConnectionTargetStatus{
			Host: t.host, Sample: t.last, Paused: t.paused,
			Visible: t.visible, LastSampleAt: t.lastGood,
		}
	}
	return ConnectionTargetStatus{Sample: portsprobe.Sample{State: portsprobe.StatePending}}
}

func (s *ConnectionScheduler) Close() error {
	if s.unwatch != nil {
		s.unwatch()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for id := range s.targets {
		s.removeLocked(id)
	}
	return nil
}

func (s *ConnectionScheduler) loop(id string, t *connectionTarget) {
	for {
		select {
		case <-t.done:
			return
		case run := <-t.trigger:
			s.sample(run, id, t, run, true)
		}
	}
}

// nudge is called under the scheduler lock. Replace a queued nudge so that
// the most recent serving generation owns the next sample.
func (t *connectionTarget) nudge() {
	select {
	case <-t.trigger:
	default:
	}
	t.trigger <- t.run
}

func (s *ConnectionScheduler) sample(parent context.Context, id string, t *connectionTarget, run context.Context, automatic bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(run, cancel)
	defer stop()

	select {
	case t.slot <- struct{}{}:
		defer func() { <-t.slot }()
	case <-ctx.Done():
		return
	}
	s.mu.Lock()
	active := t.run == run && (!automatic || !t.paused)
	s.mu.Unlock()
	if !active || ctx.Err() != nil {
		return
	}

	var result portsprobe.Sample
	var leaseCtx context.Context
	switch {
	case id != LocalTargetID:
		result, leaseCtx = sampleFarHelper(ctx, s.reg, id)
	case t.local != nil:
		result = t.local.Sample(ctx)
	default:
		result = portsprobe.Sample{
			State:          portsprobe.StateFailedTransiently,
			Classification: "local port discovery is not available",
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if t.run != run || ctx.Err() != nil || result.Canceled || (leaseCtx != nil && leaseCtx.Err() != nil) {
		return
	}
	t.last = result
	if result.State == portsprobe.StateAvailable || result.State == portsprobe.StateAvailableLimited {
		t.lastGood = time.Now()
	}
}

func (s *ConnectionScheduler) armPeriodicLocked(t *connectionTarget) {
	if !t.visible || t.period != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(sampleInterval, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if t.period != timer {
			return
		}
		t.period = nil
		t.nudge()
		s.armPeriodicLocked(t)
	})
	t.period = timer
}

func (t *connectionTarget) stopTimers() {
	if t.settle != nil {
		t.settle.Stop()
		t.settle = nil
	}
	t.stopPeriodic()
}

func (t *connectionTarget) stopPeriodic() {
	if t.period != nil {
		t.period.Stop()
		t.period = nil
	}
}
