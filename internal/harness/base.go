package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/proc"
	"github.com/shady2k/nocx/internal/shellenv"
)

// DefaultMaxLineBytes bounds one line of an agent's stdout. It is generous
// because a tool result may be large; it exists so a program that never sends
// a newline cannot exhaust the backend's memory.
const DefaultMaxLineBytes = 4 << 20

// ErrCancelled is Wait's error for a turn stopped by Cancel.
var ErrCancelled = errors.New("the turn was cancelled")

// TurnError is a turn that failed. Reason is the sentence the block shows.
type TurnError struct {
	Reason string
}

func (e *TurnError) Error() string { return e.Reason }

// Decoder turns one line of the agent's stdout into the events it states. An
// error means the connector cannot read the agent's protocol; the turn fails
// and the process is stopped rather than left running.
type Decoder func(line []byte) ([]Event, error)

// Launch is one agent process, as a connector describes it.
type Launch struct {
	// Argv is the program and its arguments. It is executed directly, with
	// no shell between nocx and the agent.
	Argv []string
	// Env is added to the login shell's environment. It carries only what
	// belongs to the agent; the turn's tool bridge environment is added to
	// the bridge child alone.
	Env []string
	// Dir is the agent's working directory; empty inherits the backend's.
	Dir string
	// Decode reads the agent's own protocol off stdout.
	Decode Decoder
	// CompleteOnEnd closes stdin after a terminal protocol response. RPC agents
	// that exit on stdin EOF use this; other connectors call CloseStdin themselves.
	CompleteOnEnd bool
	// FailureReason translates bounded stderr after an unsuccessful exit. An empty
	// result keeps the shared exit reason. It is never called on successful exit.
	FailureReason func(stderr string, exitErr error) string
}

// Base is the shared process ownership every connector uses: the agent runs in
// its own process group with stdin and stdout pipes and the login shell's
// environment, and the turn's lifetime is this type's.
type Base struct {
	// MaxLineBytes bounds one stdout line; zero means DefaultMaxLineBytes.
	MaxLineBytes int
	// MaxStderrBytes bounds captured diagnostics; zero uses 64 KiB.
	MaxStderrBytes int
	// ShutdownTimeout bounds graceful shutdown after stdin closes. Zero uses one second.
	ShutdownTimeout time.Duration
}

// Start runs one agent process and returns its turn.
func (b Base) Start(ctx context.Context, launch Launch) (*Process, error) {
	if len(launch.Argv) == 0 {
		return nil, errors.New("harness: the agent command is empty")
	}
	if launch.Decode == nil {
		return nil, errors.New("harness: the agent has no line decoder")
	}
	env, err := loginEnvironment(ctx)
	if err != nil {
		return nil, err
	}

	env = mergeEnvironment(env, launch.Env)
	executable, err := resolveExecutable(launch.Argv[0], env, launch.Dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, launch.Argv[1:]...) //nolint:gosec // the connector states the agent's argv
	cmd.Env = env
	cmd.WaitDelay = time.Second
	cmd.Dir = launch.Dir
	// The agent leads its own group, so Cancel and a leftover child are one
	// signal away and neither reaches nocx.
	proc.InOwnGroup(cmd)

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("harness: the agent's stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, fmt.Errorf("harness: the agent's stdout pipe: %w", err)
	}
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	maxStderr := b.MaxStderrBytes
	if maxStderr <= 0 {
		maxStderr = 64 << 10
	}
	stderr := &diagnostics{max: maxStderr}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("harness: the agent %s could not be started: %w", launch.Argv[0], err)
	}
	// The child holds the other ends now. Keeping ours would mean the agent
	// never sees EOF on stdin when this process dies, and the pipes would
	// never report the agent's own end.
	_ = stdinR.Close()
	_ = stdoutW.Close()

	maxLine := b.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	shutdownTimeout := b.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = time.Second
	}
	p := &Process{
		cmd:             cmd,
		stdin:           stdinW,
		stdout:          stdoutR,
		decode:          launch.Decode,
		maxLine:         maxLine,
		events:          make(chan Event, 64),
		done:            make(chan struct{}),
		procDone:        make(chan struct{}),
		stopped:         make(chan struct{}),
		completeOnEnd:   launch.CompleteOnEnd,
		failureReason:   launch.FailureReason,
		stderr:          stderr,
		shutdownTimeout: shutdownTimeout,
	}
	go p.reap()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Cancel(context.Background())
		case <-p.done:
		}
	}()
	go p.pump(ctx)
	return p, nil
}

// loginEnvironment is the environment a child of the person's login shell
// receives. It is the shared reader's job, not this package's: the login
// shell's rc files decide what a program launched from the desktop can find,
// and nocx's own environment is not a person's.
func loginEnvironment(ctx context.Context) ([]string, error) {
	shell := loginshell.New().Resolve()
	env, err := shellenv.Env(ctx, shellenv.Spec{
		Shell:   shell.Path,
		Env:     os.Environ(),
		Login:   true,
		Timeout: 10 * time.Second,
		MaxOut:  1 << 20,
	})
	if err != nil {
		return nil, fmt.Errorf("harness: the login shell's environment: %w", err)
	}
	return env, nil
}

// Process is the shared base's turn. It is concrete because a connector also
// needs the agent's stdin to speak its own protocol; the core holds only the
// Turn interface.
type Process struct {
	cmd     *exec.Cmd
	stdin   *os.File
	stdout  *os.File
	decode  Decoder
	maxLine int

	events          chan Event
	done            chan struct{}
	procDone        chan struct{}
	waitErr         error
	cleanupErr      error
	stopped         chan struct{}
	completeOnEnd   bool
	failureReason   func(string, error) string
	stderr          *diagnostics
	reaped          bool
	shutdownTimeout time.Duration
	shutdownOnce    sync.Once
	shutdownErr     error

	mu        sync.Mutex
	finished  bool
	cancelled bool

	cancelOnce sync.Once
	outcome    Outcome
	err        error
}

func (p *Process) Events() <-chan Event { return p.events }

// Stdin is the agent's stdin. A connector writes its agent's own protocol
// here; the shared base owns when the pipe is closed.
func (p *Process) Stdin() io.Writer { return p.stdin }

// CloseStdin requests graceful protocol shutdown, without cancelling the turn.
func (p *Process) CloseStdin() error {
	err := p.stdin.Close()
	p.shutdownOnce.Do(func() {
		go func() {
			timer := time.NewTimer(p.shutdownTimeout)
			defer timer.Stop()
			select {
			case <-p.procDone:
				return
			case <-timer.C:
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.reaped {
				p.shutdownErr = errors.New("the agent did not exit after stdin closed")
				_ = p.killLocked()
				_ = p.stdout.Close()
			}
		}()
	})
	return err
}

func (p *Process) Cancel(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return nil
	}
	p.cancelled = true
	p.cancelOnce.Do(func() { close(p.stopped) })
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	if p.reaped {
		return nil
	}
	return p.killLocked()
}

// killLocked is only called while the leader is still owned and unreaped.
func (p *Process) killLocked() error {
	if err := proc.KillGroupImmediately(p.cmd); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("harness: the agent's process group could not be stopped: %w", err)
	}
	return nil
}

// Wait's context belongs to this observer, not the running turn. Only the
// context passed to Start or Cancel stops the agent.
func (p *Process) Wait(ctx context.Context) (Outcome, error) {
	select {
	case <-p.done:
		return p.outcome, p.err
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	}
}

// reap observes exit without releasing the leader's PID. All group signals
// finish under mu before Wait can release that identity. Cancel uses the same
// lock and never signals once reaped is set.
func (p *Process) reap() {
	p.reapWith(observeExit, awaitGroupGone)
}

func (p *Process) reapWith(observe func(int) error, cleanup func(int) error) {
	observeErr := observe(p.cmd.Process.Pid)
	p.mu.Lock()
	var signalErr error
	// Observation infrastructure failure does not release our child. Stop it
	// before Wait can reap it, while Cancel is excluded by the same lock.
	// ECHILD alone means ownership is lost and the numeric identity is unsafe.
	if !errors.Is(observeErr, syscall.ECHILD) {
		signalErr = p.killLocked()
	}
	p.reaped = true
	p.mu.Unlock()
	p.waitErr = p.cmd.Wait()
	p.cleanupErr = errors.Join(observeErr, signalErr, cleanup(p.cmd.Process.Pid))
	if p.cleanupErr != nil {
		// A surviving writer must not hide the bounded cleanup failure from Wait.
		_ = p.stdout.Close()
	}
	close(p.procDone)
}

// pump reads the agent's stdout until it closes, then finalizes the turn. The
// terminal event is the last one forwarded: a stream that closes exactly once
// is the contract, and an event after the terminal one would be a second end.
func (p *Process) pump(ctx context.Context) {
	defer func() { _ = p.stdout.Close() }()
	var terminal *Event
	var protocolErr string

	reader := bufio.NewReaderSize(p.stdout, 64<<10)
readLoop:
	for {
		line, err := readBoundedLine(reader, p.maxLine)
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				protocolErr = fmt.Sprintf("the agent wrote a line longer than %d bytes", p.maxLine)
			} else if !errors.Is(err, io.EOF) {
				protocolErr = fmt.Sprintf("the agent's stdout could not be read: %v", err)
			}
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		events, err := p.decode(line)
		if err != nil {
			protocolErr = fmt.Sprintf("the agent sent a line this connector cannot read: %v", err)
			break
		}
		for _, ev := range events {
			if terminal != nil {
				log.From(ctx).Warn("agent event after the terminal one was dropped", "kind", ev.Kind)
				continue
			}
			select {
			case p.events <- ev:
			case <-p.stopped:
				break readLoop
			}

			if ev.Kind == EventEnd || ev.Kind == EventFailure {
				done := ev
				terminal = &done
				if p.completeOnEnd {
					_ = p.CloseStdin()
				}
			}
		}
	}

	_ = p.CloseStdin()
	// A line this side could not read is a turn this side must stop. A
	// stream that simply ended is not: the process is expected to exit, and
	// the reaper kills whatever it left once it has. Killing here would race
	// a normally exiting agent and turn its clean exit into a signal.
	if protocolErr != "" {
		p.mu.Lock()
		if !p.reaped {
			_ = p.killLocked()
		}
		p.mu.Unlock()
	}
	<-p.procDone
	_ = p.stdout.Close()
	p.finish(terminal, protocolErr)
}

// finish decides the turn's truth. Success is never read from one event: the
// terminal event is necessary and not sufficient, and the process's own exit
// is the other half.
func (p *Process) finish(terminal *Event, protocolErr string) {
	p.mu.Lock()
	cancelled := p.cancelled
	shutdownErr := p.shutdownErr
	p.mu.Unlock()

	switch {
	case p.cleanupErr != nil:
		p.err = &TurnError{Reason: "the agent's process group could not be cleaned up: " + p.cleanupErr.Error()}
		if terminal == nil {
			p.sendFailure(p.err.Error())
		}
	case cancelled:
		if terminal == nil {
			p.sendFailure("cancelled")
		}
		p.err = ErrCancelled
	case shutdownErr != nil:
		p.err = &TurnError{Reason: fmt.Sprint(shutdownErr)}
		if terminal == nil {
			p.sendFailure(p.err.Error())
		}
	case protocolErr != "":
		if terminal == nil {
			p.sendFailure(protocolErr)
		}
		p.err = &TurnError{Reason: protocolErr}
	case terminal != nil && terminal.Kind == EventFailure:
		p.err = &TurnError{Reason: terminal.Failure.Reason}
	case p.waitErr != nil:
		reason := exitReason(p.waitErr)
		if p.failureReason != nil {
			if diagnostic := p.failureReason(p.stderr.String(), p.waitErr); diagnostic != "" {
				reason = diagnostic
			}
		}
		if terminal == nil {
			p.sendFailure(reason)
		}
		p.err = &TurnError{Reason: reason}
	case terminal == nil:
		reason := "the agent ended without an end event"
		p.sendFailure(reason)
		p.err = &TurnError{Reason: reason}
	case terminal.Kind == EventEnd:
		p.outcome = Outcome{Model: terminal.End.Model, Usage: terminal.End.Usage}
	}
	p.mu.Lock()
	if p.cancelled && p.cleanupErr == nil {
		p.outcome = Outcome{}
		p.err = ErrCancelled
	}
	p.finished = true
	close(p.events)
	close(p.done)
	p.mu.Unlock()
}

func exitReason(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.Exited() {
			return fmt.Sprintf("the agent exited with status %d", exit.ExitCode())
		}
		return fmt.Sprintf("the agent was stopped: %v", err)
	}
	return fmt.Sprintf("the agent ended: %v", err)
}

var errLineTooLong = errors.New("harness: the agent's stdout line exceeds the bound")

// readBoundedLine reads one newline-terminated line without letting a program
// that never sends one grow the buffer without bound.
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max {
			return nil, errLineTooLong
		}
		if err == nil {
			return bytes.TrimSuffix(line, []byte("\n")), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// sendFailure cannot keep cancellation alive when the consumer disconnected.
func (p *Process) sendFailure(reason string) {
	ev := Event{Kind: EventFailure, Failure: &Failure{Reason: reason}}
	select {
	case p.events <- ev:
		return
	default:
	}
	select {
	case <-p.stopped:
		// The disconnected consumer cannot free space. Discard one queued delta,
		// retaining the sole terminal failure as the final event.
		select {
		case <-p.events:
		default:
		}
		p.events <- ev
	case p.events <- ev:
	}
}

type diagnostics struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func (d *diagnostics) Write(raw []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := min(len(raw), d.max-len(d.data))
	d.data = append(d.data, raw[:n]...)
	return len(raw), nil
}
func (d *diagnostics) String() string { d.mu.Lock(); defer d.mu.Unlock(); return string(d.data) }

// Zombie descendants have already terminated and cannot hold resources or run.
// Their parent's reaping policy is outside this per-turn process owner.
func awaitGroupGone(pgid int) error {
	return awaitGroupGoneWith(pgid, time.Second, groupRunning)
}

func awaitGroupGoneWith(pgid int, budget time.Duration, running func(int) (bool, error)) error {
	deadline := time.Now().Add(budget)
	for {
		alive, err := running(pgid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("process group still has running members")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
