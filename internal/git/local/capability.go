package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/git/spawn"
)

// capability answers "is git here, and is it new enough" (spec §5.1). It is
// the factory's only owner of that fact: the version is carried in
// OpenOutcome and nowhere else — a Capability method on Repo would give one
// fact two owners that could disagree, and nothing after git.open asks for
// it.
type capability struct {
	mu      sync.Mutex
	path    string
	version string
	err     error
	done    bool
}

// probe returns the resolved git path and version, probing once and caching
// the result. Only a completed probe is cached: a failure (git absent, or a
// caller's context cancelling mid-probe) is retried on the next open, so a
// git installed after the first open is found, and the panel is not poisoned
// by a transient failure.
func (c *capability) probe(ctx context.Context, env []string) (path, version string, err error) {
	c.mu.Lock()
	if c.done {
		path, version, err = c.path, c.version, c.err
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	path, version, err = probeGit(ctx, env)
	c.mu.Lock()
	if err == nil {
		c.path, c.version, c.err, c.done = path, version, nil, true
	}
	c.mu.Unlock()
	return
}

func probeGit(ctx context.Context, env []string) (string, string, error) {
	path, err := resolveGit(env)
	if err != nil {
		return "", "", err
	}
	sink := &byteSink{max: 8 << 10}
	res := run(ctx, spec{
		argv: []string{path, "--version"},
		env:  env,
		sink: sink,
	})
	if res.cancelled {
		return "", "", ctx.Err()
	}
	if res.err != nil {
		return "", "", res.err
	}
	if res.exitCode != 0 {
		return "", "", fmt.Errorf("git --version: exit %d: %s", res.exitCode, res.stderr)
	}
	return path, strings.TrimSpace(string(sink.buf)), nil
}

// resolveGit finds the git binary in the given environment's PATH. exec
// resolves the executable against the CURRENT process's PATH, not cmd.Env,
// so resolving against the user's resolved environment needs an explicit
// scan — and the resolved environment is the whole point (D6).
func resolveGit(env []string) (string, error) {
	path := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	if path == "" {
		path = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "git")
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

// minGitMajor and minGitMinor are the version floor: 2.25 (January 2020),
// set by --pathspec-from-file / --pathspec-file-nul — the flags mutations
// stage and unstage paths with (D8). --porcelain=v2 would need only 2.11.
const (
	minGitMajor = 2
	minGitMinor = 25
)

// worktreeNULMajor and worktreeNULMinor are the version that added the
// NUL-terminated worktree listing: 2.36 (April 2022). Its release notes say
// exactly why the form exists — `git worktree list --porcelain` "did not
// c-quote pathnames and lock reasons with unsafe bytes correctly, which is
// worked around by introducing NUL terminated output format with -z" — so a
// worktree list read on 2.36 or later uses the form that is unambiguous for
// every path, and one read below it uses the form git then had. The floor
// stays 2.25: this is a form the operation PREFERS, not a version it
// requires.
const (
	worktreeNULMajor = 2
	worktreeNULMinor = 36
)

// worktreeListHasNUL reports whether this git's worktree listing can be asked
// for the NUL-terminated form. An unparseable version answers false — asking
// for a flag the git does not have would fail the whole operation — though
// that answer is unreachable through Open, where belowFloor refuses such a
// version before a Repo exists.
func worktreeListHasNUL(version string) bool {
	maj, min, ok := parseVersion(version)
	if !ok {
		return false
	}
	return maj > worktreeNULMajor || (maj == worktreeNULMajor && min >= worktreeNULMinor)
}

// belowFloor reports whether a `git --version` answer is below the floor.
// An unparseable answer is treated as below the floor too — the outcome
// carries the raw string so the panel can show what git actually said.
func belowFloor(version string) bool {
	maj, min, ok := parseVersion(version)
	if !ok {
		return true
	}
	return maj < minGitMajor || (maj == minGitMajor && min < minGitMinor)
}

// parseVersion parses "git version 2.55.0" (or "2.55.0" alone) into its
// major and minor components.
func parseVersion(v string) (major, minor int, ok bool) {
	rest := strings.TrimPrefix(strings.TrimSpace(v), "git version ")
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// errNotARepository marks a rev-parse answer that says "this is not a
// repository" — non-zero exit, or output that fails validation.
var errNotARepository = errors.New("git: not a repository")

// revParse asks git for the two values that are the binding's identity: the
// worktree root and the absolute git directory (spec §5.1, D4). git prints
// exactly two lines for --show-toplevel --absolute-git-dir, so the output is
// validated, not trusted: anything other than exactly two absolute,
// non-empty lines is notARepository — never a path we hand to a subprocess.
func revParse(ctx context.Context, gitPath string, env []string, cwd string) (string, string, error) {
	sink := &byteSink{max: 8 << 10}
	res := run(ctx, spec{
		argv: append([]string{gitPath}, spawn.RevParseArgs()...),
		dir:  cwd,
		env:  env,
		sink: sink,
	})
	if res.cancelled {
		return "", "", ctx.Err()
	}
	if res.err != nil {
		return "", "", res.err
	}
	if res.exitCode != 0 {
		return "", "", errNotARepository
	}
	out := strings.TrimSuffix(string(sink.buf), "\n")
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" ||
		!filepath.IsAbs(lines[0]) || !filepath.IsAbs(lines[1]) {
		return "", "", errNotARepository
	}
	return lines[0], lines[1], nil
}
