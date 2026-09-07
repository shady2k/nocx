package skill

import (
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
