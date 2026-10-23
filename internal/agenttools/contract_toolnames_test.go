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
// The contract enumerates builtin names separately from its constrained MCP
// namespace. The generated renderer type remains a builtin union plus an MCP
// template, so a stale builtin comparison is still a type error. This test
// keeps the builtin branch equal to the declaration table when a row changes.
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
				OneOf []struct {
					Enum []string `json:"enum"`
				} `json:"oneOf"`
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
	if len(contract.Properties.Tool.OneOf) != 2 || len(contract.Properties.Tool.OneOf[0].Enum) == 0 {
		t.Fatal("approval tool must retain its builtin enum separately from the constrained MCP namespace")
	}
	inContract := slices.Clone(contract.Properties.Tool.OneOf[0].Enum)
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
