package skill_test

// nocx-j0lei. discoverDetailed refuses a skill and continues, logging. The
// Settings list simply did not contain it, so a person who put a SKILL.md on
// disk and cannot find it in the product had nothing to read but a slog line
// they will never see — the soft degrade AGENTS.md refuses.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/storage"
)

func refusalStand(t *testing.T) (*skill.Store, string) {
	t.Helper()
	configDir := t.TempDir()
	root := filepath.Join(configDir, "skills")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := skill.NewStore(skill.OSFileSystem{},
		[]skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}},
		storage.NewDocumentStore(configDir))
	return store, root
}

func writeRefusedSkill(t *testing.T, root, dir, document string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(root, dir, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatalf("write %s: %v", dir, err)
	}
}

func TestListNamesEverySkillShapedDirectoryItRefused(t *testing.T) {
	longDescription := strings.Repeat("x", 2500)
	for _, tc := range []struct {
		dir      string
		document string
		reason   skill.RefusalReason
		says     []string
	}{
		{
			dir:      "no-frontmatter",
			document: "# just a heading\n",
			reason:   skill.RefusedFrontmatter,
			says:     []string{"frontmatter"},
		},
		{
			dir:      "bad-name",
			document: "---\nname: Not A Name\ndescription: d\n---\nbody\n",
			reason:   skill.RefusedName,
			says:     []string{"Not A Name"},
		},
		{
			dir:      "no-description",
			document: "---\nname: nodesc\n---\nbody\n",
			reason:   skill.RefusedNoDescription,
			says:     []string{"description"},
		},
		{
			dir:      "long-description",
			document: "---\nname: longdesc\ndescription: " + longDescription + "\n---\nbody\n",
			reason:   skill.RefusedDescriptionTooLong,
			says:     []string{strconv.Itoa(len(longDescription))},
		},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			store, root := refusalStand(t)
			writeRefusedSkill(t, root, tc.dir, tc.document)

			listed, err := store.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(listed.Skills) != 0 {
				t.Fatalf("a refused directory was listed as a usable skill: %+v", listed.Skills)
			}
			if len(listed.Refused) != 1 {
				t.Fatalf("refused = %+v, want the one directory", listed.Refused)
			}
			got := listed.Refused[0]
			if got.Directory != tc.dir {
				t.Errorf("directory = %q, want %q", got.Directory, tc.dir)
			}
			if got.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.reason)
			}
			if got.Provenance != skill.ProvenanceAuthored {
				t.Errorf("provenance = %q, want the root it was found in", got.Provenance)
			}
			for _, want := range tc.says {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail %q does not say %q", got.Detail, want)
				}
			}
			if strings.TrimSpace(got.Detail) == "" {
				t.Error("the reason reached the person as an empty sentence")
			}
		})
	}
}

func TestARefusedDirectoryIsNeverOfferedToTheAssistant(t *testing.T) {
	store, root := refusalStand(t)
	writeRefusedSkill(t, root, "broken", "---\nname: broken\n---\nbody\n")
	writeRefusedSkill(t, root, "fine", "---\nname: fine\ndescription: a usable skill\n---\nbody\n")

	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceAuthored}}
	for _, found := range skill.Discover(roots) {
		if found.Name == "broken" {
			t.Fatal("a refused directory reached Discover, which is what the assistant is offered")
		}
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.Skills) != 1 || listed.Skills[0].Name != "fine" {
		t.Fatalf("skills = %+v, want only the usable one", listed.Skills)
	}
	if len(listed.Refused) != 1 || listed.Refused[0].Directory != "broken" {
		t.Fatalf("refused = %+v, want only the broken one", listed.Refused)
	}
}
