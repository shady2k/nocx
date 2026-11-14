package pi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeRecordCrashBeforeRenamePreservesPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.json")
	previous := []byte(`{"sessionFile":"good","bytes":42,"sha256":"checkpoint"}`)
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	temp, err := prepareResumeRecord(path, []byte(`{"sessionFile":"replacement"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(temp) }()
	// The backend can die here: the prepared file is closed and synced, but
	// has not been renamed. Its successor must still read the previous record.
	got, err := os.ReadFile(path) //nolint:gosec // test-owned private resume record
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(previous) {
		t.Fatalf("uncommitted replacement damaged previous record: %s", got)
	}
	info, err := os.Stat(temp)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("temporary record permissions: %v", info.Mode())
	}
	if err = os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path) //nolint:gosec // test-owned private resume record
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"sessionFile":"replacement"}` {
		t.Fatalf("replacement not published: %s", got)
	}
}
