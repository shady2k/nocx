package transport

// The expansion half of the approval question on the wire (nocx-4h0m7.5):
// what the verbatim command's variables read as, carried BESIDE the verbatim
// string. Three checks, and the third is the point (contracts/README row 3):
// the DTO satisfies the schema, the REAL notification off the REAL socket
// satisfies it, and the surface can tell "unsafe, left as written" from "we
// could not ask" because the wire keeps them apart.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

func expansionSuspension(facts *assistant.ExpansionFacts) func(runID string) error {
	return func(runID string) error {
		return &assistant.ApprovalRequestedError{Request: &assistant.ApprovalRequest{
			RunID: runID, Attempt: 1, Tool: "session.run", CallID: "call_1",
			Arguments: `{"command":"rm -rf $HOME/x $(id -u)","sessionId":"s"}`,
			ArgHash:   "hash-a",
			Effect:    content.EffectMutateDestructive,
			Expansion: facts,
		}}
	}
}

func askedExpansionFacts() *assistant.ExpansionFacts {
	return &assistant.ExpansionFacts{
		Asked:   true,
		Command: "rm -rf /home/dev/x $(id -u)",
		Parts: []assistant.ExpansionPart{
			{
				Text: "$HOME", Name: "HOME", Kind: assistant.ExpansionParameter,
				State: assistant.ExpansionExpanded, Value: "/home/dev",
			},
			{
				Text: "$(id -u)", Kind: assistant.ExpansionCommand,
				State:  assistant.ExpansionUnsafe,
				Reason: "it runs a command to produce its value, and nocx never runs a command to build a question",
			},
		},
		Assignments: []assistant.Assignment{{Name: "HOME", Value: "/tmp"}},
		Programs: []assistant.ProgramFact{
			{Word: "rm", Kind: assistant.ProgramAlias, Target: "rm -i"},
		},
	}
}

func unaskedExpansionFacts() *assistant.ExpansionFacts {
	return &assistant.ExpansionFacts{
		Asked:   false,
		Reason:  "nocx's shell integration is not live in this session, so no value was read",
		Command: "rm -rf $HOME/x $(id -u)",
		Parts: []assistant.ExpansionPart{
			{
				Text: "$HOME", Name: "HOME", Kind: assistant.ExpansionParameter,
				State: assistant.ExpansionUnasked,
			},
			{
				Text: "$(id -u)", Kind: assistant.ExpansionCommand,
				State:  assistant.ExpansionUnsafe,
				Reason: "it runs a command to produce its value, and nocx never runs a command to build a question",
			},
		},
	}
}

func TestAgentApprovalRequested_ExpansionDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")

	cases := map[string]*assistant.ExpansionFacts{
		"a shell answered":  askedExpansionFacts(),
		"no shell to ask":   unaskedExpansionFacts(),
		"a glob with count": {Asked: true, Command: "rm a.log", Parts: []assistant.ExpansionPart{{Text: "*.log", Kind: assistant.ExpansionGlob, State: assistant.ExpansionExpanded, Value: "a.log", Count: 143}}},
	}
	for name, facts := range cases {
		dto := agentApprovalRequested{
			RunID: "7", Attempt: 1, Tool: "session.run", CallID: "call_1",
			ArgHash: "hash-a", Arguments: `{"command":"rm -rf $HOME/x"}`,
			Reason: "policy", Effect: "mutate-destructive",
			Standing:  agentApprovalStanding{Reason: "the command uses an indirect wrapper or shell feature"},
			Expansion: facts,
		}
		raw, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		validateJSON(t, schema, raw, "agent.approvalRequested DTO with expansion ("+name+")")
	}
}

// The real notification off the real socket. A payload the test itself built
// proves the struct is well-formed, not that the server sends it.
func TestAgentApprovalRequested_ExpansionOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: expansionSuspension(askedExpansionFacts())},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please clean it", "cwd": "/repo",
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	validateJSON(t, schema, raw, "agent.approvalRequested params with expansion (real socket)")

	var got struct {
		Arguments string `json:"arguments"`
		Expansion struct {
			Asked   bool   `json:"asked"`
			Command string `json:"command"`
			Parts   []struct {
				Text  string `json:"text"`
				State string `json:"state"`
				Value string `json:"value"`
			} `json:"parts"`
		} `json:"expansion"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	// BESIDE, NEVER INSTEAD: the arguments the person answers about still
	// carry the model's own verbatim command, and the expanded form is a
	// separate field the surface labels as a reading.
	if got.Arguments != `{"command":"rm -rf $HOME/x $(id -u)","sessionId":"s"}` {
		t.Fatalf("arguments = %q, want the model's own proposal untouched", got.Arguments)
	}
	if got.Expansion.Command == got.Arguments || got.Expansion.Command != "rm -rf /home/dev/x $(id -u)" {
		t.Fatalf("expansion.command = %q, want the expanded display form", got.Expansion.Command)
	}
	var sawExpanded, sawUnsafe bool
	for _, part := range got.Expansion.Parts {
		if part.Text == "$HOME" && part.State == "expanded" && part.Value == "/home/dev" {
			sawExpanded = true
		}
		if part.Text == "$(id -u)" && part.State == "unsafe" {
			sawUnsafe = true
		}
	}
	if !sawExpanded || !sawUnsafe {
		t.Fatalf("parts = %+v, want the value shown and the substitution left as written", got.Expansion.Parts)
	}
}

// "We could not ask" is its own state on the wire. A surface that received
// it as "unsafe" would tell a person nocx had refused to read a value it had
// simply been unable to read.
func TestAgentApprovalRequested_NotAskedIsItsOwnStateOnTheWire(t *testing.T) {
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: expansionSuspension(unaskedExpansionFacts())},
	}}
	h := newAskHarness(t, client)
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": sid, "question": "please clean it", "cwd": "/repo",
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	var got struct {
		Expansion struct {
			Asked  bool   `json:"asked"`
			Reason string `json:"reason"`
			Parts  []struct {
				Text  string `json:"text"`
				State string `json:"state"`
			} `json:"parts"`
		} `json:"expansion"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if got.Expansion.Asked {
		t.Fatal("asked = true although no shell was consulted")
	}
	if got.Expansion.Reason == "" {
		t.Fatal("nothing was expanded and the wire carried no reason for the window to show")
	}
	for _, part := range got.Expansion.Parts {
		if part.Text == "$HOME" && part.State != "unasked" {
			t.Fatalf("$HOME reached the surface as %q, want unasked", part.State)
		}
	}
}
