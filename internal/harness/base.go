package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/shellenv"
)

const DefaultMaxLineBytes = 4 << 20

var ErrCancelled = errors.New("the turn was cancelled")

type TurnError struct{ Reason string }

func (e *TurnError) Error() string { return e.Reason }

// Protocol describes wire commands, never process or turn state.
type Protocol interface {
	Start(context.Context, TurnSpec) (Launch, error)
	Commands(TurnSpec) []Command
	Cancel() []Command
	Encode(id string, command Command) ([]byte, error)
	Decode(line []byte) (Decoded, error)
}
type Command struct {
	Name    string
	Payload any
}
type Response struct {
	ID    string
	Error string
}
type Decoded struct {
	Events   []Event
	Response *Response
}
type Launch struct {
	Argv []string
	Env  []string
	Dir  string
}
type Base struct {
	MaxLineBytes   int
	MaxStderrBytes int
	// ShutdownTimeout overrides the ten-second cancel/shutdown bound for tests.
	ShutdownTimeout time.Duration
	// IdleTimeout overrides the one-hour idle bound for tests.
	IdleTimeout time.Duration
}

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
// Their parent's reaping policy is outside this session process owner.
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
