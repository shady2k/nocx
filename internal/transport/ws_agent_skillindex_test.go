package transport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
)

// The run's skill index is the seam that decides what the model is told a
// skill library contains: skillRefsForGrant fills it and it lands in
// SystemPromptFacts.Skills. Asserting inertness here rather than on a field
// is the point — a `enabled: false` on a row proves the document remembers
// something, and only the index proves the assistant is not offered it.
func skillIndexStand(t *testing.T, configDir string) (*skill.Store, content.Grant, agenttools.Registry) {
	t.Helper()
	store := skill.NewStore(skill.OSFileSystem{}, []skill.Root{
		{Dir: filepath.Join(configDir, "skills"), Provenance: skill.ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: skill.ProvenanceManaged},
		{Dir: filepath.Join(configDir, "installed-skills"), Provenance: skill.ProvenanceInstalled},
	}, storage.NewDocumentStore(configDir))
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	grant := content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceContent, ID: "skill"}},
	}
	return store, grant, registry
}

func writeSkillUnder(t *testing.T, dir, name, description, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

func indexHas(refs []assistant.SkillRef, name string) bool {
	for _, ref := range refs {
		if ref.Name == name {
			return true
		}
	}
	return false
}

func TestAnInstalledSkillReachesTheRunsIndexOnlyAfterThePersonTurnsItOn(t *testing.T) {
	configDir := t.TempDir()
	store, grant, registry := skillIndexStand(t, configDir)
	writeSkillUnder(t, filepath.Join(configDir, "installed-skills"), "weather", "Answer questions about the weather", "body")
	// The digest an install records, so the only thing left keeping this
	// skill out of the index is that nobody has turned it on.
	if err := store.Approve("weather"); err != nil {
		t.Fatalf("record the installed digest: %v", err)
	}

	if refs := skillRefsForGrant(&grant, store, registry); indexHas(refs, "weather") {
		t.Fatalf("skill refs = %+v, want an installed skill nobody has turned on kept out of the run's index", refs)
	}

	if err := store.SetEnabled("weather", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	refs := skillRefsForGrant(&grant, store, registry)
	if !indexHas(refs, "weather") {
		t.Fatalf("skill refs = %+v, want the skill the person turned on", refs)
	}
}

func TestAnAuthoredSkillIsInTheRunsIndexWithNobodyTurningItOn(t *testing.T) {
	configDir := t.TempDir()
	store, grant, registry := skillIndexStand(t, configDir)
	writeSkillUnder(t, filepath.Join(configDir, "skills"), "deploy", "Deploy the service", "body")

	refs := skillRefsForGrant(&grant, store, registry)
	if !indexHas(refs, "deploy") {
		t.Fatalf("skill refs = %+v, want a skill the person wrote themselves; only the installed root arrives inert", refs)
	}
}

func TestAChangedInstalledSkillLeavesTheRunsIndexAndComesBackWithItsBytes(t *testing.T) {
	configDir := t.TempDir()
	store, grant, registry := skillIndexStand(t, configDir)
	installed := filepath.Join(configDir, "installed-skills")
	writeSkillUnder(t, installed, "weather", "Answer questions about the weather", "body")
	if err := store.Approve("weather"); err != nil {
		t.Fatalf("record the installed digest: %v", err)
	}
	if err := store.SetEnabled("weather", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if refs := skillRefsForGrant(&grant, store, registry); !indexHas(refs, "weather") {
		t.Fatalf("this test is not exercising what it claims: %+v", refs)
	}

	path := filepath.Join(installed, "weather", "SKILL.md")
	original, err := os.ReadFile(path) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, original...), "one more line\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if refs := skillRefsForGrant(&grant, store, registry); indexHas(refs, "weather") {
		t.Fatalf("skill refs = %+v, want a changed skill out of the run's index", refs)
	}

	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if refs := skillRefsForGrant(&grant, store, registry); !indexHas(refs, "weather") {
		t.Fatalf("skill refs = %+v, want the skill back once its bytes are back", refs)
	}
}
