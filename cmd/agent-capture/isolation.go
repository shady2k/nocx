package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// managedClaudeDirs are where Claude Code reads managed settings and managed
// instructions, outside any directory a run owns. A variable so a test can
// point the check at directories it made.
var managedClaudeDirs = []string{"/etc/claude-code", "/Library/Application Support/ClaudeCode"}

var (
	managedClaudeEntries = []string{"managed-settings.json", "managed-settings.d", "CLAUDE.md", filepath.Join(".claude", "rules")}
	localClaudeEntries   = []string{"CLAUDE.md", "CLAUDE.local.md", ".claude"}
)

// readEnvFile parses KEY=VALUE lines; blank lines and # comments are skipped.
func readEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator explicitly supplies the env file
	if err != nil {
		return nil, fmt.Errorf("cannot read env file: %w", err)
	}
	var env []string
	for n, line := range strings.Split(string(data), "\n") {
		n++
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || !validEnvKey(key) {
			return nil, fmt.Errorf("env file line %d: want KEY=VALUE with a key of letters, digits and underscores", n)
		}
		env = append(env, line)
	}
	return env, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// refuseOutsideClaudeConfig refuses a run in which Claude Code would read
// settings or instructions the run does not own: managed ones, or any
// CLAUDE.md, CLAUDE.local.md or .claude in the run directory or above it. A
// source that cannot be inspected refuses too — not knowing is not absence.
func refuseOutsideClaudeConfig(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("refusing to start: cannot resolve %q: %w", dir, err)
	}
	for _, managed := range managedClaudeDirs {
		for _, entry := range managedClaudeEntries {
			if err := refuseIfPresent(filepath.Join(managed, entry)); err != nil {
				return err
			}
		}
	}
	for d := abs; ; {
		for _, entry := range localClaudeEntries {
			if err := refuseIfPresent(filepath.Join(d, entry)); err != nil {
				return err
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil
		}
		d = parent
	}
}

func refuseIfPresent(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("refusing to start: Claude Code would read %s, which is outside this run", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("refusing to start: cannot inspect %s: %w", path, err)
	}
}

// resolveProgram finds name on the PATH the program will be given, not on the
// caller's: under -env-file the caller's PATH is not the program's.
func resolveProgram(name string, env []string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		return filepath.Abs(name)
	}
	var path string
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%q is not on the env file's PATH", name)
}

// writeRunMeta records what ran — the program, where it resolved, its digest —
// and the NAMES of the variables it was given, never their values. A launcher
// that adds variables of its own (the Nix claude wrapper does) is inspectable
// from the resolved path afterwards.
func writeRunMeta(path, program, resolved string, env []string) error {
	f, err := os.Open(resolved) //nolint:gosec // the program this run resolved
	if err != nil {
		return fmt.Errorf("write run metadata: open %s: %w", resolved, err)
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	_ = f.Close()
	if copyErr != nil {
		return fmt.Errorf("write run metadata: digest %s: %w", resolved, copyErr)
	}
	names := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		names = append(names, key)
	}
	sort.Strings(names)
	raw, err := json.MarshalIndent(map[string]any{
		"program": program, "resolved": resolved, "sha256": hex.EncodeToString(h.Sum(nil)), "envNames": names,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	return nil
}
