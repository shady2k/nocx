package skill_test

import (
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/skill/builtin"
)

func TestBuiltinDiscoveryWithFreshProfileRoots(t *testing.T) {
	authored := filepath.Join(t.TempDir(), "skills")
	managed := filepath.Join(t.TempDir(), "managed-skills")
	roots := []skill.Root{
		{Dir: authored, Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: managed, Provenance: skill.ProvenanceManaged},
	}

	got := skill.Discover(roots)
	if len(got) != 1 {
		t.Fatalf("fresh profile discovered %d skills, want exactly one builtin skill: %+v", len(got), got)
	}
	if got[0].Name != "skill-authoring" || got[0].Provenance != skill.ProvenanceBuiltin {
		t.Fatalf("fresh profile discovered %+v, want skill-authoring from builtin root", got[0])
	}
}

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
