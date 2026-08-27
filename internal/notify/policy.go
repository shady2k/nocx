package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Focus reports the window's attention state, which suppression consults
// (design §6.1). The window layer provides it — the policy never assumes
// who owns focus. Implementations must be safe for concurrent use.
type Focus interface {
	// WindowFocused reports whether the app window is focused (frontmost).
	WindowFocused() bool

	// FocusedSession reports the session (tab) the user is looking at; the
	// empty string means no tab is focused.
	FocusedSession() string
}

// Disposition reports what Policy.Submit did with one event.
type Disposition int

const (
	// DispositionSuppressed: the event was dropped by the focus rule —
	// nothing was delivered and the event was not counted.
	DispositionSuppressed Disposition = iota

	// DispositionOpened: the event was delivered immediately and opened a
	// debounce window, which suppresses what follows it for the window's
	// length.
	DispositionOpened

	// DispositionCoalesced: the event arrived inside an open window and was
	// held back; the window's closing summary names how many were.
	DispositionCoalesced
)

func (d Disposition) String() string {
	switch d {
	case DispositionSuppressed:
		return "suppressed"
	case DispositionOpened:
		return "opened"
	case DispositionCoalesced:
		return "coalesced"
	default:
		return fmt.Sprintf("Disposition(%d)", int(d))
	}
}

// DebounceKey identifies one debounce stream: a session and a kind. Keyed by
// session AND kind — never kind alone — or two tabs would collapse into one
// notification and lose their attribution (design §6.2). It never reads
// Title or Body.
type DebounceKey struct {
	Session string
	Kind    Kind
}

// ResultFunc receives the Outcome of every window-close delivery. A refused
// or failed delivery is then observable instead of silently dropped — a
// soft degrade must be visible in the product, not only in a log (design
// §6.4). It is called synchronously from the delivery, after the router's
// Raise returned, and must return promptly. The wire task connects the
// failure surface here.
type ResultFunc func(outcome Outcome)

// PolicyOption configures a Policy at construction.
type PolicyOption func(*Policy)

// WithResultHandler registers fn as the observer of window-close delivery
// outcomes (see ResultFunc). The default policy discards them.
func WithResultHandler(fn ResultFunc) PolicyOption {
	return func(p *Policy) { p.onResult = fn }
}

// WindowSource answers how long a debounce window opened NOW should last. The
// composition root satisfies it by reading the user's setting, so the policy
// holds no copy of the value and nothing has to be pushed into it when the
// setting moves: the registry stays the one owner of the number (AD-8), and
// this is the pull.
type WindowSource func() time.Duration

// WithWindowSource makes the debounce window live: the policy asks fn for the
// length of every window it opens, instead of using the duration it was
// constructed with.
//
// THE INTERVAL, WITH BOTH ENDS. A window's length is fixed from the moment it
// opens until the moment it closes. A change to the source therefore governs
// every window opened after it and no window already open; the last window
// sized by the old value is the one running when the change lands, and after
// that one closes the old value is not readable from anywhere.
//
// The alternative — retiming open windows, so a user who shortens the window
// sees it take effect at once rather than after the burst in flight — was
// rejected for two reasons, one of which is a correctness argument rather
// than a taste one.
//
// The correctness one: the deadline of an open window has ALREADY been used
// to answer a caller. Submit compares now against it and returns
// DispositionCoalesced, and that answer is out; the ingress has recorded the
// occurrence and the raiser has replied. Shortening an open window
// retroactively makes an event that was told "coalesced" one that should have
// opened its own window, and lengthening makes the reverse. An answer the
// pipeline has already given cannot be un-given, so retiming would make the
// disposition of an event depend on a value read after that event was
// dispositioned.
//
// The mechanical one: retiming means stopping and re-arming a Timer for every
// open stream on every settings change, racing the flush callback the old
// timer may already have entered. This file already carries one such race
// (flush's deadline re-check, for a timer that fired and lost) and a second
// one buys, at most, one window of the old length per (session, kind) already
// open — after which the new value governs everything anyway.
//
// fn is ignored when nil, and an answer that is not positive is treated as
// the constructed window (see Policy.windowNow).
func WithWindowSource(fn WindowSource) PolicyOption {
	return func(p *Policy) {
		if fn != nil {
			p.windowSource = fn
		}
	}
}

// Policy applies the attention policy between the sources and the router:
// suppression and the per-{session,kind} debounce with coalescing (design
// §6.1, §6.2). Both stages are payload-independent — suppression keys on
// focus and session, the debounce key on {session, kind}, the coalescing
// count on the number of events — which is what keeps ADR-0029's
// noninterference invariant true: resolution never depends on the
// presentation fields.
//
// The ad-hoc subscription route (ADR-0029 §3, design §6.1) bypasses this
// policy entirely: an explicit gesture delivers immediately through the
// subscription route and is never suppressed, debounced or coalesced. That
// path lands with the subscription work and must not call Submit. This
// policy governs the ordinary raise route only.
type Policy struct {
	ctx    context.Context
	router *Router
	// window is the length a window opened now would have when nothing
	// answers for it: the value NewPolicy was given, and the floor a
	// windowSource that answers nonsense falls back to.
	window time.Duration
	// windowSource, when set, is asked for the length of every window the
	// policy opens (WithWindowSource). Nil means the window is the constant
	// above and never changes.
	windowSource WindowSource
	focus        Focus
	clock        Clock

	onResult ResultFunc

	mu      sync.Mutex
	streams map[DebounceKey]*stream
}

// stream is one open debounce window. The event that OPENED it has already
// been delivered (Submit delivers on the leading edge); the window exists to
// hold back what follows, and suppressed counts how many it held. It retains
// at most the opening event and the most recent held-back event — memory is
// bounded by the number of open windows and a constant per window (design
// §6.2).
type stream struct {
	key DebounceKey
	// suppressed is how many events arrived inside the window after the one
	// that opened it. Zero means the window closes silently: the leading
	// delivery already said everything there was to say.
	suppressed int
	opening    Event // the delivered event; carries the attribution the summary reuses
	// last is the most recent held-back event. The window's closing summary
	// carries ITS message, so a banner still says what happened instead of
	// only how many times something happened (nocx-jiwq.5).
	last Event
	// deadline is when this window closes, computed once from the length it
	// opened with. It is never recomputed: the length of a window is fixed
	// from the moment it opens until the moment it closes
	// (WithWindowSource).
	deadline time.Time
	timer    Timer
}

// NewPolicy builds the attention policy around a router. ctx is the policy's
// own lifetime context — every window-close delivery runs under it, never
// under a caller's request context, because the caller's request is long
// over when a window closes. window is the debounce window (8s in the
// design, termic's number); focus and clock must not be nil.
func NewPolicy(ctx context.Context, router *Router, window time.Duration, focus Focus, clock Clock, opts ...PolicyOption) (*Policy, error) {
	if ctx == nil {
		return nil, errors.New("notify: policy needs a context")
	}
	if router == nil {
		return nil, errors.New("notify: policy needs a router")
	}
	if focus == nil {
		return nil, errors.New("notify: policy needs a focus source")
	}
	if clock == nil {
		return nil, errors.New("notify: policy needs a clock")
	}
	if window <= 0 {
		return nil, errors.New("notify: debounce window must be positive")
	}
	p := &Policy{
		ctx:     ctx,
		router:  router,
		window:  window,
		focus:   focus,
		clock:   clock,
		streams: make(map[DebounceKey]*stream),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// Submit applies the attention policy to one event and reports what the
// policy did with it. A suppressed event is dropped outright. Otherwise the
// debounce is LEADING-edge: an event that finds no open window for its key is
// delivered immediately and opens one; an event that arrives inside an open
// window is held back and counted, and the window's close delivers one
// summary naming how many were held.
//
// Leading rather than trailing, because the common case is a lone event and a
// trailing window makes every one of them late by the whole window — a build
// that finished announcing itself eight seconds afterwards (nocx-jiwq.4). The
// protection is unchanged: a loop printing OSC 9 still produces one
// notification plus one summary per window, never one per iteration.
func (p *Policy) Submit(ev Event) Disposition {
	if p.suppressed(ev.SessionID) {
		return DispositionSuppressed
	}

	key := DebounceKey{Session: ev.SessionID, Kind: ev.Kind}
	now := p.clock.Now()
	// Read the window BEFORE taking the lock: the source is the settings
	// registry, which takes a lock of its own, and this policy's mutex is not
	// a lock to hold across another package's. It is also the only read of the
	// value in this call — the deadline below and the timer beside it are
	// sized from the same answer, so a window can never be armed for one
	// length and measured against another.
	window := p.windowNow()

	var expired *stream
	p.mu.Lock()
	if s, ok := p.streams[key]; ok {
		if now.Before(s.deadline) {
			s.suppressed++
			s.last = ev
			p.mu.Unlock()
			return DispositionCoalesced
		}
		// The deadline passed but the window's timer has not fired yet (or
		// it fired and lost the race with this submit): close it now and
		// open a fresh window. The stale timer later finds no stream for
		// the key and does nothing.
		delete(p.streams, key)
		expired = s
	}
	s := &stream{
		key:      key,
		opening:  ev,
		deadline: now.Add(window),
	}
	s.timer = p.clock.AfterFunc(window, func() { p.flush(key) })
	p.streams[key] = s
	p.mu.Unlock()

	// Both deliveries happen outside the lock. The expired window's summary
	// goes first: it describes events that arrived before this one.
	if expired != nil {
		p.deliverSummary(expired)
	}
	p.deliver(ev)
	return DispositionOpened
}

// windowNow is the length of a window opened at this instant: the live source
// if one was given (WithWindowSource), otherwise the constructed constant.
//
// An answer that is not positive falls back to the constructed window rather
// than being honoured. A zero window is not "no debouncing" — with a deadline
// equal to now, every event opens its own window and delivers at once, which
// is one notification per event, the flood this policy exists to prevent. So
// the two ways a source can fail (an unreadable setting the composition root
// reports as 0, or a value bound nobody enforced) both land on the number the
// policy was built with, and the user's protection degrades to the default
// rather than to none.
func (p *Policy) windowNow() time.Duration {
	if p.windowSource == nil {
		return p.window
	}
	if d := p.windowSource(); d > 0 {
		return d
	}
	return p.window
}

// flush delivers the window for key if it is still open and its deadline has
// passed, then removes it. It is the timer callback; the delivery runs under
// the policy's own context, never a caller's.
func (p *Policy) flush(key DebounceKey) {
	p.mu.Lock()
	s, ok := p.streams[key]
	if !ok {
		p.mu.Unlock()
		return
	}
	if p.clock.Now().Before(s.deadline) {
		// A newer window for the same key superseded the one this timer was
		// scheduled for; the newer window's own timer will close it.
		p.mu.Unlock()
		return
	}
	delete(p.streams, key)
	p.mu.Unlock()

	p.deliverSummary(s)
}

// deliverSummary delivers the closing notification of one window: how many
// events it held back, carrying the content of the most recent one. A window
// that held back nothing delivers NOTHING — the leading-edge delivery already
// said what there was to say, and a "1 notification" behind every
// notification would double every one of them.
//
// The count goes in the TITLE and the newest held-back event's content in the
// BODY, and that division is the point. The leading edge already delivered
// the window-opening event with its own title and body, so this delivery says
// that more happened and what the newest of it was — the count beside the
// message, the way OS notification surfaces group a burst. Replacing the
// body with a bare count would tell the user that two things happened and
// none of what they were: the first message was already out, and every
// message held back would go unnamed (nocx-jiwq.5).
func (p *Policy) deliverSummary(s *stream) {
	if s.suppressed == 0 {
		return
	}
	noun := "notifications"
	if s.suppressed == 1 {
		noun = "notification"
	}
	ev := s.opening
	ev.Title = fmt.Sprintf("%d more %s", s.suppressed, noun)
	// The body carries the newest held-back event's content IN FULL — its
	// title and its body, joined — never half of it. Dropping the title when
	// a body exists loses exactly the kind of message this delivery exists
	// to save: an OSC 777 event with title "tests failed" and body "3
	// suites" must not become a summary whose body says only "3 suites".
	// A bodyless message falls back to its title, so a banner never says
	// only the count.
	ev.Body = s.last.Body
	if ev.Body == "" {
		ev.Body = s.last.Title
	} else if s.last.Title != "" {
		ev.Body = s.last.Title + " — " + ev.Body
	}
	p.deliver(ev)
}

// deliver raises one event through the router and reports the outcome.
// Suppression is re-checked HERE rather than only at submit, so it governs
// the window-closing summary as well as the leading edge: nothing is
// delivered about the tab the user is looking at in a focused window, even if
// it was not focused when the window opened (design §6.1).
func (p *Policy) deliver(ev Event) {
	if p.suppressed(ev.SessionID) {
		return
	}
	out := p.router.Raise(p.ctx, ev)
	if p.onResult != nil {
		p.onResult(out)
	}
}

// suppressed reports whether an event for session is delivered nowhere: the
// user is looking at that tab in a focused window (design §6.1). Only the
// ordinary raise route passes through here — the ad-hoc subscription route
// bypasses the policy (see Policy).
func (p *Policy) suppressed(session string) bool {
	return session != "" && p.focus.WindowFocused() && p.focus.FocusedSession() == session
}

// Raise presents the policy as the transport's raiser, so notify.raise
// reaches the pipeline through the attention policy rather than around it.
//
// The answer is deliberately not a delivery result. Submit returns as soon as
// the event has been accepted — suppressed, or opened into or joined onto a
// debounce window — and the delivery happens when that window closes, which
// may be seconds later. A program asking for a notification must not block
// until then, so the outcome carries no Results by construction and a nil Err
// means "accepted", never "delivered".
//
// Where a failure becomes visible therefore moves: an admission refusal or a
// sink error arrives at the result handler (WithResultHandler) rather than at
// the caller. ADR-0029 §2.2 requires a refused delivery to be visible, and the
// handler is the seam that carries it — today into the log, and into whatever
// surface reports notification health when one exists (nocx-jiwq.2).
func (p *Policy) Raise(ctx context.Context, ev Event) Outcome {
	if err := ctx.Err(); err != nil {
		return Outcome{Err: err}
	}
	p.Submit(ev)
	return Outcome{}
}
