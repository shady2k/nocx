package session

import (
	"io"
	"log/slog"
	"os"
	"sort"
	"syscall"

	"github.com/shady2k/nocx/internal/lifecyclechannel"
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
	// openPTY is internal/pty's constructor, held as a value so the failure
	// arms of Spawn can be driven without a real shell. Production wires
	// pty.NewLocal in NewLocalSpawner; nothing else may replace it, which is
	// why it is unexported and has no option or setter — a spawner that could
	// be told what to start over an API would be a second answer to the
	// question the doc comment above says only the composition root may ask.
	openPTY func(log.Logger, pty.Config) (localPTY, error)
}

// localPTY is everything Spawn and localProcess need from internal/pty, named
// as an interface so a test can supply a PTY whose Write fails — the one
// failure this function must survive and cannot provoke with a real terminal.
//
// It is deliberately larger than session.Process: Dir and SignalProcessGroup
// are what localProcess re-exports as Cwd and the ProcessGroupSignaller seam,
// and service.go reaches both by TYPE ASSERTION. An interface that omitted
// them would still compile and would silently stop the helper signalling a job
// on the host, so they are stated here where the compiler checks them.
type localPTY interface {
	Process
	Dir() string
	SignalProcessGroup(pgid int, sig syscall.Signal) error
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
	return &LocalSpawner{
		log:   log.NewSlogAdapter(logger),
		shell: shell,
		openPTY: func(l log.Logger, cfg pty.Config) (localPTY, error) {
			// Returned through the named nil rather than as one expression:
			// a (*pty.LocalPty)(nil) handed back as an interface is not nil,
			// and every failure arm below is keyed on the error, not on lp.
			lp, err := pty.NewLocal(l, cfg)
			if err != nil {
				return nil, err
			}
			return lp, nil
		},
	}
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
	var lifecycleParent, lifecycleChild *os.File
	var err error

	// release is the ONE unwind every failure arm below runs, and it is one
	// function rather than four copies because the copies drifted: the
	// bootstrap-write arm closed the parent end of the lifecycle socketpair
	// and returned with the child end still open, leaking a descriptor per
	// occurrence in a daemon that lives as long as the host session
	// (nocx-k6p18.28). Closing over the three variables rather than taking
	// them as parameters is what makes that impossible to repeat: an arm
	// cannot pass the wrong set, and a fifth arm gets the whole unwind by
	// writing one call.
	//
	// It is deliberately NOT launch.Cleanup, which the lines below compose to
	// close lifecycleChild after a SUCCESSFUL exec has duplicated it into the
	// shell. Cleanup closes the in-memory script's reader and leaves its
	// writer goroutine to finish; Abort closes the writer, waits for that
	// goroutine and then the reader. A spawn that failed has no shell to
	// deliver a script to, so Abort is the correct half of that pair, and the
	// socketpair is closed here explicitly the way the surviving arms already
	// closed it.
	release := func() {
		if launch.Abort != nil {
			launch.Abort()
		}
		if lifecycleParent != nil {
			_ = lifecycleParent.Close()
		}
		if lifecycleChild != nil {
			_ = lifecycleChild.Close()
		}
	}

	if req.SessionID != "" && len(shellArgs) == 0 {
		kind := shellintegration.LocalShellKind(shellPath)
		if kind == shellintegration.ShellBash || kind == shellintegration.ShellZsh {
			if req.Lifecycle != nil {
				lifecycleParent, lifecycleChild, err = lifecyclechannel.NewSocketPair()
				if err != nil {
					release()
					return nil, err
				}
			}
			opts := shellintegration.LaunchOptions{SessionID: req.SessionID, Enhanced: true}
			if req.Lifecycle != nil {
				opts.Lane = req.Lifecycle.Lane
				opts.Domain = req.Lifecycle.Domain
				opts.Epoch = req.Lifecycle.Epoch
				opts.Capability = req.Lifecycle.Capability
				opts.Recovery = req.Lifecycle.Recovery
				opts.LifecycleFD = 4
			}
			launch, err = shellintegration.LocalEnhancedLaunchInMemory(shellPath, kind, opts)
			if err != nil {
				release()
				return nil, err
			}
			if lifecycleChild != nil {
				launch.ExtraFiles = append(launch.ExtraFiles, lifecycleChild)
				previousCleanup := launch.Cleanup
				launch.Cleanup = func() {
					previousCleanup()
					_ = lifecycleChild.Close()
				}
			}
			cfg.Command = launch.Command
			cfg.Args = launch.Args
			cfg.Env = append(cfg.Env, launch.Env...)
			cfg.ExtraFiles = launch.ExtraFiles
		}
	}
	lp, err := s.openPTY(s.log, cfg)
	if err != nil {
		release()
		return nil, err
	}
	if len(launch.Bootstrap) > 0 {
		if _, err := lp.Write(launch.Bootstrap); err != nil {
			_ = lp.Close()
			release()
			return nil, err
		}
	}
	if launch.Cleanup != nil {
		launch.Cleanup()
	}
	return &localProcess{localPTY: lp, lifecycle: lifecycleParent}, nil
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
	localPTY
	lifecycle *os.File
}

func (p *localProcess) Lifecycle() io.ReadWriteCloser {
	if p.lifecycle == nil {
		return nil
	}
	return p.lifecycle
}

func (p *localProcess) Close() error {
	err := p.localPTY.Close()
	if p.lifecycle != nil {
		closeErr := p.lifecycle.Close()
		if err == nil {
			err = closeErr
		}
	}
	return err
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
