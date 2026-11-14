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
	"bytes"
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
	// envDumpMarker separates whatever an rc file printed on its way past
	// from the environment dump that follows it, exactly as
	// internal/git/local's resolver does (shellenv.go). Everything before the
	// marker is discarded.
	envDumpMarker = "\n__nocx_agent_path__\n"
)

// Shell answers the one question detection asks a shell: where is this
// person's login-shell PATH. It is an interface because the answer is a
// subprocess against a machine's own configuration, and a test supplies the
// PATH it wants to ask about rather than hoping the machine running the suite
// is configured the right way.
type Shell interface {
	Path(ctx context.Context) (string, error)
}

// LoginShell is the production Shell: the person's login shell, run once.
//
// It is run with `-l -i` — a login shell, and interactive — because the
// question is the login shell's PATH and because zsh, the shipping platform's
// default, keeps its PATH in .zshrc, which a non-interactive shell never
// reads. It then EXECS `env -0`, so what is parsed is the environment a child
// of that shell would actually receive, NUL-delimited and with no quoting to
// undo. internal/git/local resolves the same environment for git; this is a
// second run of the same incantation because that resolver is private to the
// git factory, whose cache lives as long as one repository handle, and
// detection needs its own lifetime and its own refresh.
type LoginShell struct{}

// Path runs the login shell and returns the PATH it resolved.
func (LoginShell) Path(ctx context.Context) (string, error) {
	shell := loginshell.New().Resolve().Path
	ctx, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()
	// Absolute /usr/bin/env for the reason git's resolver gives: a broken
	// PATH is one of the states this must still resolve through.
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", "printf '"+envDumpMarker+"'; exec /usr/bin/env -0") //nolint:gosec // shell is from the account record or a detected path
	cmd.Env = os.Environ()
	cmd.WaitDelay = time.Second
	var out cappedBuffer
	out.max = maxEnvDump
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("the login shell %s did not answer within %s", shell, loginShellTimeout)
		}
		return "", fmt.Errorf("the login shell %s exited: %w", shell, err)
	}
	return pathFromEnvDump(out.String())
}

// pathFromEnvDump reads PATH out of the NUL-delimited dump `env -0` printed.
// Anything before the marker is an rc file's greeting and is not parsed.
func pathFromEnvDump(dump string) (string, error) {
	i := strings.Index(dump, envDumpMarker)
	if i < 0 {
		return "", errors.New("the login shell printed no environment")
	}
	for _, record := range strings.Split(dump[i+len(envDumpMarker):], "\x00") {
		if path, ok := strings.CutPrefix(record, "PATH="); ok && path != "" {
			return path, nil
		}
	}
	return "", errors.New("the login shell resolved an environment with no PATH")
}

// cappedBuffer keeps the first max bytes written and silently drops the rest.
// It always reports a full write, so a chatty process is never blocked by the
// cap and never has to be killed for it.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

// Detector runs detection passes and caches the last answer.
type Detector struct {
	mu     sync.Mutex
	shell  Shell
	cached []agentrecord.Detection
	// probeTimeout is a field so a test can make a hung probe cheap to
	// observe; production leaves it at probeTimeout.
	probeTimeout time.Duration
}

// New builds a Detector over a login shell. It is wired at the composition
// root, like every other module's seam.
func New(shell Shell) *Detector {
	return &Detector{shell: shell, probeTimeout: probeTimeout}
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached != nil && !refresh {
		return append([]agentrecord.Detection(nil), d.cached...), nil
	}

	path, err := d.shell.Path(ctx)
	if err != nil {
		return nil, fmt.Errorf("agentdetect: the login shell's PATH could not be read: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("agentdetect: the login shell reported an empty PATH")
	}
	// The probes run with the login shell's PATH and the backend's remaining
	// environment: a CLI whose own shebang is `env node` finds node where the
	// person's shell would, and nothing else is taken from a shell run for a
	// different question.
	env := withPath(os.Environ(), path)
	// A relative PATH entry is relative to where the login shell started,
	// which is the person's home directory. It is only consulted for such an
	// entry; an absolute one never uses it.
	cwd, _ := os.UserHomeDir()

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
			row.Version = d.version(ctx, executable.Path, agent.VersionArgs, env)
			if agent.Readiness != nil {
				row.Readiness = d.readiness(ctx, executable.Path, *agent.Readiness, env)
			}
		}
		rows = append(rows, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.cached = rows
	return append([]agentrecord.Detection(nil), rows...), nil
}

// version runs the command's version flag and returns the first non-empty line
// it printed. Empty means it printed nothing usable, or could not run.
func (d *Detector) version(ctx context.Context, executable string, args []string, env []string) string {
	out, _, err := d.run(ctx, executable, args, env)
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
func (d *Detector) readiness(ctx context.Context, executable string, probe agentrecord.ReadinessProbe, env []string) agentrecord.Readiness {
	_, exit, err := d.run(ctx, executable, probe.Args, env)
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
func (d *Detector) run(ctx context.Context, executable string, args []string, env []string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, d.probeTimeout)
	defer cancel()
	argv := append([]string{executable}, args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // executable is the resolved record command, args are the record's
	cmd.Env = env
	cmd.Stderr = io.Discard
	// A probe that leaves a child holding its stdout must not hold the pass
	// open after the probe itself was killed.
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	switch {
	case err == nil:
		return stdout.String(), 0, nil
	case ctx.Err() != nil:
		return "", -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), exit.ExitCode(), nil
	}
	return "", -1, err
}

// withPath replaces PATH in env, rather than appending a second copy: two PATH
// entries are one environment nobody can predict, because which one a child
// sees is the platform's choice.
func withPath(env []string, path string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PATH="+path)
}
