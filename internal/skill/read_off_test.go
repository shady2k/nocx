package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
)

// A SKILL THAT IS HERE AND SWITCHED OFF IS NOT A SKILL THAT IS NOT HERE
// (nocx-tq6r7).
//
// Reported from the product: right after installing a skill, the person said
// "включи" — turn it on. The assistant has no tool that can, deliberately
// (design §8: an installed skill lands inert and the PERSON turns it on after
// looking at it), so it reached for the one thing it could do and read the
// skill. nocx answered `skill "agentmail" was not found` about a skill that
// was on disk and on the Skills page at that moment. The assistant had no way
// to know that sentence was false, so it could not explain the situation
// either.
//
// The refusal is right; only its words were wrong. It now names the state and
// where the switch is, in the shape nextStepForAPage already uses.
func TestReadOfASwitchedOffSkillSaysSoRatherThanNotFound(t *testing.T) {
	configDir := t.TempDir()
	root := filepath.Join(configDir, "skills")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeOffTestSkill(t, root, "deploy")
	store := skill.NewStore(skill.OSFileSystem{},
		[]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}},
		storage.NewDocumentStore(configDir))

	if _, err := store.Read("deploy", ""); err != nil {
		t.Fatalf("reading an enabled skill: %v", err)
	}
	if err := store.SetEnabled("deploy", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	_, err := store.Read("deploy", "")
	if err == nil {
		t.Fatal("a switched-off skill was read; the assistant may only reach what the person switched on")
	}
	got := err.Error()
	if strings.Contains(got, "was not found") {
		t.Fatalf("refusal = %q, want it to stop claiming the skill is absent", got)
	}
	if !strings.Contains(got, "deploy") {
		t.Fatalf("refusal = %q, want it to name the skill", got)
	}
	if !strings.Contains(got, "switched off") {
		t.Fatalf("refusal = %q, want it to name the state", got)
	}
	// The next step belongs to a PERSON, and saying so is the whole point:
	// the assistant offered to turn the skill on because nothing ever told
	// it that it cannot.
	if !strings.Contains(got, "person") {
		t.Fatalf("refusal = %q, want it to say whose the switch is", got)
	}
}

// And the other half, in the same test file so neither can be weakened
// without the other being read: a skill that genuinely is not there still
// says so. Losing that distinction in the other direction would be the same
// defect mirrored.
func TestReadOfAnAbsentSkillStillSaysNotFound(t *testing.T) {
	configDir := t.TempDir()
	root := filepath.Join(configDir, "skills")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeOffTestSkill(t, root, "deploy")
	store := skill.NewStore(skill.OSFileSystem{},
		[]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}},
		storage.NewDocumentStore(configDir))

	_, err := store.Read("never-existed", "")
	if err == nil {
		t.Fatal("reading a skill that is not there succeeded")
	}
	if !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("refusal = %q, want the absent-skill sentence", err.Error())
	}
	if strings.Contains(err.Error(), "switched off") {
		t.Fatalf("refusal = %q, want no claim about a switch for a skill that is not there", err.Error())
	}
}

func writeOffTestSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	document := "---\nname: " + name + "\ndescription: how we deploy\n---\n\nRun make release.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
