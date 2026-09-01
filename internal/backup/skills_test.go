package backup_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/skill/builtin"
	"github.com/shady2k/nocx/internal/storage"
)

type skillsConnStore struct{ snap profile.ConnectionSnapshot }

func (s *skillsConnStore) LoadConnectionSnapshot() (profile.ConnectionSnapshot, error) {
	return s.snap, nil
}

func (s *skillsConnStore) ReplaceConnectionSnapshot(v profile.ConnectionSnapshot) error {
	s.snap = v
	return nil
}

type skillsSettingsStore struct{ values map[string]any }

func (s *skillsSettingsStore) NonSecretOverrides() map[string]any {
	out := make(map[string]any, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}

func (s *skillsSettingsStore) ReplaceNonSecretOverrides(v map[string]any) (settings.PendingNotification, error) {
	s.values = make(map[string]any, len(v))
	for k, value := range v {
		s.values[k] = value
	}
	return settings.PendingNotification{}, nil
}
func (s *skillsSettingsStore) Publish(settings.PendingNotification) {}
func (s *skillsSettingsStore) ValidateSetting(string, any) error    { return nil }

func writeAuthoredSkill(t *testing.T, root, name, description, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "hosts.md"), []byte("prod is eu-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newSkillsBackupService(t *testing.T, configDir string, skills *skill.Store) *backup.Service {
	t.Helper()
	return backup.NewService(
		&skillsConnStore{snap: profile.ConnectionSnapshot{}},
		&skillsSettingsStore{values: map[string]any{"theme": "dark"}},
		storage.NewDocumentStore(configDir),
		nil,
		nil,
		skills,
	)
}

func TestSkillsBackupRestoreRoundTripPreservesTreesAndEnablement(t *testing.T) {
	sourceConfig := t.TempDir()
	sourceAuthored := filepath.Join(sourceConfig, "skills")
	sourceManaged := filepath.Join(sourceConfig, "managed-skills")
	writeAuthoredSkill(t, sourceAuthored, "authored", "written here", "Use the local deploy command.")
	sourceSkills := skill.NewStore(nil, []skill.Root{
		{Dir: sourceAuthored, Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: sourceManaged, Provenance: skill.ProvenanceManaged},
	}, storage.NewDocumentStore(sourceConfig))
	if err := sourceSkills.Create("managed", "approved procedure", "Use the managed deploy command."); err != nil {
		t.Fatalf("create managed skill: %v", err)
	}
	if err := sourceSkills.SetEnabled("managed", false); err != nil {
		t.Fatalf("disable managed skill: %v", err)
	}

	sourceService := newSkillsBackupService(t, sourceConfig, sourceSkills)
	created, err := sourceService.Create()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	var document backup.Document
	if decodeErr := json.Unmarshal([]byte(created.Contents), &document); decodeErr != nil {
		t.Fatalf("decode backup: %v", decodeErr)
	}
	if len(document.Skills.Authored) != 1 || len(document.Skills.Managed) != 1 {
		t.Fatalf("backup skills = %+v, want one authored and one managed tree", document.Skills)
	}
	if strings.Contains(created.Contents, `"builtin"`) || strings.Contains(created.Contents, "skill-authoring") {
		t.Fatalf("backup carried builtin skill data: %s", created.Contents)
	}

	destinationConfig := t.TempDir()
	destinationAuthored := filepath.Join(destinationConfig, "skills")
	destinationManaged := filepath.Join(destinationConfig, "managed-skills")
	destinationSkills := skill.NewStore(nil, []skill.Root{
		{Dir: destinationAuthored, Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: destinationManaged, Provenance: skill.ProvenanceManaged},
	}, storage.NewDocumentStore(destinationConfig))
	destinationService := newSkillsBackupService(t, destinationConfig, destinationSkills)
	preview, err := destinationService.Preview(created.Contents, backup.RestoreMerge)
	if err != nil {
		t.Fatalf("preview restore: %v", err)
	}
	if _, restoreErr := destinationService.Restore(created.Contents, backup.RestoreMerge, preview.PreviewToken); restoreErr != nil {
		t.Fatalf("restore backup: %v", restoreErr)
	}

	listed, err := destinationSkills.List()
	if err != nil {
		t.Fatalf("list restored skills: %v", err)
	}
	byName := make(map[string]skill.ListedSkill, len(listed.Skills))
	for _, item := range listed.Skills {
		byName[item.Name] = item
	}
	if got := byName["authored"]; !got.Enabled || got.Provenance != skill.ProvenanceAuthored {
		t.Fatalf("restored authored skill = %+v", got)
	}
	if got := byName["managed"]; got.Enabled || got.Provenance != skill.ProvenanceManaged {
		t.Fatalf("restored managed skill = %+v, want disabled managed skill", got)
	}
	if _, err := os.Stat(filepath.Join(destinationAuthored, "authored", "references", "hosts.md")); err != nil {
		t.Fatalf("restored authored reference: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationManaged, "managed", "SKILL.md")); err != nil {
		t.Fatalf("restored managed skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationAuthored, "skill-authoring")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("builtin should not be restored into authored tree, stat error = %v", err)
	}
}

type skillsJournalDoc struct {
	data   map[string][]byte
	writes [][]byte
}

func (d *skillsJournalDoc) Read(name string, into any) (bool, error) {
	raw, ok := d.data[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, into)
}

func (d *skillsJournalDoc) Write(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if d.data == nil {
		d.data = make(map[string][]byte)
	}
	d.data[name] = raw
	if name == "backup-restore-journal.json" {
		d.writes = append(d.writes, append([]byte(nil), raw...))
	}
	return nil
}

func (d *skillsJournalDoc) Delete(name string) error {
	delete(d.data, name)
	return nil
}

type journalSkillStore struct {
	current  skill.Snapshot
	failNext bool
	calls    int
}

func (s *journalSkillStore) Snapshot() (skill.Snapshot, error) {
	return s.current, nil
}

func (s *journalSkillStore) RestoreSnapshot(next skill.Snapshot) error {
	s.calls++
	s.current = next
	if s.failNext {
		s.failNext = false
		return errors.New("injected skill restore failure")
	}
	return nil
}

func TestSkillsRestoreFailureRollsBackTheJournalledSnapshot(t *testing.T) {
	before := skill.Snapshot{
		Managed: []skill.SnapshotTree{{
			Name:  "before",
			Files: []skill.SnapshotFile{{Path: "SKILL.md", Bytes: "---\nname: before\ndescription: before\n---\nold\n"}},
		}},
	}
	target := skill.Snapshot{
		Managed: []skill.SnapshotTree{{
			Name:  "after",
			Files: []skill.SnapshotFile{{Path: "SKILL.md", Bytes: "---\nname: after\ndescription: after\n---\nnew\n"}},
		}},
	}
	sourceDoc := &skillsJournalDoc{}
	sourceSkills := &journalSkillStore{current: target}
	source := backup.NewService(
		&skillsConnStore{snap: profile.ConnectionSnapshot{}},
		&skillsSettingsStore{values: map[string]any{}},
		sourceDoc,
		nil,
		nil,
		sourceSkills,
	)
	created, err := source.Create()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	destinationDoc := &skillsJournalDoc{}
	destinationSkills := &journalSkillStore{current: before, failNext: true}
	destination := backup.NewService(
		&skillsConnStore{snap: profile.ConnectionSnapshot{}},
		&skillsSettingsStore{values: map[string]any{}},
		destinationDoc,
		nil,
		nil,
		destinationSkills,
	)
	preview, err := destination.Preview(created.Contents, backup.RestoreMerge)
	if err != nil {
		t.Fatalf("preview restore: %v", err)
	}
	if _, restoreErr := destination.Restore(created.Contents, backup.RestoreMerge, preview.PreviewToken); restoreErr == nil {
		t.Fatal("restore succeeded despite an injected skill write failure")
	}
	if destinationSkills.calls != 2 {
		t.Fatalf("skill restore calls = %d, want failed write plus journal rollback", destinationSkills.calls)
	}
	restored, err := json.Marshal(destinationSkills.current)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(expected) {
		t.Fatalf("skill snapshot after failed restore = %s, want journal snapshot %s", restored, expected)
	}
	var journal struct {
		State  string          `json:"state"`
		Skills *skill.Snapshot `json:"skills"`
	}
	if _, err := destinationDoc.Read("backup-restore-journal.json", &journal); err != nil {
		t.Fatalf("read recovery journal: %v", err)
	}
	if journal.State != "idle" {
		t.Fatalf("journal state = %q, want idle after rollback", journal.State)
	}
	if len(destinationDoc.writes) < 2 {
		t.Fatalf("journal writes = %d, want prepared and idle", len(destinationDoc.writes))
	}
	var prepared struct {
		State  string          `json:"state"`
		Skills *skill.Snapshot `json:"skills"`
	}
	if err := json.Unmarshal(destinationDoc.writes[0], &prepared); err != nil {
		t.Fatalf("decode prepared journal: %v", err)
	}
	if prepared.State != "prepared" || prepared.Skills == nil {
		t.Fatalf("prepared journal = %+v, want a skill snapshot", prepared)
	}
}

func TestSkillsMutationAfterPreviewInvalidatesRestoreToken(t *testing.T) {
	target := skill.Snapshot{
		Managed: []skill.SnapshotTree{{
			Name:  "after",
			Files: []skill.SnapshotFile{{Path: "SKILL.md", Bytes: "target"}},
		}},
	}
	source := backup.NewService(
		&skillsConnStore{snap: profile.ConnectionSnapshot{}},
		&skillsSettingsStore{values: map[string]any{}},
		&skillsJournalDoc{},
		nil,
		nil,
		&journalSkillStore{current: target},
	)
	created, err := source.Create()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	destinationSkills := &journalSkillStore{current: skill.Snapshot{}}
	destination := backup.NewService(
		&skillsConnStore{snap: profile.ConnectionSnapshot{}},
		&skillsSettingsStore{values: map[string]any{}},
		&skillsJournalDoc{},
		nil,
		nil,
		destinationSkills,
	)
	preview, err := destination.Preview(created.Contents, backup.RestoreMerge)
	if err != nil {
		t.Fatalf("preview restore: %v", err)
	}
	destinationSkills.current = skill.Snapshot{
		Managed: []skill.SnapshotTree{{
			Name:  "changed",
			Files: []skill.SnapshotFile{{Path: "SKILL.md", Bytes: "changed"}},
		}},
	}
	if _, err := destination.Restore(created.Contents, backup.RestoreMerge, preview.PreviewToken); err == nil {
		t.Fatal("restore succeeded after skills changed since preview")
	}
	if destinationSkills.calls != 0 {
		t.Fatalf("skill restore calls = %d, want none for stale preview", destinationSkills.calls)
	}
}
