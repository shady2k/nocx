package app

// The wake and the backstop, at the composition level (nocx-dkawo.3).
//
// What is under test here is the thing a person depends on: a coordinator
// that has gone idle is woken by the backend when its worker declares, and it
// is woken THROUGH THE SHIPPED GATES — the real grid fed from byte zero of a
// real capture, the real rule, a real calibration verdict, and the one Typist
// the agent.type method reaches. Nothing here fakes a screen, because the
// screen is the thing that decides whether a keystroke is safe, and a test
// that faked it would be asserting that a fake permits typing.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/wave"
)

// ── the stand ─────────────────────────────────────────────────────────────

// recordingRaiser is the far end of the escalation. It is the notify seam and
// not the whole pipeline: what an escalation may REACH is internal/notify's
// and is asserted there, against a routing table this test does not own.
type recordingRaiser struct{ events []notify.Event }

func (r *recordingRaiser) Raise(_ context.Context, ev notify.Event) notify.Outcome {
	r.events = append(r.events, ev)
	return notify.Outcome{}
}

// wakeStand is a wave stand plus a REAL coordinator pane: a session with a
// pty, a grid enrolled from byte zero, an observation, and a calibration
// verdict that permits typing.
type wakeStand struct {
	*waveStand
	coordinator session.ID
	coordPTY    *recordingPTY
	grid        *panegrid.Store
	rules       *agentdriver.Registry
	raiser      *recordingRaiser
	chunks      []agentcapture.Chunk
	waveID      wave.ID
	// workerSessions is every session a worker has already been given, so a
	// second registration waits for a session that did not exist yet.
	workerSessions map[session.ID]bool
}

const wakeAgent = "claude"

func newWakeStand(t *testing.T) *wakeStand {
	t.Helper()
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)

	grid := panegrid.New(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("driver registry: %v", err)
	}
	watch := paneobserve.New(logger, grid, rules)
	raiser := &recordingRaiser{}

	// The stand is built with the backstop already in it, because the record
	// is what drives the set: wiring it afterwards would let a test assert a
	// mechanism the product does not have.
	var typist *agenttyping.Typist
	waker := &waveWaker{typist: nil, log: logger}
	backstop := wave.NewBackstop(logger, waker,
		&waveEscalation{raise: raiser, log: logger},
		wave.WithFactDeadline(waveFactDeadline))
	stand := newWaveStand(t, wave.WithBackstop(backstop))

	typist = newPaneTypist(logger, grid, rules, verifiedClaude(t), watch, stand.reg)
	waker.typist = typist

	// The coordinator's own pane, opened through the SAME one session-open
	// path the product uses (nocx-dkawo.6) — no client attached, which is the
	// whole situation a wave exists for.
	opened, err := stand.tp.OpenSession(ctx, transport.OpenSpec{
		PaneID: "pane-coordinator", Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open the coordinator's session: %v", err)
	}
	coordinator := opened.Session.ID()
	coordPTY := stand.ptys.last()

	// Its grid, opened from byte zero, and its observation — the two halves of
	// one act, exactly as the pane enroller opens them.
	if enrolErr := grid.Enrol(string(coordinator), participantCols, participantRows); enrolErr != nil {
		t.Fatalf("enrol the coordinator's pane: %v", enrolErr)
	}
	t.Cleanup(func() { grid.Withdraw(string(coordinator)) })
	watch.Watch(string(coordinator), wakeAgent)

	waveID := wave.ID("wave-wake")
	stand.ensureWave(t, waveID, string(coordinator))

	//nolint:gosec // The path is two joined literals naming a corpus in the tree.
	header, chunks, err := agentcapture.Read(
		filepath.Join("..", "agentdriver", "testdata", "captures", "claude-idle.jsonl"))
	if err != nil {
		t.Fatalf("read the idle capture: %v", err)
	}
	if header.Cols != participantCols || header.Rows != participantRows {
		t.Fatalf("the capture is %dx%d and the pane is %dx%d; the frame would be wrapped differently",
			header.Cols, header.Rows, participantCols, participantRows)
	}

	return &wakeStand{
		waveStand: stand, coordinator: coordinator, coordPTY: coordPTY,
		grid: grid, rules: rules, raiser: raiser,
		chunks: chunks, waveID: waveID,
		workerSessions: map[session.ID]bool{},
	}
}

// driveTo feeds the coordinator's real grid forward through the capture until
// the shipped rule classifies it as want.
//
// It waits on the CLASSIFICATION and never on a duration: the grid is fed
// through a pipe into an emulator on its own goroutine, so a test that slept
// would be asserting that this machine is fast enough.
func (w *wakeStand) driveTo(t *testing.T, atMs int64, want agentdriver.State) {
	t.Helper()
	through := agentcapture.ChunksThrough(w.chunks, atMs, 0)
	for _, c := range w.chunks[:through] {
		w.grid.Feed(string(w.coordinator), []byte(c.Data))
	}
	waittest.WaitFor(t, "the coordinator's pane to reach "+string(want), func() bool {
		f, err := w.grid.Frame(string(w.coordinator))
		return err == nil && w.rules.Classify(wakeAgent, f) == want
	})
}

// register starts one worker in the wake stand's wave and supplies the
// enrolment its launcher would have sent.
//
// It waits for a session it has not seen before, so a second and a third
// worker are not satisfied by the first one's session — a helper that matched
// "any session that is not the coordinator" would let a fan-out test pass
// while only one worker ever started.
func (w *wakeStand) register(t *testing.T, task string) wave.Participant {
	t.Helper()
	type outcome struct {
		p   wave.Participant
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		p, err := w.record.Register(context.Background(), wave.RegisterRequest{
			Wave: w.waveID, CoordinatorSession: string(w.coordinator),
			Role: wave.RoleWorker, Task: task, Command: wakeAgent,
		})
		done <- outcome{p, err}
	}()
	var sid session.ID
	waittest.WaitFor(t, "a new worker session to exist", func() bool {
		for _, s := range w.reg.List() {
			if s.ID() == w.coordinator || w.workerSessions[s.ID()] {
				continue
			}
			sid = s.ID()
			return true
		}
		return false
	})
	w.workerSessions[sid] = true
	lane := lifecycle.LaneID("lane-worker-" + string(sid))
	w.lanes.register(lane, string(sid))
	w.enrol.enrolled(sid, string(lane))
	got := <-done
	if got.err != nil {
		t.Fatalf("register: %v", got.err)
	}
	return got.p
}

// verifiedClaude drives a REAL calibration to completion against the shipped
// claude rule, using the corpus's own frames for the three states a person is
// asked to produce. There is no other way to obtain a verdict that permits
// typing — agentcalib.Verdict's permission is unexported — so a test that
// wanted to shortcut this would be faking the gate it is here to exercise.
func verifiedClaude(t *testing.T) agenttyping.Authority {
	t.Helper()
	frames := &stepFrames{frames: []panegrid.Frame{
		captureFrame(t, "claude-idle", 11000),       // Begin: the geometry
		captureFrame(t, "claude-idle", 11000),       // idle     → free_text
		captureFrame(t, "claude-working", 17000),    // working  → working
		captureFrame(t, "claude-permission", 49000), // asks-you → permission_choice
	}}
	store, err := agentcalib.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("calibration store: %v", err)
	}
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("driver registry: %v", err)
	}
	calib := agentcalib.New(log.NewSlogAdapter(nil), frames, store, rules)
	if _, err := calib.Begin("calibration-pane", wakeAgent); err != nil {
		t.Fatalf("begin calibration: %v", err)
	}
	for i, step := range agentcalib.Steps() {
		answer := agentcalib.AnswerCapture
		if !step.Required {
			answer = agentcalib.AnswerSkip
		}
		if _, err := calib.Answer("calibration-pane", i, answer); err != nil {
			t.Fatalf("answer step %d (%s): %v", i, step.Label, err)
		}
	}
	if v := calib.Verify(wakeAgent); !v.MayType() {
		t.Fatalf("the shipped rule did not verify against the corpus it was written from: %+v", v)
	}
	return calib
}

// stepFrames hands out one frame per read, which is what a calibration walk
// is: a person drives their agent into a state and nocx labels what it sees.
type stepFrames struct {
	frames []panegrid.Frame
	at     int
}

func (s *stepFrames) Frame(string) (panegrid.Frame, error) {
	if s.at >= len(s.frames) {
		return panegrid.Frame{}, panegrid.ErrNotEnrolled
	}
	f := s.frames[s.at]
	s.at++
	return f, nil
}

func captureFrame(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	//nolint:gosec // The path is joined literals naming a corpus in the tree.
	header, chunks, err := agentcapture.Read(
		filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	moments, err := agentcapture.Frames(log.NewSlogAdapter(nil), header, chunks, []int64{atMs})
	if err != nil {
		t.Fatalf("replay %s to %dms: %v", name, atMs, err)
	}
	return moments[0].Frame
}

// ── the criterion ─────────────────────────────────────────────────────────

// THE ACCEPTANCE CRITERION, end to end through the shipped seams: a
// coordinator that has been sitting idle is woken by the backend when its
// worker declares, and the text reaches its pane without a person touching
// anything.
//
// The four assertions are ordered the way the mechanism is: nothing is armed
// while nothing is owed; the declaration puts a fact in the set; the wake
// reaches the coordinator's own pty; and the fact is STILL OWED afterwards,
// because delivery is unacknowledged and only the coordinator's own call
// closes it.
func TestAnIdleCoordinatorIsWokenWhenItsWorkerDeclares(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	w.driveTo(t, 11000, agentdriver.StateFreeText)

	p := w.register(t, "read AGENTS.md and report")
	if got := len(w.record.Undispatched()); got != 0 {
		t.Fatalf("undispatched facts while the worker is still working = %d, want 0", got)
	}

	// The worker declares over the authenticated channel. Nothing about the
	// coordinator is asked to have happened first: it has been idle since it
	// spawned, which is the normal state and not the failure.
	before := len(w.coordPTY.read())
	if _, err := w.record.Declared(ctx, p.ID, p.Liveness,
		wave.Declaration{OK: true, Summary: "read it", At: time.Now()}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	// The bytes travel the SAME queue a person's keystrokes take, and the
	// queue drains on the session's own write loop — so this waits on the
	// pty having them rather than on a duration.
	// The condition is the WHOLE wake — the paste and the submit key that
	// follows it as a separate write. Waiting on the text alone would be
	// satisfied by the paste, which is the state in which the coordinator is
	// looking at an unsent line and no turn has started.
	var typed string
	waittest.WaitFor(t, "the wake and its submit key to reach the coordinator's pty", func() bool {
		typed = w.coordPTY.read()[before:]
		return strings.Contains(typed, string(p.ID)) && strings.HasSuffix(typed, "\r")
	})
	if !strings.Contains(typed, string(p.ID)) {
		t.Fatalf("the coordinator's pane was not told which worker: %q", typed)
	}
	if !strings.Contains(typed, "wave.holdings") {
		t.Fatalf("the coordinator's pane was not told what to call: %q", typed)
	}
	// The submit key, sent SEPARATELY so it cannot be swallowed as paste
	// content. Without it the coordinator is looking at an unsent line and no
	// turn starts, which is the difference between typing and waking.
	if !strings.HasSuffix(typed, "\r") {
		t.Fatalf("nothing submitted the wake, so no turn starts: %q", typed)
	}

	open := w.record.Undispatched()
	if len(open) != 1 || !open[0].Wake.Delivered {
		t.Fatalf("undispatched = %+v, want one fact recording a delivered wake", open)
	}

	// And the coordinator's own call is what closes it.
	if _, err := w.record.HeldBy(ctx, string(w.coordinator)); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := len(w.record.Undispatched()); got != 0 {
		t.Fatalf("undispatched after the coordinator asked = %d, want 0", got)
	}
}

// A pane that is not waiting for input receives NOTHING AT ALL, and the
// refusal is recorded with its reason rather than reported as sent.
//
// This is the hazard the whole typing gate exists for: a keystroke into a
// permission menu does not merely fail to deliver, it ANSWERS the menu, which
// can approve a tool call the person never saw.
func TestACoordinatorThatIsNotWaitingForInputIsNotTyped(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	// Far enough into the same capture that the pane is no longer idle.
	w.driveTo(t, 11000, agentdriver.StateFreeText)
	p := w.register(t, "reports into a busy coordinator")

	// Now drive the coordinator into a state the rule refuses.
	busy := replayInto(t, w, "claude-working", 17000, agentdriver.StateWorking)
	if !busy {
		t.Fatalf("the corpus did not drive the pane to working")
	}

	before := len(w.coordPTY.read())
	if _, err := w.record.Declared(ctx, p.ID, p.Liveness,
		wave.Declaration{OK: true, Summary: "done", At: time.Now()}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := w.coordPTY.read()[before:]; got != "" {
		t.Fatalf("nocx typed %q into a pane that was not waiting for input", got)
	}

	open := w.record.Undispatched()
	if len(open) != 1 {
		t.Fatalf("undispatched = %d, want the refused fact still owed", len(open))
	}
	if open[0].Wake.Delivered {
		t.Fatalf("a refusal was recorded as a delivery: %+v", open[0].Wake)
	}
	if !strings.Contains(open[0].Wake.Reason, "waiting for input") {
		t.Fatalf("the refusal does not say why: %q", open[0].Wake.Reason)
	}

	// The deadline is running under the refusal, and where it goes when it
	// elapses is asserted below — the elapsing itself belongs to
	// internal/wave, which owns the set and the alarm.
}

// The far end of the backstop: what the person is actually told.
//
// The escalation is the composition root's adapter — it stamps an event and
// hands it to ingress — so this asserts the stamping. Where an attested event
// may REACH is internal/notify's, enforced default-deny against a routing
// table this test does not own and must not restate.
func TestTheEscalationTellsThePersonWhichCoordinatorAndWhy(t *testing.T) {
	raiser := &recordingRaiser{}
	esc := &waveEscalation{raise: raiser, log: log.NewSlogAdapter(nil)}

	t.Run("the coordinator was never reached", func(t *testing.T) {
		raiser.events = nil
		esc.Escalate(context.Background(), wave.Fact{
			Participant: "p-1", Wave: "wave-wake", CoordinatorSession: "sess-coordinator",
			Kind: wave.FactDeclared, State: wave.StateLive, Task: "read AGENTS.md and report",
			Wake: wave.WakeOutcome{Attempted: true, Reason: "that pane is working"},
		})
		if len(raiser.events) != 1 {
			t.Fatalf("events = %d, want 1", len(raiser.events))
		}
		ev := raiser.events[0]
		if ev.Kind != notify.KindWaveUndispatched || ev.Trust != notify.TrustAttested {
			t.Fatalf("event = %+v, want an attested wave.undispatched", ev)
		}
		// The coordinator's pane and not the worker's: the fact is about a
		// worker, and the decision is the coordinator's, so a notification
		// that opened the worker's pane would show the screen that is NOT
		// waiting for anybody.
		if ev.SessionID != "sess-coordinator" {
			t.Fatalf("the notification names session %q, want the coordinator's", ev.SessionID)
		}
		if !strings.Contains(ev.Body, "could not reach the coordinator") ||
			!strings.Contains(ev.Body, "that pane is working") {
			t.Fatalf("the person is not told the coordinator was never reached, or why: %q", ev.Body)
		}
		if !strings.Contains(ev.Title, "read AGENTS.md and report") {
			t.Fatalf("the person is not told what the worker was doing: %q", ev.Title)
		}
	})

	t.Run("the coordinator was reached and did nothing", func(t *testing.T) {
		raiser.events = nil
		esc.Escalate(context.Background(), wave.Fact{
			Participant: "p-1", CoordinatorSession: "sess-coordinator",
			Task: "read AGENTS.md and report",
			Wake: wave.WakeOutcome{Attempted: true, Delivered: true},
		})
		// A different sentence, because it asks the person for a different
		// thing: nobody is stuck on a modal, the coordinator simply has not
		// acted.
		if !strings.Contains(raiser.events[0].Body, "has not acted") {
			t.Fatalf("body = %q", raiser.events[0].Body)
		}
	})
}

// replayInto feeds a second capture onto the coordinator's live grid and
// reports whether the rule reached want. It is a second capture on the SAME
// grid deliberately: a pane's state changes under a grid that was opened once,
// which is exactly what the interval means.
func replayInto(t *testing.T, w *wakeStand, name string, atMs int64, want agentdriver.State) bool {
	t.Helper()
	//nolint:gosec // The path is joined literals naming a corpus in the tree.
	_, chunks, err := agentcapture.Read(
		filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	through := agentcapture.ChunksThrough(chunks, atMs, 0)
	for _, c := range chunks[:through] {
		w.grid.Feed(string(w.coordinator), []byte(c.Data))
	}
	reached := false
	waittest.WaitFor(t, "the coordinator's pane to reach "+string(want), func() bool {
		f, ferr := w.grid.Frame(string(w.coordinator))
		if ferr != nil {
			return false
		}
		reached = w.rules.Classify(wakeAgent, f) == want
		return reached
	})
	return reached
}

// A wave whose coordinator's pane nocx never watched is the honest refusal at
// the other end: there is no rule to ask about that pane, so nothing is typed
// into it, and the fact is still owed.
func TestAWaveWhoseCoordinatorPaneIsNotWatchedIsNotTyped(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	w.driveTo(t, 11000, agentdriver.StateFreeText)
	p := w.register(t, "reports into an unwatched coordinator")

	// The person closed the agent in that pane: the observation ends with it.
	w.grid.Withdraw(string(w.coordinator))

	before := len(w.coordPTY.read())
	if _, err := w.record.Declared(ctx, p.ID, p.Liveness,
		wave.Declaration{OK: true, At: time.Now()}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if got := w.coordPTY.read()[before:]; got != "" {
		t.Fatalf("nocx typed %q into a pane it has no live screen for", got)
	}
	open := w.record.Undispatched()
	if len(open) != 1 || open[0].Wake.Delivered {
		t.Fatalf("undispatched = %+v, want one fact recording a refusal", open)
	}
	if open[0].Wake.Reason == "" {
		t.Fatalf("the refusal carries no reason")
	}
}

// ── the outcome translation ───────────────────────────────────────────────

// fakeTypist answers with one outcome. It is here for the ONE case the real
// gates cannot be driven into from a capture — a paste the pane took and a
// submit key it did not — and that case is the whole reason this translation
// is not a two-line switch nobody read.
type fakeTypist struct{ res agenttyping.Result }

func (f fakeTypist) Submit(string, string) agenttyping.Result { return f.res }

// ONLY a submission is a delivery.
//
// Text that reached the input region without its submit key starts no turn:
// the coordinator is looking at an unsent line, which is indistinguishable
// from having been told nothing. Calling that delivered is exactly the
// "reported as sent" the record refuses, and it is the one outcome that looks
// like a success from inside the typist.
func TestOnlyASubmittedWakeCountsAsDelivered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		res       agenttyping.Result
		delivered bool
		says      string
	}{
		{
			name:      "submitted",
			res:       agenttyping.Result{Outcome: agenttyping.OutcomeSubmitted},
			delivered: true,
		},
		{
			name: "typed but never submitted",
			res: agenttyping.Result{
				Outcome: agenttyping.OutcomeTyped,
				Reason:  "that pane is not accepting input at the moment, so nothing was written",
			},
			says: "not accepting input",
		},
		{
			name: "refused",
			res: agenttyping.Result{
				Outcome: agenttyping.OutcomeRefused,
				Reason:  "nocx is not watching that pane, so there is no rule to ask about it",
			},
			says: "not watching that pane",
		},
		{
			name: "refused with no sentence",
			res:  agenttyping.Result{Outcome: agenttyping.OutcomeRefused, State: "unknown"},
			says: "unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &waveWaker{typist: fakeTypist{res: tc.res}, log: log.NewSlogAdapter(nil)}
			got := w.Wake(context.Background(), "sess-coordinator", "wake up")
			if got.Delivered != tc.delivered {
				t.Fatalf("delivered = %v, want %v (%+v)", got.Delivered, tc.delivered, got)
			}
			if tc.delivered {
				return
			}
			if got.Reason == "" {
				t.Fatalf("a refusal carries no reason, so the record cannot tell it from a delivery")
			}
			if !strings.Contains(got.Reason, tc.says) {
				t.Fatalf("reason = %q, want it to say %q", got.Reason, tc.says)
			}
		})
	}
}

// A wave whose record holds no coordinator session has nowhere to type, and
// that is a named refusal rather than a write addressed to the empty string —
// which the typist would answer for whichever pane happens to be keyed by it.
func TestAWaveWithNoCoordinatorSessionIsARefusalAndNotAWrite(t *testing.T) {
	w := &waveWaker{
		typist: fakeTypist{res: agenttyping.Result{Outcome: agenttyping.OutcomeSubmitted}},
		log:    log.NewSlogAdapter(nil),
	}
	got := w.Wake(context.Background(), "", "wake up")
	if got.Delivered {
		t.Fatalf("a wake with no coordinator to address was reported as delivered: %+v", got)
	}
	if !strings.Contains(got.Reason, "no coordinator session") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

// A DEAD PANE: the coordinator's session is gone while nocx still holds a
// screen for it.
//
// This is the third of the bead's three refusals and the only one where the
// screen says yes: the frame is still free_text, so the gate that stops the
// other two lets this one through, and what refuses it is the pane's own
// input queue. Recording it as a delivery would leave a fact whose deadline
// never fired for a coordinator that no longer exists.
func TestACoordinatorWhoseSessionIsGoneIsNotReportedAsWoken(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	w.driveTo(t, 11000, agentdriver.StateFreeText)
	p := w.register(t, "reports into a coordinator that is gone")

	if err := w.reg.Close(w.coordinator); err != nil {
		t.Fatalf("close the coordinator's session: %v", err)
	}
	waittest.WaitFor(t, "the coordinator's session to leave the registry", func() bool {
		_, err := w.reg.Get(w.coordinator)
		return err != nil
	})

	if _, err := w.record.Declared(ctx, p.ID, p.Liveness,
		wave.Declaration{OK: true, At: time.Now()}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	open := w.record.Undispatched()
	if len(open) != 1 {
		t.Fatalf("undispatched = %d, want the fact still owed", len(open))
	}
	if open[0].Wake.Delivered {
		t.Fatalf("a wake into a session that no longer exists was recorded as delivered: %+v", open[0].Wake)
	}
	if open[0].Wake.Reason == "" {
		t.Fatalf("the refusal carries no reason")
	}
}

// ── fan-out, through the real seams (nocx-dkawo.4) ────────────────────────

// THREE WORKERS RUN, and the coordinator's pane stays quiet until the wave
// arrives.
//
// This is the routing table asserted where a person would feel it: three
// real sessions in three real panes, and the coordinator's own pty receiving
// nothing at all for the first two completions and the wake for the third.
// The unit tests decide the table; this decides that the table is the one
// wired into the product.
func TestThreeWorkersRunAndTheCoordinatorIsWokenOnceAtTheEnd(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	w.driveTo(t, 11000, agentdriver.StateFreeText)

	workers := []wave.Participant{
		w.register(t, "read AGENTS.md"),
		w.register(t, "read the architecture"),
		w.register(t, "read the vision"),
	}
	if workers[0].ID == workers[1].ID || workers[1].ID == workers[2].ID {
		t.Fatalf("three registrations produced fewer than three participants: %v", workers)
	}

	quiet := len(w.coordPTY.read())
	finishWorker(t, w, workers[0])
	finishWorker(t, w, workers[1])
	if got := w.coordPTY.read()[quiet:]; got != "" {
		t.Fatalf("the coordinator was typed into while a worker was still running: %q", got)
	}
	if got := len(w.record.Undispatched()); got != 0 {
		t.Fatalf("undispatched while a worker is still running = %d, want 0", got)
	}

	finishWorker(t, w, workers[2])
	var typed string
	waittest.WaitFor(t, "the wave's end to reach the coordinator's pty", func() bool {
		typed = w.coordPTY.read()[quiet:]
		return strings.HasSuffix(typed, "\r")
	})
	if !strings.Contains(typed, "wave.holdings") {
		t.Fatalf("the coordinator was not told what to call: %q", typed)
	}

	// One wake for the wave, not one per fact: the third worker produces both
	// a declaration and an exit, and both need judgement once nothing else is
	// running.
	if strings.Count(typed, "wave.holdings") != 1 {
		t.Fatalf("the coordinator was woken %d times for one wave: %q",
			strings.Count(typed, "wave.holdings"), typed)
	}

	// And the number the design is judged by is a read, not a guess.
	cost := w.record.Cost()
	if cost.Facts() != 6 {
		t.Fatalf("facts = %d, want six (three workers, two facts each)", cost.Facts())
	}
	if cost.Routine != 4 {
		t.Fatalf("routine = %d, want the four facts nobody was woken for", cost.Routine)
	}
	if cost.Woken != 1 {
		t.Fatalf("woken = %d, want one delivered wake", cost.Woken)
	}
	if cost.Escalated != 0 {
		t.Fatalf("escalated = %d; the coordinator was reached, so nobody should have been", cost.Escalated)
	}

	if _, err := w.record.HeldBy(ctx, string(w.coordinator)); err != nil {
		t.Fatalf("held by: %v", err)
	}
	if got := len(w.record.Undispatched()); got != 0 {
		t.Fatalf("undispatched after the coordinator asked = %d, want 0", got)
	}
}

// finishWorker declares success and closes the worker's real session, which
// is the two facts arriving the way the product produces them.
func finishWorker(t *testing.T, w *wakeStand, p wave.Participant) {
	t.Helper()
	ctx := context.Background()
	if _, err := w.record.Declared(ctx, p.ID, p.Liveness,
		wave.Declaration{OK: true, Summary: "done", At: time.Now()}); err != nil {
		t.Fatalf("declare %s: %v", p.ID, err)
	}
	if err := w.reg.Close(session.ID(p.Liveness.SessionID)); err != nil {
		t.Fatalf("close %s: %v", p.ID, err)
	}
	waittest.WaitFor(t, "the worker's exit to reach the record", func() bool {
		stored, err := w.waves.Participant(ctx, p.ID)
		return err == nil && stored.State == wave.StateCompleted
	})
}

// One card per wave, and the card says how many — so a person who reads it
// knows the other four were deliberately not raised rather than lost.
func TestTheEscalationSaysHowManyOthersAreWaiting(t *testing.T) {
	raiser := &recordingRaiser{}
	esc := &waveEscalation{raise: raiser, log: log.NewSlogAdapter(nil)}
	for _, tc := range []struct {
		also int
		says string
	}{
		{also: 0, says: ""},
		{also: 1, says: "One other worker"},
		{also: 4, says: "4 other workers"},
	} {
		raiser.events = nil
		esc.Escalate(context.Background(), wave.Fact{
			Participant: "p-1", CoordinatorSession: "sess-coordinator", Task: "read it",
			AlsoOwed: tc.also, Wake: wave.WakeOutcome{Attempted: true, Delivered: true},
		})
		body := raiser.events[0].Body
		if tc.says == "" {
			if strings.Contains(body, "also waiting") {
				t.Fatalf("a single-fact card counts others that are not there: %q", body)
			}
			continue
		}
		if !strings.Contains(body, tc.says) {
			t.Fatalf("body = %q, want it to say %q", body, tc.says)
		}
	}
}

// ── the epic's sentence, through the real seams (nocx-dkawo.13) ───────────

// ONE WAIT ON THREE WORKERS, and a close that ends the rest.
//
// This is the shape the epic's DONE WHEN names — "creates three workers,
// gives each a task, waits on all three with ONE wait that returns when the
// first settles, reads what each produced, and closes them" — driven through
// real sessions in real panes. What is missing from it is a MODEL: the calls
// a coordinator would make are asserted where they live, and this is the
// sequence they drive.
func TestOneWaitReturnsWhenTheFirstOfThreeSettlesAndACloseEndsTheRest(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	w.driveTo(t, 11000, agentdriver.StateFreeText)

	workers := []wave.Participant{
		w.register(t, "read AGENTS.md"),
		w.register(t, "read the architecture"),
		w.register(t, "read the vision"),
	}

	// One wait, on the wave and not on a worker.
	waited := make(chan []wave.Participant, 1)
	go func() {
		held, err := w.record.Wait(ctx, string(w.coordinator), w.waveID)
		if err != nil {
			t.Errorf("wait: %v", err)
		}
		waited <- held
	}()

	finishWorker(t, w, workers[0])

	var held []wave.Participant
	select {
	case held = <-waited:
	case <-time.After(15 * time.Second):
		t.Fatal("the wait never returned, so the first settling reached nobody")
	}
	var settled, live int
	for _, p := range held {
		if p.State.Terminal() {
			settled++
		} else {
			live++
		}
	}
	if settled != 1 || live != 2 {
		t.Fatalf("the wait returned %d settled and %d live, want 1 and 2", settled, live)
	}

	// And it read what that one produced.
	for _, p := range held {
		if p.ID != workers[0].ID {
			continue
		}
		if p.State != wave.StateCompleted {
			t.Fatalf("the settled worker is %q, want completed", p.State)
		}
		if p.Declared == nil || p.Declared.Summary != "done" {
			t.Fatalf("the coordinator was not told what it produced: %+v", p.Declared)
		}
	}

	// Now close the other two. The close ends the session and writes no
	// state; what terminalizes them is the exit that follows, by the same
	// path any exit takes.
	for _, p := range workers[1:] {
		if err := w.record.Close(ctx, string(w.coordinator), p.ID); err != nil {
			t.Fatalf("close %s: %v", p.ID, err)
		}
	}
	for _, p := range workers[1:] {
		waittest.WaitFor(t, "the closed worker's exit to reach the record", func() bool {
			stored, err := w.waves.Participant(ctx, p.ID)
			return err == nil && stored.State.Terminal()
		})
		stored, err := w.waves.Participant(ctx, p.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		// ABANDONED and not completed: the worker was ended and never said
		// what it produced, which is exactly what the record should say
		// about a worker somebody stopped.
		if stored.State != wave.StateAbandoned {
			t.Fatalf("a closed worker is %q, want abandoned", stored.State)
		}
	}
	if _, err := w.reg.Get(session.ID(workers[1].Liveness.SessionID)); err == nil {
		t.Fatalf("the closed worker's session is still in the registry")
	}
}

// A coordinator cannot close somebody else's worker, and the refusal comes
// from the DELEGATION rather than from anything the caller said.
func TestACoordinatorCannotCloseAWorkerItDoesNotHold(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)
	w.driveTo(t, 11000, agentdriver.StateFreeText)
	p := w.register(t, "belongs to this coordinator")

	if err := w.record.Close(ctx, "sess-somebody-else", p.ID); !errors.Is(err, wave.ErrNotDelegated) {
		t.Fatalf("close by a stranger = %v, want ErrNotDelegated", err)
	}
	if _, err := w.reg.Get(session.ID(p.Liveness.SessionID)); err != nil {
		t.Fatalf("a refused close ended the worker's session anyway: %v", err)
	}
}
