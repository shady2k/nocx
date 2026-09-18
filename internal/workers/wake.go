package workers

// The wake (nocx-luqz9.3; ADR-0070 decision 4, design §5).
//
// # What this is, and what it replaced
//
// A coordinator is an agent, and an agent acts only when it takes a turn. So a
// worker's mail — a report, or a settled state nocx saw — reaches the
// coordinator only if something STARTS ITS TURN, and the only thing that can is
// a line typed into its pane. This file is that line, and the two ends of the
// interval it opens: one line per batch of unread mail while the coordinator is
// idle, retried a bounded number of times, and then the human.
//
// The mechanism it replaced was per-FACT: every fact that needed judgement armed
// its own deadline and could reach the human on its own. That is gone
// (backstop.go's own doc says where), and not because it was wrong about
// deadlines — because two mechanisms that can both call the human will, and the
// fact that reaches a person is not "this worker declared" but "nobody has read
// anything in a while". The batch is the unit of both.
//
// # The batch, and why a read is what closes it
//
// Seeing our line echoed in an input region is evidence it was TYPED, never
// that it was acted on, so nothing here can observe its own success. What can
// be observed is the coordinator's cursor: workers.inbox advances it, and so do
// the other two calls that read the same box. That is the only event this file
// treats as "read". Until it happens the batch is unread, and an unread batch
// is retyped at the retry pause — up to the attempt limit, after which the
// human is told.
//
// # One line per batch, and what "batch" means
//
// A line is typed for a batch, and a batch is what sits past the position the
// coordinator has read through. Mail that arrives after a line was typed but
// before any read does NOT produce a second line immediately: the coordinator
// has a line already and has not acted on it, so the next arrives at the retry
// pause, like any other retry. A read clears the batch, so the next arrival
// gets its own line at once.
//
// # Only while the coordinator is idle, and nothing runs while it is not
//
// A line typed into a working agent lands in the middle of its turn, and into a
// blocked one it ANSWERS A MENU — the hazard the typing gate exists for. So
// this types only on a settled idle reading, and it cancels its timer on any
// other one: while the coordinator is working nothing is typed and no timer
// runs. A re-admitted idle reading is what gives the batch its line.
//
// # A blocked coordinator is a different report, and an immediate one
//
// A coordinator that is blocked on a menu or an error while it holds live
// workers is not going to read anything, no matter how many lines are typed.
// That is not the retry mechanism's business and it does not wait for it: it
// raises one notice as soon as the state has settled, mailbox empty or not,
// because nothing is being coordinated. One, not one per sweep, and the episode
// ends when the coordinator stops being blocked.
//
// # Every transition is taken under one lock, and a type in flight is reserved
//
// The decision to type is taken under the mutex and the write happens after it
// is released — the same shape observed.go's claim/release has, and for the
// same measured reason: two readers that both decide "no line yet" would both
// type, and the coordinator would get two lines for one batch. So the
// reservation (inFlight) is set under the lock, and a batch has a GENERATION
// that a read bumps: a write that finishes after the coordinator read its mail
// belongs to a batch that no longer exists, and it is dropped rather than
// counted.
//
// # The state comes from the same observer the worker states do
//
// A coordinator's own idle, blocked and working readings arrive from the pane
// sweep through the record's own settle machine (observed.go's
// ObserveCoordinator), which is the same witness and the same window the worker
// observations come from. Nothing here reads a screen, and nothing here decides
// what a screen shows — see Waker below.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// Waker starts a turn the coordinator did not ask for.
//
// It is a seam because reaching a coordinator means TYPING INTO A PANE, and
// that decision belongs to internal/agenttyping, on frames it reads itself.
// This package reads no screen and imports no grid; it says that a coordinator
// must be told and never whether the screen permits it.
type Waker interface {
	// Wake types text into the coordinator's pane. It returns what happened
	// and must never report a refusal as a delivery.
	Wake(ctx context.Context, coordinatorSession, text string) WakeOutcome
}

// WakeOutcome is what one attempt to start a turn came to. Delivered is
// deliberately not "sent": the only outcome that sets it is a submission the
// pane accepted, and every other shape — a modal on screen, a pane nocx is
// not watching, a queue that refused — is a refusal carrying its reason.
//
// THERE IS DELIBERATELY NO `Attempted` FIELD, and it used to be here. The
// per-fact Waker consumed one — a Fact carried the outcome of its wake, so
// "was it even tried" was a fact about the record and was asserted as one. The
// wake that replaced it (nocx-luqz9.3) counts only what reached somebody: a
// call either DELIVERED, or it did not and said why here. That the call
// happened is not something any caller acts on, so a field for it would be a
// value written and never read — which is the shape of dead state a compiler
// and a linter both eventually refuse, and the reason this comment exists.
type WakeOutcome struct {
	Delivered bool
	Reason    string
}

// Notice is what a person is told when coordination has stopped. It carries
// facts and no prose: the words a person reads belong to the notification
// surface, which is internal/notify's and internal/app's.
type Notice struct {
	// Mailbox is the coordinator's mailbox, which is also its session and so
	// the pane a person clicking the notification wants to be taken to. It is
	// the coordinator and never a worker: the decision is the coordinator's,
	// and a notification that opened a worker's pane would show the screen
	// that is not the one which stopped reading.
	Mailbox ReaderID
	// Kind says which of the two situations this is.
	Kind NoticeKind
	// Unread is how many messages of a waking kind are waiting, and Lines is
	// how many times nocx typed at the coordinator without an answer. Both
	// belong to the unread notice; the blocked one is about a screen.
	Unread int
	Lines  int
	// LiveWorkers is how many workers the coordinator holds.
	LiveWorkers int
}

// NoticeKind is which of the two failures of coordination a notice reports.
type NoticeKind string

const (
	// NoticeUnread is a batch the coordinator never read, after the last
	// attempt was typed and the pause that follows it elapsed.
	NoticeUnread NoticeKind = "unread"
	// NoticeBlocked is a coordinator blocked on its own screen while workers
	// are live, which is not a retry's business: nothing is being
	// coordinated, and no number of lines changes that.
	NoticeBlocked NoticeKind = "blocked"
)

// Escalation tells the human that coordination has stopped. It is a seam for
// the same reason the Waker is: which surfaces a notification may reach, and
// under what trust, belongs to internal/notify and is not restated here.
//
// It RETURNS an error because the failure is a fact this file has to keep:
// a notice nobody received leaves the batch exactly as it was, the attempt is
// logged, and a later read still clears it.
type Escalation interface {
	Escalate(ctx context.Context, n Notice) error
}

// Alarms is the one-shot timer seam, and the retry pause is the only thing it
// is used for.
//
// It is injected so a test can assert the claims this file makes about time —
// that no timer runs while the coordinator is not idle, and that the next line
// comes at the pause and not before — without waiting out a duration. "No test
// may depend on a duration" is not a preference here: a retry pause is two
// minutes, and a test that slept for one would be measuring nothing.
type Alarms interface {
	// After runs f once, d from now. The returned cancel is safe to call
	// after f has already run.
	After(d time.Duration, f func()) (cancel func())
}

// Mailboxes is the wake's narrow view of the mailbox (AD-8): what has arrived
// past a position, and nothing else. It reads and never advances — the
// coordinator's own fetch is what moves its cursor, which is the only event
// this file treats as a read.
type Mailboxes interface {
	Since(ctx context.Context, mailbox ReaderID, after int64, limit int) ([]Message, error)
}

// MessageKind is what a row of a mailbox IS, as far as the wake is concerned.
//
// It exists so that "does this wake the coordinator" is answered in ONE place:
// the kinds a worker's tool can report are not built yet (nocx-luqz9.4), and
// when the first of them lands it is a value here and a case in kindOf — never
// a second route into the wake.
type MessageKind string

const (
	// KindObservation is a settled state nocx saw: idle, blocked or exited.
	KindObservation MessageKind = "observation"
	// KindReport is a worker's own words about itself: a completion or a
	// question. Both wake, and neither is built yet.
	KindReport MessageKind = "report"
	// KindProgress is a checkpoint. It never wakes (design P4, §5.1): a
	// report of "still going" is exactly the traffic the batch mechanism
	// exists to keep out of a coordinator's turn.
	KindProgress MessageKind = "progress"
)

// Wakes reports whether mail of this kind starts the coordinator's turn.
func (k MessageKind) Wakes() bool { return k != KindProgress }

// kindOf reads one message's kind off the row itself rather than off a field a
// producer could set wrongly: an observation is a row carrying a state, and
// everything else is somebody's words. Progress does not exist yet, so no row
// is one today — the value is here because the wake's rule is written in terms
// of kinds and not in terms of the two rows that happen to exist, and because
// the kind that must NOT wake has to have a name before it has a producer.
func kindOf(m Message) MessageKind {
	if m.Observed != nil {
		return KindObservation
	}
	if m.Kind == KindProgress {
		return KindProgress
	}
	return KindReport
}

// DefaultRetryPause is how long nocx waits before typing the same thing again
// (design §5.4), and DefaultAttempts is how many lines a batch gets before the
// human is told.
//
// Two minutes and three lines: long enough that a coordinator that is thinking
// gets its turn twice over, short enough that a person hears about a stalled
// effort in the same coffee. Both are injected — see WakeOption — because a
// number wrong in either direction breaks the pair, and the composition root is
// where the product's real values live.
const (
	DefaultRetryPause = 2 * time.Minute
	DefaultAttempts   = 3
)

// batch is one coordinator's wake state. Nothing here is durable, for the
// record's own reason (memory.go): the backend that holds a coordinator holds
// its workers, and the two die together.
type batch struct {
	// state is the coordinator's own last SETTLED reading, and the zero value
	// means nobody has read its pane yet. That is not a third state: it is
	// "not idle", which is where every question this file asks ends up
	// anyway — a coordinator nocx has not seen is one nocx must not type
	// into.
	state ObservedState
	// live is how many workers the coordinator holds, as of its last reading.
	// It is what makes the blocked notice's condition answerable without a
	// store read at the moment of the notice.
	live int
	// read is the mailbox position this coordinator has fetched through. It
	// is the whole of "unread": a batch is what sits past this mark.
	read int64
	// lines is how many lines have been DELIVERED for the current batch. A
	// refused attempt does not count (design §5.4) — a pane that changed
	// under us told the coordinator nothing, and spending the budget on it
	// would call the human for a screen that had merely moved.
	lines int
	// gen is bumped by every read, and it is what makes a type in flight
	// harmless: a write that finishes after the coordinator read its mail
	// belongs to a batch that no longer exists, and it is dropped instead of
	// counted.
	gen int
	// inFlight is "a writer holds the right to type for this batch and has
	// not finished". It is set under the same lock the decision is taken
	// under, which is what stops two readers from both observing `lines ==
	// 0` and typing two lines for one batch.
	inFlight bool
	// notified is that the human has been told about this batch, and
	// blockedNotified is that it has been told this coordinator is blocked.
	// Both are suppressed until the batch is read or the blocked episode
	// ends, because a backstop that repeats is an alarm clock.
	notified        bool
	blockedNotified bool
	// blockedRetry is that the blocked notice's LAST attempt was refused and a
	// retry is waiting for the pause. It is separate from blockedNotified, and
	// the pair is the whole episode rule: blockedNotified is "this episode has
	// been claimed, so no reading may raise it again", and blockedRetry is
	// "the claim has not been honoured yet, so something must try again".
	//
	// THE DISTINCTION IS NOT DECORATION. Without it the retry would hang off
	// the READING, and a blocked coordinator is read once a second
	// (transport.DefaultPaneObserverSweep) — so a pipeline that refused would
	// be hammered sixty times a minute for as long as the block lasted. The
	// pause is the same injected number that paces every other retry here.
	blockedRetry bool
	// armed is whether a retry is scheduled, and cancel is how it is
	// stopped. Both, because the cancel function is nil-able in a way a bool
	// is not: "no timer runs" is asserted, and a nil field is not an answer
	// to that.
	armed  bool
	cancel func()
}

// Wake types one line into an idle coordinator's pane when its mailbox has
// something new, retries a bounded number of times, and calls the human when
// the batch outlives its attempts.
type Wake struct {
	waker  Waker
	human  Escalation
	alarms Alarms
	mail   Mailboxes
	pause  time.Duration
	// attempts is how many lines one batch gets.
	attempts int

	mu        sync.Mutex
	byMailbox map[ReaderID]*batch
}

// WakeOption configures a Wake. The two numbers the design names as injected
// are the whole of what the product varies, so they are the whole of the
// exported surface: the alarm is replaced by this package's own tests,
// in-package and by assignment, which is the same shape backstop.go's dead
// deadline had and for the same reason.
type WakeOption func(*Wake)

// WithRetryPause sets how long nocx waits before typing the same line again.
func WithRetryPause(d time.Duration) WakeOption {
	return func(w *Wake) { w.pause = d }
}

// WithAttemptLimit sets how many lines one batch gets before the human is told.
func WithAttemptLimit(n int) WakeOption {
	return func(w *Wake) { w.attempts = n }
}

// NewWake wires the wake to the pane it types into, the human it tells and the
// mailbox it counts.
//
// A nil waker, escalation, alarm or mailbox is PERMITTED and is not a silent
// degrade: each one names itself at Error at the moment it was needed, which is
// the same stance the record takes with an unwired destination and for the same
// reason — dropping the wake because a route was not built is how a feature
// that does not exist survives a release.
//
// IT TAKES NO LOGGER, and that is the house rule rather than a missing
// parameter (AGENTS.md, nocx-n14oo.9): a call site asks log.From(ctx) for its
// logger, so the line carries the module, request id, trace and span of the
// context it happened in and cannot forget to. Every method here has a context
// to give it; the one that does not is an alarm, whose context is the process's
// own and whose line belongs at the root — see armLocked.
func NewWake(wk Waker, e Escalation, m Mailboxes, opts ...WakeOption) *Wake {
	wake := &Wake{
		waker:     wk,
		human:     e,
		alarms:    systemAlarms{},
		mail:      m,
		pause:     DefaultRetryPause,
		attempts:  DefaultAttempts,
		byMailbox: make(map[ReaderID]*batch),
	}
	for _, o := range opts {
		o(wake)
	}
	return wake
}

// systemAlarms is the product's clock.
type systemAlarms struct{}

func (systemAlarms) After(d time.Duration, f func()) func() {
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// Coordinator admits one settled reading of a coordinator's own pane, with how
// many workers it holds.
//
// This is the same reading observed.go hands on for a worker, from the same
// sweep and the same witness; what it means is different because a coordinator
// is not judged, it is typed into. Idle gives an unread batch its line, working
// stops the timer, and blocked raises its own notice at once — see the package
// doc for all three.
//
// It is also the act that makes a mailbox a COORDINATOR's: the record calls it
// only for a session it has established coordinates a worker, so an entry here
// is the statement "this session may be typed into". Arrived for a mailbox
// nobody has admitted is dropped, which is what keeps a worker's own mailbox
// out of this file entirely.
func (w *Wake) Coordinator(ctx context.Context, mailbox ReaderID, state ObservedState, liveWorkers int) {
	w.mu.Lock()
	b := w.batchOf(mailbox)
	b.state = state
	b.live = liveWorkers
	switch state {
	case ObservedBlocked:
		// A blocked coordinator is never TYPED AT — a line typed at a menu
		// answers it, which is the one thing the typing gate will not do — so
		// an unread batch's retry is stopped here. The one exception is a
		// blocked notice already waiting for its pause: that timer IS this
		// episode's retry, and cancelling it would leave the episode claimed
		// with nobody ever told, which is the silent loss reach exists for.
		if !b.blockedRetry {
			w.stopLocked(b)
		}
		// THE EPISODE IS CLAIMED UNDER THIS LOCK AND GIVEN BACK IF THE NOTICE
		// DOES NOT GO OUT. Claiming here is what stops two settled readings
		// arriving together from both raising it; giving it back on failure is
		// reach's own rule, applied to this mark — the claim is not a record
		// that somebody was told, it is a reservation to tell them once.
		first := liveWorkers > 0 && !b.blockedNotified
		if first {
			b.blockedNotified = true
		}
		notice := Notice{
			Mailbox: mailbox, Kind: NoticeBlocked,
			LiveWorkers: liveWorkers, Lines: b.lines,
		}
		w.mu.Unlock()
		if first {
			w.reach(ctx, mailbox, notice)
		}
		return
	case ObservedIdle:
		// A fresh episode: whatever was blocked before has ended, so the
		// next one is news again — and a retry still waiting for its pause is
		// moot, because the situation it was about is over.
		//
		// The timer is NOT cancelled here and does not need to be: there is one
		// per batch, `fire` consumes `blockedRetry` before it consults the idle
		// gate, and so a retry armed for a blocked episode that has since ended
		// falls through to the batch's own rules and does exactly what the batch
		// owes. Cancelling it would be a second place that decides when this
		// timer should live, and the scenarios where that mattered did not
		// survive being tested.
		b.blockedNotified = false
		b.blockedRetry = false
	default:
		// Working, or a state this file does not judge. Nothing is typed and
		// no timer runs.
		b.blockedNotified = false
		b.blockedRetry = false
		w.stopLocked(b)
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.consider(ctx, mailbox)
}

// Arrived tells the wake that mail has landed in a mailbox.
//
// It is called for the RECIPIENT of every row the record commits, and a mailbox
// it has never been told about is dropped: only a session Coordinator has
// admitted — one the record established holds workers — can be typed into.
func (w *Wake) Arrived(ctx context.Context, mailbox ReaderID) {
	if !w.admitted(mailbox) {
		return
	}
	w.consider(ctx, mailbox)
}

// Read records that a coordinator fetched its mail through a position, which
// is the only thing that clears a batch.
//
// The fetch is the coordinator's own call and the cursor is the record's, so
// this is called from the one place a cursor moves (Registrar.Inbox and
// Registrar.HeldBy, which both answer with the position they handed over) and
// carries that position. A read of a mailbox nobody admitted is dropped.
func (w *Wake) Read(mailbox ReaderID, through int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, ok := w.byMailbox[mailbox]
	if !ok {
		return
	}
	if through > b.read {
		b.read = through
	}
	// The generation bump is the point of this method: a type in flight
	// belongs to the batch that was just read, and it must land nowhere.
	b.gen++
	b.lines = 0
	b.notified = false
	b.blockedNotified = false
	b.inFlight = false
	w.stopLocked(b)
}

// consider decides what happens NOW for one coordinator: nothing, a line, or
// arming the pause that leads to the next one.
//
// The order of the checks is the rule, and each one exists for a case the
// others cannot cover. Not idle, or a type already in flight: nothing happens
// (the writer will arm). Already notified: the human has the batch. The budget
// spent: nothing happens until the pause after the last line, which is what
// raises the human. A line already typed for this batch: the next one is a
// RETRY, so it waits the pause rather than arriving while the coordinator is
// still reading the first.
func (w *Wake) consider(ctx context.Context, mailbox ReaderID) {
	w.mu.Lock()
	b, ok := w.byMailbox[mailbox]
	if !ok || b.state != ObservedIdle {
		if ok {
			w.stopLocked(b)
		}
		w.mu.Unlock()
		return
	}
	switch {
	case b.inFlight:
		// A type is being written right now. It will arm the pause when it
		// lands, so arming one here would be a second timer for one pause.
	case b.notified:
		// The human has this batch. Nothing more is typed and no timer runs.
	case b.lines >= w.attempts:
		w.armLocked(b, ctx, mailbox)
	case b.lines > 0 || b.armed:
		w.armLocked(b, ctx, mailbox)
	default:
		b.inFlight = true
		gen := b.gen
		w.mu.Unlock()
		w.typeNow(ctx, mailbox, gen)
		return
	}
	w.mu.Unlock()
}

// typeNow counts what is unread and types one line about it.
//
// It is handed the GENERATION its reservation was taken under, and every step
// below validates against that rather than against the batch's current state. A
// read that lands while this line is being decided, counted or written has
// ended the batch it was for: the mail has been seen, so a line announcing it
// is about a situation that is over — and counting its outcome would spend an
// attempt on mail the coordinator has already read, or arm a retry that types
// about nothing.
//
// The count is re-read here rather than remembered from the arrival: mail
// arrives between the decision and the write, and a line that said "1" about a
// mailbox now holding two would be wrong in the one direction that matters —
// the coordinator is being told how much is waiting.
func (w *Wake) typeNow(ctx context.Context, mailbox ReaderID, gen int) {
	n, err := w.unread(ctx, mailbox)

	w.mu.Lock()
	b, ok := w.byMailbox[mailbox]
	if !ok {
		w.mu.Unlock()
		return
	}
	// THE RESERVATION IS GIVEN BACK ON EVERY PATH THAT DOES NOT TYPE, and held
	// across the one that does. Giving it back early would reopen exactly the
	// window it exists to close: a second arrival would find `lines == 0` and
	// no writer in flight, reserve, and type a second line for one batch.
	switch {
	case b.gen != gen:
		// The coordinator read while this line was being decided or counted.
		// Nothing is typed: the batch is gone, and what is left past the new
		// read mark is a NEW batch that will get its own line.
		b.inFlight = false
		w.mu.Unlock()
		log.From(ctx).Debug("worker: the coordinator read its mail while a line was being prepared; nothing was typed",
			"session_id", string(mailbox))
		w.consider(ctx, mailbox)
		return
	case b.state != ObservedIdle:
		// The coordinator stopped being idle. Nothing is typed into a pane
		// that is not waiting for input, and the batch is unchanged.
		b.inFlight = false
		w.mu.Unlock()
		return
	case err != nil:
		b.inFlight = false
		w.mu.Unlock()
		log.From(ctx).Error("worker: a coordinator's unread mail could not be counted, so nothing was typed",
			"session_id", string(mailbox), "error", err)
		return
	case n == 0:
		// Nothing is waiting: the only mail is of a kind that never wakes,
		// and the batch is over.
		b.inFlight = false
		b.lines = 0
		b.notified = false
		w.stopLocked(b)
		w.mu.Unlock()
		return
	case w.waker == nil:
		b.inFlight = false
		w.mu.Unlock()
		log.From(ctx).Error("worker: a coordinator has unread mail and this backend has nothing to type into a pane with",
			"session_id", string(mailbox), "unread", n)
		return
	}
	w.mu.Unlock()

	out := w.waker.Wake(ctx, string(mailbox), wakeText(n))

	w.mu.Lock()
	cur, ok := w.byMailbox[mailbox]
	if !ok {
		w.mu.Unlock()
		return
	}
	cur.inFlight = false
	if cur.gen != gen {
		// The batch this write was for is gone. Its outcome is not news
		// about anything still owed: the coordinator has read, and the line
		// it may or may not have received was about mail it has seen.
		w.mu.Unlock()
		log.From(ctx).Debug("worker: the coordinator read its mail while a line was being typed; the line is not counted",
			"session_id", string(mailbox), "unread", n, "delivered", out.Delivered)
		w.consider(ctx, mailbox)
		return
	}
	if out.Delivered {
		cur.lines++
		log.From(ctx).Info("worker: the coordinator was woken for its workers' mail",
			"session_id", string(mailbox), "unread", n, "line", cur.lines, "of", w.attempts)
		// The pause is armed for a DELIVERED line only, and that is the whole
		// of "a refused type is not an attempt" (design §5.4, nocx-luqz9.3):
		// a line the pane took has not been read, so it is worth retyping;
		// one the pane REFUSED was never typed, so there is nothing to come
		// back to at a pause.
		//
		// The retry for a refusal is the next idle READING — the pane leaving
		// idle and settling idle again (Coordinator) — and that is a real
		// bound rather than a delay: a refusal means the screen changed under
		// us, so the observable that says it is safe to try again is the
		// screen having come back, not a clock that would type into whatever
		// it became.
		w.armLocked(cur, ctx, mailbox)
	} else {
		// Not an error: refusing to type into a pane showing a modal is the
		// mechanism working. What would be a defect is calling it a delivery,
		// and what would be a defect twice over is spending an attempt on it:
		// the screen changed, so the coordinator was told nothing.
		log.From(ctx).Info("worker: the coordinator was not woken, and its mail stays unread",
			"session_id", string(mailbox), "unread", n, "reason", out.Reason, "lines", cur.lines)
	}
	w.mu.Unlock()
}

// fire is the retry pause elapsing, and it TYPES the next line.
//
// It is not "arm again": the pause exists so the coordinator's turn gets its
// chance to be taken, and when that chance has elapsed unanswered the line is
// typed a second time. A fire that only re-armed would be a wake that typed
// once and then counted down to a notice about mail it had stopped announcing.
//
// It is also the only place the human is told about an unread batch: the last
// attempt's pause has gone unanswered, which is what "has not read" means. That
// is why the notice is not raised where the last line is typed — the
// coordinator has had its last chance only when the pause after it has elapsed.
func (w *Wake) fire(ctx context.Context, mailbox ReaderID) {
	w.mu.Lock()
	b, ok := w.byMailbox[mailbox]
	if !ok {
		w.mu.Unlock()
		return
	}
	w.stopLocked(b)
	// A BLOCKED NOTICE WAITING FOR ITS PAUSE COMES FIRST, and the order is
	// load-bearing: the idle gate below would return for a blocked
	// coordinator, which would consume this retry and tell nobody. It is the
	// only thing that arms a timer while the coordinator is not idle.
	if b.blockedRetry {
		b.blockedRetry = false
		if b.state != ObservedBlocked || b.live == 0 {
			w.mu.Unlock()
			return
		}
		notice := Notice{
			Mailbox: mailbox, Kind: NoticeBlocked,
			LiveWorkers: b.live, Lines: b.lines,
		}
		w.mu.Unlock()
		w.reach(ctx, mailbox, notice)
		return
	}
	if b.state != ObservedIdle || b.notified || b.inFlight {
		w.mu.Unlock()
		return
	}
	if b.lines >= w.attempts {
		notice := Notice{
			Mailbox: mailbox, Kind: NoticeUnread, LiveWorkers: b.live, Lines: b.lines,
		}
		w.mu.Unlock()
		// The count is taken HERE rather than remembered from the last line,
		// because the line was typed one pause ago and mail has arrived since:
		// a person reading "3 unread" about a mailbox holding seven would be
		// told the wrong size of the problem, and the size is the whole reason
		// they are being interrupted.
		if n, err := w.unread(ctx, mailbox); err == nil {
			notice.Unread = n
		}
		w.reach(ctx, mailbox, notice)
		return
	}
	b.inFlight = true
	gen := b.gen
	w.mu.Unlock()
	w.typeNow(ctx, mailbox, gen)
}

// reach tells the human, and keeps what happened to the attempt.
//
// A NOTICE THAT FAILED IS NOT MARKED AS MADE, for either kind, and that is the
// one rule this method exists to hold: a failure clears whichever mark the
// notice would have set, so the situation it reports is RETRIED rather than
// given up on while the record has it down as handled. What "retried" means
// differs by kind, and both answers are decided here, beside both marks, so a
// reader sees them in one place:
//
//   - an UNREAD batch is retried at the PAUSE, because the coordinator's own
//     turn is what clears that situation and a pause is the only thing about
//     it that has changed by then;
//   - a BLOCKED coordinator is retried at the PAUSE as well, and that is the
//     cadence for both: at most one attempt per pause, from one timer per
//     batch. Hanging the blocked retry off the next READING was the first
//     version of this and it was wrong — the coordinator is read once a
//     second, so a refusing pipeline would have been hit sixty times a minute
//     for as long as the block lasted.
//
// Neither is a timer invented for the failure: the pause already bounds every
// retry of a batch, and the episode ends the instant the coordinator stops
// being blocked.
//
// WHAT IT CANNOT KNOW IS DELIVERY, and it does not pretend to. The pipeline in
// front of the escalation seam is asynchronous past its debounce window
// (notify.Ingress.Raise returns an empty Outcome by design), so a notice that
// was ACCEPTED and then reached no renderer is reported through that pipeline's
// own result handler — where internal/notify's failure surface already logs it
// and records it (internal/app's own notify result handler, and
// app_notify_failure_test.go asserts it). Reading Resolved or Results here would
// report every real notice as undelivered, which is a false alarm at the one
// place that must not have one.
func (w *Wake) reach(ctx context.Context, mailbox ReaderID, n Notice) {
	err := w.tell(ctx, n)

	w.mu.Lock()
	defer w.mu.Unlock()
	b, ok := w.byMailbox[mailbox]
	if !ok {
		return
	}
	switch n.Kind {
	case NoticeBlocked:
		// Re-checked rather than assumed: a coordinator that stopped being
		// blocked while the notice was in flight has already had this episode
		// closed by the reading which saw that, and touching it now would be
		// about a situation that is over.
		if b.state != ObservedBlocked || b.live == 0 {
			b.blockedRetry = false
			return
		}
		if err == nil {
			b.blockedRetry = false
			return
		}
		// REFUSED, and the retry waits for the PAUSE. It is NOT hung off the
		// next reading: a blocked coordinator is read once a second, so a
		// retry driven by the reading would hit the pipeline that just refused
		// sixty times a minute for as long as the block lasted. The pause is
		// the same injected number that paces every other retry in this file,
		// and it is armed on the one timer this batch owns.
		//
		// There is no attempt limit after which this gives up, and that is a
		// decision rather than an omission: the notice IS the last resort, so
		// there is nobody further to escalate to and the only alternative to
		// retrying is silence. Every failure is logged at Error with its
		// reason, and the episode ends when the coordinator stops being blocked
		// or an attempt is taken.
		b.blockedRetry = true
		w.armLocked(b, ctx, mailbox)
	case NoticeUnread:
		// Re-checked for the same reason: a read that landed while the notice
		// was being raised has ended the batch.
		if b.state != ObservedIdle || b.lines < w.attempts {
			return
		}
		if err == nil {
			b.notified = true
			return
		}
		// REFUSED, so the batch is still owed and the next pause tries again.
		// The timer is armed HERE because nothing else is going to: fire stopped
		// the one that brought us here, and a mark left unarmed would be a
		// notice that gave up silently on its first refusal. armLocked is
		// idempotent, so this cannot produce a second timer for one pause.
		b.notified = false
		w.armLocked(b, ctx, mailbox)
	}
}

// tell submits one notice and reports whether the pipeline took it.
func (w *Wake) tell(ctx context.Context, n Notice) error {
	if w.human == nil {
		log.From(ctx).Error("worker: coordination has stopped and this backend has nothing to tell anyone with",
			"session_id", string(n.Mailbox), "notice", string(n.Kind),
			"unread", n.Unread, "lines", n.Lines, "live_workers", n.LiveWorkers)
		return fmt.Errorf("this backend has nothing to tell anyone with")
	}
	if err := w.human.Escalate(ctx, n); err != nil {
		log.From(ctx).Error("worker: the human was not told that coordination has stopped",
			"session_id", string(n.Mailbox), "notice", string(n.Kind),
			"unread", n.Unread, "live_workers", n.LiveWorkers, "error", err)
		return err
	}
	log.From(ctx).Warn("worker: coordination has stopped and the notice was submitted",
		"session_id", string(n.Mailbox), "notice", string(n.Kind),
		"unread", n.Unread, "lines", n.Lines, "live_workers", n.LiveWorkers)
	return nil
}

// unread counts the mail of a waking kind that sits past this coordinator's
// read mark.
//
// Pages are taken until one comes back short, because a number that capped at
// one page would be a lie in exactly the case the line exists for — a
// coordinator that has been away from a busy worker.
func (w *Wake) unread(ctx context.Context, mailbox ReaderID) (int, error) {
	if w.mail == nil {
		return 0, fmt.Errorf("this backend keeps no mailbox")
	}
	w.mu.Lock()
	b, ok := w.byMailbox[mailbox]
	if !ok {
		w.mu.Unlock()
		return 0, nil
	}
	after := b.read
	w.mu.Unlock()

	total := 0
	for {
		page, err := w.mail.Since(ctx, mailbox, after, MaxFetch)
		if err != nil {
			return 0, err
		}
		for _, m := range page {
			after = m.Seq
			if kindOf(m).Wakes() {
				total++
			}
		}
		if len(page) < MaxFetch {
			return total, nil
		}
	}
}

// armLocked schedules the retry pause. The caller holds the lock.
//
// One timer per batch, not one per attempt: a second armed while the first is
// running would type twice for one pause, and the whole point of the pause is
// that the coordinator's turn gets its chance to be taken.
func (w *Wake) armLocked(b *batch, ctx context.Context, mailbox ReaderID) {
	// `armed` is the one-timer rule and `inFlight` is "a type will arm this
	// itself when it lands". `notified` is deliberately NOT consulted: it is
	// the UNREAD mark, and a blocked notice whose retry this is may well be
	// raised while an unread notice has already been sent — refusing to arm on
	// that mark would drop the blocked retry and tell nobody. Every caller
	// tests the mark that concerns it, in the branch that decides to arm.
	if b.armed || b.inFlight {
		return
	}
	if w.alarms == nil {
		log.From(ctx).Error(
			"worker: a coordinator's mail is unread and this backend has no clock to retry on",
			"session_id", string(mailbox))
		return
	}
	b.armed = true
	// THE CONTEXT THE PAUSE INHERITS IS THE ONE IT WAS ARMED FROM, with
	// cancellation detached and nothing else: the reading that armed this is
	// over by the time the alarm fires, so the request it belonged to must not
	// be able to cancel it — and everything the context CARRIES, the logger
	// included, has to survive, or every line the retry writes lands at the
	// process root with no trace of the sweep that caused it.
	ctx = context.WithoutCancel(ctx)
	b.cancel = w.alarms.After(w.pause, func() { w.fire(ctx, mailbox) })
}

// stopLocked cancels an armed retry, if there is one. The caller holds the
// lock.
func (w *Wake) stopLocked(b *batch) {
	if !b.armed {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	b.armed = false
	b.cancel = nil
}

// admitted reports whether this mailbox belongs to a coordinator, which is what
// keeps the record's own mail — a worker's, which nobody wakes — out of this
// file's bookkeeping.
func (w *Wake) admitted(mailbox ReaderID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.byMailbox[mailbox]
	return ok
}

// batchOf is one coordinator's state, created on first sight. The caller holds
// the lock, and the only caller is Coordinator.
func (w *Wake) batchOf(mailbox ReaderID) *batch {
	b, ok := w.byMailbox[mailbox]
	if !ok {
		b = &batch{}
		w.byMailbox[mailbox] = b
	}
	return b
}

// wakeText is what nocx types into the coordinator's pane.
//
// It carries a POINTER and a COUNT, and never a word of what anybody said. The
// mail it announces is a worker's own text, and typing that into another
// agent's input region would be prompt injection performed with our own hands —
// which is why the line names the tool to call and not the thing to read.
func wakeText(n int) string {
	return fmt.Sprintf("nocx: you have %d new messages from your workers. Call workers.inbox.", n)
}
