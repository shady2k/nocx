package commandnames

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shady2k/nocx/internal/proc"
)

// LocalRoute is the route identity of this machine. It is a constant rather
// than a hostname: the cache is per application instance and the machine it
// runs on cannot change underneath it.
const LocalRoute = "local"

// LocalSource enumerates this machine's PATH under a supervisor that owns
// its own process group.
//
// The supervisor is the point. Before this the enumeration was a background
// pipeline inside the user's own shell, and its budget stopped the WAIT
// rather than the WORK: the grace period expired, the shell went on to the
// prompt, and `compgen -c | sort -u | …` kept reading directories with
// nobody left to want the answer. The exit cleanup group-killed only when
// the job happened to be a process-group leader — in a non-interactive shell
// it is not — so the fallback killed the subshell and orphaned the pipeline
// it had started. Here the group is ours by construction, the deadline
// terminates it, kills it and reaps it, and a result is published only if it
// completed inside the deadline.
//
// It runs `sh`, not the user's login shell, and takes the environment the
// application would give a session. That is a deliberate UNDER-estimate: a
// PATH entry added by the user's own rc file is not in the shared set, so a
// name may be missing from completion, and no name is ever offered that the
// session cannot run. The opposite choice — running a login shell to
// discover the widest possible PATH — would offer names from directories a
// non-login session never has, which is the failure that actually hurts. The
// shell's own tables (builtins, functions, aliases) are enumerated by the
// session itself and are not affected either way.
type LocalSource struct {
	sup        proc.Supervisor
	generation string
	env        []string
	shell      string
}

// NewLocalSource builds the local source. generation is the installed
// integration generation; env is the environment a local session would get
// (nil takes the application's own, which is what the session inherits).
func NewLocalSource(generation string, env []string) *LocalSource {
	if env == nil {
		env = os.Environ()
	}
	return &LocalSource{
		sup:        proc.Supervisor{Clock: proc.RealClock{}},
		generation: generation,
		env:        env,
		shell:      "sh",
	}
}

func (s *LocalSource) Identity() Identity {
	return Identity{Route: LocalRoute, Generation: s.generation}
}

func (s *LocalSource) Probe(ctx context.Context) (Probe, error) {
	out, nonce, err := s.run(ctx, probeScript, ProbeDeadline, 256*1024)
	if err != nil {
		return Probe{}, err
	}
	return parseProbe(out, nonce)
}

func (s *LocalSource) Scan(ctx context.Context, _ Probe) (Scan, error) {
	out, nonce, err := s.run(ctx, scanScript, ScanDeadline, MaxScanBytes)
	if err != nil {
		return Scan{}, err
	}
	return parseScan(out, nonce)
}

// run executes one script under the supervisor and returns its output only
// when the run COMPLETED. Anything else — the deadline, the output bound, a
// non-zero exit, a cancelled context — returns an error and no bytes, so
// there is no path by which a partial enumeration reaches the parser.
func (s *LocalSource) run(ctx context.Context, script string, deadline time.Duration, maxBytes int) ([]byte, string, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, "", err
	}
	out, err := s.sup.Run(ctx, proc.Job{
		Argv:     []string{s.shell, "-c", script, "nocx", nonce},
		Env:      s.env,
		Deadline: deadline,
		MaxBytes: maxBytes,
	})
	switch {
	case errors.Is(err, proc.ErrDeadline), errors.Is(err, proc.ErrOutputBound):
		// Both are the same fact to the caller: the work was stopped, so
		// what we hold is a prefix. ErrScanDeadline is what makes the
		// product say "timed out" rather than "failed".
		return nil, "", fmt.Errorf("%w: %v", ErrScanDeadline, err)
	case err != nil:
		return nil, "", fmt.Errorf("commandnames: local %s: %w", s.shell, err)
	case !out.Complete:
		return nil, "", fmt.Errorf("%w: the run did not complete", ErrScanDeadline)
	}
	return out.Stdout, nonce, nil
}
