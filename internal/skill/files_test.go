package skill_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/shady2k/nocx/internal/skill"
)

// writeSkillFile puts one support file inside an already-written skill, which
// is what a bundle install produces and what nothing on the wire could name
// until Files existed.
func writeSkillFile(t *testing.T, root, name, rel, body string) {
	t.Helper()
	path := filepath.Join(root, name, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// The whole point of design §8: the card names every file the skill carries,
// scripts included, so the person can look at the executable text before they
// turn the skill on. SKILL.md leads because it is the file they came for.
func TestFilesNamesEveryFileOfABundle(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "weather", "name: weather\ndescription: d", "body")
	writeSkillFile(t, root, "weather", "references/stations.md", "stations")
	writeSkillFile(t, root, "weather", "scripts/fetch.sh", "#!/bin/sh\n")
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceInstalled}}

	got, err := skill.Files(roots, "weather")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"SKILL.md", "references/stations.md", "scripts/fetch.sh"}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %v, want %v", got.Files, want)
	}
	for i, path := range want {
		if got.Files[i] != path {
			t.Fatalf("files = %v, want %v", got.Files, want)
		}
	}
	if got.Name != "weather" || got.Provenance != skill.ProvenanceInstalled {
		t.Fatalf("got %+v, want the skill it resolved named", got)
	}
	if got.Truncated {
		t.Fatalf("truncated = true for %d files", len(got.Files))
	}
	if got.MaxFiles != skill.MaxSkillFiles {
		t.Fatalf("maxFiles = %d, want the cap %d", got.MaxFiles, skill.MaxSkillFiles)
	}
}

// A skill that is OFF is exactly the skill this list exists for: it landed
// inert so the person could open it and see what it is made of before turning
// it on, and a listing that skipped it would make that look impossible.
func TestFilesAnswersForASkillThatIsOff(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "weather", "name: weather\ndescription: d", "body")
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceInstalled}}

	got, err := skill.Files(roots, "weather")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(got.Files) != 1 || got.Files[0] != "SKILL.md" {
		t.Fatalf("files = %v, want the one file it carries", got.Files)
	}
}

// Reading is not offering, so an embedded root answers as a directory root
// does — the same rule File already keeps.
func TestFilesAnswersForAnEmbeddedRoot(t *testing.T) {
	roots := []skill.Root{{
		FS: fstest.MapFS{
			"authoring/SKILL.md":            {Data: []byte("---\nname: authoring\ndescription: d\n---\nbody\n")},
			"authoring/references/rules.md": {Data: []byte("rules")},
		},
		Provenance: skill.ProvenanceBuiltin,
	}}

	got, err := skill.Files(roots, "authoring")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(got.Files) != 2 || got.Files[0] != "SKILL.md" || got.Files[1] != "references/rules.md" {
		t.Fatalf("files = %v, want both embedded files", got.Files)
	}
}

// The cut is REPORTED, never silent: a card that quietly showed 256 of 300
// files would be the interface asserting a manifest it had not read.
func TestFilesReportsTheCut(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "big", "name: big\ndescription: d", "body")
	for i := 0; i < skill.MaxSkillFiles+8; i++ {
		writeSkillFile(t, root, "big", "references/"+strconv.Itoa(i)+".md", "x")
	}
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceInstalled}}

	got, err := skill.Files(roots, "big")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(got.Files) != skill.MaxSkillFiles {
		t.Fatalf("files = %d, want the cap %d", len(got.Files), skill.MaxSkillFiles)
	}
	if !got.Truncated {
		t.Fatal("truncated = false while the list was cut")
	}
}

// A name no root holds has nothing to describe, so it is an ERROR and not an
// empty list — the same split file.go decided for a file that is gone.
func TestFilesRefusesAnUnknownSkill(t *testing.T) {
	root := t.TempDir()
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceInstalled}}

	if got, err := skill.Files(roots, "absent"); err == nil {
		t.Fatalf("Files = %+v, want a refusal", got)
	}
}

// A symlink inside the directory is not a file the person can be shown, and
// naming one would put a path on the card whose bytes live somewhere else.
func TestFilesOmitsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "weather", "name: weather\ndescription: d", "body")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "weather", "link.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	roots := []skill.Root{{Dir: root, Provenance: skill.ProvenanceInstalled}}

	got, err := skill.Files(roots, "weather")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(got.Files) != 1 || got.Files[0] != "SKILL.md" {
		t.Fatalf("files = %v, want the symlink left out", got.Files)
	}
}
