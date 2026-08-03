package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteBackupFileReplacesExistingFileWithPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.json")
	//nolint:gosec // test intentionally starts with broader permissions.
	if err := os.WriteFile(path, []byte("old backup"), 0o644); err != nil {
		t.Fatalf("seed backup: %v", err)
	}

	if err := writeBackupFile(path, []byte("new backup")); err != nil {
		t.Fatalf("writeBackupFile: %v", err)
	}

	//nolint:gosec // path is created in this test's private temporary directory.
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if got, want := string(contents), "new backup"; got != want {
		t.Errorf("backup contents = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("backup mode = %04o, want %04o", got, want)
	}
}

func TestWriteBackupFileRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra privileges on Windows")
	}

	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	target := filepath.Join(dir, "backup.json")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatalf("create target symlink: %v", err)
	}

	if err := writeBackupFile(target, []byte("replace")); err == nil {
		t.Fatal("writeBackupFile unexpectedly wrote through symlink")
	}

	//nolint:gosec // path is created in this test's private temporary directory.
	contents, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if got, want := string(contents), "keep"; got != want {
		t.Errorf("outside contents = %q, want %q", got, want)
	}
}
