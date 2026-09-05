package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// atCap and overCap are the two sides of the boundary. They are built from
// the constant rather than from a literal so the test cannot drift away from
// the number it is pinning.
func atCap() string   { return strings.Repeat("x", maxDescriptionRunes) }
func overCap() string { return strings.Repeat("x", maxDescriptionRunes+1) }

// ordinaryDescription is the length of a real published skill's description —
// AgentMail's, 449 characters — so the "and on a normal machine it succeeds"
// case is a measurement and not a guess.
func ordinaryDescription() string {
	return strings.Repeat("word ", 89) + "tail"
}

func TestCreateRefusesADescriptionOverTheCap(t *testing.T) {
	root := t.TempDir()
	fsys := &fakeFS{}
	store := NewStore(fsys, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)

	err := store.Create("deploy", overCap(), "body")
	if err == nil {
		t.Fatal("want an over-long description refused at the write")
	}
	if !strings.Contains(err.Error(), "2048") {
		t.Errorf("refusal does not name the limit: %v", err)
	}
	if fsys.mkdirCalls != 0 {
		t.Error("an over-long description reached the filesystem")
	}
	if _, statErr := os.Stat(filepath.Join(root, "deploy")); statErr == nil {
		t.Error("an over-long description was written anyway")
	}
}

func TestUpdateRefusesADescriptionOverTheCap(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	if err := store.Create("deploy", ordinaryDescription(), "body"); err != nil {
		t.Fatalf("create with an ordinary description: %v", err)
	}
	if err := store.Update("deploy", overCap(), "body two"); err == nil {
		t.Fatal("want an over-long description refused at the update")
	}
	// #nosec G304 -- the path is this test's own temporary directory.
	got, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(got), ordinaryDescription()) {
		t.Error("the refused update replaced the description that was there")
	}
}

func TestCreateAcceptsADescriptionAtTheCapAndAnOrdinaryOne(t *testing.T) {
	for name, description := range map[string]string{
		"ordinary": ordinaryDescription(),
		"at-cap":   atCap(),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
			if err := store.Create("deploy", description, "body"); err != nil {
				t.Fatalf("Create(%d characters) = %v, want it written", len(description), err)
			}
			index := Discover([]Root{{Dir: root, Provenance: ProvenanceManaged}})
			if len(index) != 1 || index[0].Description != description {
				t.Fatalf("the written skill did not come back whole: %+v", index)
			}
		})
	}
}

// TestTheCapCountsCharactersNotBytes pins the fairness decision: a Russian
// description of exactly the cap is as long as an English one of exactly the
// cap, which a byte cap would not have made true.
func TestTheCapCountsCharactersNotBytes(t *testing.T) {
	root := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: root, Provenance: ProvenanceManaged}}, nil)
	cyrillic := strings.Repeat("ф", maxDescriptionRunes)
	if len(cyrillic) <= maxDescriptionRunes {
		t.Fatalf("the fixture is not multi-byte: %d bytes for %d runes", len(cyrillic), maxDescriptionRunes)
	}
	if err := store.Create("deploy", cyrillic, "body"); err != nil {
		t.Fatalf("a %d-character Cyrillic description was refused: %v", maxDescriptionRunes, err)
	}
	// And it must come back. A cap counted in runes against a frontmatter
	// budget counted in bytes would accept this write and then drop the file
	// as malformed, which is the same feature disappearing by the other door.
	index := Discover([]Root{{Dir: root, Provenance: ProvenanceManaged}})
	if len(index) != 1 || index[0].Description != cyrillic {
		t.Fatalf("a Cyrillic description at the cap was written but not discovered: %+v", index)
	}
	if err := store.Create("deploy-two", strings.Repeat("ф", maxDescriptionRunes+1), "body"); err == nil {
		t.Fatal("want one character over the cap refused, in any script")
	}
}

func TestDocumentPreviewRefusesADescriptionOverTheCap(t *testing.T) {
	document := "---\nname: deploy\ndescription: " + overCap() + "\n---\nbody\n"
	if _, err := documentPreview(document, "https://example.test/SKILL.md"); err == nil {
		t.Fatal("want a preview of an over-long description refused before the person is offered it")
	} else if !strings.Contains(err.Error(), "2048") {
		t.Errorf("refusal does not name the limit: %v", err)
	}
	ordinary := "---\nname: deploy\ndescription: " + ordinaryDescription() + "\n---\nbody\n"
	if _, err := documentPreview(ordinary, "https://example.test/SKILL.md"); err != nil {
		t.Fatalf("an ordinary published description was refused: %v", err)
	}
}

// TestDiscoverDropsAnAlreadyWrittenOverLongDescription is the read path: a
// file placed by hand cannot be reached by the write-time cap, and what the
// index must never carry is prose nobody bounded.
func TestDiscoverDropsAnAlreadyWrittenOverLongDescription(t *testing.T) {
	root := t.TempDir()
	writeExistingSkill(t, root, "hostile", "name: hostile\ndescription: "+overCap(), "body")
	writeExistingSkill(t, root, "ordinary", "name: ordinary\ndescription: "+ordinaryDescription(), "body")

	got := Discover([]Root{{Dir: root, Provenance: ProvenanceAuthored}})

	if len(got) != 1 {
		t.Fatalf("Discover() returned %d skills, want only the one inside the cap: %+v", len(got), got)
	}
	if got[0].Name != "ordinary" {
		t.Fatalf("Discover() kept %q, want %q", got[0].Name, "ordinary")
	}
}
