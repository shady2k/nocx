// Package discovery finds a target's listening ports by running probe
// commands over the exec seam (ExecConn): the same ladder, the same five
// result states and the same three-valued process evidence describe an SSH
// host (an owned lease on a pooled connection, adapted in ssh_adapter.go)
// and the machine the app runs on (local.go). The result is backend-owned
// metadata, never the interactive byte stream parsed (AD-6); it works while
// a command is running; and it touches neither the user's tty nor their
// history.
//
// The three things this package must not get wrong:
//
//  1. "Could not determine" is not "no ports". A sample is one of
//     available, available-limited, unavailable, failed-transiently,
//     permission-or-policy-refused. A successful EMPTY result means "no
//     listeners observed"; every other state means could-not-determine.
//  2. Process evidence is three-valued — known, permission-denied,
//     unsupported — never an empty string. "Nobody owns it" and "I was
//     not allowed to see" are different facts.
//  3. Framing, not scavenging. Every probe command wraps its output in a
//     fixed version sentinel; a sample without it is rejected whole. We
//     never scan arbitrary stdout for plausible-looking port numbers.
//
// The probe ladder (ss → netstat → busybox netstat → lsof → sockstat) is
// selected once per connection; afterwards only the selected probe runs. A
// Detector owns one pooled-connection lease and the per-connection probe
// state, the typed backoff (10s → 30s → 2min → 10min on transient failure),
// and the exactly-one-sample-in-flight guard (spec §4).
package discovery

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// State is the overall outcome of a discovery sample (spec §5). The five
// result states are exact: a successful empty result means "no listeners
// observed", and every other state means could-not-determine — never "no
// ports". StatePending is not a result: it is the state before the first
// sample has completed.
type State string

const (
	StateAvailable                 State = "available"
	StateAvailableLimited          State = "available-limited"
	StateUnavailable               State = "unavailable"
	StateFailedTransiently         State = "failed-transiently"
	StatePermissionOrPolicyRefused State = "permission-or-policy-refused"
	StatePending                   State = "pending"
)

// AddressFamily is the IP family of a listener's bind address.
type AddressFamily string

const (
	FamilyIPv4 AddressFamily = "ipv4"
	FamilyIPv6 AddressFamily = "ipv6"
)

// ProcessEvidence is three-valued — known | permission-denied | unsupported
// — never an empty string (spec §5): "nobody owns it" and "I was not allowed
// to see" are different facts and must render differently.
type ProcessEvidence string

const (
	EvidenceKnown            ProcessEvidence = "known"
	EvidencePermissionDenied ProcessEvidence = "permission-denied"
	EvidenceUnsupported      ProcessEvidence = "unsupported"
)

// Process is the process evidence for one listener. Name and PID are valid
// when Evidence is EvidenceKnown.
type Process struct {
	Evidence ProcessEvidence
	Name     string
	PID      int
}

// Listener is one remote listening TCP port.
type Listener struct {
	Family  AddressFamily
	Address string // bind address as the probe reported it (may be a wildcard)
	Port    int
	Process Process
}

// Sample is one discovery result.
type Sample struct {
	State          State
	Listeners      []Listener
	Probe          string        // dialect that produced the sample; "" when none did
	ProbesTried    []string      // probes attempted for this pass, in order
	Duration       time.Duration // wall time of the sampling pass
	Classification string        // human-readable why: refusal detail, truncation, ...
	Stderr         string        // bounded excerpt of the probe's stderr, for diagnostics
	Canceled       bool          // ctx canceled, or the lease closed (Detector.Close), while sampling; State/Listeners are the previous result
}

// Connector acquires an owned lease on the pooled SSH connection for
// discovery (spec §3) — the SSH-shaped half of the seam's acquisition,
// kept at the composition boundary so *ssh.RealClient satisfies it without
// an adapter, exactly like tunnel.Connector. The Detector takes the OWN
// reference — never the tab's — so closing the tab never kills an
// in-flight sample's connection underneath it, and the interactive session
// stays fully usable. The local machine needs no acquisition: the scheduler
// builds its native provider through the composition-root factory.
type Connector interface {
	DiscoveryConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DiscoveryConn, error)
}

// Backoff is the typed transient-failure backoff (spec §4): 10s → 30s →
// 2min → 10min, reset on success. Tool absence and policy refusals do NOT
// back off — they are cached or terminal, and Retry is the only way past a
// refusal.
type Backoff struct {
	levels []time.Duration
	idx    int
}

var defaultBackoffLevels = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

func NewBackoff() *Backoff { return &Backoff{levels: defaultBackoffLevels} }

// Next returns the current level and escalates for the next call.
func (b *Backoff) Next() time.Duration {
	d := b.levels[b.idx]
	if b.idx < len(b.levels)-1 {
		b.idx++
	}
	return d
}

// Reset returns to the first level.
func (b *Backoff) Reset() { b.idx = 0 }

// ladderState is the per-connection probe selection state (spec §5):
// selection happens once per connection, then only the selected probe runs.
// Cached outcomes survive for the connection lifetime — a missing tool is
type ladderState struct {
	selected string          // the probe that produced a valid sample; "" until selection completes
	absent   map[string]bool // tool not found; cached for the connection lifetime
	failed   map[string]bool // present but unusable (bad flags, unrecognized output shape)
	tried    []string        // probes actually run, in order (diagnostics)

	// terminal refusal: sessions refused / exec prohibited / forced-command
	// suspected. No exec runs while set; Retry clears it.
	refused    bool
	refusedWhy string
}

// Detector owns discovery for one target over an exec seam (a pooled SSH
// connection lease, or the local machine): the probe ladder selection
// (cached per connection), the typed backoff, and the
// exactly-one-sample-in-flight guard (spec §4).
type Detector struct {
	conn    ExecConn
	logger  log.Logger
	timeout time.Duration

	sem chan struct{} // capacity 1: exactly one discovery in flight per target

	mu         sync.Mutex
	ladder     *ladderState
	backoff    *Backoff
	retryAfter time.Time
	last       Sample
}

// DetectorOption configures a Detector.
type DetectorOption func(*Detector)

// WithSampleTimeout sets the hard per-sample timeout (spec §4: hard
// timeout). Default 10s.
func WithSampleTimeout(d time.Duration) DetectorOption {
	return func(dd *Detector) { dd.timeout = d }
}

// WithBackoffLevels overrides the transient-failure backoff levels. Tests
// use tiny levels to exercise the backing-off window.
func WithBackoffLevels(levels []time.Duration) DetectorOption {
	return func(dd *Detector) { dd.backoff.levels = levels }
}

// NewDetector creates a detector over an exec seam. The caller owns the
// seam: release it via Detector.Close when discovery stops. A fresh
// detector is required after a reconnect — probe selection is once per
// connection.
func NewDetector(conn ExecConn, logger log.Logger, opts ...DetectorOption) *Detector {
	d := &Detector{
		conn:    conn,
		logger:  logger,
		timeout: 10 * time.Second,
		sem:     make(chan struct{}, 1),
		ladder:  &ladderState{absent: map[string]bool{}, failed: map[string]bool{}},
		backoff: NewBackoff(),
		last:    Sample{State: StatePending},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Sample runs one discovery sample and returns the result. Exactly one
// sample is in flight per Detector: a concurrent Sample waits for the
// in-flight one (or returns Canceled if its ctx fires first).
//
// States are terminal until Retry: sessions refused or forced-command
// suspected disables automatic discovery and returns the refusal without
// executing. Transient failures back off 10s → 30s → 2min → 10min; a sample
// inside the backoff window returns the previous result without executing.
func (d *Detector) Sample(ctx context.Context) Sample {
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return d.canceled()
	}

	d.mu.Lock()
	refused := d.ladder.refused
	backingOff := time.Now().Before(d.retryAfter)
	last := d.last
	d.mu.Unlock()
	if refused || backingOff {
		return last
	}

	sampleCtx, cancel := context.WithTimeout(ctx, d.timeout)
	start := time.Now()
	res := d.sampleOnce(sampleCtx)
	cancel()
	dur := time.Since(start)

	if res.kind == outcomeCanceled {
		return d.canceled()
	}

	d.mu.Lock()
	switch res.kind {
	case outcomeValid, outcomeUnavailable:
		d.backoff.Reset()
	case outcomeTransient:
		d.retryAfter = time.Now().Add(d.backoff.Next())
	}
	s := res.finish(dur)
	d.last = s
	d.mu.Unlock()
	return s
}

// State reports the current discovery state without sampling.
func (d *Detector) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last.State
}

// Retry clears a terminal refusal (sessions refused, exec prohibited,
// forced-command suspected) so the next Sample attempts discovery again.
// Cached probe absences and failures survive — a missing tool does not come
// back mid-connection.
func (d *Detector) Retry() {
	d.mu.Lock()
	d.ladder.refused = false
	d.ladder.refusedWhy = ""
	d.mu.Unlock()
}

// Close releases the exec seam, stopping any exec still in flight. For the
// ssh adapter the pooled connection stays open for every other reference;
// for the local seam it stops in-flight probes.
func (d *Detector) Close() error { return d.conn.Close() }

func (d *Detector) canceled() Sample {
	d.mu.Lock()
	last := d.last
	d.mu.Unlock()
	last.Canceled = true
	return last
}

// outcomeKind classifies one probe attempt (or the whole pass) for the
// state machine.
type outcomeKind int

const (
	outcomeValid outcomeKind = iota
	outcomeAbsent
	outcomeUnsupported
	outcomeRefused
	outcomeTransient
	outcomeUnavailable
	outcomeCanceled
)

// probeResult is the raw classification of a sampling pass, before it is
// projected into a Sample.
type probeResult struct {
	kind      outcomeKind
	listeners []Listener
	probe     string
	tried     []string
	class     string
	stderr    string
}

func (r probeResult) finish(dur time.Duration) Sample {
	return Sample{
		State:          r.state(),
		Listeners:      r.listeners,
		Probe:          r.probe,
		ProbesTried:    r.tried,
		Duration:       dur,
		Classification: r.class,
		Stderr:         r.stderr,
	}
}

func (r probeResult) state() State {
	switch r.kind {
	case outcomeValid:
		return SampleState(r.listeners)
	case outcomeUnavailable:
		return StateUnavailable
	case outcomeRefused:
		return StatePermissionOrPolicyRefused
	case outcomeTransient:
		return StateFailedTransiently
	default:
		return StatePending
	}
}

// SampleState projects the listeners of a valid sample into a state: a
// successful empty result means "no listeners observed" (available); when
// every row's process evidence is degraded — the probe cannot provide it
// (busybox netstat) or none was visible (ss as non-root on a fully owned
// table, or a local read whose owners were not visible) — the sample is
// available-limited. The native local provider projects through this same
// function, so both transports agree on what the evidence means.
func SampleState(listeners []Listener) State {
	if len(listeners) == 0 {
		return StateAvailable
	}
	for _, l := range listeners {
		if l.Process.Evidence == EvidenceKnown {
			return StateAvailable
		}
	}
	return StateAvailableLimited
}

// sampleOnce runs one sampling pass: the selected probe, or selection from
// the top of the ladder when none is selected yet, or the next usable probe
// after the selected one was cached as failed. Cached absences and failures
// are skipped; a terminal refusal stops the pass; a transient failure stops
// it so the caller backs off rather than burning more execs.
func (d *Detector) sampleOnce(ctx context.Context) probeResult {
	d.mu.Lock()
	startIdx := ladderIndex(d.ladder.selected)
	d.mu.Unlock()

	for i := startIdx; i < len(probeLadder); i++ {
		st := probeLadder[i]
		d.mu.Lock()
		skip := d.ladder.absent[st.name] || d.ladder.failed[st.name]
		d.mu.Unlock()
		if skip {
			continue
		}

		res := d.runStep(ctx, st)
		switch res.kind {
		case outcomeValid:
			d.mu.Lock()
			d.ladder.selected = st.name
			d.ladder.tried = append(d.ladder.tried, st.name)
			d.mu.Unlock()
			res.probe = st.name
			res.tried = d.triedSoFar()
			return res
		case outcomeAbsent:
			d.mu.Lock()
			d.ladder.absent[st.name] = true
			d.ladder.tried = append(d.ladder.tried, st.name)
			if st.name == "netstat" {
				// busybox netstat IS netstat: the same binary. A not-found
				// on one is a not-found on the other, so skip the wasted
				// exec.
				d.ladder.absent["busybox-netstat"] = true
			}
			d.mu.Unlock()
			continue
		case outcomeUnsupported:
			d.mu.Lock()
			d.ladder.failed[st.name] = true
			d.ladder.tried = append(d.ladder.tried, st.name)
			d.mu.Unlock()
			continue
		case outcomeRefused:
			d.mu.Lock()
			d.ladder.refused = true
			d.ladder.refusedWhy = res.class
			d.mu.Unlock()
			res.tried = d.triedSoFar()
			return res
		case outcomeTransient, outcomeCanceled:
			res.tried = d.triedSoFar()
			return res
		}
	}

	return probeResult{
		kind:  outcomeUnavailable,
		tried: d.triedSoFar(),
		class: "no probe tool usable on this host",
	}
}

func (d *Detector) triedSoFar() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ladder.tried...)
}

// runStep runs one probe command and classifies the outcome.
func (d *Detector) runStep(ctx context.Context, st *step) probeResult {
	res, err := d.conn.Exec(ctx, st.cmd)
	if err != nil {
		var ee *ExecError
		switch {
		case errors.As(err, &ee) && ee.Kind == ExecErrSessionRefused:
			return probeResult{kind: outcomeRefused, class: "additional sessions refused"}
		case errors.As(err, &ee) && ee.Kind == ExecErrExecProhibited:
			return probeResult{kind: outcomeRefused, class: "exec request refused"}
		case errors.As(err, &ee) && ee.Kind == ExecErrCommandTooLong:
			// Refused by us, not by the host, and refused identically on
			// every retry — so it is terminal, and the class says who did
			// it. The ladder never guesses about a host, and this is the
			// one outcome where there is nothing to guess (nocx-e4ir3).
			return probeResult{kind: outcomeRefused, class: "probe refused by nocx: longer than the remote-command bound"}
		case errors.As(err, &ee) && ee.Kind == ExecErrConnectionLost:
			return probeResult{kind: outcomeTransient, class: "connection lost"}
		case errors.As(err, &ee) && ee.Kind == ExecErrLeaseClosed:
			// The exec surface was closed mid-sample (Detector.Close): the
			// result is discarded, never promoted to the detector's state,
			// and no backoff is scheduled (spec §7.3: discard late results).
			return probeResult{kind: outcomeCanceled}
		case errors.Is(err, context.Canceled):
			return probeResult{kind: outcomeCanceled}
		case errors.Is(err, context.DeadlineExceeded):
			return probeResult{kind: outcomeTransient, class: "probe timed out"}
		default:
			return probeResult{kind: outcomeTransient, class: "exec failed: " + err.Error()}
		}
	}

	body, leading, trailing := splitFrame(res.Stdout)
	if !leading {
		// The exec did not run our probe: a forced command, a login banner
		// or a policy wrapper took over the channel. Rejected whole — we
		// never scan arbitrary stdout for plausible-looking ports — and the
		// connection is treated as undiscoverable until Retry (spec §3.1).
		return probeResult{
			kind:   outcomeRefused,
			class:  "output not framed — forced command, banner or policy wrapper suspected",
			stderr: stderrExcerpt(res.Stderr),
		}
	}
	if res.ExitStatus == 127 || res.ExitStatus == 126 || notFoundOnStderr(res.Stderr) {
		return probeResult{kind: outcomeAbsent, class: st.name + " not present"}
	}
	if !trailing || res.Truncated {
		// The body was cut short: bounded output hit, or the remote died
		// mid-write. A partial table must not surface as "no ports".
		return probeResult{kind: outcomeTransient, class: "probe output incomplete", stderr: stderrExcerpt(res.Stderr)}
	}
	if res.ExitStatus != 0 && res.ExitStatus != st.noMatchExit {
		// The tool exists but cannot do its job (usage error, permission,
		// broken build): cache it as failed and try the next probe.
		return probeResult{kind: outcomeUnsupported, class: st.name + " exited " + strconv.Itoa(res.ExitStatus), stderr: stderrExcerpt(res.Stderr)}
	}
	listeners, ok := st.parse(body)
	if !ok {
		return probeResult{kind: outcomeUnsupported, class: "unrecognized " + st.name + " output", stderr: stderrExcerpt(res.Stderr)}
	}
	return probeResult{kind: outcomeValid, listeners: listeners, stderr: stderrExcerpt(res.Stderr)}
}
