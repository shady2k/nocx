// Package shellenv answers one question for the whole backend: what
// environment does the person's shell give a child of it?
//
// # Why it is one owner
//
// Two callers ask it and they used to answer it twice. internal/git/local
// resolves the environment once, at backend start, so a commit's hook runs
// with the PATH the person's shell has rather than the GUI launch's bare one
// (design D6, nocx-6pz0); internal/agentdetect asks the same question when
// Settings lists the agents on this computer (ADR-anntnxdj decision 6,
// nocx-4d6uf). The two copies agreed everywhere anyone looked, and the one
// place they differed was the mode: git's is the interactive shell a pane
// starts, detection's is the login shell too. So the mode is a parameter of
// ONE function, and everything else — the marker, `env -0`, the bound, the
// cap and the parse — is stated once.
//
// The callers keep their own caches and lifetimes: git remembers a resolved
// environment for the process and retries a failure after a cooldown, and
// detection caches a pass until Settings asks for a refresh. This package
// holds neither.
//
// # The dump is the kernel's, not the shell's
//
// `env -0` rather than `export -p`, which is what this ran until nocx-58gq.
// POSIX says `export -p` writes the exported variables in a re-enterable
// form; zsh does not write the ones tied to an array — PATH, FPATH and SHLVL
// — so on macOS, whose default shell IS zsh, the resolver read an
// environment with no PATH on EVERY machine and D6 degraded for every user of
// the shipping platform. There is no portable `export -p` incantation that
// includes them (`typeset -x` is not in dash). `env` is the same question
// asked of the kernel instead of the shell: it needs no un-quoting, it cannot
// disagree with itself between shells, and it is literally the environment a
// child gets. Absolute path because a broken PATH is one of the states this
// must still resolve through; a missing /usr/bin/env fails the exec and
// degrades with the shell's exit status, which is honest.
package shellenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/proc"
)

// Marker separates whatever the rc files printed on their way past — a
// greeting, a version notice, a fastfetch — from the environment dump that
// follows it. Everything before the first occurrence is discarded; the bytes
// after it are the dump and nothing else, because `exec` leaves no shell
// behind to write again.
//
// It is exported because a fake shell in another package's test must speak the
// same protocol; production code never prints it by hand.
const Marker = "\n__nocx_env_dump__\n"

// killGrace is the pause between the escalation steps when a shell has to be
// stopped (INT, TERM, then KILL). It is the value git's resolver has always
// used: a shell that handles INT promptly is gone in one step, and a hung rc
// file costs at most two graces before it is killed.
const killGrace = 200 * time.Millisecond

// Spec is one resolution.
type Spec struct {
	// Shell is the shell to run, by absolute path.
	Shell string
	// Env is the environment the shell starts from. It is the caller's
	// because the two callers start the shell differently on purpose: git
	// hands it the scrubbed, locale-completed environment the pane's shell
	// gets, and detection hands it the backend's own.
	Env []string
	// Dir is the explicit working directory for startup and the dump. Empty
	// preserves the caller's working directory.
	Dir string
	// Login makes the shell read its login files as well as its interactive
	// ones. It is the one fact that differs between the callers: a pane
	// starts `shell -i`, so git asks for the interactive shell; Settings
	// asks for the login shell's PATH, so detection asks for both.
	Login bool
	// Timeout bounds the whole resolution, and MaxOut bounds what the shell
	// may print. Both are the caller's numbers: how long a caller can afford
	// to wait is not this package's knowledge.
	Timeout time.Duration
	MaxOut  int64
}

// ErrOutputCap means the dump was refused because the shell exceeded MaxOut.
// No retained prefix is authoritative, even if it already contains PATH.
var ErrOutputCap = errors.New("shellenv: output cap reached")

// Env runs the shell and returns the exported environment a child of it would
// receive, NUL-delimited by `env -0` and parsed into NAME=VALUE records. The
// environment is bounded by Spec.Timeout and Spec.MaxOut, and a shell that
// outlives either is stopped with its whole process group.
func Env(ctx context.Context, spec Spec) ([]string, error) {
	if spec.Shell == "" {
		return nil, errors.New("shellenv: no shell to run")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	argv := []string{spec.Shell}
	if spec.Login {
		argv = append(argv, "-l")
	}
	tail := "printf '" + Marker + "'; exec /usr/bin/env -0"
	if spec.Dir != "" {
		// Startup files may change directory. Restore the same explicit cwd
		// that the caller will use for PATH lookup and child processes.
		tail = "cd -- '" + strings.ReplaceAll(spec.Dir, "'", "'\\''") + "' && " + tail
	}
	argv = append(argv, "-i", "-c", tail)

	stopped := make(chan struct{})
	var stopOnce sync.Once
	sink := &cappedSink{max: spec.MaxOut}
	sink.onFull = func() { stopOnce.Do(func() { close(stopped) }) }

	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the shell is the caller's; the directory is shell-quoted
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.Stdout = sink
	cmd.Stderr = io.Discard
	// The shell leads its own group, so stopping it reaches anything an rc
	// file started and nothing else (internal/proc's one behaviour).
	proc.InOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the shell %s could not be started: %w", spec.Shell, err)
	}

	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	// Whichever comes first — the cap, the deadline or the caller's context —
	// stops the group; the reaping stays the waiter's.
	go func() {
		select {
		case <-done:
		case <-stopped:
			proc.KillGroup(cmd, done, killGrace)
		case <-ctx.Done():
			proc.KillGroup(cmd, done, killGrace)
		}
	}()
	<-done

	switch {
	case sink.full():
		return nil, ErrOutputCap
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case waitErr != nil:
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) && exit.Exited() {
			return nil, fmt.Errorf("the shell exited %d while resolving the environment", exit.ExitCode())
		}
		return nil, fmt.Errorf("the shell %s ended: %w", spec.Shell, waitErr)
	}

	env, err := parseEnvDump(string(sink.buf))
	if err != nil {
		return nil, err
	}
	if _, ok := Path(env); !ok {
		return nil, errors.New("the shell resolved an environment with no PATH")
	}
	return env, nil
}

// Path returns the PATH the environment carries. A PATH that is present but
// empty is not an environment a child can find anything in, so it is reported
// as absent — the verdict every resolver here has always given.
func Path(env []string) (string, bool) {
	for _, kv := range env {
		if path, ok := strings.CutPrefix(kv, "PATH="); ok && path != "" {
			return path, true
		}
	}
	return "", false
}

// cappedSink keeps the first max bytes and, the moment there is no room left,
// tells the resolver to stop the shell. It never blocks the writer.
type cappedSink struct {
	buf    []byte
	max    int64
	onFull func()
	hit    bool
}

func (s *cappedSink) Write(p []byte) (int, error) {
	room := s.max - int64(len(s.buf))
	if room <= 0 {
		s.reachedCap()
		return 0, ErrOutputCap
	}
	if int64(len(p)) > room {
		s.buf = append(s.buf, p[:room]...)
		s.reachedCap()
		return int(room), ErrOutputCap
	}
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *cappedSink) reachedCap() {
	if s.hit {
		return
	}
	s.hit = true
	if s.onFull != nil {
		s.onFull()
	}
}

// full reports whether the cap was reached. It is read after the shell is
// reaped, which is what orders it after the copy goroutine's write.
func (s *cappedSink) full() bool { return s.hit }

// parseEnvDump parses the NUL-delimited output of `env -0` that follows
// Marker. Everything before the marker is whatever the rc files printed and
// is discarded; each record after it is one NAME=VALUE exactly as the kernel
// holds it, so there is no quoting to undo and a value containing a newline,
// a space or a quote survives verbatim.
//
// A record whose name is not a valid environment-variable name is dropped
// rather than failing the resolution: it is the shape a stray write from a
// background job in an rc file takes, and one of those must not cost the user
// the whole environment.
//
// PWD is dropped: it is per-process state, and the stale copy the shell
// exported would lie to hooks about where they are.
func parseEnvDump(out string) ([]string, error) {
	i := strings.Index(out, Marker)
	if i < 0 {
		return nil, errors.New("the shell printed no environment dump")
	}
	dump := out[i+len(Marker):]
	if dump != "" && !strings.HasSuffix(dump, "\x00") {
		return nil, errors.New("the shell printed an incomplete environment record")
	}
	var env []string
	for _, record := range strings.Split(dump, "\x00") {
		eq := strings.IndexByte(record, '=')
		if eq <= 0 {
			continue // the trailing empty record, or a name-less fragment
		}
		name := record[:eq]
		if !validName(name) || name == "PWD" {
			continue
		}
		env = append(env, record)
	}
	return env, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
