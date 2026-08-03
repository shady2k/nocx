package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SaveResult is the JSON-RPC result for backup.saveToFile.
type SaveResult struct {
	Path string `json:"path"`
}

// SaveToFile opens a native file-save dialog and writes the backup.
// Returns the chosen path, or ("", nil) when the user cancelled.
func SaveToFile(fileName, contents string) (*SaveResult, error) {
	path, err := nativeSaveDialog(fileName)
	if err != nil {
		return nil, err
	}
	if path == "" {
		// User cancelled.
		return nil, nil
	}

	if err := writeBackupFile(path, []byte(contents)); err != nil {
		return nil, fmt.Errorf("write file %s: %w", path, err)
	}

	return &SaveResult{Path: path}, nil
}

// writeBackupFile replaces a selected backup path without exposing partially
// written data. The temporary file lives beside the target so Rename is atomic.
func writeBackupFile(path string, contents []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".nocx-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary backup: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set backup permissions: %w", err)
	}
	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary backup: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary backup: %w", err)
	}

	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symlink at %s", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target %s: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace backup: %w", err)
	}
	if err := syncBackupDirectory(dir); err != nil {
		return fmt.Errorf("sync backup directory: %w", err)
	}
	return nil
}

func syncBackupDirectory(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // directory is selected by the user
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// nativeSaveDialog opens the OS file-save dialog and returns the chosen path.
// Returns "" when the user cancels.
func nativeSaveDialog(fileName string) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return zenitySave(fileName)
	case "darwin":
		return osascriptSave(fileName)
	default:
		return "", fmt.Errorf("native save dialog not supported on %s", runtime.GOOS)
	}
}

func zenitySave(fileName string) (string, error) {
	//nolint:gosec
	cmd := exec.Command("zenity",
		"--file-selection",
		"--save",
		"--confirm-overwrite",
		"--filename="+fileName,
		"--title=Save backup",
	)
	out, err := cmd.Output()
	if err != nil {
		// zenity returns exit code 1 on cancel; not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("zenity: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func osascriptSave(fileName string) (string, error) {
	// Escape double-quote to prevent AppleScript injection.
	safe := strings.ReplaceAll(fileName, `"`, `\"`)
	script := fmt.Sprintf(
		`POSIX path of (choose file name with prompt "Save backup" default name "%s")`,
		safe,
	)
	//nolint:gosec
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil // user cancelled
		}
		return "", fmt.Errorf("osascript: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
