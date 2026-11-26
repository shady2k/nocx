package portsprobe

// The ladder: which tool answers "what listens on this host", and what its
// answer means.
//
// It lives HERE because the far helper runs it on the host it is deployed to
// (design §5.10, C30): the helper has no ssh connection to ask through, so it
// must ask the host's own tools, and this package is the only one both ends of
// that wire may link. The coordinator's internal/discovery keeps its own
// Detector over an ssh-side exec seam until nocx-oalo1.13.3 deletes it; the
// two classifications are the same rules because a person is shown the same
// sentence from either side, and this file is the copy that survives.
//
// Three things the ladder must not get wrong, unchanged from discovery:
//
//  1. "Could not determine" is not "no ports". A sample is one of available,
//     available-limited, unavailable, failed-transiently. A successful EMPTY
//     result means "no listeners observed"; every other state means
//     could-not-determine.
//  2. Process evidence is three-valued — known, permission-denied, unsupported
//     — never an empty string.
//  3. There is no framing marker here and there must not be one. The rungs run
//     as argv against the host's own binaries, so nothing can prepend a banner
//     to a table the way a remote login shell could; what a rung prints is
//     either its dialect (the parser says so) or it is not, and a body that is
//     not the dialect advances the ladder rather than being scavenged for
//     plausible-looking port numbers.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// DefaultSampleTimeout is one pass's hard bound. A pass is the whole pass and
// not one rung: a host that has four slow tools must still answer inside a
// poll interval.
const DefaultSampleTimeout = 10 * time.Second

// rung is one step of the ladder: the argv that runs it, the parser for its
// dialect, and the exit status that means "ran fine, nothing matched" (lsof
// exits 1 with no matches — a valid empty sample, not a failure).
type rung struct {
	name string
	argv []string
	// env is what this rung needs beside the daemon's own environment. ss's
	// table is locale-dependent, and a translated header or decimal comma is
	// a table this package cannot parse.
	env         []string
	parse       func(body []byte) ([]Listener, bool)
	noMatchExit int
}

// ladder is the capability-selection order (spec §5): ss → netstat (whose
// flags are verified by the run itself, never hoped for) → busybox netstat
// (detected explicitly; -p may be unavailable, so process evidence is
// unsupported) → lsof → sockstat → unavailable.
//
// busybox's netstat is the SAME BINARY as netstat with flags that machine
// rejects, which is why a netstat that is not installed marks both absent:
// running it twice would be one wasted exec per pass, for the connection's
// whole life.
var ladder = []rung{
	{name: "ss", argv: []string{"ss", "-H", "-lntp"}, env: []string{"LC_ALL=C"}, parse: ParseSS},
	{name: "netstat", argv: []string{"netstat", "-lntp"}, parse: ParseNetstat},
	{name: "busybox-netstat", argv: []string{"netstat", "-ltn"}, parse: ParseBusyboxNetstat},
	{name: "lsof", argv: []string{"lsof", "-nP", "-iTCP", "-sTCP:LISTEN"}, parse: ParseLsof, noMatchExit: 1},
	{name: "sockstat", argv: []string{"sockstat", "-4", "-l"}, parse: ParseSockstat},
}

// ladderIndex returns the ladder position of the named rung, or 0 when unknown
// (an unknown name means no selection — start at the top).
func ladderIndex(name string) int {
	for i := range ladder {
		if ladder[i].name == name {
			return i
		}
	}
	return 0
}

// backoff is the typed transient-failure backoff (spec §4): 10s → 30s → 2min →
// 10min, reset on success. Tool absence and an unusable tool do NOT back off —
// they are cached for the connection's lifetime.
type backoff struct {
	levels []time.Duration
	idx    int
}

var defaultBackoffLevels = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

func (b *backoff) next() time.Duration {
	d := b.levels[b.idx]
	if b.idx < len(b.levels)-1 {
		b.idx++
	}
	return d
}

func (b *backoff) reset() { b.idx = 0 }

// ladderState is the per-connection selection state (spec §5): selection
// happens once per connection, then only the selected rung runs. Cached
// outcomes survive for the connection's lifetime — a missing tool does not
// come back mid-connection.
type ladderState struct {
	selected string          // the rung that produced a valid sample; "" until selection completes
	absent   map[string]bool // tool not found; cached for the connection lifetime
	failed   map[string]bool // present but unusable (bad flags, unrecognized output shape)
	// tried is every rung this connection has run, in the order it first ran,
	// each named once. It is the diagnostics half of an answer, and it is
	// DEDUPED because a connection samples for as long as it lives: a list that
	// grew one entry per pass would grow without bound and say nothing new
	// after the first five.
	tried []string
}

// Detector owns the ladder for one target: the selection cache, the typed
// backoff, and the exactly-one-sample-in-flight guard (spec §4). The far
// helper builds one per accepted connection.
type Detector struct {
	runner  Runner
	timeout time.Duration
	now     func() time.Time

	sem chan struct{} // capacity 1: exactly one sample in flight per target

	mu         sync.Mutex
	ladder     ladderState
	backoff    backoff
	retryAfter time.Time
	last       Sample
}

// NewDetector creates a detector over the given runner. There is nothing to
// release: a pass's process is bounded by the pass's context, which the caller
// of Sample owns.
//
// The pass bound and the clock have no setter: a caller outside this package
// takes the defaults (DefaultSampleTimeout, time.Now) because there is nothing
// for it to choose — a shorter bound would lose a slow host's answer and a
// different clock only moves a window the production code reads from the real
// one. A test that needs either sets the field directly through
// newTestDetector, which keeps this package's exported surface honest.
func NewDetector(runner Runner) *Detector {
	d := &Detector{
		runner:  runner,
		timeout: DefaultSampleTimeout,
		now:     time.Now,
		sem:     make(chan struct{}, 1),
		ladder:  ladderState{absent: map[string]bool{}, failed: map[string]bool{}},
		backoff: backoff{levels: defaultBackoffLevels},
		last:    Sample{State: StatePending},
	}
	return d
}

// Sample runs one pass and returns the result. Exactly one pass is in flight
// per Detector: a concurrent Sample waits for the in-flight one (or returns
// Canceled if its ctx fires first).
//
// Transient failures back off 10s → 30s → 2min → 10min; a sample inside the
// backoff window returns the previous result without executing. An absent or
// unusable tool does not back off: the ladder has already moved past it for
// this connection's lifetime.
func (d *Detector) Sample(ctx context.Context) Sample {
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return d.canceled()
	}

	d.mu.Lock()
	backingOff := d.now().Before(d.retryAfter)
	last := d.last
	d.mu.Unlock()
	if backingOff {
		return last
	}

	passCtx, cancel := context.WithTimeout(ctx, d.timeout)
	start := d.now()
	res := d.sampleOnce(passCtx)
	cancel()
	elapsed := d.now().Sub(start)

	if res.kind == kindCanceled {
		return d.canceled()
	}

	d.mu.Lock()
	switch res.kind {
	case kindValid, kindUnavailable:
		d.backoff.reset()
	case kindTransient:
		d.retryAfter = d.now().Add(d.backoff.next())
	}
	s := res.finish(elapsed)
	d.last = s
	d.mu.Unlock()
	return s
}

// State reports the current sample state without sampling.
func (d *Detector) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last.State
}

func (d *Detector) canceled() Sample {
	d.mu.Lock()
	last := d.last
	d.mu.Unlock()
	last.Canceled = true
	return last
}

// outcomeKind classifies one pass for the state machine.
type outcomeKind int

const (
	kindValid outcomeKind = iota
	kindAbsent
	kindUnsupported
	kindTransient
	kindUnavailable
	kindCanceled
)

// passResult is the raw classification of a pass, before it is projected into
// a Sample.
type passResult struct {
	kind      outcomeKind
	listeners []Listener
	probe     string
	tried     []string
	class     string
	stderr    string
}

func (r passResult) finish(elapsed time.Duration) Sample {
	state := StatePending
	switch r.kind {
	case kindValid:
		state = SampleState(r.listeners)
	case kindUnavailable:
		state = StateUnavailable
	case kindTransient:
		state = StateFailedTransiently
	}
	return Sample{
		State:          state,
		Listeners:      r.listeners,
		Probe:          r.probe,
		ProbesTried:    r.tried,
		Duration:       elapsed,
		Classification: r.class,
		Stderr:         r.stderr,
	}
}

// sampleOnce runs one pass: the selected rung, or selection from the top of
// the ladder when none is selected yet, or the next usable rung after the
// selected one was cached as failed. Cached absences and failures are skipped;
// a transient failure stops the pass so the caller backs off rather than
// burning more execs.
func (d *Detector) sampleOnce(ctx context.Context) passResult {
	d.mu.Lock()
	startIdx := ladderIndex(d.ladder.selected)
	d.mu.Unlock()

	for i := startIdx; i < len(ladder); i++ {
		st := ladder[i]
		d.mu.Lock()
		skip := d.ladder.absent[st.name] || d.ladder.failed[st.name]
		d.mu.Unlock()
		if skip {
			continue
		}

		res := d.runRung(ctx, st)
		switch res.kind {
		case kindValid:
			d.mu.Lock()
			d.ladder.selected = st.name
			d.markTried(st.name)
			d.mu.Unlock()
			res.probe = st.name
			res.tried = d.triedSoFar()
			return res
		case kindAbsent:
			d.mu.Lock()
			d.ladder.absent[st.name] = true
			d.markTried(st.name)
			if st.name == "netstat" {
				d.ladder.absent["busybox-netstat"] = true
			}
			d.mu.Unlock()
			continue
		case kindUnsupported:
			d.mu.Lock()
			d.ladder.failed[st.name] = true
			d.markTried(st.name)
			d.mu.Unlock()
			continue
		case kindTransient, kindCanceled:
			res.tried = d.triedSoFar()
			return res
		}
	}

	return passResult{
		kind:  kindUnavailable,
		tried: d.triedSoFar(),
		class: "no probe tool usable on this host",
	}
}

// markTried records a rung the ladder ran, once per connection. The caller
// holds d.mu.
func (d *Detector) markTried(name string) {
	for _, seen := range d.ladder.tried {
		if seen == name {
			return
		}
	}
	d.ladder.tried = append(d.ladder.tried, name)
}

func (d *Detector) triedSoFar() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ladder.tried...)
}

// runRung runs one rung as argv and classifies the outcome.
func (d *Detector) runRung(ctx context.Context, st rung) passResult {
	res, err := d.runner.Run(ctx, Command{Argv: st.argv, Env: st.env})
	if err != nil {
		switch {
		case errors.Is(err, ErrCommandAbsent):
			return passResult{kind: kindAbsent, class: st.name + " not present"}
		case errors.Is(err, context.Canceled):
			return passResult{kind: kindCanceled}
		case errors.Is(err, context.DeadlineExceeded):
			// The pass's own bound. The far helper holds no connection that a
			// slow tool could have lost, so this is never a connection fact.
			return passResult{kind: kindTransient, class: "probe timed out"}
		default:
			return passResult{kind: kindTransient, class: fmt.Sprintf("exec failed: %v", err)}
		}
	}

	if res.Truncated {
		// The capture bound was hit: an incomplete table must not surface as
		// "no ports".
		return passResult{kind: kindTransient, class: "probe output incomplete", stderr: StderrExcerpt(res.Stderr)}
	}
	if res.ExitStatus == 127 || res.ExitStatus == 126 || NotFoundOnStderr(res.Stderr) {
		// A wrapper or a shell shim can report the miss itself, with a status
		// that is not 127; the shell's own sentence is then the only evidence.
		return passResult{kind: kindAbsent, class: st.name + " not present"}
	}
	if res.ExitStatus != 0 && res.ExitStatus != st.noMatchExit {
		// The tool exists but cannot do its job (usage error, permission,
		// broken build): cache it as failed and try the next rung.
		return passResult{
			kind:   kindUnsupported,
			class:  st.name + " exited " + strconv.Itoa(res.ExitStatus),
			stderr: StderrExcerpt(res.Stderr),
		}
	}
	listeners, ok := st.parse(res.Stdout)
	if !ok {
		return passResult{
			kind:   kindUnsupported,
			class:  "unrecognized " + st.name + " output",
			stderr: StderrExcerpt(res.Stderr),
		}
	}
	return passResult{kind: kindValid, listeners: listeners, stderr: StderrExcerpt(res.Stderr)}
}
