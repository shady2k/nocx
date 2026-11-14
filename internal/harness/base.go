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
}

// Base is the shared process ownership every connector uses: the agent runs in
// its own process group with stdin and stdout pipes and the login shell's
// environment, and the turn's lifetime is this type's.
type Base struct {
	// MaxLineBytes bounds one stdout line; zero means DefaultMaxLineBytes.
	MaxLineBytes int
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

	cmd := exec.Command(launch.Argv[0], launch.Argv[1:]...) //nolint:gosec // the connector states the agent's argv
	cmd.Env = append(env, launch.Env...)
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
	cmd.Stderr = io.Discard

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
	p := &Process{
		cmd:      cmd,
		stdin:    stdinW,
		stdout:   stdoutR,
		decode:   launch.Decode,
		maxLine:  maxLine,
		events:   make(chan Event, 64),
		done:     make(chan struct{}),
		procDone: make(chan struct{}),
	}
	go p.reap()
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

	events   chan Event
	done     chan struct{}
	procDone chan struct{}
	waitErr  error

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

func (p *Process) Cancel(ctx context.Context) error {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return nil
	}
	p.cancelled = true
	p.mu.Unlock()

	var cancelErr error
	p.cancelOnce.Do(func() {
		if err := proc.KillGroupImmediately(p.cmd); err != nil && !errors.Is(err, syscall.ESRCH) {
			cancelErr = fmt.Errorf("harness: the agent's process group could not be stopped: %w", err)
		}
		// Closing stdin is the polite half: a program that watches it leaves
		// even if a signal is late. The group kill is the one that must hold.
		_ = p.stdin.Close()
	})
	return cancelErr
}

func (p *Process) Wait(ctx context.Context) (Outcome, error) {
	select {
	case <-p.done:
		return p.outcome, p.err
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	}
}

// reap owns the one Wait on the process. When it returns, the leader is gone;
// anything the agent left in its group is killed here, because the group id
// stays allocated while a member lives and the leader's pid cannot be reused
// while it names that group.
func (p *Process) reap() {
	p.waitErr = p.cmd.Wait()
	close(p.procDone)
	_ = proc.KillGroupImmediately(p.cmd)
}

// pump reads the agent's stdout until it closes, then finalizes the turn. The
// terminal event is the last one forwarded: a stream that closes exactly once
// is the contract, and an event after the terminal one would be a second end.
func (p *Process) pump(ctx context.Context) {
	var terminal *Event
	var protocolErr string

	reader := bufio.NewReaderSize(p.stdout, 64<<10)
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
			p.events <- ev
			if ev.Kind == EventEnd || ev.Kind == EventFailure {
				done := ev
				terminal = &done
			}
		}
	}

	_ = p.stdin.Close()
	// A line this side could not read is a turn this side must stop. A
	// stream that simply ended is not: the process is expected to exit, and
	// the reaper kills whatever it left once it has. Killing here would race
	// a normally exiting agent and turn its clean exit into a signal.
	if protocolErr != "" {
		_ = proc.KillGroupImmediately(p.cmd)
	}
	<-p.procDone
	p.awaitGroupGone()
	p.finish(terminal, protocolErr)
}

// finish decides the turn's truth. Success is never read from one event: the
// terminal event is necessary and not sufficient, and the process's own exit
// is the other half.
func (p *Process) finish(terminal *Event, protocolErr string) {
	p.mu.Lock()
	cancelled := p.cancelled
	p.finished = true
	p.mu.Unlock()

	switch {
	case cancelled:
		if terminal == nil {
			p.events <- Event{Kind: EventFailure, Failure: &Failure{Reason: "cancelled"}}
		}
		p.err = ErrCancelled
	case protocolErr != "":
		if terminal == nil {
			p.events <- Event{Kind: EventFailure, Failure: &Failure{Reason: protocolErr}}
		}
		p.err = &TurnError{Reason: protocolErr}
	case terminal != nil && terminal.Kind == EventFailure:
		p.err = &TurnError{Reason: terminal.Failure.Reason}
	case p.waitErr != nil:
		reason := exitReason(p.waitErr)
		if terminal == nil {
			p.events <- Event{Kind: EventFailure, Failure: &Failure{Reason: reason}}
		}
		p.err = &TurnError{Reason: reason}
	case terminal == nil:
		reason := "the agent ended without an end event"
		p.events <- Event{Kind: EventFailure, Failure: &Failure{Reason: reason}}
		p.err = &TurnError{Reason: reason}
	case terminal.Kind == EventEnd:
		p.outcome = Outcome{Model: terminal.End.Model, Usage: terminal.End.Usage}
	}
	close(p.events)
	close(p.done)
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

// awaitGroupGone waits until no process is left in the agent's group. SIGKILL
// was already sent; the loop observes the kernel's answer rather than sleeping
// for a duration.
func (p *Process) awaitGroupGone() {
	if p.cmd.Process == nil {
		return
	}
	pgid := p.cmd.Process.Pid
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}
