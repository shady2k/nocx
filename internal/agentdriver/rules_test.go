package agentdriver_test

// Where a rule COMES FROM (nocx-y6w66): the seam that lets a person's own
// document replace the one this build carries, switch an agent off, or break
// in a way that refuses rather than falls back.
//
// These are the registry's own three answers. What the store does with a file
// is internal/agentrule's, and the pair of them on a real pane is
// internal/transport's.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
)

// stubRules is a RuleSource with one canned answer per agent.
type stubRules map[string]agentdriver.RuleState

func (s stubRules) RuleState(agent string) agentdriver.RuleState { return s[agent] }

// shippedFrame is the frame the shipped claude rule reads as free_text: an
// idle input box bounded by two full-width rules, with the cursor parked after
// the prompt marker — the screen the whole typing decision is about. It is the
// same chrome internal/transport's tests feed, because a frame this rule reads
// is the only thing that makes the assertions below mean anything.
func shippedFrame(t *testing.T) panegrid.Frame {
	t.Helper()
	const cols = 32
	rule := strings.Repeat("─", cols)
	lines := make([]string, 14)
	lines[6] = "              0 tokens"
	lines[7] = rule
	lines[8] = "❯ "
	lines[9] = rule
	lines[11] = "  ⏵⏵ auto mode on"
	return screen(t, cols, 14, lines, 2, 8)
}

// TestTheShippedRulesAreReadableBackOutOfTheRegistry is what a surface edits
// from and what a delete restores: the documents this build carries, by the
// agents that carry them. Reading them back is not the registry's usual job —
// Classify is — and it is the only way a person can be shown one.
func TestTheShippedRulesAreReadableBackOutOfTheRegistry(t *testing.T) {
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	agents := reg.Agents()
	if len(agents) != 1 || agents[0] != "claude" {
		t.Fatalf("agents = %v, want [claude]", agents)
	}
	shipped := reg.ShippedRules()
	if len(shipped) != 1 || shipped[0].Agent != "claude" {
		t.Fatalf("shipped rules = %+v, want one for claude", shipped)
	}
	if len(shipped[0].Branches) == 0 || len(shipped[0].Anchors) == 0 {
		t.Error("the shipped rule came back empty, so a person would be editing nothing")
	}
	// It is the rule the registry actually evaluates, not a copy that could
	// drift from it.
	if driver, ok := reg.For("claude"); !ok {
		t.Fatal("the registry has no driver for claude")
	} else if documented, ok := driver.(agentdriver.Documented); !ok {
		t.Fatal("the shipped driver is not readable as a document")
	} else if documented.Rule().Default != shipped[0].Default {
		t.Error("the document read back is not the document the driver evaluates")
	}
}

// TestCompileHoldsAPersonToTheShippedGrammar: a rule a person writes is read
// by the same code the embedded ones are, so what it refuses is refused while
// they are looking at the document and not on the first frame that finds it
// silent.
func TestCompileHoldsAPersonToTheShippedGrammar(t *testing.T) {
	if _, err := agentdriver.Compile([]byte(`{"agent":"claude","default":"unknown"}`)); err != nil {
		t.Errorf("a minimal valid rule was refused: %v", err)
	}
	for name, raw := range map[string]string{
		"not JSON":                    `{`,
		"no agent":                    `{"default":"unknown"}`,
		"a state that does not exist": `{"agent":"claude","default":"probably_idle"}`,
		"an anchor from nothing":      `{"agent":"claude","anchors":[{"name":"a","kind":"offset","from":"nope"}],"default":"unknown"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := agentdriver.Compile([]byte(raw)); err == nil {
				t.Fatal("a document that could never answer was compiled")
			}
		})
	}
}

// TestARegistryWithoutASourceAnswersFromWhatItCarries is the state every test
// in this repository is in, and it must stay exactly what it was.
func TestARegistryWithoutASourceAnswersFromWhatItCarries(t *testing.T) {
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if got := reg.Classify("claude", shippedFrame(t)); got != agentdriver.StateFreeText {
		t.Errorf("an idle screen reads %q, want %q", got, agentdriver.StateFreeText)
	}
}

// TestASourceReplacesSwitchesOffAndRefusesRatherThanFallingBack is the whole
// policy in one place: the three things a person's own document can mean, and
// the failing-closed direction of the two that are not a usable rule.
func TestASourceReplacesSwitchesOffAndRefusesRatherThanFallingBack(t *testing.T) {
	replacement, err := agentdriver.Compile([]byte(`{"agent":"claude","default":"error"}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	cases := []struct {
		name  string
		state agentdriver.RuleState
		want  agentdriver.State
	}{
		{"no document of theirs", agentdriver.RuleState{}, agentdriver.StateFreeText},
		{"their own rule", agentdriver.RuleState{Driver: replacement}, agentdriver.StateError},
		{"switched off", agentdriver.RuleState{Off: true}, agentdriver.StateUnknown},
		{"a document that does not compile", agentdriver.RuleState{Broken: true}, agentdriver.StateUnknown},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reg, err := agentdriver.NewRegistry(agentdriver.Claude())
			if err != nil {
				t.Fatalf("registry: %v", err)
			}
			reg.SetRuleSource(stubRules{"claude": test.state})
			if got := reg.Classify("claude", shippedFrame(t)); got != test.want {
				t.Errorf("state = %q, want %q", got, test.want)
			}
		})
	}
}

// TestASourceCannotIntroduceAnAgentThisBuildDoesNotRead: a rule REPLACES a
// shipped one, so there is nothing for a document about an agent this build
// never learned to read to replace. The registry's own set is the bound.
func TestASourceCannotIntroduceAnAgentThisBuildDoesNotRead(t *testing.T) {
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	replacement, err := agentdriver.Compile([]byte(`{"agent":"gemini","default":"free_text"}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	reg.SetRuleSource(stubRules{"gemini": {Driver: replacement}})
	if got := reg.Classify("gemini", shippedFrame(t)); got != agentdriver.StateUnknown {
		t.Errorf("gemini reads %q, want %q", got, agentdriver.StateUnknown)
	}
	if _, ok := reg.For("gemini"); ok {
		t.Error("a source introduced an agent into the registry")
	}
}

// TestDetachingASourcePutsTheShippedRulesBack: nil is a state, and it is the
// one a registry built for a test is in.
func TestDetachingASourcePutsTheShippedRulesBack(t *testing.T) {
	reg, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	reg.SetRuleSource(stubRules{"claude": {Off: true}})
	if _, ok := reg.For("claude"); ok {
		t.Fatal("a switched-off agent still has a driver")
	}
	reg.SetRuleSource(nil)
	if got := reg.Classify("claude", shippedFrame(t)); got != agentdriver.StateFreeText {
		t.Errorf("after detaching, an idle screen reads %q, want %q", got, agentdriver.StateFreeText)
	}
}
