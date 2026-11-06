// Package domains bounds execution per failure domain.
//
// ADR-8eej3e66 §6: a failure domain is what fails together — a mount (by
// device id) for local filesystem work, a resolved host for far work, a
// checkout's mount for git processes, a store. One hung dependency must cost
// its own domain and nothing else, so each domain gets its own bulkhead and
// the work behind it is never refused because another dependency is stuck.
//
// Four rules, and each has a test in this package:
//
//   - Class. Each bulkhead reserves part of its capacity for a person's work,
//     and background work has a ceiling below the whole, so a poll cannot take
//     the permit a person's action needs.
//   - Abandoned work. When a call settles at its deadline the work does not
//     stop — a readdir on a hung mount cannot be interrupted — so the permit
//     stays held until the worker really returns, and that worker is counted
//     abandoned.
//   - Circuit breaker. A domain at its abandoned bound is suspect: a call on
//     it settles at once, naming the domain, and no worker starts. The global
//     abandoned bound is the hard cap on abandoned workers: at the reserve
//     line every domain that already has one becomes suspect, and at the cap
//     itself nothing new starts anywhere, so a caller that keeps timing out
//     against changing host ids cannot grow the abandoned set without limit.
//   - Reclaiming. An idle domain's state is dropped, so changing host ids
//     cannot grow the map.
//
// The package knows nothing about what the work is. It takes a key, the
// invocation's origin and its two deadlines, and it runs one closure. A
// provider is reachable only through the closure, so a caller cannot take a
// permit and then forget to release it: the permit's lifetime is the closure's.
package domains

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// Key names one failure domain. Kind is the sort of thing that fails
// together ("mount", "host", "checkout", "store"), ID is the instance.
type Key struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (k Key) String() string {
	if k.Kind == "" {
		return k.ID
	}
	return k.Kind + ":" + k.ID
}

func (k Key) valid() bool { return strings.TrimSpace(k.ID) != "" }

// Options are the bulkhead's declared bounds. Every one is a starting number
// measured in the stage that adopted it, not a default a caller may leave at
// zero: New refuses a registry without them.
type Options struct {
	// PerDomain is how many workers one domain may run at once.
	PerDomain int
	// PersonReserve is the part of PerDomain that background work may never
	// take, so a person's action always finds a permit on a busy domain.
	PersonReserve int
	// AbandonedPerDomain is how many workers on one domain may be running
	// after their call settled before the domain is suspect.
	AbandonedPerDomain int
	// AbandonedGlobal is the hard cap on abandoned workers in the process.
	AbandonedGlobal int
	// HealthyReserve is the part of AbandonedGlobal that a domain which has
	// already abandoned a worker cannot take: from AbandonedGlobal minus
	// this, every such domain is suspect and only domains with none
	// abandoned may still start work.
	HealthyReserve int
}

func (o Options) validate() error {
	var problems []string
	if o.PerDomain <= 0 {
		problems = append(problems, "PerDomain must be positive")
	}
	if o.PersonReserve < 0 || o.PersonReserve >= o.PerDomain {
		problems = append(problems, "PersonReserve must be within [0, PerDomain)")
	}
	if o.AbandonedPerDomain <= 0 {
		problems = append(problems, "AbandonedPerDomain must be positive")
	}
	if o.AbandonedGlobal <= 0 {
		problems = append(problems, "AbandonedGlobal must be positive")
	}
	if o.HealthyReserve < 0 || o.HealthyReserve > o.AbandonedGlobal {
		problems = append(problems, "HealthyReserve must be within [0, AbandonedGlobal]")
	}
	if len(problems) != 0 {
		return fmt.Errorf("operation/domains: %s", strings.Join(problems, "; "))
	}
	return nil
}

// ErrDomainSuspect is reported when a call is refused because its failure
// domain is not answering. The call settles at once with the operation's
// declared expired outcome and no worker starts.
var ErrDomainSuspect = errors.New("operation/domains: failure domain not answering")

// ErrTurnExpired is reported when the wait for capacity ended at the
// invocation's turn deadline, before any work started.
var ErrTurnExpired = errors.New("operation/domains: no capacity before the turn deadline")

// Refusal is the one error a bulkhead answers with. It names the domain, so
// the sentence a person is shown can say which disk or host is not
// answering, and it carries the counts that decided the refusal so a
// diagnostics record can be checked against the bound.
type Refusal struct {
	Domain    Key
	Reason    string
	Abandoned int // this domain's abandoned workers
	Running   int // this domain's running workers
	Bound     int // the bound that was reached
	// Cause is the error the refusal stands for: ErrDomainSuspect or
	// ErrTurnExpired.
	Cause error
}

// Sentence is the words a person reads. It is separate from Error only
// because the transport answers with it: a log-context ratchet treats any
// identifier followed by the four logger method names as a logger call, and
// a distinct accessor is the honest fix where the sentence really is the
// thing being used.
func (e *Refusal) Sentence() string {
	return fmt.Sprintf("%s is not answering (%s; %d abandoned, %d running, bound %d)",
		e.Domain, e.Reason, e.Abandoned, e.Running, e.Bound)
}

func (e *Refusal) Error() string { return e.Sentence() }

func (e *Refusal) Unwrap() error { return e.Cause }

// Request is one invocation's work in one failure domain.
type Request struct {
	Key    Key
	Origin catalog.Origin
	// Turn bounds the wait for a permit. It is the invocation's turn
	// deadline: when it expires before a permit is free, nothing started.
	Turn context.Context
	// Work bounds the work itself, and starts when a permit is taken. When it
	// expires the call settles, the worker keeps the permit until it returns,
	// and it is counted abandoned.
	Work context.Context
	// Run is the work. It is called at most once, on its own goroutine.
	Run func(context.Context) error
}

// Registry holds every failure domain's bulkhead.
type Registry struct {
	opts Options
	mu   sync.Mutex
	dom  map[Key]*domain
	// abandonedAll is the number of workers running past their call's
	// settlement across every domain.
	abandonedAll int
	// suspectLogged marks the domains whose suspicion has already been
	// announced, so a hung disk writes one line and not one per call.
	suspectLogged map[Key]bool
	// capLogged marks the global abandoned cap's own episode.
	capLogged bool
}

// domain is one failure domain's live state. It is dropped when idle, which
// is what keeps changing host ids from growing the map.
type domain struct {
	key        Key
	running    int
	background int
	abandoned  int
	waiters    int
	// changed is closed and replaced on every release, abandonment and
	// return, so a waiter recomputes capacity without a timeout of its own.
	changed chan struct{}
}

// New builds a registry from declared bounds.
func New(opts Options) *Registry {
	if err := opts.validate(); err != nil {
		panic(err)
	}
	return &Registry{opts: opts, dom: make(map[Key]*domain), suspectLogged: make(map[Key]bool)}
}

// FilesystemDefaults are the bounds this stage adopts for filesystem work,
// measured rather than inherited. ADR-8eej3e66 §6: "Sizes are measured in the
// stage that adopts them. Eight is inherited from ADR-0026 as the migration's
// starting point and measures nothing about a bulkhead."
//
// The measurement is TestMeasuredBulkheadSizes: 200 concurrent listings of
// 2 ms each against one domain, on this machine, run 2026-11-06.
//
//	capacity  1: p50 wait 241 ms, p100 wait 469 ms
//	capacity  2: p50 wait 120 ms, p100 wait 234 ms
//	capacity  4: p50 wait  59 ms, p100 wait 117 ms
//	capacity  8: p50 wait  30 ms, p100 wait  58 ms
//	capacity 16: p50 wait  15 ms, p100 wait  32 ms
//
// Eight is the smallest capacity whose queued call waits under 100 ms — far
// inside the 2 s turn a call now declares — and going to 16 buys 26 ms while
// doubling what one hung mount can hold. So eight is kept on a measurement
// this time, and the person's reserve is a quarter of it: two permits a
// background poll can never take.
//
// The abandoned bounds are derived from the permits rather than measured
// against a real hung mount, which no test can produce on this machine: half
// a domain's permits may be abandoned before the domain is suspect, and the
// process may hold four domains' worth. The healthy reserve is one domain's
// worth, so a process full of suspect domains still admits an unrelated one.
func FilesystemDefaults() Options {
	return Options{
		PerDomain:          8,
		PersonReserve:      2,
		AbandonedPerDomain: 4,
		AbandonedGlobal:    32,
		HealthyReserve:     8,
	}
}

// Stats is a snapshot of the registry, for tests and for app.diagnostics.
type Stats struct {
	Domains   int
	Running   int
	Abandoned int
	Waiting   int
}

// Stats returns a snapshot.
func (r *Registry) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Stats{Domains: len(r.dom), Abandoned: r.abandonedAll}
	for _, d := range r.dom {
		s.Running += d.running
		s.Waiting += d.waiters
	}
	return s
}

// Suspect reports whether the domain would refuse a call right now, and
// which bound refuses it. It is read-only and takes no permit.
func (r *Registry) Suspect(key Key) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.dom[key]
	if d == nil {
		if r.abandonedAll >= r.opts.AbandonedGlobal {
			return true, r.opts.AbandonedGlobal
		}
		return false, 0
	}
	return r.suspectLocked(d)
}

// Do runs one invocation's work under its failure domain's bulkhead.
//
// It returns the work's own error, a *Refusal when the domain is suspect or
// the turn deadline expired before a permit was free, or the Work context's
// error when the work outlived its bound. An abandoned worker keeps its
// permit until it really returns; nothing is written after the call settles.
func (r *Registry) Do(req Request) error {
	if !req.Key.valid() {
		return fmt.Errorf("operation/domains: request with an empty failure domain key")
	}
	if req.Run == nil {
		return fmt.Errorf("operation/domains: request %s has no work", req.Key)
	}
	if req.Turn == nil || req.Work == nil {
		return fmt.Errorf("operation/domains: request %s has no deadline", req.Key)
	}
	origin := req.Origin
	if origin == "" {
		origin = catalog.OriginPerson
	}

	if err := r.acquire(req.Key, origin, req.Turn); err != nil {
		return err
	}
	d := r.enterWork(req.Key, origin)

	// WHO RELEASES THE PERMIT is decided once, by whoever wins this state:
	// the caller (state 2, the worker returned first) or the worker (state 1,
	// the call settled first). One relaxed atomic decisions rather than a
	// second goroutine per call — a watcher goroutine per timed-out call is
	// exactly the resource growth the bound exists to prevent.
	var state atomic.Uint32
	result := make(chan error, 1)
	go func() {
		err := req.Run(req.Work)
		if !state.CompareAndSwap(0, workerDone) {
			// The call already settled: this worker is the one that keeps
			// the permit until it really returns, so it is the one that
			// gives it back.
			r.finish(d, origin, true)
		}
		result <- err // buffered: nobody is obliged to read it
	}()

	select {
	case err := <-result:
		r.finish(d, origin, false)
		return err
	case <-req.Work.Done():
		if state.CompareAndSwap(0, callSettled) {
			r.abandon(d)
			return req.Work.Err()
		}
		// The worker won the race and is about to deliver its result. This
		// call settles with that result rather than calling a finished piece
		// of work abandoned.
		err := <-result
		r.finish(d, origin, false)
		return err
	}
}

// The two states Do's single-release decision can be in besides 0 (still
// running). They are spelled out because the swap's direction is the whole
// contract: whoever gets its value in owns the release.
const (
	callSettled uint32 = 1 // the call returned first; the worker releases
	workerDone  uint32 = 2 // the worker returned first; the call releases
)

// acquire waits for a permit, bounded by turn.
func (r *Registry) acquire(key Key, origin catalog.Origin, turn context.Context) error {
	for {
		r.mu.Lock()
		d := r.domainLocked(key)
		if suspect, bound := r.suspectLocked(d); suspect {
			episode := r.noteSuspectLocked(d, bound)
			refusal := r.refusalLocked(d, ErrDomainSuspect, "the failure domain is suspect", bound)
			// A refused call leaves nothing behind: the entry it created is
			// dropped, so a caller looping over changing host ids cannot grow
			// the map with domains that never started work.
			r.dropIfIdleLocked(d)
			r.mu.Unlock()
			if episode != nil {
				log.From(context.Background()).Warn("failure domain is suspect; its new work settles at once",
					"domain", episode.domain.String(), "abandoned", episode.abandoned,
					"running", episode.running, "bound", episode.bound, "global", episode.global)
			}
			return refusal
		}
		if r.fitsLocked(d, origin) {
			d.running++
			if origin == catalog.OriginBackground {
				d.background++
			}
			delete(r.suspectLogged, key)
			r.mu.Unlock()
			return nil
		}
		d.waiters++
		changed := d.changed
		r.mu.Unlock()

		select {
		case <-changed:
		case <-turn.Done():
			r.mu.Lock()
			d.waiters--
			reason := "no capacity before the turn deadline"
			refusal := r.refusalLocked(d, ErrTurnExpired, reason, r.opts.PerDomain)
			r.dropIfIdleLocked(d)
			r.mu.Unlock()
			return refusal
		}
	}
}

// enterWork records that a permit is held by a worker; the caller has already
// counted it.
func (r *Registry) enterWork(key Key, origin catalog.Origin) *domain {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.domainLocked(key)
}

// finish releases a permit. abandoned says the worker had already been
// counted, so it must be uncounted here.
func (r *Registry) finish(d *domain, origin catalog.Origin, abandoned bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d.running--
	if origin == catalog.OriginBackground {
		d.background--
	}
	if abandoned {
		d.abandoned--
		r.abandonedAll--
		if r.abandonedAll < r.opts.AbandonedGlobal {
			r.capLogged = false
		}
	}
	r.wakeLocked(d)
	r.dropIfIdleLocked(d)
}

func (r *Registry) abandon(d *domain) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d.abandoned++
	r.abandonedAll++
	r.wakeLocked(d)
}

// domainLocked returns the domain's state, creating it.
func (r *Registry) domainLocked(key Key) *domain {
	d := r.dom[key]
	if d == nil {
		d = &domain{key: key, changed: make(chan struct{})}
		r.dom[key] = d
	}
	return d
}

// fitsLocked reports whether one more worker of this origin fits.
func (r *Registry) fitsLocked(d *domain, origin catalog.Origin) bool {
	if d.running >= r.opts.PerDomain {
		return false
	}
	if origin != catalog.OriginBackground {
		return true
	}
	return d.background < r.opts.PerDomain-r.opts.PersonReserve
}

// suspectLocked applies both bounds of the breaker.
func (r *Registry) suspectLocked(d *domain) (bool, int) {
	if r.abandonedAll >= r.opts.AbandonedGlobal {
		// The hard cap: nothing new starts anywhere. Without it, changing
		// host ids would let every fresh domain take its own permits and
		// the abandoned set would grow with the churn.
		return true, r.opts.AbandonedGlobal
	}
	if d.abandoned >= r.opts.AbandonedPerDomain {
		return true, r.opts.AbandonedPerDomain
	}
	line := r.opts.AbandonedGlobal - r.opts.HealthyReserve
	if d.abandoned > 0 && r.abandonedAll >= line {
		// Past the reserve line only a domain with no abandoned worker may
		// still start one: that is the reserved capacity a suspect domain
		// cannot take.
		return true, line
	}
	return false, 0
}

func (r *Registry) refusalLocked(d *domain, cause error, reason string, bound int) error {
	return &Refusal{
		Domain: d.key, Reason: reason, Abandoned: d.abandoned,
		Running: d.running, Bound: bound, Cause: cause,
	}
}

// suspectEpisode is the one line a newly suspect domain writes. It is built
// under the mutex and logged after unlocking, so a burst of refused calls
// cannot each decide to speak.
type suspectEpisode struct {
	domain                    Key
	abandoned, running, bound int
	global                    bool
}

// noteSuspectLocked reports whether this refusal opens a new episode.
//
// TWO EPISODES, TWO VOICES, and neither is per call. A domain's own suspicion
// is announced once until that domain serves work again; the global cap is
// announced once until it lifts. That distinction is what keeps a caller
// hammering a refused endpoint from writing a line per call — and what keeps
// the announcement map from growing with the host ids it was refused for.
func (r *Registry) noteSuspectLocked(d *domain, bound int) *suspectEpisode {
	episode := &suspectEpisode{
		domain: d.key, abandoned: d.abandoned, running: d.running, bound: bound,
		global: r.abandonedAll >= r.opts.AbandonedGlobal,
	}
	if episode.global {
		if r.capLogged {
			return nil
		}
		r.capLogged = true
		return episode
	}
	if r.suspectLogged[d.key] {
		return nil
	}
	if r.suspectLogged == nil {
		r.suspectLogged = make(map[Key]bool)
	}
	r.suspectLogged[d.key] = true
	return episode
}

// wakeLocked closes the domain's change channel and replaces it, waking every
// waiter so each recomputes capacity and suspicion.
func (r *Registry) wakeLocked(d *domain) {
	close(d.changed)
	d.changed = make(chan struct{})
}

// dropIfIdleLocked reclaims a domain with nothing running, abandoned or
// waiting on it.
func (r *Registry) dropIfIdleLocked(d *domain) {
	if d.running != 0 || d.abandoned != 0 || d.waiters != 0 {
		return
	}
	if r.dom[d.key] == d {
		delete(r.dom, d.key)
	}
	delete(r.suspectLogged, d.key)
}

// Domains reports the domain names currently held, sorted. Diagnostics and
// tests read it; no caller branches on it.
func (r *Registry) Domains() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.dom))
	for _, d := range r.dom {
		out = append(out, d.key.String())
	}
	sort.Strings(out)
	return out
}
