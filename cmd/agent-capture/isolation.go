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

// userHomeDir is os.UserHomeDir, indirected so a test can make it fail or
// return empty without needing an actual passwd entry with no home.
var userHomeDir = os.UserHomeDir

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

// refuseIfNonEmpty refuses when dir already exists and holds anything. A
// directory that does not exist yet is not a refusal -- the caller
// (record.sh, a test) routinely creates CLAUDE_CONFIG_DIR immediately before
// this check runs. A source that cannot be inspected refuses too, the same
// not-knowing-is-not-absence rule every other check in this file follows;
// that also covers dir existing as a plain file, which os.ReadDir reports as
// an error rather than an empty listing.
func refuseIfNonEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil:
		if len(entries) > 0 {
			return fmt.Errorf("refusing to start: CLAUDE_CONFIG_DIR %q already holds configuration; Claude Code must read only what this run creates", dir)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("refusing to start: cannot inspect CLAUDE_CONFIG_DIR %q: %w", dir, err)
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
//
// Finding the real home directory itself the same source that cannot be
// inspected refuses too: an os.UserHomeDir error or an empty result used to
// be read as "nothing to compare against" and let the run proceed, which is
// the one fail-open this function had against the not-knowing-is-not-absence
// rule every other check here follows.
func refuseEnvOutsideClaudeConfig(env []string) error {
	home, homeSet := lookupEnv(env, "HOME")
	if !homeSet || home == "" {
		return errors.New("refusing to start: the env file sets no HOME; Claude Code would fall back to this machine's real home directory")
	}
	configDir, configSet := lookupEnv(env, "CLAUDE_CONFIG_DIR")
	if !configSet || configDir == "" {
		return errors.New("refusing to start: the env file sets no CLAUDE_CONFIG_DIR; Claude Code would fall back to this machine's real ~/.claude")
	}
	// A relative value has no fixed meaning here: agent-capture's own cwd is
	// not the program's -- it is started with cmd.Dir set to -dir, which is
	// what every other check in this function inspects. Refusing outright
	// (nocx-nru89.10 finding 2) is simpler and stricter than resolving a
	// relative path against some base, and it is what the operator meant
	// anyway: HOME and CLAUDE_CONFIG_DIR are absolute paths by convention on
	// every platform Claude Code runs on.
	if !filepath.IsAbs(home) {
		return fmt.Errorf("refusing to start: HOME %q is not an absolute path", home)
	}
	if !filepath.IsAbs(configDir) {
		return fmt.Errorf("refusing to start: CLAUDE_CONFIG_DIR %q is not an absolute path", configDir)
	}
	for _, entry := range localClaudeEntries {
		if err := refuseIfPresent(filepath.Join(home, entry)); err != nil {
			return err
		}
	}
	// Claude must read only configuration this run creates, so a
	// CLAUDE_CONFIG_DIR that already holds anything -- wherever it lives, not
	// only inside the real ~/.claude -- is exactly the "outside the run"
	// source the flag promises against (nocx-nru89.10 finding 2). A
	// directory that does not exist yet (the common case: the caller creates
	// it fresh right before this check) is not a refusal.
	if err := refuseIfNonEmpty(configDir); err != nil {
		return err
	}
	realHome, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("refusing to start: cannot determine this machine's real home directory: %w", err)
	}
	if realHome == "" {
		return errors.New("refusing to start: this machine's real home directory came back empty")
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

// resolveMaybe resolves p to an absolute, symlink-free path. When p exists in
// full, that is exactly filepath.EvalSymlinks(p). When it does not --
// CLAUDE_CONFIG_DIR and a fresh HOME are routinely created after this check
// runs -- it resolves symlinks through the nearest ancestor that DOES exist
// and rejoins the missing suffix unresolved (a path component that does not
// exist cannot itself be a symlink). Stopping at the first ENOENT and
// returning the unresolved lexical path, as an earlier version did, missed
// exactly the case a run-owned directory hits on every use: a symlinked
// parent (nocx-nru89.10 finding 2) whose not-yet-created child is checked
// before the caller creates it.
func resolveMaybe(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	suffix := ""
	dir := abs
	for {
		real, evalErr := filepath.EvalSymlinks(dir)
		if evalErr == nil {
			if suffix == "" {
				return real, nil
			}
			return filepath.Join(real, suffix), nil
		}
		if !errors.Is(evalErr, fs.ErrNotExist) {
			return "", evalErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the root without finding an existing ancestor; return
			// the error from the shallowest, most informative attempt.
			return "", evalErr
		}
		if suffix == "" {
			suffix = filepath.Base(dir)
		} else {
			suffix = filepath.Join(filepath.Base(dir), suffix)
		}
		dir = parent
	}
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

// probeVersion runs resolved once with versionArg under env and dir, the same
// isolated environment and working directory the capture itself uses, and
// returns its trimmed combined output. It is the tool's own answer to "which
// build produced this capture", so record.sh no longer needs a second,
// differently-isolated invocation of the program to learn its version.
//
// dir must be the same directory refuseOutsideClaudeConfig already inspected
// (nocx-nru89.10 finding 3 / epic review finding 9): an empty dir here left
// the probe running in agent-capture's own, uninspected cwd -- under
// record.sh, the nocx checkout with its own CLAUDE.md above it.
func probeVersion(resolved, versionArg string, env []string, dir string) (string, error) {
	//nolint:gosec // the operator explicitly supplies the program and the version flag
	cmd := exec.Command(resolved, versionArg)
	cmd.Env = env
	cmd.Dir = dir
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
	// exportPattern matches nixpkgs makeShellWrapper's two export shapes:
	// `export VAR=...` (from --set/--set-default, always has an `=`) and the
	// bare `export VAR` nixpkgs' addValue emits at the end of every
	// --prefix/--suffix block (setup-hooks/make-wrapper.sh: five plain
	// `VAR=...` assignment lines with no `export` keyword at all, followed
	// by one `export VAR` with no `=`). The `=` arm and the end-of-line arm
	// are alternatives so a bare `export VAR` is matched too -- the gap
	// nocx-nru89.10 finding 1 named: the old pattern required `=` right
	// after the name, so PATH and LD_LIBRARY_PATH additions (always
	// --prefix, never --set) were silently missed on every real Nix
	// makeShellWrapper output.
	exportPattern = regexp.MustCompile(`(?m)^[ \t]*export[ \t]+([A-Za-z_][A-Za-z0-9_]*)(?:=|[ \t]*\r?$)`)
	execPattern   = regexp.MustCompile(`(?m)^[ \t]*exec[ \t]+(?:-a[ \t]+\S+[ \t]+)?"?([^"'\s]+)"?`)

	// binaryWrapperFlagPattern and binaryWrapperExecPattern read the
	// makeBinaryWrapper docstring a compiled Nix wrapper embeds in its own
	// .rodata (nixpkgs pkgs/by-name/ma/makeBinaryWrapper/make-binary-wrapper.sh,
	// docstring()/formatArgs()): a `makeCWrapper '<executable>' \` line
	// followed by one `--set`/`--set-default`/`--unset`/`--prefix`/`--suffix`
	// line per flag, each with its variable name single-quoted (bash `@Q`
	// quoting, which never needs anything but plain single quotes for a
	// valid environment-variable name). Verified against
	// /run/current-system/sw/bin/claude's resolved store path with
	// `strings -n8` (nocx-nru89.10 finding 1): the file is ELF, so
	// looksLikeScript reports it as binary, but its own documentation of
	// itself is plain, greppable text.
	binaryWrapperFlagPattern = regexp.MustCompile(`(?m)^[ \t]*--(?:set-default|set|unset|prefix|suffix)[ \t]+'([A-Za-z_][A-Za-z0-9_]*)'`)
	binaryWrapperExecPattern = regexp.MustCompile(`(?m)^[ \t]*makeCWrapper[ \t]+'([^']*)'`)
)

// maxWrapperHopBytes bounds how much of a hop's file inspectLauncher will
// read. A real Nix wrapper -- shell script or compiled makeBinaryWrapper
// binary -- is a few KB to a few tens of KB; the program at the end of the
// chain (the real `claude`, a bundled ~200MB Node/Bun executable on this
// machine) is not, and is not itself a further wrapper. Stopping the chain
// at a hop over this bound, rather than reading it fully looking for a
// pattern it cannot contain, is the deliberate boundary between "still
// might be a wrapper" and "this is the program" -- distinct from an
// unreadable hop, which is a real I/O failure and is noted as one.
const maxWrapperHopBytes = 8 * 1024 * 1024

// inspectLauncher reads resolved and, when it is a launcher whose own
// environment additions are readable as text -- either a shell script (the
// nixpkgs makeShellWrapper shape: `export`/bare-`export` lines ending in an
// `exec` of the real program) or a compiled makeBinaryWrapper binary (whose
// own generating command is embedded as a readable docstring) -- reports the
// names of the variables it sets, defaults, unsets or prefixes/suffixes. It
// follows one launcher naming another as its wrapped executable -- a chain
// Nix wrappers are routinely built as -- up to a handful of hops, and never
// executes anything: running the launcher to see what it does would run
// Claude. Names already found survive a later hop that cannot be read; only
// hop 0 being unreadable, or being neither shape, empties the result --
// losing an earlier hop's real findings because a later one failed was
// nocx-nru89.10 finding 1's second gap.
func inspectLauncher(resolved string) launcherInspection {
	names := map[string]struct{}{}
	visited := map[string]struct{}{}
	note := ""
	path := resolved
	for hop := 0; hop < 5; hop++ {
		if path == "" {
			break
		}
		if _, seen := visited[path]; seen {
			break
		}
		visited[path] = struct{}{}

		info, statErr := os.Stat(path)
		if statErr != nil {
			if hop == 0 {
				return launcherInspection{Note: fmt.Sprintf("could not read %s: %v", path, statErr)}
			}
			note = fmt.Sprintf("could not read %s: %v", path, statErr)
			break
		}
		if info.Size() > maxWrapperHopBytes {
			if hop == 0 {
				return launcherInspection{Note: fmt.Sprintf("%s is too large (%d bytes) to inspect as a launcher; its environment additions, if any, could not be read", path, info.Size())}
			}
			break
		}

		data, err := os.ReadFile(path) //nolint:gosec // resolved is what this run resolved to start, size-bounded above
		if err != nil {
			if hop == 0 {
				return launcherInspection{Note: fmt.Sprintf("could not read %s: %v", path, err)}
			}
			note = fmt.Sprintf("could not read %s: %v", path, err)
			break
		}

		next := ""
		switch {
		case looksLikeScript(data):
			for _, m := range exportPattern.FindAllSubmatch(data, -1) {
				names[string(m[1])] = struct{}{}
			}
			if m := execPattern.FindSubmatch(data); m != nil {
				candidate := string(m[1])
				if strings.Contains(candidate, string(filepath.Separator)) && !strings.HasPrefix(candidate, "$") {
					next = candidate
				}
			}
		case binaryWrapperExecPattern.Match(data):
			for _, m := range binaryWrapperFlagPattern.FindAllSubmatch(data, -1) {
				names[string(m[1])] = struct{}{}
			}
			if m := binaryWrapperExecPattern.FindSubmatch(data); m != nil {
				next = string(m[1])
			}
		default:
			if hop == 0 {
				return launcherInspection{Note: fmt.Sprintf("%s is not a text script or a makeBinaryWrapper binary; its environment additions, if any, could not be read", path)}
			}
			// Neither shape: the chain has reached the real program, not a
			// further wrapper. Not an error -- there is nothing more to
			// learn, and the names already collected stand.
		}
		path = next
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	result := launcherInspection{Names: sorted}
	if note != "" {
		result.Note = note
	}
	return result
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

// redactHomePrefix replaces this machine's real home directory prefix in p
// with "~", the same way a shell prints it. A default native install
// (`~/.local/bin/claude`) puts resolved's value under the operator's own
// home, and nothing else about meta.json identifies whose machine it is
// (nocx-nru89.10 finding 4 / epic review finding 12) -- SKILL.md's promise is
// names, not values, and a full home path is close enough to an identifying
// value to redact the same way. If the real home cannot be determined, or p
// is not under it, p is returned unchanged: redaction is a courtesy on top
// of the refusals above, not itself a safety boundary.
func redactHomePrefix(p string) string {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return p
	}
	cleanHome := filepath.Clean(home)
	cleanPath := filepath.Clean(p)
	if cleanPath == cleanHome {
		return "~"
	}
	rel, err := filepath.Rel(cleanHome, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.Join("~", rel)
}

// writeRunMeta records what ran — the program, where it resolved (its home
// directory prefix, if any, replaced with "~"), its digest, the NAMES of the
// variables it was given (never their values), what its own launcher adds on
// top (also names only), and, when probeVersion supplied one, the program's
// version string.
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
		"program": program, "resolved": redactHomePrefix(resolved), "sha256": hex.EncodeToString(h.Sum(nil)),
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
