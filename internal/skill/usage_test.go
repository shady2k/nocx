package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

func usageStand(t *testing.T) *Store {
	t.Helper()
	configDir := t.TempDir()
	return NewStore(OSFileSystem{},
		[]Root{{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored}},
		storage.NewDocumentStore(configDir),
		WithClock(func() time.Time { return time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC) }))
}

func TestRecordUseCountsAndDates(t *testing.T) {
	store := usageStand(t)
	store.RecordUse("deploy")
	store.RecordUse("deploy")
	if err := store.FlushUsage(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d, want 2", got.Count)
	}
	if got.LastUsedAt != "2026-03-03T10:00:00Z" {
		t.Fatalf("lastUsedAt = %q", got.LastUsedAt)
	}
}

func TestRecordUseDoesNotWritePerCall(t *testing.T) {
	store := usageStand(t)
	store.RecordUse("deploy")
	got, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Count != 0 {
		t.Fatalf("count = %d before a flush, want 0", got.Count)
	}
}

func writeSkillAt(t *testing.T, store *Store, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(store.roots[0].Dir, name), 0o750); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(store.roots[0].Dir, name, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFirstSeenIsStampedOnceAndNeverMoves(t *testing.T) {
	store := usageStand(t)
	writeSkillAt(t, store, "deploy", "Deploy the service")

	if _, err := store.List(); err != nil {
		t.Fatalf("first list: %v", err)
	}
	first, err := store.usageFor("deploy")
	if err != nil || first.FirstSeenAt == "" {
		t.Fatalf("firstSeenAt = %q, %v; want it stamped by the first discovery", first.FirstSeenAt, err)
	}

	store.now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	if _, listErr := store.List(); listErr != nil {
		t.Fatalf("second list: %v", listErr)
	}
	second, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if second.FirstSeenAt != first.FirstSeenAt {
		t.Fatalf("firstSeenAt moved from %q to %q: it is when nocx FIRST saw the skill",
			first.FirstSeenAt, second.FirstSeenAt)
	}
}

func usageStandWithIdleDays(t *testing.T, days int) *Store {
	t.Helper()
	configDir := t.TempDir()
	return NewStore(OSFileSystem{},
		[]Root{{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored}},
		storage.NewDocumentStore(configDir),
		WithClock(func() time.Time { return time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC) }),
		WithIdleDays(func() int { return days }))
}

func listedByName(t *testing.T, listed ListResult, name string) ListedSkill {
	t.Helper()
	for _, row := range listed.Skills {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("skill %q not listed: %+v", name, listed.Skills)
	return ListedSkill{}
}

func disabledNames(t *testing.T, store *Store) []string {
	t.Helper()
	raw, err := os.ReadFile(store.DocumentPath()) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Disabled
}

func TestAQuietSkillIsSwitchedOffAndTheRecordSaysNocxDidIt(t *testing.T) {
	store := usageStandWithIdleDays(t, 30)
	writeSkillAt(t, store, "deploy", "Deploy the service")

	if _, err := store.List(); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	store.now = func() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) }

	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	row := listedByName(t, listed, "deploy")
	if row.Enabled {
		t.Fatal("a skill unused past the threshold is still enabled")
	}
	if row.AutoOff == nil {
		t.Fatal("no record of who switched it off")
	}
	if row.AutoOff.SilentSince == "" || row.AutoOff.Days != 30 {
		t.Fatalf("autoOff = %+v, want the date measured from and threshold applied", row.AutoOff)
	}
	if got := disabledNames(t, store); len(got) != 0 {
		t.Fatalf("disabled = %v, want empty: nocx switched this off", got)
	}
}

func TestAutoOffLeavesSkillInsideThresholdEnabled(t *testing.T) {
	store := usageStandWithIdleDays(t, 30)
	writeSkillAt(t, store, "deploy", "Deploy")
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC) }
	row := listedByName(t, mustList(t, store), "deploy")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("row = %+v, want untouched inside threshold", row)
	}
}

func TestAutoOffMeasuresFromLastUseWhenPresent(t *testing.T) {
	store := usageStandWithIdleDays(t, 30)
	writeSkillAt(t, store, "deploy", "Deploy")
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC) }
	store.RecordUse("deploy")
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC) }
	row := listedByName(t, mustList(t, store), "deploy")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("row = %+v, want age measured from the recent read", row)
	}
}

func TestAutoOffNeverSwitchesBuiltin(t *testing.T) {
	configDir := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{
		{FS: builtinFSForTest(), Provenance: ProvenanceBuiltin},
		{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored},
	}, storage.NewDocumentStore(configDir),
		WithClock(func() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) }),
		WithIdleDays(func() int { return 1 }))
	row := listedByName(t, mustList(t, store), "skill-authoring")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("builtin row = %+v, want enabled without auto-off", row)
	}
}

func TestAutoOffZeroThresholdDoesNothing(t *testing.T) {
	store := usageStandWithIdleDays(t, 0)
	writeSkillAt(t, store, "deploy", "Deploy")
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2027, 1, 1, 10, 0, 0, 0, time.UTC) }
	row := listedByName(t, mustList(t, store), "deploy")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("row = %+v, want no automatic switch at zero", row)
	}
}

func TestAutoOffWithoutOptionDoesNothing(t *testing.T) {
	store := usageStand(t)
	writeSkillAt(t, store, "deploy", "Deploy")
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2027, 1, 1, 10, 0, 0, 0, time.UTC) }
	row := listedByName(t, mustList(t, store), "deploy")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("row = %+v, want no automatic switch without option", row)
	}
}

func TestReenablingClearsAutoOff(t *testing.T) {
	store := usageStandWithIdleDays(t, 30)
	writeSkillAt(t, store, "deploy", "Deploy")
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) }
	if row := listedByName(t, mustList(t, store), "deploy"); row.AutoOff == nil {
		t.Fatal("seed list did not create auto-off mark")
	}
	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	row := listedByName(t, mustList(t, store), "deploy")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("row after re-enable = %+v, want enabled without automatic mark", row)
	}
}
