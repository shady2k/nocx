package agenttools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestSkillsResolveDeclarationMatchesFetchBoundary(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	decl, ok := reg.Lookup("skills.resolve")
	if !ok {
		t.Fatal("skills.resolve is not declared")
	}
	if decl.Executes != InGo {
		t.Fatalf("executes = %v, want InGo", decl.Executes)
	}
	if decl.Narrow == nil {
		t.Fatal("skills.resolve has no capability constructor")
	}
	if len(decl.Declaration.Effect) != 1 || decl.Declaration.Effect[0] != content.EffectCrossBoundary {
		t.Fatalf("effects = %v, want only cross-boundary", decl.Declaration.Effect)
	}
	if len(decl.ResourceKinds) != 1 || decl.ResourceKinds[0] != content.ResourceDestination {
		t.Fatalf("resource kinds = %v, want only destination", decl.ResourceKinds)
	}
	resources, err := decl.ResolveResources(map[string]any{"url": "https://github.com/acme/skills"}, RunContext{})
	if err != nil {
		t.Fatalf("ResolveResources: %v", err)
	}
	if len(resources) != 1 || resources[0].Kind != content.ResourceDestination || resources[0].ID != "https://github.com/acme/skills" {
		t.Fatalf("resources = %+v, want the supplied address", resources)
	}
}

func TestSkillsResolveWordingPassesThroughUnknownAddress(t *testing.T) {
	url := skillsResolveURLDescription(t)
	for _, phrase := range []string{
		"exactly as the person gave it",
		"as a page named it",
		"do not judge in advance whether the address is enumerable",
		"I cannot enumerate that",
		"fall back to skills.install",
	} {
		if !strings.Contains(url, phrase) {
			t.Fatalf("url description %q omits wording %q", url, phrase)
		}
	}
}

func skillsResolveURLDescription(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/skills.resolve.schema.json")
	if err != nil {
		t.Fatalf("read the skills.resolve contract: %v", err)
	}
	var contract struct {
		Properties struct {
			URL struct {
				Description string `json:"description"`
			} `json:"url"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse the skills.resolve contract: %v", err)
	}
	if contract.Properties.URL.Description == "" {
		t.Fatal("the skills.resolve contract's url field carries no description: the model is then told nothing about the address it must resolve")
	}
	return contract.Properties.URL.Description
}
