package agentcalib_test

// Calibration, from the outside: a person is ASKED for a state, produces it,
// and the frame is labelled with the state they were asked for.
//
// The two falsifiers the bead names are what these tests are for, and both are
// about what the API makes IMPOSSIBLE rather than about what it does when
// asked nicely:
//
//   - a label may not be attached to a frame the person did not produce for
//     it. There is no label on any signature here, and no frame on any
//     signature either: the label comes out of the pending step and the frame
//     is read from the pane's live grid at the moment the person says "now".
//   - a completed calibration may not lack idle, working or asks-you.
//     Completeness is DERIVED from the labels present, never stored, so a
//     hand-edited file that claims to be complete is not.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

const (
	pane  = "0123456789abcdef0123456789abcdef"
	agent = "claude"
	cols  = 40
	rows  = 14
)

// screens is the pane the person is driving, as the product sees one: a real
// panegrid Store the test paints into, and the Calibrations reads out of.
type screens struct{ store *panegrid.Store }

func newScreens(t *testing.T) *screens {
	t.Helper()
	s := &screens{store: panegrid.New(log.NewSlogAdapter(nil))}
	if err := s.store.Enrol(pane, cols, rows); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { s.store.Withdraw(pane) })
	return s
}

func (s *screens) Frame(paneID string) (panegrid.Frame, error) { return s.store.Frame(paneID) }

// drive paints a distinguishable screen, which is what a person does with
// their agent between one step and the next.
func (s *screens) drive(t *testing.T, text string) panegrid.Frame {
	t.Helper()
	s.store.Feed(pane, []byte("\x1b[2J\x1b[3;1H"+text+"\x1b[3;1H"))
	f, err := s.store.Frame(pane)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return f
}

func newCalibrations(t *testing.T) (*agentcalib.Calibrations, *screens, agentcalib.Store) {
	t.Helper()
	sc := newScreens(t)
	store, err := agentcalib.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return agentcalib.New(log.NewSlogAdapter(nil), sc, store), sc, store
}

// walkAll answers every step: captured unless its label is in skip.
func walkAll(t *testing.T, c *agentcalib.Calibrations, sc *screens, skip map[agentcalib.Label]bool) map[agentcalib.Label]panegrid.Frame {
	t.Helper()
	if _, err := c.Begin(pane, agent); err != nil {
		t.Fatalf("begin: %v", err)
	}
	produced := map[agentcalib.Label]panegrid.Frame{}
	for {
		st, err := c.Status(pane, agent)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if st.Walk == nil || st.Walk.Pending < 0 {
			return produced
		}
		step := st.Steps[st.Walk.Pending]
		if skip[step.Label] {
			if _, err := c.Answer(pane, st.Walk.Pending, agentcalib.AnswerSkip); err != nil {
				t.Fatalf("skip %s: %v", step.Label, err)
			}
			continue
		}
		produced[step.Label] = sc.drive(t, "state: "+string(step.Label))
		if _, err := c.Answer(pane, st.Walk.Pending, agentcalib.AnswerCapture); err != nil {
			t.Fatalf("capture %s: %v", step.Label, err)
		}
	}
}

// TestLabelledSetRoundTripsThroughReplay is the bead's own test. The set holds
// a capture and a mark per label and no frames at all; the frame a person
// produced is derived by replaying to the mark. So this asserts the only thing
// that makes that sound: replay yields the same frames.
func TestLabelledSetRoundTripsThroughReplay(t *testing.T) {
	c, sc, store := newCalibrations(t)
	produced := walkAll(t, c, sc, nil)

	set, found, err := store.Load(agent)
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	frames, err := set.Frames(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(frames) != len(produced) {
		t.Fatalf("replayed %d labelled frames, produced %d", len(frames), len(produced))
	}
	for _, lf := range frames {
		want, ok := produced[lf.Label]
		if !ok {
			t.Fatalf("replay produced a frame for %s, which was never labelled", lf.Label)
		}
		if diff := frameDiff(lf.Frame, want); diff != "" {
			t.Fatalf("%s replays to a different screen than the person produced:\n%s", lf.Label, diff)
		}
	}
}

// TestTheCaptureIsTheOneTheCommandReplays keeps the promise the format was
// extracted for: the set on disk is a capture, and the marks in labels.json
// are marks in it.
func TestTheCaptureIsTheOneTheCommandReplays(t *testing.T) {
	root := t.TempDir()
	sc := newScreens(t)
	store, err := agentcalib.NewFileStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c := agentcalib.New(log.NewSlogAdapter(nil), sc, store)
	walkAll(t, c, sc, nil)

	path := filepath.Join(root, "agents", "calibration", agent, "capture.jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("the set's capture does not read as a capture: %v", err)
	}
	if header.Cols != cols || header.Rows != rows {
		t.Fatalf("capture geometry %dx%d, want the pane's %dx%d", header.Cols, header.Rows, cols, rows)
	}
	set, _, err := store.Load(agent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, rec := range set.Labels {
		if rec.Skipped {
			continue
		}
		if rec.AtMs == nil {
			t.Fatalf("label %s was captured and carries no mark", rec.Label)
		}
		if agentcapture.ChunksThrough(chunks, *rec.AtMs, 0) == 0 {
			t.Fatalf("label %s marks %dms, which is before the capture's first chunk", rec.Label, *rec.AtMs)
		}
	}
}

// TestTheFirstLabelKeepsItsMarkOnDisk is a defect this suite did not have
// until a real set was read by eye: the first captured label sits at mark
// ZERO, and a mark written with encoding/json's omitempty vanished from the
// file — indistinguishable there from the skipped label that genuinely has
// none. It replayed correctly by luck, because absent decodes to zero and
// zero was the right answer. The next mark scheme would not have been so
// lucky.
func TestTheFirstLabelKeepsItsMarkOnDisk(t *testing.T) {
	root := t.TempDir()
	sc := newScreens(t)
	store, err := agentcalib.NewFileStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	walkAll(t, agentcalib.New(log.NewSlogAdapter(nil), sc, store), sc, nil)

	data, err := os.ReadFile(filepath.Join(root, "agents", "calibration", agent, "labels.json")) //nolint:gosec // a path this test built
	if err != nil {
		t.Fatalf("read labels: %v", err)
	}
	var doc struct {
		Labels []struct {
			Label string `json:"label"`
			AtMs  *int64 `json:"atMs"`
		} `json:"labels"`
	}
	if unmarshalErr := json.Unmarshal(data, &doc); unmarshalErr != nil {
		t.Fatalf("decode labels: %v", unmarshalErr)
	}
	for _, rec := range doc.Labels {
		if rec.AtMs == nil {
			t.Fatalf("label %s was captured and has no mark on disk:\n%s", rec.Label, data)
		}
	}
	if *doc.Labels[0].AtMs != 0 {
		t.Fatalf("the first label marks %d, want the capture's first moment", *doc.Labels[0].AtMs)
	}
}

// TestARequiredStepCannotBeSkipped is the second falsifier at its source. The
// walk refuses, in the person's own vocabulary, rather than accepting and
// producing a set that quietly cannot verify anything.
func TestARequiredStepCannotBeSkipped(t *testing.T) {
	c, _, _ := newCalibrations(t)
	if _, err := c.Begin(pane, agent); err != nil {
		t.Fatalf("begin: %v", err)
	}
	st, err := c.Status(pane, agent)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Steps[0].Required {
		t.Fatalf("step 0 is %s, which is optional; the walk must ask for the required states first", st.Steps[0].Label)
	}
	_, err = c.Answer(pane, 0, agentcalib.AnswerSkip)
	if err == nil {
		t.Fatal("skipping a required step succeeded")
	}
	if !strings.Contains(err.Error(), string(st.Steps[0].Label)) {
		t.Fatalf("refusal %q does not name the step it refused", err)
	}
}

// TestCompletenessIsDerivedNotStored is the same falsifier at the other end.
// A set arrives from a file a person can edit, so "complete" may not be a
// field in it.
func TestCompletenessIsDerivedNotStored(t *testing.T) {
	root := t.TempDir()
	sc := newScreens(t)
	store, err := agentcalib.NewFileStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	c := agentcalib.New(log.NewSlogAdapter(nil), sc, store)
	walkAll(t, c, sc, nil)

	set, _, err := store.Load(agent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !set.Complete() {
		t.Fatal("a walk that captured every step is not complete")
	}

	labels := filepath.Join(root, "agents", "calibration", agent, "labels.json")
	data, err := os.ReadFile(labels) //nolint:gosec // a path this test built
	if err != nil {
		t.Fatalf("read labels: %v", err)
	}
	if strings.Contains(string(data), "complete") {
		t.Fatalf("labels.json stores a completeness claim, which a person can edit:\n%s", data)
	}
	// Take the asks-you label out by hand, the way a person with an editor
	// would, and the set stops being complete.
	edited := removeLabel(t, string(data), agentcalib.LabelAsksYou)
	if writeErr := os.WriteFile(labels, []byte(edited), 0o600); writeErr != nil {
		t.Fatalf("write labels: %v", writeErr)
	}
	set, found, err := store.Load(agent)
	if err != nil || !found {
		t.Fatalf("reload: found=%v err=%v", found, err)
	}
	if set.Complete() {
		t.Fatal("a set with no asks-you label reports itself complete")
	}
	if set.Calibrated(agentcalib.LabelAsksYou) {
		t.Fatal("asks-you is calibrated in a set that has no such label")
	}
}

// TestSkippedIsNotTheSameAsNeverAsked is what the optional labels are for. An
// optional state that was offered and declined is written down as declined;
// one nobody was ever asked for is absent. Both fall to unknown, which is
// busy, which is a refusal rather than a wrong answer — but only the first is
// a decision the person made, and a surface that cannot tell them apart
// cannot offer to fill the gap in.
func TestSkippedIsNotTheSameAsNeverAsked(t *testing.T) {
	c, sc, store := newCalibrations(t)
	walkAll(t, c, sc, map[agentcalib.Label]bool{agentcalib.LabelError: true})

	set, _, err := store.Load(agent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rec, asked := set.Record(agentcalib.LabelError)
	if !asked {
		t.Fatal("a skipped label left no record; skipped and never-asked became the same thing")
	}
	if !rec.Skipped {
		t.Fatal("the skipped label was recorded as captured")
	}
	if set.Calibrated(agentcalib.LabelError) {
		t.Fatal("a skipped label reports itself calibrated")
	}
	if !set.Complete() {
		t.Fatal("skipping an OPTIONAL label stopped the calibration completing")
	}
	if _, asked := set.Record("never-offered"); asked {
		t.Fatal("a label nobody was asked for has a record")
	}
	// And a skipped label carries no mark, because there is no frame behind
	// it: a mark that pointed anywhere would point at another step's screen.
	frames, err := set.Frames(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, lf := range frames {
		if lf.Label == agentcalib.LabelError {
			t.Fatal("the skipped label replayed to a frame")
		}
	}
}

// TestAStaleAnswerIsRefused is the other half of binding a label to a step. A
// surface that redrew late must not be able to answer the step it is still
// showing: the pending step is the server's, and an answer that names a
// different one is a person answering a question they were no longer being
// asked.
func TestAStaleAnswerIsRefused(t *testing.T) {
	c, sc, _ := newCalibrations(t)
	if _, err := c.Begin(pane, agent); err != nil {
		t.Fatalf("begin: %v", err)
	}
	sc.drive(t, "idle")
	if _, err := c.Answer(pane, 0, agentcalib.AnswerCapture); err != nil {
		t.Fatalf("capture step 0: %v", err)
	}
	if _, err := c.Answer(pane, 0, agentcalib.AnswerCapture); err == nil {
		t.Fatal("answering step 0 a second time succeeded; a label was re-pointed at a later frame")
	}
	if _, err := c.Answer(pane, 3, agentcalib.AnswerCapture); err == nil {
		t.Fatal("answering a step the walk has not reached succeeded")
	}
}

// TestEveryLabelIsAStateADriverCanAnswer keeps the two vocabularies in step.
// The person is asked for a screen in the design's words; verification
// replays the rule and compares its answer to a driver State, and a label
// that mapped to nothing could never be verified against anything.
func TestEveryLabelIsAStateADriverCanAnswer(t *testing.T) {
	required := 0
	for _, s := range agentcalib.Steps() {
		if !s.Expect.Valid() {
			t.Fatalf("label %s expects %q, which is not in the driver's closed set", s.Label, s.Expect)
		}
		if s.Expect == agentdriver.StateUnknown || s.Expect == agentdriver.StateExited {
			t.Fatalf("label %s expects %s, which is not a screen a person can produce", s.Label, s.Expect)
		}
		if s.Required {
			required++
		}
		if s.Ask == "" {
			t.Fatalf("label %s asks the person for nothing", s.Label)
		}
	}
	if required != 3 {
		t.Fatalf("%d required steps, want the three a person can produce on demand", required)
	}
}

// TestARestartDoesNotDestroyTheStoredSet is a failure path with teeth: the
// person starts calibrating again, gets half way and gives up. The set that
// was verified yesterday must still be there.
func TestARestartDoesNotDestroyTheStoredSet(t *testing.T) {
	c, sc, store := newCalibrations(t)
	walkAll(t, c, sc, nil)
	before, _, err := store.Load(agent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if _, beginErr := c.Begin(pane, agent); beginErr != nil {
		t.Fatalf("begin again: %v", beginErr)
	}
	sc.drive(t, "idle again")
	if _, answerErr := c.Answer(pane, 0, agentcalib.AnswerCapture); answerErr != nil {
		t.Fatalf("capture: %v", answerErr)
	}
	c.Abandon(pane)

	after, found, err := store.Load(agent)
	if err != nil || !found {
		t.Fatalf("reload: found=%v err=%v", found, err)
	}
	if !after.Complete() || len(after.Labels) != len(before.Labels) {
		t.Fatalf("an abandoned walk replaced the stored set: %d labels, complete=%v",
			len(after.Labels), after.Complete())
	}
}

// TestAPaneThatStoppedBeingWatchedRefuses is the external call every step
// makes: the grid. A pane whose observation closed cannot produce a frame, and
// the answer is a refusal rather than a label on an empty screen.
func TestAPaneThatStoppedBeingWatchedRefuses(t *testing.T) {
	c, _, _ := newCalibrations(t)
	if _, err := c.Begin(pane, agent); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := c.Answer("some-other-pane", 0, agentcalib.AnswerCapture); err == nil {
		t.Fatal("a pane with no walk answered a step")
	}
	if _, err := c.Begin("not-enrolled", agent); err == nil {
		t.Fatal("a pane with no grid began a calibration")
	}
}

// TestRedoReAsksTheStep is why the missing picker costs nothing. A person who
// captured at the wrong moment gets the step asked again, and the next
// capture takes a fresh live frame for it — the label still comes from the
// step and the frame is still one produced for that step.
func TestRedoReAsksTheStep(t *testing.T) {
	c, sc, store := newCalibrations(t)
	if _, err := c.Begin(pane, agent); err != nil {
		t.Fatalf("begin: %v", err)
	}
	sc.drive(t, "mistimed")
	if _, err := c.Answer(pane, 0, agentcalib.AnswerCapture); err != nil {
		t.Fatalf("capture: %v", err)
	}
	st, err := c.Answer(pane, 1, agentcalib.AnswerRedo)
	if err != nil {
		t.Fatalf("redo: %v", err)
	}
	if st.Walk.Pending != 0 {
		t.Fatalf("after redo the pending step is %d, want 0", st.Walk.Pending)
	}
	want := sc.drive(t, "state: idle")
	if _, answerErr := c.Answer(pane, 0, agentcalib.AnswerCapture); answerErr != nil {
		t.Fatalf("recapture: %v", answerErr)
	}
	for {
		st, statusErr := c.Status(pane, agent)
		if statusErr != nil {
			t.Fatalf("status: %v", statusErr)
		}
		if st.Walk == nil || st.Walk.Pending < 0 {
			break
		}
		sc.drive(t, "state: "+string(st.Steps[st.Walk.Pending].Label))
		if _, captureErr := c.Answer(pane, st.Walk.Pending, agentcalib.AnswerCapture); captureErr != nil {
			t.Fatalf("capture: %v", captureErr)
		}
	}
	set, _, err := store.Load(agent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	frames, err := set.Frames(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, lf := range frames {
		if lf.Label != agentcalib.LabelIdle {
			continue
		}
		if diff := frameDiff(lf.Frame, want); diff != "" {
			t.Fatalf("idle still holds the mistimed frame:\n%s", diff)
		}
	}
}

// TestTheStoreRefusesAnAgentNameThatIsAPath is the boundary the set's
// directory name crosses. The agent is named by the enrolment act, which
// carries a string, and a string that walks out of the app directory is a
// wiring mistake with a filesystem behind it.
func TestTheStoreRefusesAnAgentNameThatIsAPath(t *testing.T) {
	store, err := agentcalib.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, name := range []string{"", "..", "../escape", "a/b", ".hidden", strings.Repeat("x", 200)} {
		if _, _, err := store.Load(name); err == nil {
			t.Fatalf("Load(%q) was allowed", name)
		}
	}
}

// removeLabel edits the document the way a person with an editor would, and
// structurally rather than by line, so the test asserts about the set rather
// than about the formatting of the file it came out of.
func removeLabel(t *testing.T, doc string, label agentcalib.Label) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("labels.json is not JSON: %v", err)
	}
	entries, ok := parsed["labels"].([]any)
	if !ok {
		t.Fatalf("labels.json has no labels array:\n%s", doc)
	}
	kept := make([]any, 0, len(entries))
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok && m["label"] == string(label) {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(entries) {
		t.Fatalf("label %s was not in the document to remove:\n%s", label, doc)
	}
	parsed["labels"] = kept
	out, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("re-encode labels: %v", err)
	}
	return string(out)
}

func frameDiff(got, want panegrid.Frame) string {
	if got.Cols != want.Cols || got.Rows != want.Rows {
		return "geometry differs"
	}
	if got.CursorX != want.CursorX || got.CursorY != want.CursorY {
		return "cursor differs"
	}
	var b strings.Builder
	for y := range want.Lines {
		if got.Text(y) != want.Text(y) {
			fmt.Fprintf(&b, "row %d: got %q, want %q\n", y, got.Text(y), want.Text(y))
		}
	}
	return b.String()
}
