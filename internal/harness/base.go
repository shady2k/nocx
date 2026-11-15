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

// ErrTurnNotRunning refuses a person's message that has no turn to land in —
// the turn ended, was cancelled, or has not reached the agent yet.
var ErrTurnNotRunning = errors.New("harness: the turn is not running")

// ErrNotSteerable refuses a person's message when the agent's own protocol has
// no steering: the message cannot be delivered into a turn already under way.
var ErrNotSteerable = errors.New("harness: this agent does not take a message while it answers")

type TurnError struct{ Reason string }

func (e *TurnError) Error() string { return e.Reason }

// Protocol describes process launch and creates isolated wire state for each
// process incarnation. The actor owns command execution and response matching.
type Protocol interface {
	Start(context.Context, TurnSpec) (Launch, error)
	NewCodec(SessionInfo, Launch) Codec
}

// SessionInfo identifies one process incarnation within a conversation.
type SessionInfo struct {
	ConversationID string
	Generation     uint64
}

// Codec belongs to one process incarnation, never to multiple sessions.
// Setup runs once. BeginTurn resets turn-local decoding state and supplies a
// turn plan beginning with a barrier. Each step advances only after its matched
// response. The barrier response must follow all output from the previous turn.
type Codec interface {
	Setup() []Step
	BeginTurn(TurnSpec) []Step
	CancelSequence() []Command
	// SteerCommand is the command that carries a person's message into the
	// turn already running. ok is false when the agent's protocol has no
	// steering, and the actor refuses the message rather than writing a
	// command the agent cannot read (ADR-anntnxdj decision 4).
	SteerCommand(text string) (Command, bool)
	Encode(id string, command Command) ([]byte, error)
	Decode(line []byte) (Decoded, error)
}

// Step is a response-dependent command. Check may inspect the matched response
// payload and update codec state needed by later commands. BeginTurn's first
// step must set Barrier: its matched response fences output from the previous
// turn before any new turn events are admitted. Only the final step sets Prompt.
type Step struct {
	// Model reports the model resolved by a successful response check.
	Model func() string

	// Steer marks the command a person's message became. Its matched response
	// is the agent's acceptance of that message, which is what the person's
	// Steer call waits for.
	Steer bool

	Command Command
	Check   func(any) error
	Barrier bool
	Prompt  bool
}

type Command struct {
	Name    string
	Payload any
}
type Response struct {
	ID      string
	Error   string
	Payload any
}
type Decoded struct {
	// Outcome is the latest accumulated turn facts, including completed model calls.
	// The actor retains them if it must publish a terminal without the agent's terminal.
	Outcome  *Outcome
	Events   []Event
	Response *Response
}
type Launch struct {
	// PromptPath names the private system-prompt file owned by this launch.
	// Connectors that use a prompt file pass this exact path to their codec.
	PromptPath string
	Argv       []string
	Env        []string
	Dir        string
	// Cleanup releases launch resources after spawn failure or process teardown.
	// The actor calls it once; it must tolerate resources already consumed by the child.
	Cleanup func() error
}
type Base struct {
	// Tools creates a tool endpoint owned by one process incarnation.
	Tools ToolSessionFactory
	// Observe receives actor transitions synchronously; callbacks must not block.
	Observe        func(Transition)
	MaxLineBytes   int
	MaxStderrBytes int
	// ShutdownTimeout overrides the ten-second cancel/shutdown bound for tests.
	ShutdownTimeout time.Duration
	// IdleTimeout overrides the one-hour idle bound for tests.
	IdleTimeout time.Duration
	// Environment resolves the environment an agent starts in. Empty means the
	// person's login shell, read on every start (the agent sees what their
	// terminal sees); tests that start thousands of agents supply their own.
	Environment func(context.Context) ([]string, error)
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
