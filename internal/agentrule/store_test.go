package agentrule_test

// The rule store, asserted against the properties the decision was made for
// (nocx-y6w66 / D12) rather than against the code that implements them. Each
// test below names one of them, and four of them are the falsifiers the brief
// asked for: a shipped default written to disk on first run, a user rule
// merging field-by-field with a shipped one, disable and delete doing the same
// thing, and an upgrade that does not reach an untouched install.
//
// The subjects are synthetic agents rather than claude, because the property
// under test is the STORE's and not any one rule's: what a rule answers is the
// evaluator's business (internal/agentdriver) and is exercised there.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agentrule"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// ── fixtures ───────────────────────────────────────────────────────────

// shippedV1 answers free_text on the fixture frame, through a BRANCH rather
// than by defaulting to it. The branch is what makes "replaces, never merges"
// observable: a user rule with no branch of its own answers differently, and a
// merge — the user's default with the shipped branches still in front of it —
// would answer free_text.
const shippedV1 = `{
  "agent": "alpha",
  "anchors": [],
  "branches": [
    {"state": "free_text", "when": [{"kind": "nearestNonBlankAboveCursorContains", "text": "READY"}]}
  ],
  "default": "unknown"
}`

// shippedV2 is the same rule after a release that reads the frame better: the
// branch answers working instead. It is what an upgrade ships.
const shippedV2 = `{
  "agent": "alpha",
  "anchors": [],
  "branches": [
    {"state": "working", "when": [{"kind": "nearestNonBlankAboveCursorContains", "text": "READY"}]}
  ],
  "default": "unknown"
}`

const shippedSecond = `{
  "agent": "beta",
  "anchors": [],
  "branches": [{"state": "free_text", "when": [{"kind": "cursorOpensItsRow"}]}],
  "default": "unknown"
}`

// The second agent of the upgrade pair, before and after the release: the
// untouched install is one whose rule the new build reads BETTER.
const (
	shippedSecondV1 = `{"agent": "beta", "anchors": [], "branches": [], "default": "unknown"}`
	shippedSecondV2 = `{"agent": "beta", "anchors": [], "branches": [], "default": "working"}`
)

// edited is a rule a person wrote: it answers permission_choice where the
// shipped rule answered free_text, and it has no branch at all — so if it
// reached the evaluator merged with the shipped one, the shipped branch would
// still match first and this answer would never be seen.
const edited = `{"agent": "alpha", "anchors": [], "branches": [], "default": "permission_choice"}`

// editedElsewhere answers a state neither shipped version does, so an upgrade
// that reached it would be visible.
const editedElsewhere = `{"agent": "alpha", "anchors": [], "branches": [], "default": "error"}`

// ruleFor parses a document fixture into what New takes.
func ruleFor(t *testing.T, raw string) agentdriver.Document {
	t.Helper()
	var doc agentdriver.Document
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return doc
}

// registryFor builds the registry a build would have from the same fixtures,
// so the store is attached to a real one rather than to a stub.
func registryFor(t *testing.T, raws ...string) *agentdriver.Registry {
	t.Helper()
	drivers := make([]agentdriver.Driver, 0, len(raws))
	for _, raw := range raws {
		driver, err := agentdriver.Compile([]byte(raw))
		if err != nil {
			t.Fatalf("fixture does not compile: %v", err)
		}
		drivers = append(drivers, driver)
	}
	reg, err := agentdriver.NewRegistry(drivers...)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

// fixtureFrame is the screen the shipped rule's branch matches: "READY" one
// row above the cursor.
func fixtureFrame(t *testing.T) panegrid.Frame {
	t.Helper()
	grid := panegrid.New(log.NewSlogAdapter(nil))
	const pane = "fixture"
	if err := grid.Enrol(pane, 24, 4); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { grid.Withdraw(pane) })
	grid.Feed(pane, []byte("\x1b[2J\x1b[2;1HREADY     \x1b[4;1H"))
	frame, err := grid.Frame(pane)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return frame
}

// env is one store under one temporary app config directory.
type env struct {
	dir   string
	store *agentrule.Store
	reg   *agentdriver.Registry
}

// newEnv builds a store for the given shipped fixtures and attaches it to a
// registry built from the same ones — the composition root's arrangement.
func newEnv(t *testing.T, raws ...string) *env {
	t.Helper()
	dir := t.TempDir()
	shipped := make([]agentdriver.Document, 0, len(raws))
	for _, raw := range raws {
		shipped = append(shipped, ruleFor(t, raw))
	}
	store, err := agentrule.New(dir, shipped)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	reg := registryFor(t, raws...)
	reg.SetRuleSource(store)
	return &env{dir: dir, store: store, reg: reg}
}

// classify is what a pane running the agent would report.
func (e *env) classify(t *testing.T, agent string) agentdriver.State {
	t.Helper()
	return e.reg.Classify(agent, fixtureFrame(t))
}

// reopen is a restart: the same app directory, opened by a store this test
// attaches to the same registry. What survives is what is on disk, which is
// the whole point of the arrangement.
func (e *env) reopen(t *testing.T, raws ...string) {
	t.Helper()
	shipped := make([]agentdriver.Document, 0, len(raws))
	for _, raw := range raws {
		shipped = append(shipped, ruleFor(t, raw))
	}
	restarted, err := agentrule.New(e.dir, shipped)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	e.store = restarted
	e.reg.SetRuleSource(restarted)
}

// state is the one agent's entry as the Settings surface reads it.
func (e *env) state(t *testing.T, agent string) agentrule.Entry {
	t.Helper()
	for _, entry := range e.store.Rules() {
		if entry.Agent == agent {
			return entry
		}
	}
	t.Fatalf("no entry for %q", agent)
	return agentrule.Entry{}
}

// documentPath is where a hand edit would land.
func (e *env) documentPath(agent string) string {
	return filepath.Join(e.dir, agentrule.DirName, agent+".json")
}

// files lists the documents on disk, for the falsifier about writing them.
func (e *env) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dir, agentrule.DirName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read dir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// ── falsifier 1: a shipped default is not written to disk ──────────────

// TestAShippedRuleIsNeverWrittenToDisk is the first falsifier. An install
// nobody has edited has NO documents at all: the build's rules stay in the
// binary, which is the property that lets a later release improve them. A
// store that copied them out on first run would pass every other test here and
// fail this one at the second upgrade.
func TestAShippedRuleIsNeverWrittenToDisk(t *testing.T) {
	e := newEnv(t, shippedV1, shippedSecond)

	if got := e.files(t); len(got) != 0 {
		t.Fatalf("first run wrote %v; a shipped rule is not written to disk", got)
	}
	if got := e.state(t, "alpha").State; got != agentrule.StateShipped {
		t.Errorf("state = %q, want %q", got, agentrule.StateShipped)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
		t.Errorf("the shipped rule reads the frame as %q, want %q", got, agentdriver.StateFreeText)
	}
}

// ── falsifier 2 and acceptance 2: a user rule replaces the shipped one ─

// TestAUserRuleReplacesTheShippedOneEntirely is the second falsifier. The
// shipped rule answers free_text through a branch; the person's rule has no
// branch and answers permission_choice. A merge — their defaults with the
// shipped branches still in front — would answer free_text, so this is the
// frame and the pair of documents that can tell the two designs apart.
func TestAUserRuleReplacesTheShippedOneEntirely(t *testing.T) {
	e := newEnv(t, shippedV1)
	if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
		t.Fatalf("before the edit the shipped rule answers %q, want %q", got, agentdriver.StateFreeText)
	}

	if err := e.store.Set("alpha", edited); err != nil {
		t.Fatalf("set: %v", err)
	}

	entry := e.state(t, "alpha")
	if entry.State != agentrule.StateUser {
		t.Fatalf("state = %q, want %q", entry.State, agentrule.StateUser)
	}
	if !sameRule(t, entry.Document, edited) {
		t.Errorf("the surface is shown %q, want the person's own document", entry.Document)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StatePermissionChoice {
		t.Errorf("after the edit the frame reads %q, want %q — the shipped branch is still in front of it, so a merge would answer %q",
			got, agentdriver.StatePermissionChoice, agentdriver.StateFreeText)
	}
}

// ── acceptance 3: deleting restores the shipped rule ───────────────────

// TestDeletingAUserRuleRestoresTheShippedOne follows the replacement to its
// end. The end state is not "no rule": it is the build's rule, reading the pane
// again.
func TestDeletingAUserRuleRestoresTheShippedOne(t *testing.T) {
	e := newEnv(t, shippedV1)
	if err := e.store.Set("alpha", edited); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := e.store.Delete("alpha"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if entry := e.state(t, "alpha"); entry.State != agentrule.StateShipped {
		t.Errorf("state = %q after a delete, want %q", entry.State, agentrule.StateShipped)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
		t.Errorf("after a delete the frame reads %q, want the shipped rule's %q", got, agentdriver.StateFreeText)
	}
	if got := e.files(t); len(got) != 0 {
		t.Errorf("documents left after delete: %v", got)
	}
	// The surface is offering the SHIPPED text again, which is what a person
	// editing from scratch starts with.
	if got := e.state(t, "alpha").Document; !strings.Contains(got, `"free_text"`) {
		t.Errorf("the document offered after a delete is not the shipped rule:\n%s", got)
	}
}

// ── acceptance 4 and falsifier 3: off is not delete ────────────────────

// TestSwitchingDetectionOffIsUnknownAndIsNotDelete is the third falsifier,
// and it is the one with teeth: off must make the pane report unknown — the
// state nocx treats as busy and never types into — while the person's own
// document stays exactly where they left it, so switching back on returns
// their rule rather than the shipped one.
func TestSwitchingDetectionOffIsUnknownAndIsNotDelete(t *testing.T) {
	e := newEnv(t, shippedV1)
	if err := e.store.Set("alpha", edited); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := e.store.SetEnabled("alpha", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if got := e.classify(t, "alpha"); got != agentdriver.StateUnknown {
		t.Errorf("a switched-off agent reads %q, want %q", got, agentdriver.StateUnknown)
	}
	entry := e.state(t, "alpha")
	if entry.State != agentrule.StateDisabled {
		t.Fatalf("state = %q, want %q", entry.State, agentrule.StateDisabled)
	}
	if !sameRule(t, entry.Document, edited) {
		t.Errorf("their document was lost when detection was switched off: %q", entry.Document)
	}
	if _, err := os.Stat(e.documentPath("alpha")); err != nil {
		t.Errorf("switching off removed the document from disk: %v", err)
	}

	// The two are different operations, so switching back on returns the
	// rule they wrote and NOT the shipped one — which is what delete does.
	if err := e.store.SetEnabled("alpha", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StatePermissionChoice {
		t.Errorf("after switching back on the frame reads %q, want the person's rule %q", got, agentdriver.StatePermissionChoice)
	}
	if err := e.store.SetEnabled("alpha", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := e.store.Delete("alpha"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
		t.Errorf("after a delete the frame reads %q, want the shipped rule's %q", got, agentdriver.StateFreeText)
	}
	if entry := e.state(t, "alpha"); entry.State != agentrule.StateShipped {
		t.Errorf("delete from off left state = %q, want %q", entry.State, agentrule.StateShipped)
	}
}

// TestSwitchingDetectionOffSurvivesARestart is the half of "off is a state"
// that a restart would break if the switch lived only in memory: a person
// switches an agent off, quits, and starts nocx again — the pane is still
// unknown, and their document is still there to switch back on.
func TestSwitchingDetectionOffSurvivesARestart(t *testing.T) {
	e := newEnv(t, shippedV1)
	if err := e.store.Set("alpha", edited); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := e.store.SetEnabled("alpha", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	e.reopen(t, shippedV1)

	if got := e.classify(t, "alpha"); got != agentdriver.StateUnknown {
		t.Errorf("after a restart a switched-off agent reads %q, want %q", got, agentdriver.StateUnknown)
	}
	entry := e.state(t, "alpha")
	if entry.State != agentrule.StateDisabled || !sameRule(t, entry.Document, edited) {
		t.Errorf("after a restart state = %q and document = %q, want %q with the person's document",
			entry.State, entry.Document, agentrule.StateDisabled)
	}
}

// TestSwitchingOffWithNoDocumentWritesOnlyTheSwitch covers the other entry
// into off: an install nobody has edited. There is no rule of theirs to keep,
// so the switch is the whole document — and it must not be a copy of the
// shipped rule, which is the same falsifier as the first test one step later.
func TestSwitchingOffWithNoDocumentWritesOnlyTheSwitch(t *testing.T) {
	e := newEnv(t, shippedV1)
	if err := e.store.SetEnabled("alpha", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateUnknown {
		t.Errorf("state = %q, want %q", got, agentdriver.StateUnknown)
	}
	raw, err := os.ReadFile(e.documentPath("alpha"))
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	if strings.Contains(string(raw), `"free_text"`) {
		t.Errorf("switching detection off wrote the shipped rule to disk:\n%s", raw)
	}
	entry := e.state(t, "alpha")
	if entry.State != agentrule.StateDisabled {
		t.Errorf("state = %q, want %q", entry.State, agentrule.StateDisabled)
	}
	// The rule shown is the build's — nothing of theirs is in the way — and
	// switching back on returns the shipped rule with the file gone.
	if !strings.Contains(entry.Document, `"free_text"`) {
		t.Errorf("the document offered while off is not the shipped rule:\n%s", entry.Document)
	}
	if err := e.store.SetEnabled("alpha", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
		t.Errorf("state = %q after switching back on, want the shipped rule's %q", got, agentdriver.StateFreeText)
	}
	if got := e.files(t); len(got) != 0 {
		t.Errorf("documents left after switching back on with no rule of theirs: %v", got)
	}
}

// ── acceptance 5: an upgrade, both ways ───────────────────────────────

// TestAnUpgradeReachesAnUntouchedInstallAndLeavesAnEditedOneAlone is the
// acceptance criterion that says the whole arrangement is worth having, and it
// is asserted rather than assumed: the same app directory is opened by two
// builds, one shipping a better rule than the other.
//
// The untouched agent follows the new build. The one somebody edited keeps
// their document — including through a release that reads the frame
// differently, which is exactly when a silent overwrite would be worst.
func TestAnUpgradeReachesAnUntouchedInstallAndLeavesAnEditedOneAlone(t *testing.T) {
	dir := t.TempDir()
	shipped := func(alpha, beta string) []agentdriver.Document {
		return []agentdriver.Document{ruleFor(t, alpha), ruleFor(t, beta)}
	}
	frame := fixtureFrame(t)

	first, err := agentrule.New(dir, shipped(shippedV1, shippedSecondV1))
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	// The person edits ALPHA and never touches BETA.
	if setErr := first.Set("alpha", editedElsewhere); setErr != nil {
		t.Fatalf("set: %v", setErr)
	}
	before := registryFor(t, shippedV1, shippedSecondV1)
	before.SetRuleSource(first)
	if got := before.Classify("alpha", frame); got != agentdriver.StateError {
		t.Fatalf("the edited agent reads %q before the upgrade, want %q", got, agentdriver.StateError)
	}

	// The upgrade: the same app directory, opened by a build whose rules read
	// better than the ones that were there.
	second, err := agentrule.New(dir, shipped(shippedV2, shippedSecondV2))
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	upgraded := registryFor(t, shippedV2, shippedSecondV2)
	upgraded.SetRuleSource(second)

	if got := upgraded.Classify("beta", frame); got != agentdriver.StateWorking {
		t.Errorf("the untouched agent reads %q after the upgrade, want the NEW rule's %q — the shipped rule did not reach it",
			got, agentdriver.StateWorking)
	}
	if got := upgraded.Classify("alpha", frame); got != agentdriver.StateError {
		t.Errorf("the edited agent reads %q after the upgrade, want the document its author wrote (%q) — the upgrade overwrote it",
			got, agentdriver.StateError)
	}
	for _, entry := range second.Rules() {
		if entry.Agent == "alpha" && !sameRule(t, entry.Document, editedElsewhere) {
			t.Errorf("the edited agent's document after the upgrade is %q", entry.Document)
		}
	}
}

// ── a hand edit that cannot be used ────────────────────────────────────

// TestADocumentThatCannotBeUsedIsUnknownRatherThanTheShippedRule is the
// failing-closed half of the hand-editing promise. Every case below is a real
// hand edit — a truncated file, a mistyped state, a copy of another agent's
// document, a file somebody emptied — and each one answers unknown rather than
// quietly standing aside for the build's rule, because standing aside tells
// the person their edit took effect when it did not.
func TestADocumentThatCannotBeUsedIsUnknownRatherThanTheShippedRule(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"truncated", `{"version": 1, "rule": {"agent": "alpha"`},
		{"not a state", `{"version": 1, "rule": {"agent": "alpha", "default": "probably_idle"}}`},
		{"another agent's document", `{"version": 1, "rule": ` + editedForBeta + `}`},
		{"written by a newer nocx", `{"version": 99, "rule": ` + edited + `}`},
		{"emptied", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t, shippedV1)
			if err := os.MkdirAll(filepath.Dir(e.documentPath("alpha")), 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(e.documentPath("alpha"), []byte(test.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			e.reopen(t, shippedV1)

			if got := e.classify(t, "alpha"); got != agentdriver.StateUnknown {
				t.Errorf("the pane reads %q, want %q", got, agentdriver.StateUnknown)
			}
			entry := e.state(t, "alpha")
			if entry.State != agentrule.StateUnreadable {
				t.Fatalf("state = %q, want %q", entry.State, agentrule.StateUnreadable)
			}
			if entry.Problem == "" {
				t.Error("a document that cannot be used is reported with no reason")
			}
			if entry.Document != "" {
				t.Errorf("the surface is offered text to save over a document it cannot read: %q", entry.Document)
			}
			// The way back is delete, and it works from this state.
			if err := e.store.Delete("alpha"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
				t.Errorf("after deleting the unusable document the pane reads %q, want the shipped rule's %q", got, agentdriver.StateFreeText)
			}
		})
	}
}

// TestAnUnreadableDocumentRefusesTheSwitch keeps the repair honest: switching
// detection would rewrite a document whose contents cannot be read, which is
// how a half-finished repair gets deleted by a person who pressed the wrong
// control. Delete is the way out of that state, and it is the only one.
func TestAnUnreadableDocumentRefusesTheSwitch(t *testing.T) {
	e := newEnv(t, shippedV1)
	if err := os.MkdirAll(filepath.Dir(e.documentPath("alpha")), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(e.documentPath("alpha"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	e.reopen(t, shippedV1)

	if err := e.store.SetEnabled("alpha", false); err == nil {
		t.Error("switching detection off over an unreadable document was allowed")
	}
	if err := e.store.Set("alpha", edited); err != nil {
		t.Errorf("writing a NEW document over an unreadable one is the repair, and was refused: %v", err)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StatePermissionChoice {
		t.Errorf("after the repair the pane reads %q, want %q", got, agentdriver.StatePermissionChoice)
	}
}

// ── what a write refuses, and what it leaves behind ────────────────────

// TestSetRefusesADocumentThatCouldNotAnswer keeps the write path where a
// person can see it: a document that cannot be used is refused at the moment
// they save it, and nothing is written. A store that accepted it would put the
// pane into unknown and explain itself only in a log line.
func TestSetRefusesADocumentThatCouldNotAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not JSON", `{`},
		{"no agent", `{"anchors": [], "branches": [], "default": "unknown"}`},
		{"a state that does not exist", `{"agent": "alpha", "anchors": [], "branches": [], "default": "probably"}`},
		{"another agent", editedForBeta},
		{"empty", "   "},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t, shippedV1, shippedSecond)
			if err := e.store.Set("alpha", test.body); err == nil {
				t.Fatal("a document that could not answer was accepted")
			}
			if got := e.files(t); len(got) != 0 {
				t.Fatalf("a refused document was written anyway: %v", got)
			}
			if got := e.classify(t, "alpha"); got != agentdriver.StateFreeText {
				t.Errorf("a refused document changed what reads the pane: %q", got)
			}
			// The other agent is untouched by the attempt.
			if got := e.classify(t, "beta"); got != agentdriver.StateUnknown {
				t.Errorf("beta reads %q, want %q", got, agentdriver.StateUnknown)
			}
		})
	}
}

// TestAnAgentThisBuildShipsNoRuleForCannotBeWrittenTo is the bound on what a
// rule may replace: there is nothing to replace for an agent nocx was never
// taught to read, and this seam does not take on new agents.
func TestAnAgentThisBuildShipsNoRuleForCannotBeWrittenTo(t *testing.T) {
	e := newEnv(t, shippedV1)
	for _, err := range []error{
		e.store.Set("gamma", `{"agent": "gamma", "anchors": [], "branches": [], "default": "unknown"}`),
		e.store.SetEnabled("gamma", false),
		e.store.Delete("gamma"),
	} {
		if err == nil {
			t.Error("an agent with no shipped rule was written to")
		}
	}
}

// TestTheStoreNamesWhereTheDocumentsLive is what the Settings page shows a
// person who would rather edit the file: one directory, and a document in it
// per agent, named after the agent.
func TestTheStoreNamesWhereTheDocumentsLive(t *testing.T) {
	e := newEnv(t, shippedV1)
	want := filepath.Join(e.dir, agentrule.DirName)
	if got := e.store.Directory(); got != want {
		t.Errorf("directory = %q, want %q", got, want)
	}
	if err := e.store.Set("alpha", edited); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(want, "alpha.json")); err != nil {
		t.Errorf("the document is not where the store says it is: %v", err)
	}
}

// TestTheRuleWrittenIsTheRuleReadBack is the hand-editing promise from the
// other end: what a person's file holds is what the surface shows them and
// what the evaluator runs, field for field.
func TestTheRuleWrittenIsTheRuleReadBack(t *testing.T) {
	e := newEnv(t, shippedV1)
	const mine = `{"agent": "alpha", "anchors": [], "branches": [], "default": "modal_choice"}`
	if err := e.store.Set("alpha", mine); err != nil {
		t.Fatalf("set: %v", err)
	}
	e.reopen(t, shippedV1)
	if got := e.state(t, "alpha"); !sameRule(t, got.Document, mine) {
		t.Errorf("the reopened document is %q, want what was written", got.Document)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateModalChoice {
		t.Errorf("the reopened rule reads %q, want %q", got, agentdriver.StateModalChoice)
	}
}

// TestSetWhileOffLeavesItOff: editing a rule is not switching one on. A page
// whose save control also re-enabled detection would make every save a decision
// about two things.
func TestSetWhileOffLeavesItOff(t *testing.T) {
	e := newEnv(t, shippedV1)
	if err := e.store.SetEnabled("alpha", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := e.store.Set("alpha", edited); err != nil {
		t.Fatalf("set: %v", err)
	}
	if entry := e.state(t, "alpha"); entry.State != agentrule.StateDisabled {
		t.Errorf("state = %q after saving while off, want %q", entry.State, agentrule.StateDisabled)
	}
	if got := e.classify(t, "alpha"); got != agentdriver.StateUnknown {
		t.Errorf("the pane reads %q after saving while off, want %q", got, agentdriver.StateUnknown)
	}
}

// editedForBeta is a valid rule that names the wrong agent for the document it
// is written into.
const editedForBeta = `{"agent": "beta", "anchors": [], "branches": [], "default": "free_text"}`

// TestTheStateSetIsClosed pins the vocabulary the wire and the page share.
func TestTheStateSetIsClosed(t *testing.T) {
	for _, s := range agentrule.States() {
		if !s.Valid() {
			t.Errorf("%q is listed and not valid", s)
		}
	}
	if agentrule.State("off").Valid() {
		t.Error("a state nobody wrote a branch for is accepted")
	}
	if !agentrule.StateUser.Enabled() || !agentrule.StateShipped.Enabled() {
		t.Error("a state that reads the pane is reported as switched off")
	}
	if agentrule.StateDisabled.Enabled() || agentrule.StateUnreadable.Enabled() {
		t.Error("a state that does not read the pane is reported as switched on")
	}
}

// sameRule compares two rule documents as RULES rather than as bytes: the
// store writes JSON through one marshaller, so a save from the product
// re-indents what it was given while keeping every field and its order.
func sameRule(t *testing.T, got, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(got), &a); err != nil {
		t.Fatalf("the stored document does not parse: %v (%q)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("the expected document does not parse: %v", err)
	}
	return reflect.DeepEqual(a, b)
}
