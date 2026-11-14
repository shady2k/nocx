package local

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/shellenv"
)

// The resolved environment (spec D6): git's subprocesses — notably the
// pre-commit hook — run with an environment resolved once from the user's
// shell, not with the backend's own. A GUI-launched app inherits a bare
// launchd PATH from os.Environ(), so a commit from the panel would run its
// hook with no way to find go, node or bd, and fail where the terminal
// succeeds. The pty's shell starts interactive from
// scrubLauncherSession(os.Environ()) plus terminal vars and reads the rc
// files; the resolved environment is the closest a subprocess can get
// without a shell of its own, and the two are named in the product.
//
// The guarantee is deliberately narrow: an environment resolved once at
// backend start cannot contain anything created inside a tab after it
// started — a direnv, a virtualenv, an export the user typed — and AD-6
// forbids learning it from the stream. The panel says so, and a hook that
// passes in the terminal but fails here is an explicable difference rather
// than a mystery.
//
// Resolution is off Open's critical path (nocx-6pz0): the factory resolves
// once, in the background from construction, and Open reads the settled
// answer, so opening a repository costs what git costs and a hung rc file
// never holds the panel. Only the commit path blocks on resolution, joining
// the shared attempt or retrying a remembered failure after a cooldown, so
// D6's guarantee — the commit runs with the resolved environment when one is
// obtainable — still holds. The invocation is unchanged (`-i`, 5 s default),
// now stated once in internal/shellenv and shared with internal/agentdetect:
// it is the only non-pty way to read the rc files the way the pty's shell
// reads them, and the cost is now paid once in the background, not on every
// open.
type envCache struct {
	mu       sync.Mutex
	shell    string
	timeout  time.Duration
	maxOut   int64
	cooldown time.Duration // pause after a failed attempt before retrying
	env      []string
	state    envState  // envUnknown, envResolved or envDegraded
	reason   string    // why degraded; the panel's text
	lastTry  time.Time // when the last attempt settled; zero: never attempted
	inflight bool
	done     chan struct{} // non-nil while an attempt runs; closed when it settles
}

// envState is the resolver's own state machine, one step richer than the
// wire's: unknown is the pre-attempt state the wire reports conservatively
// as degraded, never as resolved.
type envState int

const (
	envUnknown envState = iota
	envResolved
	envDegraded
)

// envRetryCooldown is the pause after a failed resolution attempt before the
// next caller may retry. One failure is plausibly transient (a hung rc file,
// a missing shell) and deserves a second chance; a permanently broken shell
// must not turn every commit into a timeout, and a cooldown is the bound
// that says so. Chosen well above the resolution deadline so a machine whose
// shell always hangs retries at most a handful of times an hour, and well
// below any realistic poll cadence so a recovery is picked up promptly.
const envRetryCooldown = 30 * time.Second

// newEnvCache builds the resolver: shell is the binary to interrogate
// ("" — detect, like the pty does), timeout and maxOut bound one attempt.
// A success is cached; a failure is remembered for the cooldown and retried
// after it, so the cache resolves at most once per cooldown window.
func newEnvCache(shell string, timeout time.Duration, maxOut int64) *envCache {
	if shell == "" {
		shell = detectShell()
	}
	// The 5 s default is deliberate (nocx-6pz0): it no longer holds a UI —
	// resolution runs in the background and only a commit joins it — and a
	// shorter bound would degrade slow-but-healthy shells (a large rc, a
	// network mount), weakening D6's "when one is obtainable".
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if maxOut <= 0 {
		maxOut = 256 << 10
	}
	return &envCache{shell: shell, timeout: timeout, maxOut: maxOut, cooldown: envRetryCooldown}
}

// resolve returns the cached environment, resolving it on demand. A completed
// resolution is cached for the process lifetime; a failed one is remembered
// for the cooldown and retried after it, so a transient failure (a hung rc
// file, a missing shell) degrades some opens and recovers — it neither
// re-attempts on every call nor poisons the panel for the process lifetime.
// Attempts are single-flight: a caller that arrives while one runs joins it
// instead of starting a second shell.
func (c *envCache) resolve(ctx context.Context) (env []string, state git.EnvState, reason string) {
	for {
		c.mu.Lock()
		if c.state == envResolved {
			env, state, reason = c.env, git.EnvResolved, ""
			c.mu.Unlock()
			return
		}
		if c.state == envDegraded && time.Since(c.lastTry) < c.cooldown {
			env, state, reason = nil, git.EnvDegraded, c.reason
			c.mu.Unlock()
			return
		}
		if c.inflight {
			done := c.done
			c.mu.Unlock()
			select {
			case <-done:
				continue // the attempt settled; re-read the answer
			case <-ctx.Done():
				return nil, git.EnvDegraded, ctx.Err().Error()
			}
		}
		c.inflight = true
		c.done = make(chan struct{})
		c.mu.Unlock()

		env, err := resolveShellEnv(ctx, c.shell, c.timeout, c.maxOut)
		c.mu.Lock()
		c.lastTry = time.Now()
		c.inflight = false
		close(c.done)
		if err != nil {
			c.state, c.reason = envDegraded, err.Error()
			c.mu.Unlock()
			return nil, git.EnvDegraded, err.Error()
		}
		c.env, c.state, c.reason = env, envResolved, ""
		c.mu.Unlock()
		return env, git.EnvResolved, ""
	}
}

// known returns the current answer without attempting or waiting: the
// resolved environment, a remembered failure, or — before the background
// attempt settles — a conservative degraded. Open uses this: the repository
// answer must not wait on the resolution (nocx-6pz0), and the panel must
// never be shown a "resolved" the resolution has not earned.
func (c *envCache) known() (env []string, state git.EnvState, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case envResolved:
		return c.env, git.EnvResolved, ""
	case envDegraded:
		return nil, git.EnvDegraded, c.reason
	default:
		return nil, git.EnvDegraded, "the shell environment has not been resolved yet; the first commit will wait for it"
	}
}

// waitSettled blocks until no resolution attempt is in flight. Factory.Stop
// cancels the background attempt and calls this so no resolution child can
// outlive the process.
func (c *envCache) waitSettled() {
	for {
		c.mu.Lock()
		if !c.inflight {
			c.mu.Unlock()
			return
		}
		done := c.done
		c.mu.Unlock()
		<-done
	}
}

// degradedError is a plain reason carrier; the distinction the product needs
// is the EnvState, and the reason is the panel's text.
type degradedError struct{ reason string }

func (e *degradedError) Error() string { return e.reason }

// resolveShellEnv asks internal/shellenv for the environment the user's shell
// gives a child of it. The incantation — the marker, `env -0`, the bound, the
// cap and the parse — lives there because internal/agentdetect asks the same
// question for Settings, and two copies of it would agree everywhere anyone
// looked and disagree in the one place that mattered (ADR-anntnxdj decision 6).
//
// The mode is this caller's, and it is the interactive shell rather than the
// login shell: the pty starts `shell -i`, so `-i` is what makes the shell read
// the rc files the way the pane's shell does (nocx-6pz0). Detection asks for
// `-l -i` because its question is the login shell's PATH; D6's guarantee is
// the environment the pane's shell gives, which is this one.
//
// The shell is a process this adapter starts, and it ran under the
// git-process operation while the incantation lived here; the scope keeps the
// catalogue's coverage of it unchanged (ADR-8eej3e66).
func resolveShellEnv(ctx context.Context, shell string, timeout time.Duration, maxOut int64) ([]string, error) {
	scope := catalog.BeginInternal(ctx, catalog.OpInternalGitProcess)
	defer scope.Complete()
	env, err := shellenv.Env(ctx, shellenv.Spec{
		Shell: shell,
		// The shell starts from the same scrubbed launcher environment plus
		// terminal vars as the pty's shells (pty_local.go:132), so the
		// resolved environment and the tab's agree about everything except
		// what the tab created itself. The scrub list and the locale gap-fill
		// are copied from internal/pty rather than imported: they are that
		// package's private vocabulary, and the comment naming this copy is
		// what keeps the two in step.
		Env:     withUTF8Locale(append(scrubLauncherSession(os.Environ()), "TERM=xterm-256color", "COLORTERM=truecolor")),
		Login:   false,
		Timeout: timeout,
		MaxOut:  maxOut,
	})
	if err != nil {
		return nil, &degradedError{reason: err.Error()}
	}
	return env, nil
}

// detectShell asks the one owner of "which shell is this user's login shell"
// (internal/loginshell) rather than deriving it here.
//
// It used to be a copy of the pty's own derivation, "kept in step by a
// comment", and the two agreed everywhere anyone looked. Where they had to
// agree and could not was the launch shape this resolver exists for: a
// Dock-launched app inherits launchd's environment, so $SHELL — which both
// copies asked first — is absent or stale exactly then, and both fell through
// to bash on a platform whose users run zsh. D6's guarantee is that a commit
// runs with the environment the user's shell would give it; reading the rc
// files of a shell the user does not use is not that environment, it is a
// third one (nocx-wwz0).
func detectShell() string {
	return loginshell.New().Resolve().Path
}

// launcherSessionVars mirrors internal/pty/pty_local.go's list, kept in step
// by this comment: the pty scrubs these from its shells' environment so a
// coding agent's session markers do not leak into spawned programs, and the
// resolved environment must match what the pty's shell sees — a pre-commit
// hook that invokes the same agent would otherwise inherit the same markers.
var launcherSessionVars = []string{
	"CLAUDECODE=",
	"CLAUDE_CODE_ENTRYPOINT=",
	"CLAUDE_CODE_EXECPATH=",
	"CLAUDE_CODE_SESSION_ID=",
	"CLAUDE_CODE_CHILD_SESSION=",
	"CLAUDE_PID=",
	"CLAUDE_EFFORT=",
	"NO_COLOR=",
	"TERM=",
}

func scrubLauncherSession(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, prefix := range launcherSessionVars {
			if strings.HasPrefix(kv, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// localeVars are checked in POSIX precedence order, the same list the pty
// uses; the gap-fill keeps git's output (and a hook's) on a UTF-8 locale
// when the launcher environment declares none.
var localeVars = []string{"LC_ALL=", "LC_CTYPE=", "LANG="}

func withUTF8Locale(env []string) []string {
	for _, kv := range env {
		for _, prefix := range localeVars {
			if strings.HasPrefix(kv, prefix) {
				return env
			}
		}
	}
	return append(env, "LANG=en_US.UTF-8")
}
