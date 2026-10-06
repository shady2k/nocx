package backup_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/storage"
)

func sandboxBackupService(t *testing.T, enabled bool, roots sandbox.ProfileRoots) (*backup.Service, *sandbox.ProfileRepository, *fakeDocStore, *fakeSettingsStore) {
	t.Helper()
	_, connections, settings, journal, _ := newFakeService()
	profiles := sandbox.NewProfileRepository(storage.NewDocumentStore(t.TempDir()), sandbox.StandardDocumentName, nil)
	if _, err := profiles.UpdateStandard(0, enabled, roots); err != nil {
		t.Fatal(err)
	}
	return backup.NewService(connections, settings, journal, nil, nil, nil, profiles), profiles, journal, settings
}

func TestSandboxBackupPreviewRejectsConcurrentProfileChangeBeforeOtherWrites(t *testing.T) {
	source, _, _, _ := sandboxBackupService(t, true, sandbox.ProfileRoots{ReadOnlyDirs: []string{"/source-read"}})
	created, err := source.Create()
	if err != nil {
		t.Fatal(err)
	}
	destination, profiles, journal, settings := sandboxBackupService(t, false, sandbox.ProfileRoots{ReadWriteDirs: []string{"/destination-work"}})
	current, err := profiles.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	settings.overrides["clipboard.osc52Suppressed"] = true
	preview, err := destination.Preview(created.Contents, backup.RestoreReplace)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := profiles.UpdateStandard(current.Revision, true, sandbox.ProfileRoots{ReadOnlyDirs: []string{"/new-local-choice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = destination.Restore(created.Contents, backup.RestoreReplace, preview.PreviewToken); !errors.Is(err, backup.ErrInvalidDocument) {
		t.Fatalf("stale profile preview error = %v", err)
	}
	after, err := profiles.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, changed) || settings.overrides["clipboard.osc52Suppressed"] != true || journal.writes != 0 {
		t.Fatal("stale preview changed profiles/settings or began a restore journal")
	}
}

func TestSandboxBackupCommitFailureRestoresValuesWithoutRewindingClock(t *testing.T) {
	source, _, _, _ := sandboxBackupService(t, true, sandbox.ProfileRoots{ReadOnlyDirs: []string{"/source-read"}})
	created, err := source.Create()
	if err != nil {
		t.Fatal(err)
	}
	destination, profiles, journal, _ := sandboxBackupService(t, false, sandbox.ProfileRoots{ReadWriteDirs: []string{"/destination-work"}})
	before, err := profiles.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := destination.Preview(created.Contents, backup.RestoreReplace)
	if err != nil {
		t.Fatal(err)
	}
	journal.failOnWrite = 2 // prepared succeeded; final commit journal fails.
	if _, err = destination.Restore(created.Contents, backup.RestoreReplace, preview.PreviewToken); err == nil {
		t.Fatal("restore succeeded after its commit journal failed")
	}
	after, err := profiles.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled != before.Enabled || !reflect.DeepEqual(after.ProfileRoots, before.ProfileRoots) || after.Revision <= before.Revision {
		t.Fatal("rollback lost local profile values or rewound its CAS clock")
	}
	if _, err := profiles.UpdateStandard(before.Revision, true, before.ProfileRoots); !errors.Is(err, sandbox.ErrProfileConflict) {
		t.Fatalf("pre-restore CAS was revived by rollback: %v", err)
	}
	if state, _ := readTestJournal(journal); state.state != "idle" {
		t.Fatal("successful rollback left a prepared journal")
	}
}

func TestSandboxPreparedRecoveryLeavesJournalUntilWorkspaceStoreCanRecover(t *testing.T) {
	_, connections, settings, journal, _ := newFakeService()
	profiles := sandbox.NewProfileRepository(storage.NewDocumentStore(t.TempDir()), sandbox.StandardDocumentName, nil)
	before, err := profiles.ExportConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	beforeSettings := map[string]any{"clipboard.osc52Suppressed": true}
	if err = journal.Write("backup-restore-journal.json", map[string]any{
		"version": 1, "state": "prepared", "connections": connections.snap,
		"settings": beforeSettings, "sandbox": before,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = profiles.UpdateStandard(0, true, sandbox.ProfileRoots{ReadWriteDirs: []string{"/partial-import"}}); err != nil {
		t.Fatal(err)
	}
	if err = backup.RecoverPrerequisites(connections, settings, journal); err != nil {
		t.Fatal(err)
	}
	if settings.overrides["clipboard.osc52Suppressed"] != true {
		t.Fatal("ContentDB prerequisites saw unrestored settings")
	}
	var pending struct {
		State string `json:"state"`
	}
	if err = json.Unmarshal(journal.data["backup-restore-journal.json"], &pending); err != nil || pending.State != "prepared" {
		t.Fatal("prerequisite recovery erased unfinished sandbox rollback")
	}
	service := backup.NewService(connections, settings, journal, nil, nil, nil, profiles)
	if err = service.Recover(); err != nil {
		t.Fatal(err)
	}
	after, err := profiles.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled || len(after.ReadOnlyDirs) != 0 || len(after.ReadWriteDirs) != 0 || after.Revision <= before.Standard.Revision {
		t.Fatal("full recovery left partially imported mutable policy or reset revision")
	}
}
