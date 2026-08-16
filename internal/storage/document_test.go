package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- helpers ---

type testDoc struct {
	Key   string `json:"key"`
	Value int    `json:"value"`
}

func newTestModule() Module {
	return Module{
		Name:    "test",
		Current: 2,
		Migrations: []Migration{
			{From: 0, To: 1, Up: func(data []byte) ([]byte, error) {
				// v0→v1: initial versioning — add version field
				return data, nil
			}},
			{From: 1, To: 2, Up: func(data []byte) ([]byte, error) {
				// v1→v2: add "extra": "migrated"
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					return nil, err
				}
				m["extra"] = "migrated"
				return json.Marshal(m)
			}},
		},
	}
}

func newDocumentStore(t *testing.T, dir string) *documentStore {
	t.Helper()
	return &documentStore{
		dir:     dir,
		syncDir: syncDirectory,
	}
}

// --- DocumentStore tests ---

func TestDocumentStore_WriteRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	doc := testDoc{Key: "hello", Value: 42}
	if err := store.Write("mydoc.json", doc); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var got testDoc
	found, err := store.Read("mydoc.json", &got)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("Read returned found=false")
	}
	if got.Key != "hello" || got.Value != 42 {
		t.Errorf("Read returned %+v", got)
	}

	// File should exist at <dir>/mydoc.json
	if _, err := os.Stat(filepath.Join(dir, "mydoc.json")); err != nil {
		t.Errorf("file not created on disk: %v", err)
	}
}

func TestDocumentStore_ReadNotFound(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	var got testDoc
	found, err := store.Read("nonexistent.json", &got)
	if err != nil {
		t.Fatalf("Read on missing file should not error: %v", err)
	}
	if found {
		t.Error("Read on missing file returned found=true")
	}
}

func TestDocumentStore_WritePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	if err := store.Write("permtest.json", testDoc{Key: "x"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dir, "permtest.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", fi.Mode().Perm())
	}

	// Directory should be 0700 (MkdirAll creates it)
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", di.Mode().Perm())
	}
}

func TestDocumentStore_AtomicWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	if err := store.Write("atom.json", testDoc{Key: "x"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "atom.json" {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}

func TestDocumentStore_SymlinkRefusal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	// We need the directory to exist before symlink creation
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	targetPath := filepath.Join(dir, "linked.json")
	linkTarget := filepath.Join(dir, "real_target")

	// Create a real file that the symlink will point to
	if err := os.WriteFile(linkTarget, []byte("original"), 0o600); err != nil {
		t.Fatalf("WriteFile link target: %v", err)
	}
	// Create symlink at the document path
	if err := os.Symlink(linkTarget, targetPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	store := newDocumentStore(t, dir)
	err := store.Write("linked.json", testDoc{Key: "should-fail"})
	if err == nil {
		t.Fatal("Write to symlink path should error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should mention symlink, got: %v", err)
	}

	// Symlink should still exist and point to the original target
	fi, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat after failed write: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced — should still be a symlink")
	}
	content, err := os.ReadFile(linkTarget) //nolint:gosec // reading file test itself created
	if err != nil || string(content) != "original" {
		t.Errorf("link target was modified: content=%q err=%v", content, err)
	}
}

func TestDocumentStore_DirFsync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	var syncedDirs []string
	store.syncDir = func(path string) error {
		syncedDirs = append(syncedDirs, path)
		return syncDirectory(path)
	}

	if err := store.Write("synctest.json", testDoc{Key: "x"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := store.Write("synctest2.json", testDoc{Key: "y"}); err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	if len(syncedDirs) == 0 {
		t.Fatal("syncDir was never called — directory not fsynced after rename")
	}
	for _, sd := range syncedDirs {
		if sd != dir {
			t.Errorf("syncDir called with %q, want %q", sd, dir)
		}
	}
}

func TestDocumentStore_WriteOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	if err := store.Write("overwrite.json", testDoc{Key: "first", Value: 1}); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := store.Write("overwrite.json", testDoc{Key: "second", Value: 2}); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	var got testDoc
	found, err := store.Read("overwrite.json", &got)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("Read returned found=false")
	}
	if got.Key != "second" || got.Value != 2 {
		t.Errorf("Read returned %+v, want {Key:second Value:2}", got)
	}
}

// --- Schema version protocol tests ---

func TestSchemaVersion_Migrate(t *testing.T) {
	mod := newTestModule()

	v1Data := json.RawMessage(`{"key":"old","value":1}`)
	migrated, err := mod.Migrate(v1Data, 1)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(migrated, &result); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}
	if result["extra"] != "migrated" {
		t.Errorf("migration did not apply: extra=%v", result["extra"])
	}
	if result["key"] != "old" {
		t.Errorf("original data lost: key=%v", result["key"])
	}
}

func TestSchemaVersion_MigrateAlreadyCurrent(t *testing.T) {
	mod := newTestModule()
	v2Data := json.RawMessage(`{"key":"current","value":2}`)

	migrated, err := mod.Migrate(v2Data, 2)
	if err != nil {
		t.Fatalf("Migrate at current version: %v", err)
	}
	if string(migrated) != string(v2Data) {
		t.Errorf("data changed when already at current version: %s → %s", v2Data, migrated)
	}
}

func TestSchemaVersion_MigrateNewerVersion(t *testing.T) {
	mod := newTestModule()
	v3Data := json.RawMessage(`{"key":"future"}`)

	_, err := mod.Migrate(v3Data, 3)
	if err == nil {
		t.Fatal("Migrate with version newer than module should error")
	}
	if !errors.Is(err, ErrVersionTooNew) {
		t.Errorf("error should be ErrVersionTooNew, got: %v", err)
	}
}

func TestSchemaVersion_MigrateMissingMigration(t *testing.T) {
	mod := Module{
		Name:    "gapped",
		Current: 3,
		Migrations: []Migration{
			{From: 0, To: 1, Up: func(data []byte) ([]byte, error) { return data, nil }},
			{From: 1, To: 2, Up: func(data []byte) ([]byte, error) { return data, nil }},
			// Missing 2→3
		},
	}

	_, err := mod.Migrate(json.RawMessage(`{}`), 1)
	if err == nil {
		t.Fatal("Migrate with missing migration step should error")
	}
}

func TestSchemaVersion_MigrateZeroVersion(t *testing.T) {
	mod := newTestModule()
	v0Data := json.RawMessage(`{"key":"v0"}`)

	migrated, err := mod.Migrate(v0Data, 0)
	if err != nil {
		t.Fatalf("Migrate from version 0: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(migrated, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["extra"] != "migrated" {
		t.Error("migration from v0 did not apply")
	}
}

func TestSchemaVersion_MigrateMultiStep(t *testing.T) {
	mod := Module{
		Name:    "multi",
		Current: 3,
		Migrations: []Migration{
			{From: 0, To: 1, Up: func(data []byte) ([]byte, error) { return data, nil }},
			{From: 1, To: 2, Up: func(data []byte) ([]byte, error) {
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					return nil, err
				}
				m["step1"] = true
				return json.Marshal(m)
			}},
			{From: 2, To: 3, Up: func(data []byte) ([]byte, error) {
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					return nil, err
				}
				m["step2"] = true
				return json.Marshal(m)
			}},
		},
	}

	v1Data := json.RawMessage(`{"key":"multi"}`)
	migrated, err := mod.Migrate(v1Data, 1)
	if err != nil {
		t.Fatalf("Migrate multi-step: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(migrated, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["step1"] != true || result["step2"] != true {
		t.Errorf("multi-step migration incomplete: %+v", result)
	}
}

func TestSchemaVersion_MigrateNonMonotonic(t *testing.T) {
	mod := Module{
		Name:    "loop",
		Current: 2,
		Migrations: []Migration{
			{From: 0, To: 0, Up: func(data []byte) ([]byte, error) { return data, nil }},
		},
	}

	_, err := mod.Migrate(json.RawMessage(`{}`), 0)
	if err == nil {
		t.Fatal("Migrate with To == From (non-monotonic) should error")
	}
}

// Delete is what the vault reset needs to return the vault to uninitialized:
// the absence of the document IS the uninitialized state, so removing it is
// the operation, not a cleanup after one.
func TestDocumentStore_Delete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	if err := store.Write("mydoc.json", testDoc{Key: "hello", Value: 42}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := store.Delete("mydoc.json"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var got testDoc
	found, err := store.Read("mydoc.json", &got)
	if err != nil {
		t.Fatalf("Read after Delete: %v", err)
	}
	if found {
		t.Error("document still readable after Delete")
	}
}

// Deleting what is not there is success, not an error. The reset runs this
// against both vault documents and only one of them need exist — a vault set
// up with no secrets yet has no file blob at all — and re-running a reset that
// was interrupted must not fail on the half it already finished.
func TestDocumentStore_DeleteAbsentIsNotAnError(t *testing.T) {
	store := newDocumentStore(t, filepath.Join(t.TempDir(), "docs"))
	if err := store.Delete("never-existed.json"); err != nil {
		t.Errorf("Delete of an absent document: %v, want nil", err)
	}
}

// The store's documented 8 MiB ceiling is enforced on both read and write,
// and oversized input is rejected before JSON decode or the atomic write.
func TestDocumentStore_ReadTooLarge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.json"), make([]byte, maxDocumentBytes+1), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var got any
	_, err := store.Read("big.json", &got)
	if err == nil {
		t.Fatal("Read on oversized document succeeded, want error")
	}
	if !errors.Is(err, ErrDocumentTooLarge) {
		t.Errorf("error = %v, want ErrDocumentTooLarge", err)
	}
}

func TestDocumentStore_WriteTooLarge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)

	doc := map[string]string{"blob": strings.Repeat("x", maxDocumentBytes)}
	err := store.Write("big.json", doc)
	if err == nil {
		t.Fatal("Write of oversized document succeeded, want error")
	}
	if !errors.Is(err, ErrDocumentTooLarge) {
		t.Errorf("error = %v, want ErrDocumentTooLarge", err)
	}
	// Nothing must have been written (the bound fires before the temp file).
	if _, statErr := os.Stat(filepath.Join(dir, "big.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("oversized document was written to disk (stat err = %v)", statErr)
	}
}

// An empty document still reads as not-found, beside the new size bound.
func TestDocumentStore_ReadEmptyStillNotFound(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs")
	store := newDocumentStore(t, dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.json"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var got testDoc
	found, err := store.Read("empty.json", &got)
	if err != nil {
		t.Fatalf("Read on empty file: %v", err)
	}
	if found {
		t.Error("empty document returned found=true")
	}
}
