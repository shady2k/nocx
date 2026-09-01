package session

import (
	"log/slog"
	"sort"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/shellintegration"
)

// LocalSpawner starts the shell through internal/pty — the same package the
// coordinator's own local sessions go through, and deliberately not a second
// one. "How do you start a shell under a PTY on this OS" is already answered
// in this repository, complete with the environment scrubbing, the UTF-8
// locale and the login-shell resolution that answer took to get right; a
// helper-local reimplementation would be a second answer that agrees
// everywhere anybody looks.
//
// # The shell is chosen HERE, and never over the wire
//
// SpawnParams carries no command and no argv: host.Register refuses any op
// whose params carry a free-form []string (D3), and the point of that refusal
// is that a remote caller may not name a program. So the shell is a
// composition-root decision — internal/loginshell by default, an explicit one
// when a test or a future product decision names it — and it travels by
// construction rather than by request.
type LocalSpawner struct {
	log   log.Logger
	shell Shell
}

// Shell pins what a LocalSpawner starts. Its ZERO VALUE means "ask
// internal/loginshell", which is what production passes and what makes the
// login-shell resolution stay in one place.
//
// It is a constructor argument rather than a functional option because a
// variadic option that only tests ever pass is dead code by the one measure
// this repository actually gates on — and because "which shell" is not
// optional, it is the composition root's central decision. Passing it
// explicitly is what makes the alternative visible at the call site: a caller
// naming a shell here is a caller that decided, and no caller over the WIRE
// can decide it at all (D3).
type Shell struct {
	Path string
	Args []string
}

// NewLocalSpawner builds the spawner. Production passes a zero Shell.
func NewLocalSpawner(logger *slog.Logger, shell Shell) *LocalSpawner {
	return &LocalSpawner{log: log.NewSlogAdapter(logger), shell: shell}
}

// Spawn starts one shell under one PTY. The PTY is created with setsid, so the
// shell leads its own process group and the helper owns that group — which is
// what makes signalling a job on the host possible at all (D3).
func (s *LocalSpawner) Spawn(req SpawnRequest) (Process, error) {
	shellPath, shellArgs := s.shell.Path, s.shell.Args
	if shellPath == "" {
		shell := loginshell.New().Resolve()
		shellPath = shell.Path
		shellArgs = nil
	}

	cfg := pty.Config{
		Command: shellPath,
		Args:    shellArgs,
		Cwd:     req.Cwd,
		Env:     envSlice(req.Env),
		Cols:    req.Cols,
		Rows:    req.Rows,
	}
	var launch shellintegration.LocalLaunch
	var err error
	if req.SessionID != "" && len(shellArgs) == 0 {
		kind := shellintegration.LocalShellKind(shellPath)
		if kind == shellintegration.ShellBash || kind == shellintegration.ShellZsh {
			launch, err = shellintegration.LocalEnhancedLaunchInMemory(
				shellPath,
				kind,
				shellintegration.LaunchOptions{SessionID: req.SessionID, Enhanced: true},
			)
			if err != nil {
				return nil, err
			}
			cfg.Command = launch.Command
			cfg.Args = launch.Args
			cfg.Env = append(cfg.Env, launch.Env...)
			cfg.ExtraFiles = launch.ExtraFiles
		}
	}

	lp, err := pty.NewLocal(s.log, cfg)
	if err != nil {
		if launch.Abort != nil {
			launch.Abort()
		}
		return nil, err
	}
	if len(launch.Bootstrap) > 0 {
		if _, err := lp.Write(launch.Bootstrap); err != nil {
			if launch.Abort != nil {
				launch.Abort()
			}
			_ = lp.Close()
			return nil, err
		}
	}
	if launch.Cleanup != nil {
		launch.Cleanup()
	}
	return &localProcess{LocalPty: lp}, nil
}

// envSlice turns the wire's map into exec's slice, in a STABLE order. The wire
// shape is a map because a map cannot express a positional argument (so no
// caller can smuggle argv through it) and because a duplicate key is
// impossible rather than last-wins; sorting is what keeps the resulting
// environment reproducible rather than depending on Go's map iteration.
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// localProcess is internal/pty's LocalPty plus the two facts the launch record
// needs and the Pty interface does not carry: where the shell actually
// started, and which process group the helper owns for it.
type localProcess struct {
	*pty.LocalPty
}

// Cwd is the directory the shell was actually started in — the RESOLVED one.
// internal/pty owns that resolution (an empty request becomes the user's home,
// the way Terminal.app and iTerm do), so this asks it rather than repeating
// the rule.
func (p *localProcess) Cwd() string { return p.Dir() }

// ProcessGroup is the group the helper signals. It is pid by construction —
// the PTY is started with setsid, so the shell leads its own group — and the
// syscall is the CROSS-CHECK rather than the authority, which is why a failure
// falls back to the construction instead of failing the spawn. A shell that
// exists must not be refused because a bookkeeping call did not answer.
func (p *localProcess) ProcessGroup() int { return processGroupOf(p.Pid()) }
