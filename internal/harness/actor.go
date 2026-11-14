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
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/proc"
)

// TurnActivity is driven by the endpoint. A receipt belongs to this exact turn,
// not whichever turn happens to run when its completion arrives.
type TurnActivity interface {
	AcceptCall(context.Context) (func(), error)
	Approval(context.Context) (func(), error)
}

type SessionManager struct {
	mu       sync.Mutex
	base     Base
	protocol Protocol
	sessions map[string]*session
	closed   bool
}

func NewSessionManager(base Base, protocol Protocol) *SessionManager {
	return &SessionManager{base: base, protocol: protocol, sessions: make(map[string]*session)}
}

func (m *SessionManager) Run(ctx context.Context, spec TurnSpec) (Turn, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("harness: manager closed")
	}
	s := m.sessions[spec.ConversationID]
	if s == nil {
		s = newSession(m.base, m.protocol)
		m.sessions[spec.ConversationID] = s
		go s.loop()
	}
	m.mu.Unlock()
	t := &actorTurn{ctx: ctx, spec: spec, session: s, events: make(chan Event, 65), done: make(chan struct{})}
	select {
	case s.in <- request{turn: t}:
		return t, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("harness: session closed")
	}
}

func (m *SessionManager) CloseConversation(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.close(ctx)
}

func (m *SessionManager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	ss := m.sessions
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	var err error
	for _, s := range ss {
		err = errors.Join(err, s.close(ctx))
	}
	return err
}

type request struct {
	turn     *actorTurn
	cancel   bool
	activity int
	release  bool
	reply    chan error
	close    bool
}
type actorTurn struct {
	ctx      context.Context
	spec     TurnSpec
	session  *session
	events   chan Event
	done     chan struct{}
	terminal Event
	number   uint64
}

func (t *actorTurn) Events() <-chan Event { return t.events }
func (t *actorTurn) Cancel(ctx context.Context) error {
	reply := make(chan error, 1)
	select {
	case <-t.done:
		return nil
	case t.session.in <- request{turn: t, cancel: true, reply: reply}:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.session.done:
		return nil
	}
	select {
	case err := <-reply:
		return err
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *actorTurn) Wait(ctx context.Context) (Outcome, error) {
	select {
	case <-t.done:
		if t.terminal.Kind == EventEnd {
			return Outcome{Model: t.terminal.End.Model, Usage: t.terminal.End.Usage}, nil
		}
		if t.terminal.Failure.Reason == "cancelled" {
			return Outcome{}, ErrCancelled
		}
		return Outcome{}, &TurnError{Reason: t.terminal.Failure.Reason}
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	}
}
func (t *actorTurn) AcceptCall(ctx context.Context) (func(), error) { return t.activity(ctx, 1) }
func (t *actorTurn) Approval(ctx context.Context) (func(), error)   { return t.activity(ctx, 2) }
func (t *actorTurn) activity(ctx context.Context, kind int) (func(), error) {
	reply := make(chan error, 1)
	select {
	case t.session.in <- request{turn: t, activity: kind, reply: reply}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, errors.New("harness: stale turn")
	}
	// Once submitted, admission's answer must be observed to avoid losing a receipt.
	var err error
	select {
	case err = <-reply:
	case <-t.session.done:
		return nil, errors.New("harness: session closed")
	}
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case t.session.in <- request{turn: t, activity: kind, release: true}:
			case <-t.session.done:
			}
		})
	}, nil
}

func publish(t *actorTurn, ev Event) {
	// One slot is reserved for the terminal. Cancellation may abandon queued deltas.
	t.terminal = ev
	select {
	case t.events <- ev:
	default:
		panic("harness: terminal reserve exhausted")
	}
	close(t.events)
	close(t.done)
}

func failure(reason string) Event {
	return Event{Kind: EventFailure, Failure: &Failure{Reason: reason}}
}

type startResult struct {
	child *child
	err   error
}

type lineResult struct {
	line []byte
	err  error
}
type child struct {
	cmd           *exec.Cmd
	stdin, stdout *os.File
	fd            int
	lines         chan lineResult
	observed      chan error
	reap          chan struct{}
	gone          chan error
	stop          chan struct{}
	readDone      chan struct{}
	stderr        *diagnostics
}

func spawn(ctx context.Context, b Base, l Launch) (*child, error) {
	if len(l.Argv) == 0 {
		return nil, errors.New("harness: empty command")
	}
	env, err := loginEnvironment(ctx)
	if err != nil {
		return nil, err
	}
	env = mergeEnvironment(env, l.Env)
	path, err := resolveExecutable(l.Argv[0], env, l.Dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, l.Argv[1:]...) //nolint:gosec // connector supplies direct argv
	cmd.Env = env
	cmd.Dir = l.Dir
	cmd.WaitDelay = time.Second
	proc.InOwnGroup(cmd)
	ir, iw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	or, ow, err := os.Pipe()
	if err != nil {
		_ = ir.Close()
		_ = iw.Close()
		return nil, err
	}
	maxErr := b.MaxStderrBytes
	if maxErr <= 0 {
		maxErr = 64 << 10
	}
	c := &child{cmd: cmd, stdin: iw, stdout: or, fd: int(iw.Fd()), lines: make(chan lineResult, 1), observed: make(chan error, 1), reap: make(chan struct{}), gone: make(chan error, 1), stop: make(chan struct{}), readDone: make(chan struct{}), stderr: &diagnostics{max: maxErr}}
	cmd.Stdin = ir
	cmd.Stdout = ow
	cmd.Stderr = c.stderr
	err = cmd.Start()
	_ = ir.Close()
	_ = ow.Close()
	if err != nil {
		_ = iw.Close()
		_ = or.Close()
		return nil, err
	}
	if err = syscall.SetNonblock(c.fd, true); err != nil {
		_ = proc.KillGroupImmediately(cmd)
		_ = cmd.Wait()
		_ = iw.Close()
		_ = or.Close()
		return nil, err
	}
	maxLine := b.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	go func() {
		defer close(c.readDone)
		reader := bufio.NewReaderSize(or, 64<<10)
		for {
			line, err := readBoundedLine(reader, maxLine)
			select {
			case c.lines <- lineResult{line, err}:
			case <-c.stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		c.observed <- observeExit(cmd.Process.Pid)
		<-c.reap
		err := cmd.Wait()
		c.gone <- errors.Join(err, awaitGroupGone(cmd.Process.Pid))
	}()
	return c, nil
}
func (c *child) kill() { _ = proc.KillGroupImmediately(c.cmd) }
func (c *child) finish() {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	close(c.stop)
	<-c.readDone
	close(c.reap)
}

type session struct {
	base     Base
	protocol Protocol
	in       chan request
	done     chan struct{}
}

func newSession(b Base, p Protocol) *session {
	return &session{base: b, protocol: p, in: make(chan request), done: make(chan struct{})}
}

func (s *session) close(ctx context.Context) error {
	select {
	case s.in <- request{close: true}:
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) loop() {
	defer close(s.done)
	bound := s.base.ShutdownTimeout
	if bound <= 0 {
		bound = 10 * time.Second
	}
	idle := s.base.IdleTimeout
	if idle <= 0 {
		idle = time.Hour
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var c *child
	var starting <-chan startResult
	var stopStart context.CancelFunc
	var current *actorTurn
	var queue []*actorTurn
	var terminal *Event
	var commands []Command
	var wire []byte
	var delivery []Event
	var calls, approvals int
	var cancelling, closing, ending bool
	var deadline time.Time
	idleAt := time.Now().Add(idle)
	var serial uint64
	var turnNumber uint64
	var stdoutEnded bool
	pending := map[string]bool{}
	setTerminal := func(ev Event) {
		if terminal == nil {
			if cancelling {
				ev = failure("cancelled")
			}
			terminal = &ev
			commands = nil
			wire = nil
		}
	}
	cancelTurn := func() {
		if current == nil || cancelling {
			return
		}
		if terminal != nil {
			delivery = nil // Keep an already selected terminal, even for a stalled reader.
			return
		}
		cancelling = true
		delivery = nil
		if stopStart != nil {
			stopStart()
		}
		commands = s.protocol.Cancel()
		wire = nil
		deadline = time.Now().Add(bound)
	}
	for {
		if current != nil && terminal != nil && calls == 0 && approvals == 0 && len(delivery) == 0 {
			publish(current, *terminal)
			pending = map[string]bool{}
			current = nil
			terminal = nil
			commands = nil
			wire = nil
			if cancelling && c != nil {
				ending = true
				_ = c.stdin.Close()
			}
			idleAt = time.Now().Add(idle)
		}
		if c == nil && current == nil && len(queue) > 0 && !closing {
			current = queue[0]
			turnNumber++
			current.number = turnNumber
			queue = queue[1:]
			cancelling = false
			ending = false
			launchCtx, cancelStart := context.WithCancel(current.ctx)
			stopStart = cancelStart
			result := make(chan startResult, 1)
			starting = result
			spec := current.spec
			go func() {
				launch, err := s.protocol.Start(launchCtx, spec)
				var process *child
				if err == nil {
					process, err = spawn(launchCtx, s.base, launch)
				}
				result <- startResult{child: process, err: err}
			}()
			stdoutEnded = false
		}
		if c != nil && current == nil && !ending && len(queue) > 0 && !closing {
			current = queue[0]
			turnNumber++
			current.number = turnNumber
			queue = queue[1:]
			cancelling = false
			commands = s.protocol.Commands(current.spec)
		}
		if closing && current == nil && c == nil {
			return
		}
		var lines <-chan lineResult
		var observed <-chan error
		var ctxDone <-chan struct{}
		if c != nil {
			observed = c.observed
			if !stdoutEnded && len(delivery) == 0 && (current == nil || len(current.events) < 64 || cancelling || terminal != nil) {
				lines = c.lines
			}
		}
		if current != nil && terminal == nil && !cancelling {
			ctxDone = current.ctx.Done()
		}
		var output chan Event
		var next Event
		if current != nil && len(delivery) > 0 && len(current.events) < 64 {
			output = current.events
			next = delivery[0]
		}
		select {
		case result := <-starting:
			starting = nil
			if stopStart != nil {
				stopStart()
				stopStart = nil
			}
			c = result.child
			if result.err != nil {
				setTerminal(failure(result.err.Error()))
			} else if cancelling {
				commands = s.protocol.Cancel()
			} else {
				commands = s.protocol.Commands(current.spec)
			}
		case output <- next:
			delivery = delivery[1:]
		case r := <-s.in:
			if r.close {
				closing = true
				for _, t := range queue {
					publish(t, failure("cancelled"))
				}
				queue = nil
				cancelTurn()
				if c != nil && current == nil {
					ending = true
					deadline = time.Now().Add(bound)
					_ = c.stdin.Close()
				}
			}
			if r.turn != nil && !r.cancel && r.activity == 0 {
				if closing {
					publish(r.turn, failure("cancelled"))
				} else {
					queue = append(queue, r.turn)
				}
			}
			if r.cancel {
				if r.turn == current {
					cancelTurn()
				} else {
					for i, t := range queue {
						if t == r.turn {
							publish(t, failure("cancelled"))
							queue = append(queue[:i], queue[i+1:]...)
							break
						}
					}
				}
				r.reply <- nil
			}
			if r.activity != 0 {
				if r.turn != current || (!r.release && (terminal != nil || cancelling)) {
					if r.reply != nil {
						r.reply <- errors.New("harness: stale turn")
					}
					continue
				}
				n := &calls
				if r.activity == 2 {
					n = &approvals
				}
				if r.release {
					*n--
				} else {
					*n++
					r.reply <- nil
				}
			}
		case <-ctxDone:
			cancelTurn()
		case result := <-lines:
			if result.err != nil {
				stdoutEnded = true
				if current != nil && !errors.Is(result.err, io.EOF) {
					reason := fmt.Sprint("the agent stdout ended: ", result.err)
					if errors.Is(result.err, errLineTooLong) {
						reason = "the agent wrote a line longer than " + strconv.Itoa(s.base.MaxLineBytes) + " bytes"
					}
					setTerminal(failure(reason))
				}
				ending = true
				deadline = time.Now().Add(bound)
				_ = c.stdin.Close()
				continue
			}
			line := bytes.TrimSpace(result.line)
			if len(line) == 0 {
				continue
			}
			decoded, err := s.protocol.Decode(line)
			if err != nil {
				if current != nil {
					reason := fmt.Sprint("agent protocol: ", err)
					setTerminal(failure(reason))
				}
				ending = true
				c.kill()
				continue
			}
			if response := decoded.Response; response != nil {
				if pending[response.ID] {
					delete(pending, response.ID)
					if response.Error != "" && current != nil {
						setTerminal(failure(response.Error))
					}
				}
			}
			for _, ev := range decoded.Events {
				if current == nil {
					ending = true
					deadline = time.Now().Add(bound)
					_ = c.stdin.Close()
					break
				}
				if terminal != nil {
					continue
				}
				if ev.Kind == EventEnd || ev.Kind == EventFailure {
					setTerminal(ev)
				} else if !cancelling {
					delivery = append(delivery, ev)
				}
			}
		case <-observed:
			c.kill()
			c.finish()
			err := <-c.gone
			if current != nil {
				reason := "the agent exited without an end event"
				if err != nil {
					reason = exitReason(err)
				}
				if text := c.stderr.String(); text != "" {
					reason += ": " + text
				}
				setTerminal(failure(reason))
			}
			c = nil
			ending = false
			pending = map[string]bool{}
		case now := <-tick.C:
			if c == nil {
				continue
			}
			if (cancelling || ending) && !deadline.IsZero() && !now.Before(deadline) {
				c.kill()
			}
			if current == nil && !ending && !now.Before(idleAt) {
				ending = true
				deadline = now.Add(bound)
				_ = c.stdin.Close()
			}
			if current != nil && terminal == nil && len(wire) == 0 && len(commands) > 0 {
				serial++
				id := strconv.FormatUint(serial, 10)
				command := commands[0]
				commands = commands[1:]
				var err error
				wire, err = s.protocol.Encode(id, command)
				if err != nil {
					reason := fmt.Sprint(err)
					setTerminal(failure(reason))
					continue
				}
				pending[id] = true
			}
			if len(wire) > 0 {
				n, err := syscall.Write(c.fd, wire)
				if n > 0 {
					wire = wire[n:]
				}
				if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
					reason := fmt.Sprint("agent stdin: ", err)
					setTerminal(failure(reason))
					ending = true
					c.kill()
				}
			}
		}
	}
}
