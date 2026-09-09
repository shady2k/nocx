package agenttyping_test

// NOCX TYPES INTO A PANE, AND ONLY WHEN THE DRIVER SAYS IT MAY (nocx-dkawo.1,
// D14 of the orchestration mechanism design).
//
// These tests are about what the package REFUSES. A mistimed keystroke does
// not merely fail to arrive: it answers whatever modal is on screen, and the
// modal a coding agent puts up is a tool approval whose first option is Yes.
// So the sweep below is per state, off the real corpus in
// internal/agentdriver/testdata/captures, and it asserts twice for each — that
// nothing reached the pane's input, and that the refusal names the state that
// refused.
//
// The paired positive is the other half and is not optional: a rule that has
// EARNED typing authority, on a pane the same rule reads as free_text, does
// receive the bytes. Without it this file would pass on a package that refuses
// everything.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

const (
	pane  = "0123456789abcdef0123456789abcdef"
	agent = "claude"
)

// ── the doubles, each a real seam and none of them a stand-in for a gate ──

// screens is the pane's live grid, and it is the seam that lets a test move
// the screen UNDER a decision that has already been taken — which is the whole
// of what a staleness window is. changeAfter(n, f) makes the n-th read the last
// one to see the screen it had.
type screens struct {
	frame panegrid.Frame
	err   error
	reads int
	then  map[int]panegrid.Frame
}

func (s *screens) Frame(string) (panegrid.Frame, error) {
	if s.err != nil {
		return panegrid.Frame{}, s.err
	}
	f := s.frame
	s.reads++
	if next, ok := s.then[s.reads]; ok {
		s.frame = next
	}
	return f, nil
}

func (s *screens) changeAfter(n int, f panegrid.Frame) {
	if s.then == nil {
		s.then = map[int]panegrid.Frame{}
	}
	s.then[n] = f
}

// enrolled is the enrolment act's answer: which agent this pane was enrolled
// under. Empty means nocx is not watching it.
type enrolled struct{ agent map[string]string }

func (e enrolled) AgentOn(paneID string) (string, bool) {
	a, ok := e.agent[paneID]
	return a, ok
}

// queue is the pane's own input queue. It records what arrived, in the order
// it arrived, so a test can say "nothing at all" and mean it.
type queue struct {
	jobs   [][]byte
	refuse bool
}

func (q *queue) Accept(paneID string, b []byte) bool {
	if q.refuse {
		return false
	}
	q.jobs = append(q.jobs, append([]byte(nil), b...))
	return true
}

func (q *queue) all() string {
	var b strings.Builder
	for _, j := range q.jobs {
		b.Write(j)
	}
	return b.String()
}

// unverified is the verdict every path that is not a completed, agreeing
// calibration produces. It is the ZERO verdict deliberately: agentcalib's
// mayType is unexported and written in one statement, so this is the only
// thing a caller outside that package can build.
type unverified struct{}

func (unverified) Verify(string) agentcalib.Verdict { return agentcalib.Verdict{} }

// ── the wiring, as the composition root assembles it ──────────────────────

func rulesOf(t *testing.T) *agentdriver.Registry {
	t.Helper()
	r, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

// typistOn builds the production wiring with one screen in front of it.
func typistOn(t *testing.T, f panegrid.Frame, auth agenttyping.Authority) (*agenttyping.Typist, *screens, *queue) {
	t.Helper()
	sc := &screens{frame: f}
	q := &queue{}
	ty := agenttyping.New(log.NewSlogAdapter(nil), sc, rulesOf(t), auth,
		enrolled{agent: map[string]string{pane: agent}}, q)
	return ty, sc, q
}

// ── THE REFUSAL, PER STATE, OFF THE REAL CORPUS ───────────────────────────

// Every state that is not free_text receives NOTHING, and the refusal names
// the state that refused. The screens are the corpus's own — a permission
// dialog whose selected row uses the same glyph as the input marker, a turn in
// flight, a menu the person opened, the TUI's own error chrome, a pane blocked
// on a background agent, and a screen the rule cannot read at all.
func TestEveryStateThatIsNotFreeTextReceivesNothing(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		atMs    int64
		want    agentdriver.State
	}{
		{"a tool-approval dialog", "claude-permission", 49000, agentdriver.StatePermissionChoice},
		{"a menu the person opened", "claude-modal", 20000, agentdriver.StateModalChoice},
		{"a turn in flight", "claude-working", 17000, agentdriver.StateWorking},
		{"the TUI's own error chrome", "claude-error", 41000, agentdriver.StateError},
		{"blocked on a background agent", "claude-subagent", 30000, agentdriver.StateWorking},
		{"a screen before the TUI has drawn", "claude-idle", 0, agentdriver.StateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := replay(t, tc.capture, tc.atMs)
			// The rule is asked first, so this test cannot pass because the
			// corpus moved under it: the state it asserts the refusal names
			// is the state the shipped rule actually answers here.
			if got := rulesOf(t).Classify(agent, f); got != tc.want {
				t.Fatalf("the corpus classifies as %q, and this case is written for %q", got, tc.want)
			}
			ty, _, q := typistOn(t, f, verifiedFor(t))
			got := ty.Submit(pane, "wake up")
			if got.Outcome != agenttyping.OutcomeRefused {
				t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
			}
			if got.State != tc.want {
				t.Fatalf("the refusal names state %q, want the state that refused, %q", got.State, tc.want)
			}
			if got.Reason == "" {
				t.Fatal("a refusal nobody can read is how this degrades into typing blindly")
			}
			if len(q.jobs) != 0 {
				t.Fatalf("%d byte jobs reached the pane, want none: %q", len(q.jobs), q.all())
			}
		})
	}
}

// ── AND THE PAIRED POSITIVE ───────────────────────────────────────────────

// A verified rule on a free_text pane receives the text, framed as a bracketed
// paste, and the submit key as a SEPARATE write so it cannot be swallowed as
// paste content.
func TestAVerifiedRuleOnAFreeTextPaneReceivesTheTextAndASeparateSubmitKey(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))

	got := ty.Submit(pane, "wake up")
	if got.Outcome != agenttyping.OutcomeSubmitted {
		t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Reason, agenttyping.OutcomeSubmitted)
	}
	if got.State != agentdriver.StateFreeText {
		t.Fatalf("state = %q, want %q", got.State, agentdriver.StateFreeText)
	}
	if len(q.jobs) != 2 {
		t.Fatalf("%d writes reached the pane, want the text and the submit key as two: %q",
			len(q.jobs), q.all())
	}
	if want := "\x1b[200~wake up\x1b[201~"; string(q.jobs[0]) != want {
		t.Fatalf("first write = %q, want the bracketed paste %q", q.jobs[0], want)
	}
	if string(q.jobs[1]) != "\r" {
		t.Fatalf("second write = %q, want the submit key on its own", q.jobs[1])
	}
}

// Typing without submitting is the other half of the same primitive: the text
// lands in the input region and no key answers anything.
func TestTypeWithoutSubmitSendsNoSubmitKey(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))

	got := ty.Type(pane, "wake up")
	if got.Outcome != agenttyping.OutcomeTyped {
		t.Fatalf("outcome = %q (%s), want %q", got.Outcome, got.Reason, agenttyping.OutcomeTyped)
	}
	if len(q.jobs) != 1 {
		t.Fatalf("%d writes reached the pane, want only the text: %q", len(q.jobs), q.all())
	}
}

// ── THE SECOND GATE: AUTHORITY IS EARNED, NOT ASSUMED ─────────────────────

// The same free_text screen, and a rule that has not earned typing authority.
// Nothing is written, and the reason is the verdict's own.
func TestAnUnverifiedRuleTypesNothingIntoAPaneItReadsAsFreeText(t *testing.T) {
	f := replay(t, "claude-idle", 11000)
	if got := rulesOf(t).Classify(agent, f); got != agentdriver.StateFreeText {
		t.Fatalf("the corpus classifies as %q, and this test needs free_text", got)
	}
	ty, _, q := typistOn(t, f, unverified{})

	got := ty.Submit(pane, "wake up")
	if got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q — an unverified rule may light a dot and may not type",
			got.Outcome, agenttyping.OutcomeRefused)
	}
	if got.State != agentdriver.StateUnknown {
		t.Fatalf("state = %q; without a verified rule nocx does not know what the pane is, and unknown is busy",
			got.State)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("%d writes reached the pane, want none: %q", len(q.jobs), q.all())
	}
}

// ── THE INTERVAL, AND THE END THAT CLOSES IT ──────────────────────────────

// The decision is re-taken from a frame read immediately before each write, so
// a screen that changes between the grant and the submit key stops the submit
// key. The text is already in the input region and is reported as such —
// partial, named, and recoverable by the person looking at it.
func TestAScreenThatChangesAfterTheTextStopsTheSubmitKey(t *testing.T) {
	ty, sc, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	// Read 1 grants, read 2 admits the text. The dialog is up by read 3,
	// which is the one that gates the submit key — the byte that would
	// answer it.
	sc.changeAfter(2, replay(t, "claude-permission", 49000))

	got := ty.Submit(pane, "wake up")
	if got.Outcome != agenttyping.OutcomeTyped {
		t.Fatalf("outcome = %q, want %q — the text landed and the submit key must not",
			got.Outcome, agenttyping.OutcomeTyped)
	}
	if got.State != agentdriver.StatePermissionChoice {
		t.Fatalf("the refusal names %q, want the state that refused", got.State)
	}
	if len(q.jobs) != 1 {
		t.Fatalf("%d writes reached the pane, want only the text: %q", len(q.jobs), q.all())
	}
}

// And the same at the other end of the same interval: a screen that changed
// between the grant and the FIRST write sends nothing at all.
func TestAScreenThatChangesBeforeTheFirstWriteSendsNothing(t *testing.T) {
	ty, sc, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	// Read 1 grants; the dialog is up by read 2, which is the one that gates
	// the first write.
	sc.changeAfter(1, replay(t, "claude-permission", 49000))

	got := ty.Submit(pane, "wake up")
	if got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("%d writes reached the pane, want none: %q", len(q.jobs), q.all())
	}
}

// ── THE PANE NOCX IS NOT WATCHING ─────────────────────────────────────────

// A pane nobody enrolled has no agent, no rule and no frame nocx was fed from
// byte zero. It receives nothing, and the reason says why rather than naming a
// state nothing could have read.
func TestAPaneNocxIsNotWatchingReceivesNothing(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))

	got := ty.Submit("ffffffffffffffffffffffffffffffff", "wake up")
	if got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
	}
	if got.State != agentdriver.StateUnknown {
		t.Fatalf("state = %q, want %q", got.State, agentdriver.StateUnknown)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("%d writes reached a pane nocx is not watching: %q", len(q.jobs), q.all())
	}
}

// A pane that IS enrolled but whose grid has gone — the ordinary race with a
// session ending — receives nothing rather than a guess.
func TestAPaneWithNoLiveScreenReceivesNothing(t *testing.T) {
	ty, sc, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	sc.err = panegrid.ErrNotEnrolled

	got := ty.Submit(pane, "wake up")
	if got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("%d writes reached a pane with no screen: %q", len(q.jobs), q.all())
	}
}

// ── WHAT MAY BE TYPED ─────────────────────────────────────────────────────

// The escape that would end a bracketed paste early, and every other C0
// control, is refused whole rather than stripped: a submission nocx edited is
// a submission nobody wrote.
func TestTextCarryingAControlCharacterIsRefusedWhole(t *testing.T) {
	// The last one is the one that is easy to miss: a terminal decoding UTF-8
	// reads U+009B as CSI, so an escape sequence can be spelled without the
	// byte 0x1b appearing anywhere in the text.
	for _, bad := range []string{"wake\x1b[201~ up", "wake\x00up", "wake\x07up", "wake\u009b201~up"} {
		ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
		got := ty.Submit(pane, bad)
		if got.Outcome != agenttyping.OutcomeRefused {
			t.Fatalf("%q: outcome = %q, want %q", bad, got.Outcome, agenttyping.OutcomeRefused)
		}
		if len(q.jobs) != 0 {
			t.Fatalf("%q: %d writes reached the pane: %q", bad, len(q.jobs), q.all())
		}
	}
}

func TestEmptyTextIsRefused(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	if got := ty.Submit(pane, "   "); got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("%d writes reached the pane for an empty submission", len(q.jobs))
	}
}

func TestTextBeyondTheBoundIsRefused(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	got := ty.Submit(pane, strings.Repeat("a", agenttyping.MaxText+1))
	if got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("%d writes reached the pane for an oversized submission", len(q.jobs))
	}
}

// ── THE QUEUE'S OWN REFUSAL ───────────────────────────────────────────────

// The pane's input queue refuses — the bootstrap window's quarantine, or a
// full queue — and the answer says so rather than reporting a submission that
// never happened.
func TestAQueueThatRefusesIsReportedAsARefusal(t *testing.T) {
	ty, _, q := typistOn(t, replay(t, "claude-idle", 11000), verifiedFor(t))
	q.refuse = true
	if got := ty.Submit(pane, "wake up"); got.Outcome != agenttyping.OutcomeRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, agenttyping.OutcomeRefused)
	}
}

// ── the verified verdict, produced the only way there is ──────────────────

// verifiedFor drives a REAL calibration to completion against the shipped
// claude rule, using the corpus's own frames for the three states a person is
// asked to produce. There is no other way to obtain a verdict that permits
// typing: agentcalib.Verdict.mayType is unexported and written in exactly one
// statement, so a test that wanted to fake this would be faking the gate.
func verifiedFor(t *testing.T) agenttyping.Authority {
	t.Helper()
	// One frame per read: Begin, then the three required captures. The
	// optional three are declined, and a decline reads nothing.
	walkScreens := &stepScreens{frames: []panegrid.Frame{
		replay(t, "claude-idle", 11000),       // Begin: the geometry
		replay(t, "claude-idle", 11000),       // idle     → free_text
		replay(t, "claude-working", 17000),    // working  → working
		replay(t, "claude-permission", 49000), // asks-you → permission_choice
	}}
	calib := agentcalib.New(log.NewSlogAdapter(nil), walkScreens,
		mustFileStore(t, t.TempDir()), rulesOf(t))
	if _, err := calib.Begin(pane, agent); err != nil {
		t.Fatalf("begin calibration: %v", err)
	}
	for i, step := range agentcalib.Steps() {
		answer := agentcalib.AnswerCapture
		if !step.Required {
			answer = agentcalib.AnswerSkip
		}
		if _, err := calib.Answer(pane, i, answer); err != nil {
			t.Fatalf("answer step %d (%s): %v", i, step.Label, err)
		}
	}
	if v := calib.Verify(agent); !v.MayType() {
		t.Fatalf("the shipped rule did not verify against the corpus it was written from: %+v", v)
	}
	return calib
}

func mustFileStore(t *testing.T, root string) agentcalib.Store {
	t.Helper()
	s, err := agentcalib.NewFileStore(root)
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	return s
}

// stepScreens hands the walk one frame per read, in order: Begin reads the
// first (which is where the header's geometry comes from), and each capture
// reads the next.
type stepScreens struct {
	frames []panegrid.Frame
	at     int
}

func (s *stepScreens) Frame(string) (panegrid.Frame, error) {
	f := s.frames[s.at]
	if s.at < len(s.frames)-1 {
		s.at++
	}
	return f, nil
}
