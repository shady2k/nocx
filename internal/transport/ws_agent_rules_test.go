package transport

// A person's own rule for an agent, off the real socket (AGENTS.md testing
// rule 5) and through the production wiring: a real panegrid Store fed real
// bytes, the real watcher, the real registry, and the real rule store writing
// a real document into a directory this test owns.
//
// What a person gets that they could not before: they open Settings, write or
// edit the rule that reads their agent, or switch detection off for it, and
// the pane is read by what they wrote — without waiting for a release.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agentrule"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
)

type agentRulesEnv struct {
	env   *lifecycleTestEnv
	watch *paneobserve.Watcher
	sid   string
}

func newAgentRulesEnv(t *testing.T) *agentRulesEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	grid := paneviewtest.NewViews(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	dir := t.TempDir()
	store, err := agentrule.New(dir, rules.ShippedRules())
	if err != nil {
		t.Fatalf("rule store: %v", err)
	}
	// The composition root's own arrangement: the store is built from the
	// rules this build ships and attached to the registry everything else
	// already reads through.
	rules.SetRuleSource(store)
	watcher := paneobserve.New(logger, grid, rules, paneobserve.Config{})
	env := newLifecycleTestEnv(t,
		WithPaneScreens(grid.Store), WithPaneObserver(watcher), WithAgentRules(rules),
		WithAgentRuleStore(store))
	watcher.SetEmitter(env.ws.EmitPaneObservation)
	sid := env.openSession(t, 1)
	if err := grid.Watch(sid, 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { grid.Withdraw(sid) })
	watcher.Watch(sid, "claude")
	grid.Feed(sid, []byte(claudeIdleScreen(40)))
	return &agentRulesEnv{env: env, watch: watcher, sid: sid}
}

// pane reads what the WATCHER reports for the pane — the chain a person's
// keystroke decision goes through, and not a classification this test made up.
func (e *agentRulesEnv) pane(t *testing.T) agentdriver.State {
	t.Helper()
	o, ok := e.watch.Classify(e.sid)
	if !ok {
		t.Fatal("the pane is not being watched")
	}
	return o.State
}

func (e *agentRulesEnv) call(t *testing.T, method string, params any, id int) agentRulesResult {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.env.conn, method, params, id)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("%s error: %+v", method, env.Error)
	}
	// The REAL result off the REAL socket, which is the check that a DTO test
	// cannot make (contracts/README.md).
	validateJSON(t, loadSchema(t, "agent.rules.schema.json"), env.Result, method+" wire")
	var out agentRulesResult
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

func (e *agentRulesEnv) callErr(t *testing.T, method string, params any, id int) *jsonrpcErrorObj {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.env.conn, method, params, id)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env.Error
}

// rowOf is the named agent's row, which is the whole of what a page draws for
// it. An agent the answer does not carry is a failure rather than a zero row:
// a surface would draw an empty editor for an agent that has no rule.
func (e *agentRulesEnv) rowOf(t *testing.T, res agentRulesResult, agent string) agentRuleRow {
	t.Helper()
	for _, row := range res.Rules {
		if row.Agent == agent {
			return row
		}
	}
	t.Fatalf("no row for %q in %+v", agent, res.Rules)
	return agentRuleRow{}
}

// aRuleADifferentAnswer is a valid claude rule that is NOT the shipped one:
// empty branches and a default of error. On the idle screen the shipped rule
// answers free_text and this answers error, so the state the pane reports is
// the whole of what says which of the two is in force.
const aRuleADifferentAnswer = `{"agent":"claude","anchors":[],"branches":[],"default":"error"}`

// TestAgentRules_OverTheWireConformsToContract walks the whole surface through
// the socket: read, write, switch off, switch on, delete — with the state
// checked at each step, because the wire carries a decision about whether nocx
// types into a pane.
func TestAgentRules_OverTheWireConformsToContract(t *testing.T) {
	e := newAgentRulesEnv(t)
	id := 2

	read := e.call(t, "agent.rules", nil, id)
	id++
	if read.Directory == "" {
		t.Error("the answer does not say where the documents live")
	}
	shipped := e.rowOf(t, read, "claude")
	if shipped.State != string(agentrule.StateShipped) {
		t.Fatalf("state = %q before anything was edited, want %q", shipped.State, agentrule.StateShipped)
	}
	if shipped.Problem != "" {
		t.Errorf("a shipped rule is reported with a problem: %q", shipped.Problem)
	}
	// Editing starts from SOMETHING: the shipped rule, which is what a person
	// without a rule of their own has to edit from.
	if !strings.Contains(shipped.Document, `"free_text"`) {
		t.Errorf("the document offered for editing is not the shipped rule:\n%s", shipped.Document)
	}

	wrote := e.call(t, "agent.rules.set", map[string]any{
		"agent": "claude", "document": aRuleADifferentAnswer,
	}, id)
	id++
	mine := e.rowOf(t, wrote, "claude")
	if mine.State != string(agentrule.StateUser) {
		t.Fatalf("state = %q after writing a rule, want %q", mine.State, agentrule.StateUser)
	}
	if !strings.Contains(mine.Document, `"error"`) {
		t.Errorf("the answer does not carry the document that was written:\n%s", mine.Document)
	}

	off := e.call(t, "agent.rules.setEnabled", map[string]any{"agent": "claude", "enabled": false}, id)
	id++
	if got := e.rowOf(t, off, "claude"); got.State != string(agentrule.StateDisabled) {
		t.Fatalf("state = %q after switching detection off, want %q", got.State, agentrule.StateDisabled)
	}
	// Off is not delete: their document is still the one that will read the
	// pane when they switch back on.
	if got := e.rowOf(t, off, "claude"); !strings.Contains(got.Document, `"error"`) {
		t.Errorf("switching detection off lost the person's document:\n%s", got.Document)
	}

	on := e.call(t, "agent.rules.setEnabled", map[string]any{"agent": "claude", "enabled": true}, id)
	id++
	if got := e.rowOf(t, on, "claude"); got.State != string(agentrule.StateUser) {
		t.Fatalf("state = %q after switching detection back on, want the person's rule %q", got.State, agentrule.StateUser)
	}

	gone := e.call(t, "agent.rules.delete", map[string]any{"agent": "claude"}, id)
	if got := e.rowOf(t, gone, "claude"); got.State != string(agentrule.StateShipped) {
		t.Fatalf("state = %q after deleting the rule, want %q", got.State, agentrule.StateShipped)
	}
}

// TestARuleEditedFromSettingsReadsThePaneItWasWrittenFor is this bead's
// acceptance criterion watched end to end on a REAL pane: the idle claude
// screen reads free_text, a rule written over the socket makes the same pane
// read error, switching detection off makes it read unknown, and deleting the
// rule puts the shipped reading back.
func TestARuleEditedFromSettingsReadsThePaneItWasWrittenFor(t *testing.T) {
	e := newAgentRulesEnv(t)
	if got := e.pane(t); got != agentdriver.StateFreeText {
		t.Fatalf("the untouched pane reads %q, want the shipped rule's %q", got, agentdriver.StateFreeText)
	}

	e.call(t, "agent.rules.set", map[string]any{"agent": "claude", "document": aRuleADifferentAnswer}, 2)
	if got := e.pane(t); got != agentdriver.StateError {
		t.Errorf("the pane reads %q after the edit, want the person's rule %q — the pane a person is looking at is not the one their rule reaches",
			got, agentdriver.StateError)
	}

	e.call(t, "agent.rules.setEnabled", map[string]any{"agent": "claude", "enabled": false}, 3)
	if got := e.pane(t); got != agentdriver.StateUnknown {
		t.Errorf("a switched-off pane reads %q, want %q — unknown is what nocx will not type into", got, agentdriver.StateUnknown)
	}

	e.call(t, "agent.rules.setEnabled", map[string]any{"agent": "claude", "enabled": true}, 4)
	if got := e.pane(t); got != agentdriver.StateError {
		t.Errorf("switching back on reads %q, want the person's rule %q", got, agentdriver.StateError)
	}

	e.call(t, "agent.rules.delete", map[string]any{"agent": "claude"}, 5)
	if got := e.pane(t); got != agentdriver.StateFreeText {
		t.Errorf("after the delete the pane reads %q, want the shipped rule's %q", got, agentdriver.StateFreeText)
	}
}

// TestARuleWrittenOverTheWireIsTheFileByHand is the other end of the
// hand-editing promise: what the socket wrote is a document in the directory
// the answer named, under the agent's own name, and a person opening that
// directory finds it.
//
// The two halves are checked through two seams on purpose. The directory is
// LISTED, which is what an editor shows a person; the rule inside it is read
// back by a STORE opened over the same app directory, which is the code that
// would read their hand edit on the next start — so neither claim rests on
// bytes this test parsed itself.
func TestARuleWrittenOverTheWireIsTheFileByHand(t *testing.T) {
	e := newAgentRulesEnv(t)
	res := e.call(t, "agent.rules.set", map[string]any{"agent": "claude", "document": aRuleADifferentAnswer}, 2)

	names := make([]string, 0, 1)
	entries, err := os.ReadDir(res.Directory)
	if err != nil {
		t.Fatalf("the rule directory the answer named could not be listed: %v", err)
	}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 1 || names[0] != "claude.json" {
		t.Fatalf("the directory the answer named holds %v, want one document named after the agent", names)
	}

	// Reopened at the PARENT of the directory the answer named, which is the
	// app config directory the store is rooted at — so this asks where the
	// answer says it is, and not merely that some file exists.
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	restarted, err := agentrule.New(filepath.Dir(res.Directory), rules.ShippedRules())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, row := range restarted.Rules() {
		if row.Agent != "claude" {
			continue
		}
		if row.State != agentrule.StateUser {
			t.Errorf("a store opened over that directory reads %q, want the rule that was written (%q)", row.State, agentrule.StateUser)
		}
		if !strings.Contains(row.Document, `"error"`) {
			t.Errorf("the document on disk does not hold the rule that was written:\n%s", row.Document)
		}
	}
}

// TestAgentRulesRefuseADocumentThatCouldNotAnswer keeps the refusal where the
// person can see it: the write is refused while they are looking at the
// document, and nothing of theirs changes — a rule that reached the store
// anyway would put the pane into unknown and explain itself in a log line.
func TestAgentRulesRefuseADocumentThatCouldNotAnswer(t *testing.T) {
	e := newAgentRulesEnv(t)
	before := e.pane(t)

	for _, bad := range []string{
		`{`,
		`{"agent":"claude","anchors":[],"branches":[],"default":"probably_idle"}`,
		`{"agent":"gemini","anchors":[],"branches":[],"default":"unknown"}`,
		``,
	} {
		err := e.callErr(t, "agent.rules.set", map[string]any{"agent": "claude", "document": bad}, 2)
		if err == nil {
			t.Fatalf("a document that could not answer was accepted: %q", bad)
		}
		if err.Code != -32602 || err.Message == "" {
			t.Errorf("the refusal of %q is not an invalid-params error: %+v", bad, err)
		}
	}
	if got := e.pane(t); got != before {
		t.Errorf("a refused document changed what reads the pane: %q, want %q", got, before)
	}
	// Nothing of theirs was written, so the list still says shipped.
	res := e.call(t, "agent.rules", nil, 3)
	if got := e.rowOf(t, res, "claude").State; got != string(agentrule.StateShipped) {
		t.Errorf("state = %q after four refused writes, want %q", got, agentrule.StateShipped)
	}
}

// TestAgentRulesRefuseAnAgentThisBuildShipsNoRuleFor is the other bound: a
// rule REPLACES a shipped one, and nocx does not take on new agents here.
func TestAgentRulesRefuseAnAgentThisBuildShipsNoRuleFor(t *testing.T) {
	e := newAgentRulesEnv(t)
	if err := e.callErr(t, "agent.rules.set", map[string]any{"agent": "gemini", "document": aRuleADifferentAnswer}, 2); err == nil {
		t.Error("a rule was accepted for an agent this build ships none for")
	}
	if err := e.callErr(t, "agent.rules.setEnabled", map[string]any{"agent": "gemini", "enabled": false}, 3); err == nil {
		t.Error("detection was switched for an agent this build ships no rule for")
	}
	if err := e.callErr(t, "agent.rules.delete", map[string]any{"agent": "gemini"}, 4); err == nil {
		t.Error("a delete was accepted for an agent this build ships no rule for")
	}
}

// TestAgentRulesUnwiredIsNotFound keeps the availability gate honest: a page
// that listed no agents at all would read as "nocx has no rules", which is a
// different claim from "this build cannot edit them".
func TestAgentRulesUnwiredIsNotFound(t *testing.T) {
	env := newLifecycleTestEnv(t)
	for _, call := range []struct {
		method string
		params any
	}{
		{"agent.rules", nil},
		{"agent.rules.set", map[string]any{"agent": "claude", "document": aRuleADifferentAnswer}},
		{"agent.rules.setEnabled", map[string]any{"agent": "claude", "enabled": false}},
		{"agent.rules.delete", map[string]any{"agent": "claude"}},
	} {
		err := func() *jsonrpcErrorObj {
			resp := jsonrpcCallWithID(t, env.conn, call.method, call.params, 7)
			var out rpcEnvelope
			if unmarshalErr := json.Unmarshal(resp, &out); unmarshalErr != nil {
				t.Fatalf("decode envelope: %v", unmarshalErr)
			}
			return out.Error
		}()
		if err == nil || err.Code != -32601 {
			t.Errorf("%s unwired answered %+v, want -32601", call.method, err)
		}
	}
}

// TestAgentRulesParamsAreRefusedBeforeAnythingIsBuilt pins the validator: a
// payload that names no agent, or carries a document that is not a string, is
// refused as params rather than reaching the store.
func TestAgentRulesParamsAreRefusedBeforeAnythingIsBuilt(t *testing.T) {
	e := newAgentRulesEnv(t)
	for _, bad := range []struct {
		method string
		params any
	}{
		{"agent.rules.set", map[string]any{"document": aRuleADifferentAnswer}},
		{"agent.rules.set", map[string]any{"agent": "claude", "document": 7}},
		{"agent.rules.set", map[string]any{"agent": "claude"}},
		{"agent.rules.setEnabled", map[string]any{"agent": "claude"}},
		{"agent.rules.setEnabled", map[string]any{"agent": "claude", "enabled": "off"}},
		{"agent.rules.delete", map[string]any{}},
		{"agent.rules", map[string]any{"agent": "claude"}},
	} {
		err := e.callErr(t, bad.method, bad.params, 9)
		if err == nil || err.Code != -32602 {
			t.Errorf("%s accepted %v: %+v", bad.method, bad.params, err)
		}
	}
}

// TestAgentRulesDTOConformsToContract is the cheap half of the same check:
// field tags, how an empty list renders, and whether a row's optional problem
// disappears rather than arriving as an empty string.
func TestAgentRulesDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.rules.schema.json")
	empty, err := json.Marshal(agentRulesResult{Directory: "/tmp/rules", Rules: []agentRuleRow{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), `"rules":null`) {
		t.Fatalf("an empty set marshals as null: %s", empty)
	}
	validateJSON(t, schema, empty, "the empty set")

	full, err := json.Marshal(agentRulesResult{
		Directory: "/tmp/rules",
		Rules: []agentRuleRow{{
			Agent: "claude", State: string(agentrule.StateUnreadable), Problem: "the file is empty",
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, full, "an unreadable document")

	noProblem, err := json.Marshal(agentRuleRow{Agent: "claude", State: string(agentrule.StateShipped)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(noProblem), `"problem"`) {
		t.Errorf("a row with no problem carries one anyway: %s", noProblem)
	}
}
