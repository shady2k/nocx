package discovery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// Scheduler owns the discovery cadence (spec §4) for one or more
// authenticated targets. The Detector answers "what does the remote listen
// on"; the Scheduler answers "when may I ask" — and background polls become
// bugs exactly here, so each timer exists for a stated reason:
//
//   - Settle sample: one sample after a connection comes up, NOT on the
//     first prompt's critical path. Services take a moment to bind; a panel
//     that samples instantly shows an empty host.
//   - Prompt debounce: a completed command is a HINT that the listener set
//     most likely changed. Debounced (spec: 750–1500 ms) and coalesced per
//     target, so a user hammering Enter never queues probes.
//   - Hidden tab pause: periodic sampling runs only while a watcher (the
//     ports panel) is visible AND the user has not paused it. A background
//     tab running `ss` on a loop against a production host is a defect, not
//     a feature.
//   - One in flight: the Detector's semaphore already enforces this; the
//     per-target trigger channel coalesces timers onto the single sampler
//     loop instead of building a second scheduler that defeats it.
//
// Targets are keyed by profile id — the authenticated target, the unit the
// spec's consent model names — plus the reserved LocalTargetID for the
// machine the app runs on. Two tabs sharing a profile coalesce onto one
// detector (one lease, one probe selection, one sample in flight); every
// local tab coalesces onto the single local target. A target is created on
// ConnectionUp and forgotten on ConnectionDown (the last tab closed): the
// lease is released, so no poll outlives its consumer, and no pooled SSH
// connection is held open for a profile nobody is using.
//
// Connection loss: the lease's Done channel closes, the target is marked
// conn-lost, and no further execs are attempted. ConnectionUp after a loss
// starts a FRESH detector — probe selection is once per connection, and a
// result from the old connection never applies after a reconnect.
type Scheduler struct {
	connector Connector
	// localProvider builds the local-machine sampling provider for
	// LocalTargetID — one provider per target lifecycle. Wired at the
	// composition root (AD-8) via WithLocalProvider; nil disables local
	// discovery.
	localProvider func(log.Logger) Provider
	logger        log.Logger

	settleDelay    time.Duration
	promptDebounce time.Duration
	sampleInterval time.Duration
	sampleTimeout  time.Duration

	mu      sync.Mutex
	targets map[string]*schedTarget
	closed  bool
}

// SchedulerOption configures a Scheduler. Cadence defaults follow spec §4:
// 1 s settle, 1 s prompt debounce, 10 s periodic.
type SchedulerOption func(*Scheduler)

// WithSettleDelay sets the delay between ConnectionUp and the first sample.
func WithSettleDelay(d time.Duration) SchedulerOption {
	return func(s *Scheduler) { s.settleDelay = d }
}

// WithPromptDebounce sets the debounce for prompt hints.
func WithPromptDebounce(d time.Duration) SchedulerOption {
	return func(s *Scheduler) { s.promptDebounce = d }
}

// WithSampleInterval sets the periodic sampling interval while a watcher is
// visible and nothing is paused. Zero disables periodic sampling.
func WithSampleInterval(d time.Duration) SchedulerOption {
	return func(s *Scheduler) { s.sampleInterval = d }
}

// acquireTimeout bounds the lease acquisition (a dial can hang on a wedged
// network). The sample itself is bounded by the detector's own hard timeout,
// so both halves of a synchronous SampleNow are bounded end to end.
const acquireTimeout = 15 * time.Second

// NewScheduler creates a scheduler over the given connector. The caller owns
// the lifetime: release everything with Close.
func NewScheduler(conn Connector, logger log.Logger, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		connector:      conn,
		logger:         logger,
		settleDelay:    1 * time.Second,
		promptDebounce: time.Second,
		sampleInterval: 10 * time.Second,
		sampleTimeout:  10 * time.Second,
		targets:        make(map[string]*schedTarget),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// schedTarget is the per-profile cadence state. All fields are guarded by
// Scheduler.mu except trigger/done, which are written before the loop starts
// and only read afterwards.
type schedTarget struct {
	s         *Scheduler
	profileID string
	host      string
	opts      []ssh.ConnectOption

	provider Provider
	conn     Lease
	connDead bool

	paused  bool // user Pause: suppresses every automatic sample
	visible bool // a watcher (the ports panel) is present and visible
	torn    bool // target torn down; timers must not re-arm

	// Singleflight for lease acquisition: the sampler loop and a manual
	// SampleNow (an RPC goroutine) can both want a detector at once, and
	// two leases on one target would leak one of them.
	acquiring bool
	acquired  chan struct{}

	last     Sample
	lastGood time.Time // wall time of the last successful sample; zero before any

	trigger chan struct{} // capacity 1: coalesces timer nudges onto the loop
	done    chan struct{} // closed on teardown; the loop exits

	settleT *time.Timer
	promptT *time.Timer
	periodT *time.Timer
}

// ConnectionUp is called when a session on the target opens (an SSH session
// on a profile, or a local tab for LocalTargetID). It creates the target
// and schedules the settle sample. On a reconnect (the previous connection
// died) it resets the stale result — a result from the old connection never
// applies after reconnect (spec §4).
func (s *Scheduler) ConnectionUp(profileID, host string, opts ...ssh.ConnectOption) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	t := s.targets[profileID]
	if t == nil {
		t = &schedTarget{
			s:         s,
			profileID: profileID,
			host:      host,
			opts:      opts,
			trigger:   make(chan struct{}, 1),
			done:      make(chan struct{}),
			// The window between a connection coming up and its first sample
			// landing is a real state a user looks at, and the zero Sample
			// reports State "" — which is in no enum, satisfies no arm of the
			// renderer's switch, and rendered as a section with a heading and
			// nothing under it (owner, 2026-08-04). Status() already refused
			// to return the zero string for an ABSENT target; the target that
			// exists and has not yet sampled is the same fact and was missed.
			last: Sample{State: StatePending},
		}
		s.targets[profileID] = t
		go t.loop()
	} else {
		if t.connDead {
			// Fresh detector on the next sample: probe selection is once per
			// connection, and the old lease is gone.
			t.connDead = false
			t.provider = nil
			t.conn = nil
			t.last = Sample{State: StatePending}
			t.lastGood = time.Time{}
		}
		if t.host == "" {
			t.host = host
		}
		if len(opts) > 0 {
			t.opts = opts
		}
	}
	paused := t.paused
	s.mu.Unlock()

	if paused {
		return
	}
	t.armSettle()
}

// ConnectionDown is called when the last session on the profile closed. The
// target is forgotten and its lease released — no background poll outlives
// its consumer, and no pooled connection is held for a profile nobody uses.
func (s *Scheduler) ConnectionDown(profileID string) {
	s.mu.Lock()
	t := s.targets[profileID]
	delete(s.targets, profileID)
	if t != nil {
		t.torn = true
		t.stopTimersLocked()
		close(t.done)
	}
	s.mu.Unlock()
	if t != nil && t.provider != nil {
		_ = t.provider.Close()
	}
}

// PromptHint reports a completed command on the target — the listener set
// most likely changed. Debounced, never queued.
func (s *Scheduler) PromptHint(profileID string) {
	s.mu.Lock()
	t := s.targets[profileID]
	paused := t != nil && t.paused
	s.mu.Unlock()
	if t == nil || paused {
		return
	}
	t.armPrompt()
}

// SetVisible is the watcher (ports panel) visibility signal. Periodic
// sampling runs only while visible; hiding the panel stops the background
// poll. Becoming visible refreshes promptly.
func (s *Scheduler) SetVisible(profileID string, visible bool) {
	s.mu.Lock()
	t := s.targets[profileID]
	if t != nil {
		t.visible = visible
		switch {
		case !visible:
			t.stopPeriodicLocked()
		case t.paused || t.connDead || t.torn:
			// no automatic sampling while paused or dead
		default:
			s.armPeriodicLocked(t)
			t.nudge()
		}
	}
	s.mu.Unlock()
}

// SetPaused is the user's Pause/Resume control. Paused suppresses every
// automatic sample (settle, prompt, periodic); SampleNow still works — it is
// the Retry path. Resuming refreshes promptly.
func (s *Scheduler) SetPaused(profileID string, paused bool) {
	s.mu.Lock()
	t := s.targets[profileID]
	if t != nil {
		t.paused = paused
		switch {
		case paused:
			t.stopTimersLocked()
		case t.connDead || t.torn:
			// nothing to sample on a dead connection
		default:
			t.nudge()
			if t.visible {
				s.armPeriodicLocked(t)
			}
		}
	}
	s.mu.Unlock()
}

// SampleNow runs one sample immediately, clearing a terminal refusal first —
// the panel's Retry (spec §4: retry is the only way past a refusal). It is
// synchronous and returns the fresh sample, so the ports.sample RPC answers
// with the retried state, not the pre-retry one. Manual: Pause does not
// suppress it. A nil detector on a dead connection returns the last state.
func (s *Scheduler) SampleNow(profileID string) Sample {
	s.mu.Lock()
	t := s.targets[profileID]
	if t == nil || s.closed || t.torn || t.connDead {
		s.mu.Unlock()
		if t == nil {
			return Sample{State: StatePending}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return t.last
	}
	if t.provider != nil {
		t.provider.Retry()
	}
	s.mu.Unlock()

	p := s.acquireProvider(t)
	if p == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return t.last
	}
	smp := p.Sample(context.Background())
	s.mu.Lock()
	t.last = smp
	if smp.State == StateAvailable || smp.State == StateAvailableLimited {
		t.lastGood = time.Now()
	}
	s.mu.Unlock()
	return smp
}

// TargetStatus is the read path the ports RPC renders: the last sample, the
// cadence flags, and whether the underlying connection died.
type TargetStatus struct {
	ProfileID string
	Host      string
	Sample    Sample
	Paused    bool
	Visible   bool
	ConnLost  bool
	// LastSampleAt is the wall time of the last successful sample; the zero
	// time when none has completed yet.
	LastSampleAt time.Time
}

// Status reports the current state for a profile without sampling.
func (s *Scheduler) Status(profileID string) TargetStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.targets[profileID]
	if t == nil {
		// No connection yet: the state before the first sample is Pending —
		// never the zero string, which would render as an empty badge.
		return TargetStatus{ProfileID: profileID, Sample: Sample{State: StatePending}}
	}
	// Belt and braces on the same invariant. Every construction site sets a
	// state, and one of them did not, for weeks: the wire is where the cost
	// lands, so the wire is where the guarantee belongs. State "" is in no
	// enum and matches no arm of the renderer's switch, so it does not
	// degrade the panel — it blanks it.
	smp := t.last
	if smp.State == "" {
		smp.State = StatePending
	}
	return TargetStatus{
		ProfileID:    profileID,
		Host:         t.host,
		Sample:       smp,
		Paused:       t.paused,
		Visible:      t.visible,
		ConnLost:     t.connDead,
		LastSampleAt: t.lastGood,
	}
}

// Close releases every lease and stops every timer. Idempotent.
func (s *Scheduler) Close() error {
	s.mu.Lock()
	s.closed = true
	targets := make([]*schedTarget, 0, len(s.targets))
	for _, t := range s.targets {
		t.torn = true
		t.stopTimersLocked()
		close(t.done)
		targets = append(targets, t)
	}
	s.targets = map[string]*schedTarget{}
	s.mu.Unlock()
	for _, t := range targets {
		if t.provider != nil {
			_ = t.provider.Close()
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The sampler loop
// ---------------------------------------------------------------------------

// loop is the target's single sampler goroutine. Timers never call Sample
// directly: they nudge the buffered trigger channel, so any number of nudges
// while a sample is in flight collapse to at most one follow-up — the
// detector's one-in-flight guard, not a second scheduler.
func (t *schedTarget) loop() {
	for {
		select {
		case <-t.done:
			return
		case <-t.trigger:
			t.s.sampleTriggered(t)
		}
	}
}

// nudge schedules one sample; never blocks and never queues more than one.
func (t *schedTarget) nudge() {
	select {
	case t.trigger <- struct{}{}:
	default:
	}
}

// sampleTriggered runs on the target's loop goroutine, so automatic samples
// are serialized per target by construction; the Detector's semaphore is the
// second, independent guard.
func (s *Scheduler) sampleTriggered(t *schedTarget) {
	s.mu.Lock()
	ok := !s.closed && !t.torn && !t.connDead && !t.paused
	s.mu.Unlock()
	if !ok {
		return
	}
	p := s.acquireProvider(t)
	if p == nil {
		return
	}
	smp := p.Sample(context.Background())

	s.mu.Lock()
	t.last = smp
	if smp.Canceled {
		// The lease died under the sample (connection loss). Nothing more
		// runs until ConnectionUp builds a fresh detector.
		t.connDead = true
		t.provider = nil
		t.conn = nil
		t.stopTimersLocked()
	} else if smp.State == StateAvailable || smp.State == StateAvailableLimited {
		t.lastGood = time.Now()
	}
	s.mu.Unlock()
}

// acquireProvider returns the target's sampling provider, building a fresh
// one when none exists. Runs OUTSIDE the scheduler lock: a slow or
// auth-blocked dial must not stall ConnectionDown, Close, Status or the
// timers. The singleflight guard ensures exactly one build per target even
// when the sampler loop and a manual SampleNow race. A nil return means the
// target cannot sample right now; a build failure is recorded as the last
// sample, never surfaced as "no ports".
//
// The local machine (LocalTargetID) has nothing to acquire: its provider is
// built by the composition-root factory and there is no connection to
// watch, so no lease is held and no watchConn runs.
func (s *Scheduler) acquireProvider(t *schedTarget) Provider {
	s.mu.Lock()
	if s.closed || t.torn || t.connDead {
		s.mu.Unlock()
		return nil
	}
	if t.provider != nil {
		p := t.provider
		s.mu.Unlock()
		return p
	}
	if t.acquiring {
		ch := t.acquired
		s.mu.Unlock()
		<-ch
		s.mu.Lock()
		p := t.provider
		s.mu.Unlock()
		return p
	}
	t.acquiring = true
	ch := make(chan struct{})
	t.acquired = ch
	host := t.host
	opts := t.opts
	isLocal := t.profileID == LocalTargetID
	s.mu.Unlock()

	var p Provider
	var lease Lease // the connection-loss watcher's material; nil for local
	var acquireErr error
	if isLocal {
		if s.localProvider == nil {
			acquireErr = errors.New("local port discovery is not wired at the composition root")
		} else {
			p = s.localProvider(s.logger)
		}
	} else {
		acquireCtx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
		conn, err := s.connector.Lease(acquireCtx, host, opts...)
		cancel()
		if err != nil {
			acquireErr = err
		} else {
			lease = conn
			// The detector's hard timeout and transient backoff are
			// mechanism parameters this scheduler names explicitly (the
			// defaults it wants) — the constructors stay reachable from
			// production, not test-only.
			p = NewDetector(conn, s.logger, WithSampleTimeout(s.sampleTimeout), WithBackoffLevels(defaultBackoffLevels))
		}
	}

	s.mu.Lock()
	t.acquiring = false
	t.acquired = nil
	close(ch)
	switch {
	case acquireErr != nil:
		// The connection is not up (or a sealed vault refuses the
		// resolve, or local discovery is not wired); surface it as a
		// transient state, never as "no ports" and never as a crash.
		t.last = Sample{
			State:          StateFailedTransiently,
			Classification: "discovery connection unavailable: " + acquireErr.Error(),
		}
		s.mu.Unlock()
		return nil
	case t.torn || s.closed:
		// Re-check under the lock: the target may have been torn down while
		// the lease was being acquired, and a lease nobody will release
		// keeps a pooled connection open forever.
		s.mu.Unlock()
		if lease != nil {
			_ = lease.Close()
		}
		return nil
	}
	t.provider = p
	if lease != nil {
		t.conn = lease
		go s.watchConn(t, lease)
	}
	s.mu.Unlock()
	return p
}

// watchConn marks the target conn-lost when the lease's Done channel closes —
// the transport-death signal. The lease releases itself on loss; there is no
// Close to call.
func (s *Scheduler) watchConn(t *schedTarget, conn Lease) {
	<-conn.Done()
	s.mu.Lock()
	if t.conn == conn {
		t.connDead = true
		t.provider = nil
		t.conn = nil
		t.stopTimersLocked()
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Cadence timers. Each is a one-shot AfterFunc that nudges the loop; stale
// fires are harmless (the trigger coalesces), so stop+reset races are safe.
// armSettle and armPrompt mutate the timer fields, so they run under the
// scheduler lock — a ConnectionUp racing stopTimersLocked must not leave a
// live timer behind on a torn-down target.
// ---------------------------------------------------------------------------

func (t *schedTarget) armSettle() {
	s := t.s
	if s.settleDelay <= 0 {
		t.nudge()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.torn || s.closed {
		return
	}
	if t.settleT != nil {
		t.settleT.Stop()
	}
	t.settleT = time.AfterFunc(s.settleDelay, t.nudge)
}

func (t *schedTarget) armPrompt() {
	s := t.s
	if s.promptDebounce <= 0 {
		t.nudge()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.torn || s.closed {
		return
	}
	if t.promptT != nil {
		t.promptT.Stop()
	}
	t.promptT = time.AfterFunc(s.promptDebounce, t.nudge)
}

// armPeriodicLocked starts the periodic timer. Callers hold s.mu. The timer
// re-arms itself while the target is still visible, unpaused, alive and the
// scheduler is open.
func (s *Scheduler) armPeriodicLocked(t *schedTarget) {
	if s.sampleInterval <= 0 || t.periodT != nil {
		return
	}
	var fire func()
	fire = func() {
		t.nudge()
		s.mu.Lock()
		defer s.mu.Unlock()
		if !t.torn && !s.closed && t.visible && !t.paused && !t.connDead {
			t.periodT = time.AfterFunc(s.sampleInterval, fire)
		}
	}
	t.periodT = time.AfterFunc(s.sampleInterval, fire)
}

func (t *schedTarget) stopPeriodicLocked() {
	if t.periodT != nil {
		t.periodT.Stop()
		t.periodT = nil
	}
}

func (t *schedTarget) stopTimersLocked() {
	if t.settleT != nil {
		t.settleT.Stop()
		t.settleT = nil
	}
	if t.promptT != nil {
		t.promptT.Stop()
		t.promptT = nil
	}
	t.stopPeriodicLocked()
}
