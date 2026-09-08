package agenttools

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The renderer's approval window keys two sentences on the tool name the
// wire carries — the command block for the command carrier, the network row
// for fetch.url — and for four days it keyed them on `run`, a name this
// table stopped declaring when the shell tool became `session.run`
// (d71263ab). Nothing went red: the renderer's type said `string`, so every
// fixture agreed with the component, and the component was wrong.
//
// contracts/agent.approvalRequested.schema.json now ENUMERATES the names, so
// the generated renderer type is a union and a comparison against a name no
// tool declares is a compile error rather than a branch never taken. This
// test is the other end of that chain: the enum is a copy of this table, and
// a copy that nothing checks is exactly the trap being closed. Rename a row
// here and this fails in the same second.
//
// It reads the repository's contract rather than an embedded copy on
// purpose: the file the generator reads is the file that must agree.
func TestApprovalRequestedToolEnumMatchesTheTable(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/agent.approvalRequested.schema.json")
	if err != nil {
		t.Fatalf("read the approval contract: %v", err)
	}
	var contract struct {
		Properties struct {
			Tool struct {
				Enum []string `json:"enum"`
			} `json:"tool"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse the approval contract: %v", err)
	}
	declared := make([]string, 0, len(declarations))
	for _, d := range declarations {
		declared = append(declared, d.Name)
	}
	if len(contract.Properties.Tool.Enum) == 0 {
		t.Fatal("contracts/agent.approvalRequested.schema.json declares no enum for `tool`: " +
			"without it the renderer's type is a bare string again and a rename in this table goes dark")
	}
	inContract := slices.Clone(contract.Properties.Tool.Enum)
	inTable := slices.Clone(declared)
	slices.Sort(inContract)
	slices.Sort(inTable)
	if !slices.Equal(inContract, inTable) {
		for _, name := range inTable {
			if !slices.Contains(inContract, name) {
				t.Errorf("tool %q is declared here and missing from the contract's enum: the renderer cannot be handed a name the wire type does not admit", name)
			}
		}
		for _, name := range inContract {
			if !slices.Contains(inTable, name) {
				t.Errorf("the contract's enum names %q, which no declaration carries: a renderer comparing against it compares against a dead string", name)
			}
		}
	}
}
