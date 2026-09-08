package skill

import (
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

func managedUsageStand(t *testing.T) *Store {
	t.Helper()
	configDir := t.TempDir()
	return NewStore(OSFileSystem{}, []Root{{Dir: configDir + "/managed-skills", Provenance: ProvenanceManaged}},
		storage.NewDocumentStore(configDir),
		WithClock(func() time.Time { return time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC) }))
}

func managedUsageStandWithIdleDays(t *testing.T, days int) *Store {
	t.Helper()
	configDir := t.TempDir()
	return NewStore(OSFileSystem{}, []Root{{Dir: configDir + "/managed-skills", Provenance: ProvenanceManaged}},
		storage.NewDocumentStore(configDir),
		WithClock(func() time.Time { return time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC) }),
		WithIdleDays(func() int { return days }))
}

func TestKeepUnchangedRefusesTheMachineAndOnlyForThatSkill(t *testing.T) {
	store := managedUsageStand(t)
	writeSkillAt(t, store, "pinned", "Do not touch")
	writeSkillAt(t, store, "ordinary", "An ordinary skill")
	if err := store.SetPin("pinned", PinKeepUnchanged, true); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	err := store.Update("pinned", "Do not touch", "new body")
	if err == nil {
		t.Fatal("a pinned skill was updated")
	}
	if !strings.Contains(err.Error(), "pinned") {
		t.Errorf("refusal = %q, want it to name the skill", err)
	}
	if err := store.Update("ordinary", "An ordinary skill", "new body"); err != nil {
		t.Fatalf("an unpinned skill refused an update: %v", err)
	}

	if err := store.Delete("pinned"); err == nil {
		t.Fatal("a pinned skill was deleted")
	}
	if err := store.Delete("ordinary"); err != nil {
		t.Fatalf("an unpinned skill refused deletion: %v", err)
	}
}

func TestKeepEnabledCancelsAutoOffAndNotThePersonsSwitch(t *testing.T) {
	store := managedUsageStandWithIdleDays(t, 30)
	writeSkillAt(t, store, "deploy", "Deploy")
	if err := store.SetPin("deploy", PinKeepEnabled, true); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	_, _ = store.List()
	store.now = func() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) }
	row := listedByName(t, mustList(t, store), "deploy")
	if !row.Enabled || row.AutoOff != nil {
		t.Fatalf("keepEnabled row = %+v, want enabled without auto-off", row)
	}
	if err := store.SetEnabled("deploy", false); err != nil {
		t.Fatalf("person disable: %v", err)
	}
	row = listedByName(t, mustList(t, store), "deploy")
	if row.Enabled {
		t.Fatal("keepEnabled prevented the person's own switch")
	}
}

func TestUnknownPinKindIsRefused(t *testing.T) {
	store := managedUsageStand(t)
	writeSkillAt(t, store, "deploy", "Deploy")
	if err := store.SetPin("deploy", PinKind("unknown"), true); err == nil {
		t.Fatal("unknown pin kind was silently accepted")
	}
}
