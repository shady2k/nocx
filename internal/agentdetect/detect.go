// Package agentdetect finds the agents nocx's assistant can run on this
// machine: which of them are on the person's login shell's PATH, what version
// each prints, and whether it says it is ready.
//
// # The login shell is the frame, not the backend
//
// A GUI-launched nocx inherits launchd's or the desktop session's PATH, and an
// agent installed with npm or a version manager is not on it. Resolving an
// agent record's bare command on the app's own PATH was a real defect
// (nocx-4d6uf), and detection must not repeat it: it asks the person's login
// shell where its PATH is and resolves every command through that. A shell
// that cannot answer is an error, never a fallback to the backend's PATH —
// reporting every agent "not installed" because we looked in the wrong place
// is the failure this package exists to avoid.
//
// # One pass, cached
//
// A pass runs the login shell once, then each agent's version command and, for
// an agent that states one, its readiness probe. It is cached until a caller
// asks for a refresh: Settings opens the same answer every time, and a person
// who has just installed an agent asks again.
//
// # Every probe is bounded
//
// A version command or a readiness probe that hangs is killed at its bound and
// answers unknown. Unknown is never ready: a probe's exit status is the answer,
// and a probe that never produced one has not said the agent works.
package agentdetect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/proc"
	"github.com/shady2k/nocx/internal/shellenv"
)

const (
	// probeTimeout bounds one version command or one readiness probe. It is
	// generous for a CLI that starts a runtime of its own — pi and omp are
	// Node programs — and short enough that a hung one is a visible unknown
	// rather than a Settings section that never opens.
	probeTimeout = 2 * time.Second
	// loginShellTimeout bounds the one login shell a pass runs. It matches
	// the bound internal/git/local gives its own resolution (nocx-6pz0): a
	// large rc file on a network mount may be slow, and a hung one must not
	// hold detection open.
	loginShellTimeout = 5 * time.Second
	// maxEnvDump bounds what the login shell may print. An rc file that
	// greets the person is normal and small; one that writes without end must
	// not cost detection its memory.
	maxEnvDump = 256 << 10
)

// Shell answers which full environment and working directory this
// person's login shell gives a probe. It is an interface because the answer is a
// subprocess against a machine's own configuration, and a test supplies the
// PATH it wants to ask about rather than hoping the machine running the suite
// is configured the right way.
type Shell interface {
	Environment(ctx context.Context) ([]string, string, error)
}

// LoginShell is the production Shell: the person's login shell, run once.
//
// It is run with `-l -i` — a login shell, and interactive — because the
// question is the login shell's PATH and because zsh, the shipping platform's
// default, keeps its PATH in .zshrc, which a non-interactive shell never
// reads. The incantation itself (the marker, `env -0`, the bound, the cap and
// the parse) is internal/shellenv's, shared with internal/git/local, which
// asks the same question for git; only the mode and the numbers are this
// caller's.
type LoginShell struct{}

// Environment resolves the full login environment at the person's home directory.
func (LoginShell) Environment(ctx context.Context) ([]string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	env, err := shellenv.Env(ctx, shellenv.Spec{
		Shell: loginshell.New().Resolve().Path, Env: os.Environ(), Dir: home,
		Login: true, Timeout: loginShellTimeout, MaxOut: maxEnvDump,
	})
	return env, home, err
}

// Detector runs detection passes and caches the last answer.
type Detector struct {
	mu     sync.Mutex
	shell  Shell
	cached []agentrecord.Detection
	flight *detectionPass
	// probeTimeout is a field so a test can make a hung probe cheap to
	// observe; production leaves it at probeTimeout.
	probeTimeout time.Duration
}

// New builds a Detector over a login shell. It is wired at the composition
// root, like every other module's seam.
func New(shell Shell) *Detector {
	return &Detector{shell: shell, probeTimeout: probeTimeout}
}

type detectionPass struct {
	done chan struct{}
	rows []agentrecord.Detection
	err  error
}

// Detect answers for every agent the assistant can run, in the record's order.
// The answer is cached; refresh asks the login shell again.
//
// One pass holds the detector, so two windows asking at once run one pass and
// both read its answer — the login shell is asked once, as it should be.
func (d *Detector) Detect(ctx context.Context, refresh bool) ([]agentrecord.Detection, error) {
	if d == nil || d.shell == nil {
		return nil, errors.New("agentdetect: no login shell is wired")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	pass := d.flight
	if pass == nil {
		if d.cached != nil && !refresh {
			rows := append([]agentrecord.Detection(nil), d.cached...)
			d.mu.Unlock()
			return rows, nil
		}
		pass = &detectionPass{done: make(chan struct{})}
		d.flight = pass
		go d.completePass(context.WithoutCancel(ctx), pass)
	}
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pass.done:
		return append([]agentrecord.Detection(nil), pass.rows...), pass.err
	}
}

// completePass owns the bounded shared work, independently of any one waiter.
func (d *Detector) completePass(ctx context.Context, pass *detectionPass) {
	rows, err := d.detect(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	pass.rows, pass.err = rows, err
	if err == nil {
		d.cached = rows
	}
	d.flight = nil
	close(pass.done)
}

func (d *Detector) detect(ctx context.Context) ([]agentrecord.Detection, error) {
	env, cwd, err := d.shell.Environment(ctx)
	if err != nil {
		return nil, fmt.Errorf("agentdetect: the login shell's environment could not be read: %w", err)
	}
	path, _ := shellenv.Path(env)

	agents := agentrecord.SupportedAgents()
	rows := make([]agentrecord.Detection, 0, len(agents))
	for _, agent := range agents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := agentrecord.Detection{
			ID:          agent.ID,
			DisplayName: agent.DisplayName,
			Readiness:   agentrecord.ReadinessNotInstalled,
		}
		if executable, resolveErr := agentapproval.IdentityForExecutableInPath(agent.Command, path, cwd); resolveErr == nil {
			row.Found = true
			row.Readiness = agentrecord.ReadinessInstalled
			row.Version = d.version(ctx, executable.Path, agent.VersionArgs, env, cwd)
			if agent.Readiness != nil {
				row.Readiness = d.readiness(ctx, executable.Path, *agent.Readiness, env, cwd)
			}
		}
		rows = append(rows, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]agentrecord.Detection(nil), rows...), nil
}

// version runs the command's version flag and returns the first non-empty line
// it printed. Empty means it printed nothing usable, or could not run.
func (d *Detector) version(ctx context.Context, executable string, args []string, env []string, cwd string) string {
	out, _, err := d.run(ctx, executable, args, env, cwd, true)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// readiness runs the agent's own probe and reads its exit status through the
// record's answers. A status the record does not interpret, or a probe that
// could not run or did not finish, is unknown.
func (d *Detector) readiness(ctx context.Context, executable string, probe agentrecord.ReadinessProbe, env []string, cwd string) agentrecord.Readiness {
	_, exit, err := d.run(ctx, executable, probe.Args, env, cwd, false)
	if err != nil {
		return agentrecord.ReadinessUnknown
	}
	for _, answer := range probe.Answers {
		if answer.Exit == exit {
			return answer.Readiness
		}
	}
	return agentrecord.ReadinessUnknown
}

// run executes one probe under its own bound and reports the exit status of a
// process that ran. An error means the process did not run, or did not finish:
// its output and status are then not an answer.
// maxVersionOutput is a byte prefix, not an unbounded CLI transcript.
const maxVersionOutput = 4096

// prefixSink drains all writes but retains only the bounded prefix.
type prefixSink struct{ data []byte }

func (s *prefixSink) Write(p []byte) (int, error) {
	n := len(p)
	room := maxVersionOutput - len(s.data)
	if len(p) > room {
		p = p[:room]
	}
	s.data = append(s.data, p...)
	return n, nil
}

func (d *Detector) run(ctx context.Context, executable string, args []string, env []string, cwd string, capture bool) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, d.probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...) //nolint:gosec // resolved record command and record args
	cmd.Env, cmd.Dir = env, cwd
	cmd.Stderr = io.Discard
	var stdout prefixSink
	cmd.Stdout = io.Discard
	if capture {
		cmd.Stdout = &stdout
	}
	proc.InOwnGroup(cmd)
	cmd.Cancel = func() error { return proc.KillGroupImmediately(cmd) }
	// A wrapper may exit while a descendant retains its output pipes. WaitDelay
	// also starts at leader exit, when CommandContext no longer calls Cancel.
	cmd.WaitDelay = d.probeTimeout
	err := cmd.Run()
	// Discovery owns the whole group, including children of an exited wrapper.
	_ = proc.KillGroupImmediately(cmd)
	if ctx.Err() != nil {
		return "", -1, ctx.Err()
	}
	if err == nil {
		return string(stdout.data), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(stdout.data), exit.ExitCode(), nil
	}
	return "", -1, err
}
