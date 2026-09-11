package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// lookupEnv finds KEY's value in a KEY=VALUE slice such as readEnvFile
// returns, reporting whether it was present at all (as distinct from present
// and empty).
func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value, true
		}
	}
	return "", false
}

// refuseOutsideClaudeConfig refuses a run in which Claude Code would read
// settings or instructions the run does not own: managed ones, or any
// CLAUDE.md, CLAUDE.local.md or .claude in the run directory or above it. A
// source that cannot be inspected refuses too — not knowing is not absence.
//
// Both the unresolved (logical) and the symlink-resolved (physical) ancestor
// chains of dir are inspected, and either one matching is a refusal. The
// physical chain is the one that matters operationally: exec.Cmd sets no PWD
// when Env is replaced, so a child's getcwd(2) — and therefore Claude's own
// upward walk for CLAUDE.md — sees the resolved path, never a symlink name.
// The logical chain is inspected too, deliberately more strict than that one
// fact requires: a symlink is exactly the place an operator can point two
// different names at the same bytes, and a future Claude version, or a tool
// spawned from a shell that does set PWD, could read either name. Refusing
// on either is the side to fail on.
func refuseOutsideClaudeConfig(dir string) error {
	for _, managed := range managedClaudeDirs {
		for _, entry := range managedClaudeEntries {
			if err := refuseIfPresent(filepath.Join(managed, entry)); err != nil {
				return err
			}
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("refusing to start: cannot resolve %q: %w", dir, err)
	}
	if walkErr := walkLocalAncestors(abs); walkErr != nil {
		return walkErr
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("refusing to start: cannot resolve %q: %w", abs, err)
	}
	if physical != abs {
		if walkErr := walkLocalAncestors(physical); walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// walkLocalAncestors checks start and every ancestor of it for
// localClaudeEntries.
func walkLocalAncestors(start string) error {
	for d := start; ; {
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

// refuseEnvOutsideClaudeConfig refuses when the run's HOME or
// CLAUDE_CONFIG_DIR could route Claude to configuration the run did not
// create. Both are required: Claude falls back to the passwd home (and from
// there to the real ~/.claude) the moment either is unset, and the tool has
// no way to see that fallback happen. Given both, the check stays
// proportionate to what the flag promises — "Claude must not read
// configuration the run did not create" — rather than enumerating every file
// Claude might ever read:
//
//   - HOME is checked the same way a run directory is: does it already carry
//     its own CLAUDE.md, CLAUDE.local.md or .claude (a leftover, or a HOME
//     reused from a previous, non-isolated run)?
//   - CLAUDE_CONFIG_DIR is checked against the operator's real home
//     directory: it must not be, or sit inside, the real ~/.claude, which is
//     the specific "outside the run" source the flag's promise names.
func refuseEnvOutsideClaudeConfig(env []string) error {
	home, homeSet := lookupEnv(env, "HOME")
	if !homeSet || home == "" {
		return errors.New("refusing to start: the env file sets no HOME; Claude Code would fall back to this machine's real home directory")
	}
	configDir, configSet := lookupEnv(env, "CLAUDE_CONFIG_DIR")
	if !configSet || configDir == "" {
		return errors.New("refusing to start: the env file sets no CLAUDE_CONFIG_DIR; Claude Code would fall back to this machine's real ~/.claude")
	}
	for _, entry := range localClaudeEntries {
		if err := refuseIfPresent(filepath.Join(home, entry)); err != nil {
			return err
		}
	}
	realHome, err := os.UserHomeDir()
	if err != nil || realHome == "" {
		return nil
	}
	realConfig := filepath.Join(realHome, ".claude")
	within, err := isSameOrWithin(configDir, realConfig)
	if err != nil {
		return fmt.Errorf("refusing to start: cannot inspect CLAUDE_CONFIG_DIR %q: %w", configDir, err)
	}
	if within {
		return fmt.Errorf("refusing to start: CLAUDE_CONFIG_DIR %q is, or is inside, this machine's real ~/.claude, outside the run", configDir)
	}
	withinHome, err := isSameOrWithin(home, realHome)
	if err != nil {
		return fmt.Errorf("refusing to start: cannot inspect HOME %q: %w", home, err)
	}
	if withinHome {
		return fmt.Errorf("refusing to start: HOME %q is, or is inside, this machine's real home directory, outside the run", home)
	}
	return nil
}

// resolveMaybe resolves p to an absolute, symlink-free path when p exists,
// and to a Cleaned absolute path when it does not (CLAUDE_CONFIG_DIR and a
// fresh HOME are routinely created after this check runs).
func resolveMaybe(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Clean(abs), nil
		}
		return "", err
	}
	return real, nil
}

// isSameOrWithin reports whether candidate is base itself or a descendant of
// it, after resolving both.
func isSameOrWithin(candidate, base string) (bool, error) {
	c, err := resolveMaybe(candidate)
	if err != nil {
		return false, err
	}
	b, err := resolveMaybe(base)
	if err != nil {
		return false, err
	}
	if c == b {
		return true, nil
	}
	rel, err := filepath.Rel(b, c)
	if err != nil {
		return false, nil //nolint:nilerr // not comparable (different volumes etc.) means not within
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

// resolveProgram finds name on the PATH the program will be given, not on the
// caller's: under -env-file the caller's PATH is not the program's.
func resolveProgram(name string, env []string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		return filepath.Abs(name)
	}
	path, _ := lookupEnv(env, "PATH")
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%q is not on the env file's PATH", name)
}

// probeVersion runs resolved once with versionArg under env, the same
// isolated environment the capture itself uses, and returns its trimmed
// combined output. It is the tool's own answer to "which build produced this
// capture", so record.sh no longer needs a second, differently-isolated
// invocation of the program to learn its version.
func probeVersion(resolved, versionArg string, env []string) (string, error) {
	//nolint:gosec // the operator explicitly supplies the program and the version flag
	cmd := exec.Command(resolved, versionArg)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %q %s: %w", resolved, versionArg, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// launcherInspection is what could be learned about a resolved program's own
// launcher (a wrapper script) without running it.
type launcherInspection struct {
	// Names are the environment variables the launcher sets or prefixes,
	// parsed from its source. Never their values: a launcher can carry a
	// credential in one.
	Names []string
	// Note explains why Names is empty when the launcher could not be read
	// as a script — most commonly because it is a binary, in which case its
	// own additions (if any) are compiled in rather than written as text.
	Note string
}

var (
	exportPattern = regexp.MustCompile(`(?m)^[ \t]*export[ \t]+([A-Za-z_][A-Za-z0-9_]*)=`)
	execPattern   = regexp.MustCompile(`(?m)^[ \t]*exec[ \t]+(?:-a[ \t]+\S+[ \t]+)?"?([^"'\s]+)"?`)
)

// inspectLauncher reads resolved and, when it is a text launcher script
// (the shape the Nix `claude` wrapper takes: `export`/`wrapProgram`-style
// lines ending in an `exec` of the real program), reports the names of the
// variables it sets or prefixes. It follows one launcher exec'ing another —
// a chain nix wrappers are built as — up to a handful of hops, and never
// executes anything: running the launcher to see what it does would run
// Claude.
func inspectLauncher(resolved string) launcherInspection {
	names := map[string]struct{}{}
	visited := map[string]struct{}{}
	path := resolved
	for hop := 0; hop < 5; hop++ {
		if path == "" {
			break
		}
		if _, seen := visited[path]; seen {
			break
		}
		visited[path] = struct{}{}
		data, err := os.ReadFile(path) //nolint:gosec // resolved is what this run resolved to start
		if err != nil {
			return launcherInspection{Note: fmt.Sprintf("could not read %s: %v", path, err)}
		}
		if !looksLikeScript(data) {
			if hop == 0 {
				return launcherInspection{Note: fmt.Sprintf("%s is not a text script; its environment additions, if any, could not be read", path)}
			}
			break
		}
		for _, m := range exportPattern.FindAllSubmatch(data, -1) {
			names[string(m[1])] = struct{}{}
		}
		next := ""
		if m := execPattern.FindSubmatch(data); m != nil {
			candidate := string(m[1])
			if strings.Contains(candidate, string(filepath.Separator)) && !strings.HasPrefix(candidate, "$") {
				next = candidate
			}
		}
		path = next
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	return launcherInspection{Names: sorted}
}

// looksLikeScript is a cheap, deliberately narrow text/binary test: a
// shebang is certainly a script, a handful of well-known binary magic
// numbers are certainly not, and anything else with a NUL in its first
// kilobyte is treated as binary rather than guessed at further.
func looksLikeScript(data []byte) bool {
	if bytes.HasPrefix(data, []byte("#!")) {
		return true
	}
	binaryMagics := [][]byte{
		{0x7f, 'E', 'L', 'F'},    // ELF
		{0xCF, 0xFA, 0xED, 0xFE}, // Mach-O 64-bit
		{0xCE, 0xFA, 0xED, 0xFE}, // Mach-O 32-bit
		{0xFE, 0xED, 0xFA, 0xCF}, // Mach-O 64-bit, big-endian
		{0xFE, 0xED, 0xFA, 0xCE}, // Mach-O 32-bit, big-endian
		{'M', 'Z'},               // PE/COFF
		{0xCA, 0xFE, 0xBA, 0xBE}, // Mach-O universal / Java class
	}
	for _, magic := range binaryMagics {
		if bytes.HasPrefix(data, magic) {
			return false
		}
	}
	probe := data
	if len(probe) > 1024 {
		probe = probe[:1024]
	}
	return !bytes.ContainsRune(probe, 0)
}

// writeRunMeta records what ran — the program, where it resolved, its digest,
// the NAMES of the variables it was given (never their values), what its own
// launcher adds on top (also names only), and, when probeVersion supplied
// one, the program's version string.
func writeRunMeta(path, program, resolved string, env []string, version string) error {
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
	launcher := inspectLauncher(resolved)
	launcherNames := launcher.Names
	if launcherNames == nil {
		launcherNames = []string{}
	}
	meta := map[string]any{
		"program": program, "resolved": resolved, "sha256": hex.EncodeToString(h.Sum(nil)),
		"envNames": names, "launcherEnvNames": launcherNames,
	}
	if launcher.Note != "" {
		meta["launcherNote"] = launcher.Note
	}
	if version != "" {
		meta["programVersion"] = version
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	return nil
}
