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

	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		return nil, fmt.Errorf("write file %s: %w", path, err)
	}

	return &SaveResult{Path: path}, nil
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
