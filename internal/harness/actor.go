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
	turn   *actorTurn
	cancel bool
	reply  chan error
	close  bool
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

// Transition is an observation of an actor action, not a source of state.
// Observe callbacks must not block. Data contains bytes actually written to stdin.
type Transition struct {
	Kind             string
	Generation, Turn uint64
	Data             []byte
}

type pendingStep struct {
	id               string
	generation, turn uint64
	step             Step
}
type (
	settlement struct {
		generation, turn uint64
		err              error
	}
	cleanupResult struct {
		generation uint64
		err        error
	}
	incarnationStart struct {
		generation uint64
		child      *child
		tools      ToolSession
		bridge     ToolBridge
		err        error
	}
)

func disposeChild(c *child) error {
	if c == nil {
		return nil
	}
	c.kill()
	c.finish()
	return <-c.gone
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
	var process *child
	var codec Codec
	var tools ToolSession
	var current *actorTurn
	var queue []*actorTurn
	var generation, turnNumber, serial uint64
	var starting, retiring, closing, cancelling, promptWritten, settling, settled bool
	var deathPending bool
	var terminal *Event
	var plan []Step
	var cancelCommands []Command
	var pending *pendingStep
	var frame []byte
	var frameWritten int
	var frameStep Step
	var delivery []Event
	var deadline time.Time
	var stopStart context.CancelFunc
	var settleCancel context.CancelFunc
	idleAt := time.Now().Add(idle)
	starts := make(chan incarnationStart)
	settlements := make(chan settlement)
	cleanups := make(chan cleanupResult)
	observe := func(kind string, data []byte) {
		if s.base.Observe != nil {
			var turn uint64
			if current != nil {
				turn = current.number
			}
			s.base.Observe(Transition{Kind: kind, Generation: generation, Turn: turn, Data: append([]byte(nil), data...)})
		}
	}
	// Cleanup owns only blocking pipe/reaper waits. It never changes actor state.
	retire := func() {
		if retiring {
			return
		}
		retiring = true
		if stopStart != nil {
			stopStart()
			stopStart = nil
		}
		starting = false
		child, sessionTools, gen := process, tools, generation
		cleanupDeadline := deadline
		if cleanupDeadline.IsZero() {
			cleanupDeadline = time.Now().Add(bound)
		}
		if child != nil {
			child.kill()
		}
		process = nil
		codec = nil
		tools = nil
		frame = nil
		pending = nil
		plan = nil
		cancelCommands = nil
		go func() {
			ctx, cancel := context.WithDeadline(context.Background(), cleanupDeadline)
			defer cancel()
			var err error
			if sessionTools != nil {
				err = sessionTools.Retire(ctx)
			}
			err = errors.Join(err, disposeChild(child))
			select {
			case cleanups <- cleanupResult{gen, err}:
			case <-s.done:
			}
		}()
	}
	settle := func() {
		if current == nil || settling || settled {
			return
		}
		settling = true
		gen, number, sessionTools := generation, current.number, tools
		ctx := context.Background()
		var cancel context.CancelFunc
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, deadline)
		} else {
			ctx, cancel = context.WithTimeout(ctx, bound)
		}
		settleCancel = cancel
		go func() {
			var err error
			if sessionTools != nil {
				err = sessionTools.EndTurn(ctx)
			}
			cancel()
			select {
			case settlements <- settlement{gen, number, err}:
			case <-s.done:
			}
		}()
	}
	choose := func(ev Event) {
		if current == nil || terminal != nil {
			return
		}
		if cancelling {
			ev = failure("cancelled")
		}
		terminal = &ev
		plan = nil
		pending = nil
		// A selected terminal prevents new work. A partially written work frame
		// cannot be reused by any later command.
		if len(frame) > 0 && frameWritten > 0 {
			retire()
		}
		frame = nil
		observe("terminal-taken", []byte(ev.Kind))
		settle()
	}
	fail := func(reason string) { choose(failure(reason)); retire() }
	cancelTurn := func() {
		if current == nil {
			return
		}
		if terminal != nil {
			delivery = nil
			return
		}
		if cancelling {
			return
		}
		cancelling = true
		deadline = time.Now().Add(bound)
		observe("cancel", nil)
		delivery = nil
		plan = nil
		pending = nil
		// Never append abort to the suffix of an incomplete JSON frame.
		partial := len(frame) > 0 && frameWritten > 0
		frame = nil
		settle()
		if stopStart != nil {
			stopStart()
			stopStart = nil
		}
		if starting || process == nil || partial {
			choose(failure("cancelled"))
			retire()
			return
		}
		cancelCommands = codec.CancelSequence()
	}
	begin := func(t *actorTurn) {
		current = t
		cancelling = false
		promptWritten = false
		settling = false
		settled = false
		terminal = nil
		delivery = nil
		frame = nil
		pending = nil
		deadline = time.Time{}
		observe("begin-turn", nil)
		if tools != nil {
			if err := tools.BeginTurn(t.spec.Authority); err != nil {
				fail(fmt.Sprint("tools: ", err))
				return
			}
		}
		plan = codec.BeginTurn(t.spec)
	}
	for {
		// Publication is the only point at which Wait and Events become terminal.
		if current != nil && terminal != nil && settled && len(delivery) == 0 {
			observe("terminal", nil)
			publish(current, *terminal)
			current = nil
			terminal = nil
			plan = nil
			pending = nil
			frame = nil
			idleAt = time.Now().Add(idle)
			if cancelling {
				retire()
			}
		}
		if closing && current == nil && !starting && !retiring && process == nil {
			return
		}
		var lines <-chan lineResult
		var exited <-chan error
		if process != nil {
			exited = process.observed
			if len(delivery) == 0 {
				lines = process.lines
			}
		}
		var ctxDone <-chan struct{}
		if current != nil && terminal == nil && !cancelling {
			ctxDone = current.ctx.Done()
		}
		var output chan Event
		var next Event
		if current != nil && len(delivery) > 0 && len(current.events) < 64 {
			output = current.events
			next = delivery[0]
		}
		handleLine := func(result lineResult) {
			if process == nil || codec == nil {
				return
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					deathPending = true
					settle()
					retire()
					return
				}
				reason := fmt.Sprint("the agent stdout ended: ", result.err)
				if errors.Is(result.err, errLineTooLong) {
					reason = "the agent wrote a line longer than " + strconv.Itoa(s.base.MaxLineBytes) + " bytes"
				}
				fail(reason)
				return
			}
			line := bytes.TrimSpace(result.line)
			if len(line) == 0 {
				return
			}
			decoded, err := codec.Decode(line)
			if err != nil {
				fail(fmt.Sprint("agent protocol: ", err))
				return
			}
			if response := decoded.Response; response != nil {
				if pending == nil || pending.id != response.ID || pending.generation != generation || current == nil || pending.turn != current.number {
					return
				}
				if pending != nil && pending.id == response.ID && pending.generation == generation && current != nil && pending.turn == current.number {
					step := pending.step
					pending = nil
					if response.Error != "" {
						fail(response.Error)
						return
					}
					if step.Check != nil {
						if err := step.Check(response.Payload); err != nil {
							fail(fmt.Sprint("agent response: ", err))
							return
						}
					}
					if len(plan) > 0 {
						plan = plan[1:]
					}
				}
			}
			for _, ev := range decoded.Events {
				if current == nil || !promptWritten {
					fail("agent output before a turn prompt was written")
					return
				}
				if terminal != nil {
					fail("agent output after the turn terminal")
					return
				}
				if ev.Kind == EventEnd || ev.Kind == EventFailure {
					choose(ev)
					if cancelling {
						retire()
					}
				} else if !cancelling {
					delivery = append(delivery, ev)
				}
			}
		}
		select {
		case output <- next:
			delivery = delivery[1:]
		case r := <-s.in:
			if r.close {
				closing = true
				for _, t := range queue {
					publish(t, failure("cancelled"))
				}
				queue = nil
				if current != nil {
					cancelTurn()
				} else if process != nil || starting {
					retire()
				}
			}
			if r.turn != nil && !r.cancel {
				if closing {
					publish(r.turn, failure("cancelled"))
				} else {
					turnNumber++
					r.turn.number = turnNumber
					queue = append(queue, r.turn)
					if s.base.Observe != nil {
						s.base.Observe(Transition{Kind: "turn-queued", Generation: generation, Turn: r.turn.number})
					}
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
		case <-ctxDone:
			cancelTurn()
		case result := <-starts:
			if result.generation != generation || !starting || retiring {
				go func() {
					_ = disposeChild(result.child)
					if result.tools != nil {
						ctx, cancel := context.WithTimeout(context.Background(), bound)
						defer cancel()
						_ = result.tools.Retire(ctx)
					}
				}()
				continue
			}
			starting = false
			if stopStart != nil {
				stopStart()
				stopStart = nil
			}
			process, tools = result.child, result.tools
			if result.err != nil {
				fail(fmt.Sprint(result.err))
				continue
			}
			codec = s.protocol.NewCodec(SessionInfo{ConversationID: current.spec.ConversationID, Generation: generation})
			spec := current.spec
			if len(result.bridge.Command) > 0 {
				spec.Tools = result.bridge
				current.spec = spec
			}
			if tools != nil {
				if err := tools.BeginTurn(spec.Authority); err != nil {
					fail(fmt.Sprint("tools: ", err))
					continue
				}
			}
			plan = append(codec.Setup(), codec.BeginTurn(spec)...)
		case result := <-settlements:
			if current == nil || result.generation != generation || result.turn != current.number {
				continue
			}
			settling = false
			settled = true
			if settleCancel != nil {
				settleCancel()
				settleCancel = nil
			}
			observe("settled", nil)
			if result.err != nil && !cancelling {
				terminalEvent := failure(fmt.Sprint("tool settlement: ", result.err))
				terminal = &terminalEvent
				retire()
			}
		case result := <-cleanups:
			if result.generation == generation {
				retiring = false
				if deathPending {
					deathPending = false
					reason := "the agent exited without an end event"
					if result.err != nil {
						reason = exitReason(result.err)
					}
					choose(failure(reason))
				}
			}
		case line := <-lines:
			handleLine(line)
		case <-exited:
			deathPending = true
			settle()
			retire()
		case now := <-tick.C:
			if current != nil && cancelling && !deadline.IsZero() && !now.Before(deadline) {
				choose(failure("cancelled"))
				retire()
			}
			// A queued line wins over admission and writes, never over cancellation.
			if lines != nil {
				select {
				case line := <-lines:
					handleLine(line)
					continue
				default:
				}
			}
			if current == nil && process != nil && !retiring && !now.Before(idleAt) {
				observe("idle-expired", nil)
				if tools == nil || !tools.Pending() {
					retire()
				}
			}
			if current == nil && !retiring && !starting && len(queue) > 0 && !closing {
				t := queue[0]
				queue = queue[1:]
				if process != nil {
					begin(t)
				} else {
					generation++
					current = t
					cancelling = false
					promptWritten = false
					settling = false
					settled = false
					terminal = nil
					deadline = time.Time{}
					starting = true
					observe("begin-turn", nil)
					ctx, cancel := context.WithCancel(t.ctx)
					stopStart = cancel
					info := SessionInfo{ConversationID: t.spec.ConversationID, Generation: generation}
					spec := t.spec
					go func() {
						var ts ToolSession
						var bridge ToolBridge
						var err error
						if s.base.Tools != nil {
							ts, bridge, err = s.base.Tools(ctx, info)
						}
						if len(bridge.Command) > 0 {
							spec.Tools = bridge
						}
						var launch Launch
						var child *child
						if err == nil {
							launch, err = s.protocol.Start(ctx, spec)
						}
						if err == nil {
							child, err = spawn(ctx, s.base, launch)
						}
						result := incarnationStart{generation: info.Generation, child: child, tools: ts, bridge: bridge, err: err}
						select {
						case starts <- result:
						case <-ctx.Done():
							_ = disposeChild(child)
							if ts != nil {
								retireCtx, cancel := context.WithTimeout(context.Background(), bound)
								defer cancel()
								_ = ts.Retire(retireCtx)
							}
						case <-s.done:
							_ = disposeChild(child)
							if ts != nil {
								retireCtx, cancel := context.WithTimeout(context.Background(), bound)
								defer cancel()
								_ = ts.Retire(retireCtx)
							}
						}
					}()
				}
			}
			if process == nil || current == nil || terminal != nil {
				continue
			}
			if len(frame) == 0 {
				var step Step
				if cancelling {
					if len(cancelCommands) == 0 {
						continue
					}
					step = Step{Command: cancelCommands[0]}
					cancelCommands = cancelCommands[1:]
				} else {
					if pending != nil || len(plan) == 0 {
						continue
					}
					step = plan[0]
				}
				serial++
				id := strconv.FormatUint(generation, 10) + "/" + strconv.FormatUint(current.number, 10) + "/" + strconv.FormatUint(serial, 10)
				var err error
				frame, err = codec.Encode(id, step.Command)
				if err != nil {
					fail(fmt.Sprint(err))
					continue
				}
				frameWritten = 0
				frameStep = step
				if !cancelling {
					pending = &pendingStep{id: id, generation: generation, turn: current.number, step: step}
				}
			}
			if len(frame) > 0 {
				n, err := syscall.Write(process.fd, frame)
				if n > 0 {
					observe("write", frame[:n])
					frame = frame[n:]
					frameWritten += n
				}
				if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
					fail(fmt.Sprint("agent stdin: ", err))
					continue
				}
				if len(frame) == 0 && frameStep.Prompt && !cancelling {
					promptWritten = true
					observe("prompt-written", nil)
				}
			}
		}
	}
}
