package skill_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/skill/builtin"
)

func TestPrecedenceAuthoredBeatsBuiltinBeatsManaged(t *testing.T) {
	authored, managed := t.TempDir(), t.TempDir()
	writeSkill(t, authored, "skill-authoring", "name: skill-authoring\ndescription: mine", "a")
	writeSkill(t, managed, "skill-authoring", "name: skill-authoring\ndescription: drafted", "m")
	roots := []skill.Root{
		{Dir: authored, Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: managed, Provenance: skill.ProvenanceManaged},
	}

	got := skill.Discover(roots)
	if len(got) != 1 || got[0].Provenance != skill.ProvenanceAuthored {
		t.Fatalf("want the authored skill to win, got %+v", got)
	}
}

func TestPrecedenceFallsBackToBuiltinThenManaged(t *testing.T) {
	authored, managed := t.TempDir(), t.TempDir()
	writeSkill(t, authored, "skill-authoring", "name: skill-authoring\ndescription: mine", "a")
	writeSkill(t, managed, "skill-authoring", "name: skill-authoring\ndescription: drafted", "m")
	roots := []skill.Root{
		{Dir: authored, Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: managed, Provenance: skill.ProvenanceManaged},
	}

	got := skill.Discover(roots[1:])
	if len(got) != 1 || got[0].Provenance != skill.ProvenanceBuiltin {
		t.Fatalf("want the builtin skill after authored removal, got %+v", got)
	}

	got = skill.Discover(roots[2:])
	if len(got) != 1 || got[0].Provenance != skill.ProvenanceManaged {
		t.Fatalf("want the managed skill after authored and builtin removal, got %+v", got)
	}
}

func TestBuiltinSkillUsesNormalDiscoveryParsing(t *testing.T) {
	got := skill.Discover([]skill.Root{{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin}})
	if len(got) != 1 {
		t.Fatalf("want one builtin skill, got %d", len(got))
	}
	if got[0].Name != "skill-authoring" {
		t.Fatalf("name = %q, want skill-authoring", got[0].Name)
	}
	if got[0].Description == "" {
		t.Fatal("builtin skill has empty description")
	}
}
