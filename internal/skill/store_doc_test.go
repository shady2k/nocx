package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

func TestCorruptDisabledDocumentFailsClosed(t *testing.T) {
	configDir := t.TempDir()
	authored := filepath.Join(configDir, "skills")
	writeExistingSkill(t, authored, "deploy", "name: deploy\ndescription: d", "body")
	if err := os.WriteFile(filepath.Join(configDir, DocumentName), []byte(`{"schemaVersion":1,"disabled":[`), 0o600); err != nil {
		t.Fatalf("write corrupt document: %v", err)
	}

	source := NewStore(OSFileSystem{}, []Root{
		{Dir: authored, Provenance: ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: ProvenanceManaged},
	}, storage.NewDocumentStore(configDir))

	if got := source.Index(); len(got) != 0 {
		t.Fatalf("want no skills on a corrupt document, got %d", len(got))
	}
	if _, err := source.Read("deploy", ""); err == nil {
		t.Fatal("want skills.read to refuse a skill when the disabled document is corrupt")
	}
}

func TestSetEnabledPersistsAndHidesSkill(t *testing.T) {
	configDir := t.TempDir()
	authored := filepath.Join(configDir, "skills")
	writeExistingSkill(t, authored, "deploy", "name: deploy\ndescription: d", "body")
	roots := []Root{
		{Dir: authored, Provenance: ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: ProvenanceManaged},
	}
	docStore := storage.NewDocumentStore(configDir)
	source := NewStore(OSFileSystem{}, roots, docStore)

	if err := source.SetEnabled("deploy", false); err != nil {
		t.Fatalf("disable skill: %v", err)
	}
	list, err := source.List()
	if err != nil {
		t.Fatalf("list disabled skill: %v", err)
	}
	if len(list.Skills) != 1 || list.Skills[0].Enabled {
		t.Fatalf("list = %+v, want one disabled skill", list.Skills)
	}
	if got := source.Index(); len(got) != 0 {
		t.Fatalf("index = %+v, want no disabled skills", got)
	}
	if _, readErr := source.Read("deploy", ""); readErr == nil {
		t.Fatal("want skills.read to refuse a disabled skill")
	}

	fresh := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	if got := fresh.Index(); len(got) != 0 {
		t.Fatalf("fresh index = %+v, want no disabled skills", got)
	}
	freshList, err := fresh.List()
	if err != nil {
		t.Fatalf("fresh list: %v", err)
	}
	if len(freshList.Skills) != 1 || freshList.Skills[0].Enabled {
		t.Fatalf("fresh list = %+v, want one disabled skill", freshList.Skills)
	}
}

func TestApprovedDigestCoversEveryFileAndIsDeterministic(t *testing.T) {
	configDir := t.TempDir()
	managed := filepath.Join(configDir, "managed-skills")
	store := NewStore(OSFileSystem{}, []Root{{Dir: managed, Provenance: ProvenanceManaged}}, storage.NewDocumentStore(configDir))
	if err := store.Create("deploy", "deploy", "body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(managed, "deploy", "references"), 0o700); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	for _, file := range []struct {
		name string
		body string
	}{
		{name: "z.md", body: "last"},
		{name: "a.md", body: "first"},
	} {
		if err := os.WriteFile(filepath.Join(managed, "deploy", "references", file.name), []byte(file.body), 0o600); err != nil {
			t.Fatalf("write %s: %v", file.name, err)
		}
	}
	first, err := hashSkillDirectory(filepath.Join(managed, "deploy"))
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second, err := hashSkillDirectory(filepath.Join(managed, "deploy"))
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first != second {
		t.Fatalf("hash changed without content change: %q != %q", first, second)
	}
	if got := store.Index(); len(got) != 0 {
		t.Fatalf("index = %+v, want reference files to invalidate approval", got)
	}
}
