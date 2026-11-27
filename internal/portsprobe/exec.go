package portsprobe

// The execution seam the ladder runs on, and its one implementation over
// os/exec.
//
// A rung is ARGV — a program and its fixed arguments — and never a shell
// string. That is the whole reason this package can run inside the artifact
// deployed to somebody else's host (design §5.10): there is no shell to
// reinterpret an argument, so no caller-supplied text can become syntax, and
// the ladder's own definitions are the only commands that exist.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/proc"
)

// OutputCap bounds what one rung's stdout and stderr may capture, per stream.
// Past it the capture is TRUNCATED, and the ladder reports a transient failure:
// a prefix of a table is not a table, and reading one as "no listeners" is the
// defect the bound exists to prevent.
const OutputCap = 64 << 10

// StderrExcerptCap bounds the stderr carried on a Sample for diagnostics: the
// panel shows a truncated excerpt, never an uncontrolled dump of a host's
// output.
const StderrExcerptCap = 2 << 10

// ErrCommandAbsent reports that a rung's program is not on the daemon's PATH.
// It is its own error because "this host does not have that tool" is a cached
// fact about the host, while every other exec failure is transient.
var ErrCommandAbsent = errors.New("portsprobe: the probe command is not on PATH")

// Command is one rung's invocation: argv, and the environment entries this rung
// needs beside the daemon's own.
type Command struct {
	// Argv is the program and its arguments, Argv[0] naming the program. It is
	// never a command line and never passes through a shell.
	Argv []string
	// Env is appended to the daemon's environment for this rung only.
	Env []string
}

// Result is what one rung's program printed and how it ended.
type Result struct {
	Stdout     []byte
	Stderr     []byte
	ExitStatus int
	// Truncated reports that a capture hit OutputCap, so the bytes here are a
	// prefix rather than the whole stream.
	Truncated bool
}

// Runner executes one rung. It is injected so the ladder's classification can
// be asserted without an exec and without a host; the far helper's composition
// root passes NewExecRunner.
type Runner interface {
	Run(ctx context.Context, cmd Command) (*Result, error)
}

// ExecRunner runs rungs as child processes of this daemon.
//
// The program is resolved on THIS PROCESS's PATH, which is the daemon's — the
// PATH of the shell a person types into is the shell's, and the helper has
// never consulted it. A missing program is ErrCommandAbsent; a non-zero exit is
// an ANSWER and is returned in the Result rather than as an error, because the
// ladder's classification is the caller's.
type ExecRunner struct {
	cap int
}

// NewExecRunner returns the runner the deployed helper uses.
func NewExecRunner() *ExecRunner { return &ExecRunner{cap: OutputCap} }

// Run executes one rung and captures its streams.
func (r *ExecRunner) Run(ctx context.Context, cmd Command) (*Result, error) {
	if len(cmd.Argv) == 0 {
		return nil, errors.New("portsprobe: a rung with no argv")
	}
	if _, err := exec.LookPath(cmd.Argv[0]); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCommandAbsent, cmd.Argv[0])
	}

	// The child's life is this request's, and a capture past the bound ends it:
	// a tool that prints a gigabyte is not allowed to run to the pass's
	// deadline while its bytes are discarded.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &cappedBuffer{limit: r.cap, overflow: cancel}
	stderr := &cappedBuffer{limit: r.cap, overflow: cancel}

	child := exec.CommandContext(runCtx, cmd.Argv[0], cmd.Argv[1:]...) // #nosec G204 — argv is this package's own ladder, never caller text
	child.Env = append(os.Environ(), cmd.Env...)
	child.Stdout = stdout
	child.Stderr = stderr
	// The whole GROUP dies with the request, not just the program this runner
	// spawned: a rung is often a wrapper whose tool runs as a child of it, and a
	// child that inherited stdout keeps the capture open after its parent is
	// killed. Without the group, Run waits for that descendant's EOF and the
	// pass's bound becomes its lifetime — the one promise this seam makes.
	proc.InOwnGroup(child)
	child.Cancel = func() error { return proc.KillGroupImmediately(child) }
	// A descendant may still hold the pipe after the leader is reaped; this
	// bounds the wait the group kill did not have to see.
	child.WaitDelay = time.Second

	runErr := child.Run()
	// Cleanup for every ending, not only the deadline: a wrapper that exits
	// successfully leaving a helper behind would otherwise keep the group alive
	// for the rest of the daemon's life.
	_ = proc.KillGroupImmediately(child)
	res := &Result{
		Stdout:     stdout.buf,
		Stderr:     stderr.buf,
		Truncated:  stdout.truncated || stderr.truncated,
		ExitStatus: -1, // no process state: killed, or never started
	}
	if child.ProcessState != nil {
		res.ExitStatus = child.ProcessState.ExitCode()
	}
	if res.Truncated {
		return res, nil
	}
	// A request that died owns the answer: the exit status of a killed child is
	// not a fact about the host's tools.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var exit *exec.ExitError
	switch {
	case runErr == nil:
		return res, nil
	case errors.As(runErr, &exit):
		return res, nil
	default:
		return nil, runErr
	}
}

// cappedBuffer captures up to limit bytes and remembers that it dropped the
// rest. overflow runs the moment the bound is crossed.
type cappedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
	overflow  func()
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
		if room < len(p) {
			b.over()
		}
	} else if len(p) > 0 {
		b.over()
	}
	return len(p), nil
}

func (b *cappedBuffer) over() {
	b.truncated = true
	if b.overflow != nil {
		b.overflow()
	}
}

// NotFoundOnStderr reports whether stderr says the tool was not found — a
// shell's "sh: ss: not found" / "command not found" — independent of the exit
// status, for a host whose wrapper reports the miss itself.
func NotFoundOnStderr(stderr []byte) bool {
	low := strings.ToLower(string(stderr))
	return strings.Contains(low, "not found") || strings.Contains(low, "no such file")
}

// StderrExcerpt bounds the stderr a Sample carries.
func StderrExcerpt(b []byte) string {
	if len(b) > StderrExcerptCap {
		return string(b[:StderrExcerptCap]) + " [truncated]"
	}
	return string(b)
}
