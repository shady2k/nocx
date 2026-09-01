package skill_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/skill"
)

func writeSkill(t *testing.T, root, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	doc := "---\n" + frontmatter + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDiscoverReadsNameAndDescription(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "name: deploy\ndescription: How we ship this service.", "Run make release.")

	got := skill.Discover([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}})

	if len(got) != 1 {
		t.Fatalf("want 1 skill, got %d", len(got))
	}
	if got[0].Name != "deploy" {
		t.Errorf("name = %q, want %q", got[0].Name, "deploy")
	}
	if got[0].Description != "How we ship this service." {
		t.Errorf("description = %q", got[0].Description)
	}
	if got[0].Provenance != skill.ProvenanceAuthored {
		t.Errorf("provenance = %q, want authored", got[0].Provenance)
	}
}

func TestDiscoverSkipsSymlinkedSkillDocument(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "hostile.md")
	if err := os.WriteFile(outside, []byte("---\nname: x\ndescription: injected\n---\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	dir := filepath.Join(root, "x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got := skill.Discover([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}); len(got) != 0 {
		t.Fatalf("want the symlinked skill skipped, got %d", len(got))
	}
}

func TestDiscoverSkipsSymlinkedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "escape", "name: escape\ndescription: injected", "body")
	if err := os.Symlink(filepath.Join(outside, "escape"), filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got := skill.Discover([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}); len(got) != 0 {
		t.Fatalf("want the symlinked skill directory skipped, got %d", len(got))
	}
}

func TestDiscoverSkipsMalformedAndOversizedFrontmatter(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o700); err != nil {
		t.Fatalf("mkdir broken: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", "SKILL.md"), []byte("---\nname: broken\ndescription: missing closing\n"), 0o600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	writeSkill(t, root, "large", "name: large\ndescription: "+string(make([]byte, skill.MaxFrontmatterBytes)), "body")
	writeSkill(t, root, "empty", "name: empty\ndescription:", "body")

	if got := skill.Discover([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}); len(got) != 0 {
		t.Fatalf("want malformed, oversized, and empty-description skills skipped, got %d", len(got))
	}
}

func TestDiscoverDefaultsNameAndDeduplicatesByPrecedence(t *testing.T) {
	authored := t.TempDir()
	managed := t.TempDir()
	writeSkill(t, authored, "shared", "description: authored", "body")
	writeSkill(t, managed, "shared", "name: shared\ndescription: managed", "body")
	writeSkill(t, managed, "other", "description: other", "body")

	got := skill.Discover([]skill.Root{
		{Dir: authored, Provenance: skill.ProvenanceAuthored},
		{Dir: managed, Provenance: skill.ProvenanceManaged},
	})
	if len(got) != 2 {
		t.Fatalf("want two deduplicated skills, got %d", len(got))
	}
	if got[0].Name != "shared" || got[0].Description != "authored" || got[0].Provenance != skill.ProvenanceAuthored {
		t.Fatalf("first root must win for shared skill: %+v", got[0])
	}
	if got[1].Name != "other" {
		t.Fatalf("second unique skill = %+v", got[1])
	}
}

func TestDiscoverStopsAtTheEntryCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 300; i++ {
		writeSkill(t, root, "skill-"+itoa(i), "name: skill-"+itoa(i)+"\ndescription: d", "b")
	}

	got := skill.Discover([]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}})

	if len(got) != 256 {
		t.Fatalf("want the 256-entry cap applied, got %d", len(got))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
