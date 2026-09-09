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
	// agentToolSocketPath is THIS backend's tool endpoint socket, if it is
	// running one — a fact about this daemon's whole life (set once at
	// NewLocalSpawner, from the composition root, never per-request) and
	// never re-derived here: internal/toolendpoint is the one owner of the
	// socket's file name (AD-8), and this field is simply what the
	// composition root read off Endpoint.SocketPath(). Empty means no
	// endpoint was started (nocx-2tesu's soft degrade): every enhanced launch below
	// renders no NOCX_TOOL_SOCKET at all, and the shell's own refusal text
	// is what a user sees, rather than a pane pointed at an empty path.
	agentToolSocketPath string
	// agentHelperPath is the executable a pane's shell should exec to reach
	// this generation's MCP adapter. It is a path and never a bare name: the
	// wrapper's own fallback is `${NOCX_AGENT_HELPER_PATH:-nocx-helper}`, and
	// nothing in this product puts a `nocx-helper` on PATH — `make helpers`
	// writes gzipped per-platform artifacts for deployment to a remote host.
	// Unset, that fallback stands and an agent's MCP server fails to start
	// with every other fact about it correct (nocx-o36tr).
	//
	// The DAEMON'S OWN executable, so the pane execs the generation that owns
	// it. Nothing else in this process knows a better answer, and a path
	// handed down from the coordinator could name a different generation than
	// the one that forked the shell.
	agentHelperPath string
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

// NewLocalSpawner builds the spawner. Production passes a zero Shell and,
// when this daemon's process carried NOCX_TOOL_SOCKET at its own start
// (cmd/nocx-helper's composition, inherited from the coordinator that
// spawned it), that path as agentToolSocketPath — see the field's doc for
// why it is a constructor argument rather than a per-Spawn one.
func NewLocalSpawner(logger *slog.Logger, shell Shell, agentToolSocketPath, agentHelperPath string) *LocalSpawner {
	return &LocalSpawner{
		log:                 log.NewSlogAdapter(logger),
		shell:               shell,
		agentToolSocketPath: agentToolSocketPath,
		agentHelperPath:     agentHelperPath,
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

	// THE LAUNCH DECISION, SAID BEFORE IT IS ACTED ON (nocx-n14oo.2).
	//
	// Everything about whether a pane can ever integrate is decided in the
	// next few lines, and none of it was written down. A pane that takes the
	// plain arm gets no capability and no lifecycle, so a caller that asked
	// for one waits out its whole hello budget and learns only that the
	// channel was lost — in another process, ten seconds later, with nothing
	// naming the shell or the tier that made it inevitable.
	//
	// Note what the enhanced arm requires: a session id, no explicit shell
	// args, and a shell LocalShellKind recognises as bash or zsh. Anything
	// else takes the plain arm DELIBERATELY — the POSIX tier has no local
	// launch semantics yet and ShellUnknown means "start it, integrate
	// nothing, and say so" rather than "substitute bash" (nocx-k28e,
	// shellintegration.LocalShellKind). The saying-so is this line: it was the
	// half that did not exist.
	launchKind := shellintegration.LocalShellKind(shellPath)
	enhanced := req.SessionID != "" && len(shellArgs) == 0 &&
		(launchKind == shellintegration.ShellBash || launchKind == shellintegration.ShellZsh)
	s.log.Info("pane launch decided",
		"session", req.SessionID,
		"shell", shellPath,
		"shell_kind", string(launchKind),
		"enhanced", enhanced,
		"explicit_shell_args", len(shellArgs),
		"lifecycle_requested", req.Lifecycle != nil)

	if req.SessionID != "" && len(shellArgs) == 0 {
		kind := launchKind
		if kind == shellintegration.ShellBash || kind == shellintegration.ShellZsh {
			if req.Lifecycle != nil {
				lifecycleParent, lifecycleChild, err = lifecyclechannel.NewSocketPair()
				if err != nil {
					release()
					return nil, err
				}
			}
			opts := shellintegration.LaunchOptions{
				SessionID:           req.SessionID,
				Enhanced:            true,
				AgentToolSocketPath: s.agentToolSocketPath,
				AgentHelperPath:     s.agentHelperPath,
			}
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
				s.log.Error("pane launch: the enhanced tier could not be built",
					"session", req.SessionID, "shell", shellPath, "shell_kind", string(kind), "error", err)
				release()
				return nil, err
			}
			// The argv SHAPE and not its contents: the capability rides in
			// the script text these arguments point at, and printing it would
			// put a bearer token in a log file.
			s.log.Info("pane launch: the enhanced tier is built",
				"session", req.SessionID, "shell", launch.Command, "argv", len(launch.Args),
				"extra_files", len(launch.ExtraFiles), "bootstrap_bytes", len(launch.Bootstrap),
				"lifecycle_fd", opts.LifecycleFD, "lane", opts.Lane, "epoch", opts.Epoch)
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
		s.log.Error("pane launch: the pty could not be opened",
			"session", req.SessionID, "shell", cfg.Command, "error", err)
		release()
		return nil, err
	}
	if len(launch.Bootstrap) > 0 {
		// The bootstrap is a LINE INTO THE TERMINAL — `. /dev/fd/3` for the
		// tiers that cannot take a descriptor as an rcfile — so it is the one
		// thing here that shares a channel with the user's own input.
		if _, err := lp.Write(launch.Bootstrap); err != nil {
			s.log.Error("pane launch: the bootstrap line could not be written",
				"session", req.SessionID, "bytes", len(launch.Bootstrap), "error", err)
			_ = lp.Close()
			release()
			return nil, err
		}
		s.log.Debug("pane launch: the bootstrap line is written",
			"session", req.SessionID, "bytes", len(launch.Bootstrap))
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
