package skill

import (
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
