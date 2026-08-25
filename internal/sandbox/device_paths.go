package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// writableDevicePaths returns the finite device allowlist needed for an
// interactive shell. /dev itself stays read-only; no other device can be
// opened for writing through this policy. Missing optional nodes are skipped
// because device layouts differ between supported platforms.
func writableDevicePaths() (files, dirs []string, err error) {
	for _, path := range []string{
		"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty",
	} {
		canonical, ok, resolveErr := canonicalOptionalPath(path)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
		if ok {
			files = append(files, canonical)
		}
	}
	for _, path := range []string{"/dev/pts"} {
		canonical, ok, resolveErr := canonicalOptionalDir(path)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
		if ok {
			dirs = append(dirs, canonical)
		}
	}
	return files, dirs, nil
}

func canonicalOptionalPath(path string) (string, bool, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if _, err := os.Stat(canonical); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return canonical, true, nil
}
