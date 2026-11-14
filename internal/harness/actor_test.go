package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type shellProtocol struct{}

func (shellProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", `while read -r id name; do
case "$name" in
barrier) echo "response $id";;
prompt) echo end;;
esac
done`}}, nil
}

func (p shellProtocol) NewCodec(SessionInfo) Codec { return p }

func (shellProtocol) Setup() []Step { return nil }

func (shellProtocol) BeginTurn(TurnSpec) []Step {
	return []Step{{Command: Command{Name: "barrier"}, Barrier: true}, {Command: Command{Name: "prompt"}, Prompt: true}}
}

func (shellProtocol) CancelSequence() []Command { return []Command{{Name: "abort"}} }

func (shellProtocol) Encode(id string, c Command) ([]byte, error) {
	return []byte(id + " " + c.Name + "\n"), nil
}

func (shellProtocol) Decode(line []byte) (Decoded, error) {
	if strings.HasPrefix(string(line), "response ") {
		return Decoded{Response: &Response{ID: strings.TrimPrefix(string(line), "response ")}}, nil
	}
	return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: "test"}}}}, nil
}

func TestSessionTwoTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	m := NewSessionManager(Base{}, shellProtocol{})
	defer func() { _ = m.Close(ctx) }()
	for range 2 {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "one"})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := turn.Wait(ctx); err != nil || out.Model != "test" {
			t.Fatalf("%+v %v", out, err)
		}
	}
}

type startupProtocol struct {
	shellProtocol
	entered chan struct{}
}

func (p startupProtocol) Start(ctx context.Context, _ TurnSpec) (Launch, error) {
	close(p.entered)
	<-ctx.Done()
	return Launch{}, ctx.Err()
}

func TestCancelDuringStartup(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	p := startupProtocol{entered: make(chan struct{})}
	m := NewSessionManager(Base{}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	<-p.entered
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
}

type oversizedProtocol struct{ shellProtocol }

func (oversizedProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", "printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\\n'; cat >/dev/null"}}, nil
}

func TestSessionBoundsLines(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	m := NewSessionManager(Base{MaxLineBytes: 32}, oversizedProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = turn.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "longer than 32 bytes") {
		t.Fatalf("Wait=%v", err)
	}
}

type brokenProtocol struct{ shellProtocol }

func (p brokenProtocol) NewCodec(SessionInfo) Codec { return p }

func (brokenProtocol) Decode([]byte) (Decoded, error) { return Decoded{}, errors.New("invalid line") }

func TestProtocolFailureRestartsSession(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	m := NewSessionManager(Base{}, brokenProtocol{})
	defer func() { _ = m.Close(ctx) }()
	for range 2 {
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "restart"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = turn.Wait(ctx)
		if err == nil || !strings.Contains(err.Error(), "invalid line") {
			t.Fatalf("Wait=%v", err)
		}
	}
}

// A connector that ignores ctx must not extend the actor's cancellation bound.
// Release it after the terminal so the test also exercises disposal of a late
// launch result instead of leaving a deliberately blocked goroutine behind.
type ignoringStartProtocol struct {
	shellProtocol
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (p ignoringStartProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	close(p.entered)
	<-p.release
	defer close(p.returned)
	return Launch{Argv: []string{"/bin/sh", "-c", "while read x; do echo end; done"}}, nil
}

func TestCancelBoundsIgnoringStart(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	p := ignoringStartProtocol{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	m := NewSessionManager(Base{ShutdownTimeout: 20 * time.Millisecond}, p)
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.entered:
	case <-ctx.Done():
		t.Fatal("Start not entered")
	}
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	_, waitErr := turn.Wait(ctx)
	close(p.release)
	if !errors.Is(waitErr, ErrCancelled) {
		t.Fatalf("Wait=%v", waitErr)
	}
	select {
	case <-p.returned:
	case <-ctx.Done():
		t.Fatal("late Start not released")
	}
	if err = m.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCancelDoesNotAppendToPartialFrame(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	trace := &wireTrace{changed: make(chan struct{}, 1)}
	gate := filepath.Join(t.TempDir(), "drain")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	gateFile, err := os.OpenFile(gate, os.O_RDWR|syscall.O_NONBLOCK, 0o600) //nolint:gosec // private test FIFO
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateFile.Close() }()
	p := partialDrainProtocol{gate: gate}
	originalObserve := trace.observe
	observer := func(tr Transition) {
		originalObserve(tr)
		if tr.Kind == "cancel" {
			_, _ = gateFile.Write([]byte("go\n"))
		}
	}
	m := NewSessionManager(Base{ShutdownTimeout: 20 * time.Millisecond, Observe: observer}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	for {
		partial := false
		for _, tr := range trace.snapshot() {
			if tr.Kind == "write" && len(tr.Data) > 1024 {
				partial = true
			}
		}
		if partial {
			break
		}
		select {
		case <-trace.changed:
		case <-ctx.Done():
			t.Fatal("no partial write")
		}
	}
	before := trace.snapshot()
	count := 0
	for _, tr := range before {
		if tr.Kind == "write" {
			count += len(tr.Data)
		}
	}
	if count == 0 || count >= 1<<20 {
		t.Fatalf("expected a partial frame, wrote %d", count)
	}
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
	checkWire(t, -1, trace.snapshot())
}

// The fake endpoint keeps accepted work pending until EndTurn cancellation has
// been observed. It models settlement, not manually released actor counters.
type pendingToolSession struct {
	mu      sync.Mutex
	pending bool
	began   chan struct{}
	ending  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *pendingToolSession) BeginTurn(TurnAuthority) error {
	s.mu.Lock()
	s.pending = true
	s.mu.Unlock()
	s.once.Do(func() { close(s.began) })
	return nil
}
func (s *pendingToolSession) Pending() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.pending }
func (s *pendingToolSession) EndTurn(ctx context.Context) error {
	select {
	case s.ending <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		s.mu.Lock()
		s.pending = false
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *pendingToolSession) Retire(context.Context) error {
	s.mu.Lock()
	s.pending = false
	s.mu.Unlock()
	return nil
}

func TestCancellationBoundsPendingTools(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	tools := &pendingToolSession{began: make(chan struct{}), ending: make(chan struct{}, 4), release: make(chan struct{})}
	trace := &wireTrace{changed: make(chan struct{}, 1)}
	factory := func(context.Context, SessionInfo) (ToolSession, ToolBridge, error) { return tools, ToolBridge{}, nil }
	p := &interleaveProtocol{mode: "hold", decoded: make(chan struct{}, 1)}
	m := NewSessionManager(Base{ShutdownTimeout: 20 * time.Millisecond, Observe: trace.observe, Tools: factory}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-tools.began:
	case <-ctx.Done():
		t.Fatal("tools not bound")
	}
	if !trace.await(ctx, "prompt-written") {
		t.Fatal("prompt not written")
	}
	if !tools.Pending() {
		t.Fatal("test did not establish pending tools")
	}
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
	if tools.Pending() {
		t.Fatal("terminal before endpoint settlement")
	}
}

// An endpoint may retain a process-level approval after turn settlement. Its
// Pending report must prevent idle expiry from retiring that generation.
type idleApprovalSession struct {
	pendingToolSession
	retired    chan struct{}
	retireOnce sync.Once
}

func (s *idleApprovalSession) EndTurn(context.Context) error { return nil }
func (s *idleApprovalSession) Retire(context.Context) error {
	s.retireOnce.Do(func() { close(s.retired) })
	return nil
}

func TestApprovalSurvivesConfirmedIdleExpiry(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	tools := &idleApprovalSession{pendingToolSession: pendingToolSession{began: make(chan struct{}), ending: make(chan struct{}, 4), release: make(chan struct{})}, retired: make(chan struct{})}
	trace := &wireTrace{changed: make(chan struct{}, 1)}
	factory := func(context.Context, SessionInfo) (ToolSession, ToolBridge, error) { return tools, ToolBridge{}, nil }
	m := NewSessionManager(Base{IdleTimeout: 5 * time.Millisecond, ShutdownTimeout: 200 * time.Millisecond, Observe: trace.observe, Tools: factory}, shellProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if !trace.await(ctx, "idle-expired") {
		t.Fatal("idle expiry not observed")
	}
	if !tools.Pending() {
		t.Fatal("approval lost across idle expiry")
	}
	select {
	case <-tools.retired:
		t.Fatal("idle expiry retired pending approval")
	default:
	}
	tools.mu.Lock()
	tools.pending = false
	tools.mu.Unlock()
	select {
	case <-tools.retired:
	case <-ctx.Done():
		t.Fatal("idle generation not retired after approval settled")
	}
}

func TestAcceptedApprovalWithholdsTerminal(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	tools := &pendingToolSession{began: make(chan struct{}), ending: make(chan struct{}, 4), release: make(chan struct{})}
	factory := func(context.Context, SessionInfo) (ToolSession, ToolBridge, error) { return tools, ToolBridge{}, nil }
	m := NewSessionManager(Base{ShutdownTimeout: 200 * time.Millisecond, Tools: factory}, shellProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-tools.ending:
	case <-ctx.Done():
		t.Fatal("EndTurn not entered")
	}
	owned, ok := turn.(*actorTurn)
	if !ok {
		t.Fatal("expected actor turn")
	}
	select {
	case <-owned.done:
		t.Fatal("terminal before approval settlement")
	default:
	}
	close(tools.release)
	if _, err = turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

type prematureFailureProtocol struct{ shellProtocol }

func (prematureFailureProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", `echo failure; exec sleep 30`}}, nil
}

func (prematureFailureProtocol) NewCodec(SessionInfo) Codec {
	return &interleaveProtocol{mode: "hold", decoded: make(chan struct{}, 1)}
}

func TestRejectFailureBeforePrompt(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	trace := &wireTrace{changed: make(chan struct{}, 1)}
	m := NewSessionManager(Base{Observe: trace.observe}, prematureFailureProtocol{})
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); err == nil || !strings.Contains(err.Error(), "before a turn prompt") {
		t.Fatalf("Wait=%v", err)
	}
	for _, tr := range trace.snapshot() {
		if tr.Kind == "prompt-written" {
			t.Fatal("test did not establish pre-prompt output")
		}
	}
}

// The peer drains only after cancel is taken, so an appended abort really reaches
// the pipe in the mutant instead of being hidden by an always-full pipe.
type partialDrainProtocol struct{ gate string }

func (p partialDrainProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	return Launch{Argv: []string{"/bin/sh", "-c", `read -r id name; echo "response $id"; read x < "$1"; cat >/dev/null`, "agent", p.gate}}, nil
}

func (partialDrainProtocol) NewCodec(SessionInfo) Codec {
	return &interleaveProtocol{mode: "blocked", decoded: make(chan struct{}, 1)}
}

// EndTurn's context expiring does not establish endpoint settlement. Only a
// successful Retire may clear accepted work and permit a terminal publication.
type timeoutToolSession struct {
	pendingToolSession
	retireErr error
	retired   bool
}

func (s *timeoutToolSession) EndTurn(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *timeoutToolSession) Retire(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retired = true
	if s.retireErr == nil {
		s.pending = false
	}
	return s.retireErr
}

func TestEndTurnTimeoutRequiresSuccessfulRetire(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		for _, retireFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("cancel=%t/retireFails=%t", cancelTurn, retireFails), func(t *testing.T) {
				ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				tools := &timeoutToolSession{pendingToolSession: pendingToolSession{began: make(chan struct{})}}
				if retireFails {
					tools.retireErr = errors.New("retire rejected")
				}
				factories := 0
				factory := func(context.Context, SessionInfo) (ToolSession, ToolBridge, error) {
					factories++
					if factories == 1 {
						return tools, ToolBridge{}, nil
					}
					return nil, ToolBridge{}, nil
				}
				trace := &wireTrace{changed: make(chan struct{}, 1)}
				mode := "end"
				if cancelTurn {
					mode = "hold"
				}
				m := NewSessionManager(Base{Tools: factory, ShutdownTimeout: 20 * time.Millisecond, Observe: trace.observe}, &interleaveProtocol{mode: mode, decoded: make(chan struct{}, 1)})
				defer func() { _ = m.Close(ctx) }()
				turn, err := m.Run(ctx, TurnSpec{ConversationID: "settlement"})
				if err != nil {
					t.Fatal(err)
				}
				if cancelTurn {
					if !trace.await(ctx, "prompt-written") {
						t.Fatal("prompt not written")
					}
					if err = turn.Cancel(ctx); err != nil {
						t.Fatal(err)
					}
				}
				_, err = turn.Wait(ctx)
				if errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("terminal was not bounded")
				}
				tools.mu.Lock()
				retired, pending := tools.retired, tools.pending
				tools.mu.Unlock()
				if !retired {
					t.Fatal("EndTurn error treated as settled without Retire")
				}
				if pending != retireFails {
					t.Fatalf("pending=%t retireFails=%t", pending, retireFails)
				}
				if retireFails {
					if err == nil || !strings.Contains(err.Error(), "confirmed") || !strings.Contains(err.Error(), "tool") {
						t.Fatalf("expected explicit unconfirmed tool failure, got %v", err)
					}
				} else if cancelTurn {
					if !errors.Is(err, ErrCancelled) {
						t.Fatalf("Wait=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				assertTerminal(t, ctx, -1, turn)
				next, err := m.Run(ctx, TurnSpec{ConversationID: "settlement"})
				if err != nil {
					t.Fatal(err)
				}
				if cancelTurn {
					select {
					case <-next.Events():
					case <-ctx.Done():
						t.Fatal("second turn did not start")
					}
					if err = next.Cancel(ctx); err != nil {
						t.Fatal(err)
					}
				}
				assertTerminal(t, ctx, -1, next)
				if factories < 2 {
					t.Fatal("reused generation after EndTurn timeout")
				}
			})
		}
	}
}

type closeCancelProtocol struct {
	shellProtocol
	ignore bool
}

func (p closeCancelProtocol) NewCodec(SessionInfo) Codec { return p }
func (p closeCancelProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	script := `while read -r id name; do
case "$name" in
barrier) echo "response $id";;
prompt) echo delta;;
abort) echo end;;
esac
done`
	if p.ignore {
		script += "; exec sleep 30"
	}
	return Launch{Argv: []string{"/bin/sh", "-c", script}}, nil
}
func (closeCancelProtocol) CancelSequence() []Command { return []Command{{Name: "abort"}} }
func (closeCancelProtocol) Decode(line []byte) (Decoded, error) {
	if string(line) == "end" {
		return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: "test"}}}}, nil
	}
	if strings.HasPrefix(string(line), "response ") {
		return Decoded{Response: &Response{ID: strings.TrimPrefix(string(line), "response ")}}, nil
	}
	return Decoded{Events: []Event{{Kind: EventAnswerDelta, Text: string(line)}}}, nil
}

func TestCancelClosesStdinBeforeDeadlineKill(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(fmt.Sprintf("ignoreClose=%t", ignore), func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			trace := &wireTrace{changed: make(chan struct{}, 1)}
			var cancelAt, killAt time.Time
			observe := func(tr Transition) {
				if tr.Kind == "cancel" {
					cancelAt = time.Now()
				}
				if tr.Kind == "kill" {
					killAt = time.Now()
				}
				trace.observe(tr)
			}
			const bound = 100 * time.Millisecond
			m := NewSessionManager(Base{ShutdownTimeout: bound, Observe: observe}, closeCancelProtocol{ignore: ignore})
			defer func() { _ = m.Close(ctx) }()
			turn, err := m.Run(ctx, TurnSpec{})
			if err != nil {
				t.Fatal(err)
			}
			if !trace.await(ctx, "prompt-written") {
				t.Fatal("prompt not written")
			}
			if err = turn.Cancel(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err = turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
				t.Fatalf("Wait=%v", err)
			}
			closed, killed := false, false
			for _, tr := range trace.snapshot() {
				if tr.Kind == "stdin-close" {
					closed = true
				}
				if tr.Kind == "kill" {
					if !closed {
						t.Fatal("killed before closing stdin")
					}
					killed = true
				}
			}
			if !closed {
				t.Fatal("stdin never closed")
			}
			if killed != ignore {
				t.Fatalf("killed=%t ignoreClose=%t", killed, ignore)
			}
			if killed && killAt.Sub(cancelAt) < bound {
				t.Fatalf("kill before original deadline: %s", killAt.Sub(cancelAt))
			}
			cleaned := false
			for _, tr := range trace.snapshot() {
				if tr.Kind == "cleanup" {
					cleaned = true
					if !ignore && len(tr.Data) != 0 {
						t.Fatalf("cooperative process did not exit cleanly: %s", tr.Data)
					}
				}
			}
			if !cleaned {
				t.Fatal("terminal before process cleanup")
			}
		})
	}
}

type settlementCancelProtocol struct {
	closeCancelProtocol
	tools           *pendingToolSession
	pendingAtCancel chan bool
}

func (p settlementCancelProtocol) NewCodec(SessionInfo) Codec { return p }
func (p settlementCancelProtocol) CancelSequence() []Command {
	p.pendingAtCancel <- p.tools.Pending()
	return p.closeCancelProtocol.CancelSequence()
}

func TestCancelProtocolWaitsForToolResponses(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	tools := &pendingToolSession{began: make(chan struct{}), ending: make(chan struct{}, 4), release: make(chan struct{})}
	observed := make(chan bool, 1)
	trace := &wireTrace{changed: make(chan struct{}, 1)}
	factory := func(context.Context, SessionInfo) (ToolSession, ToolBridge, error) { return tools, ToolBridge{}, nil }
	protocol := settlementCancelProtocol{tools: tools, pendingAtCancel: observed}
	manager := NewSessionManager(Base{Tools: factory, Observe: trace.observe}, protocol)
	defer func() { _ = manager.Close(ctx) }()
	turn, err := manager.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if !trace.await(ctx, "prompt-written") {
		t.Fatal("prompt not written")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	<-tools.ending
	close(tools.release)
	select {
	case pending := <-observed:
		if pending {
			t.Error("agent abort requested before tool cancellation responses settled")
		}
	case <-ctx.Done():
		t.Fatal("cancel sequence not requested")
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
}

type receivedToolProtocol struct {
	closeCancelProtocol
	gate         string
	cancelCalled chan struct{}
}

func (p receivedToolProtocol) NewCodec(SessionInfo) Codec { return p }
func (p receivedToolProtocol) Start(ctx context.Context, spec TurnSpec) (Launch, error) {
	if p.gate == "" {
		return p.closeCancelProtocol.Start(ctx, spec)
	}
	return Launch{Argv: []string{"/bin/sh", "-c", `while read -r id name; do
case "$name" in
barrier) echo "response $id";;
prompt) echo delta; read result < "$1"; echo tool-finished;;
abort) echo end;;
esac
done`, "agent", p.gate}}, nil
}

func (p receivedToolProtocol) Decode(line []byte) (Decoded, error) {
	switch string(line) {
	case "delta":
		return Decoded{Events: []Event{{Kind: EventToolStarted, Tool: &ToolCall{ID: "call"}}}}, nil
	case "tool-finished":
		return Decoded{Events: []Event{{Kind: EventToolFinished, Tool: &ToolCall{ID: "call"}}}}, nil
	}
	return p.closeCancelProtocol.Decode(line)
}

func (p receivedToolProtocol) CancelSequence() []Command {
	p.cancelCalled <- struct{}{}
	return p.closeCancelProtocol.CancelSequence()
}

func TestCancelWaitsForAgentToolResultConsumption(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("consumed=%t", consumed), func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			trace := &wireTrace{changed: make(chan struct{}, 1)}
			protocol := receivedToolProtocol{cancelCalled: make(chan struct{}, 2)}
			if consumed {
				protocol.gate = t.TempDir() + "/result"
				if err := syscall.Mkfifo(protocol.gate, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manager := NewSessionManager(Base{Observe: trace.observe, ShutdownTimeout: 100 * time.Millisecond}, protocol)
			defer func() { _ = manager.Close(ctx) }()
			turn, err := manager.Run(ctx, TurnSpec{})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-turn.Events():
				if ev.Kind != EventToolStarted {
					t.Fatal(ev)
				}
			case <-ctx.Done():
				t.Fatal("no tool start")
			}
			if err := turn.Cancel(ctx); err != nil {
				t.Fatal(err)
			}
			if !trace.await(ctx, "settled") {
				t.Fatal("tool endpoint did not settle")
			}
			select {
			case <-protocol.cancelCalled:
				t.Fatal("abort before tool result consumed")
			default:
			}
			if consumed {
				if err := os.WriteFile(protocol.gate, []byte("cancelled\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
				t.Fatalf("Wait=%v", err)
			}
			select {
			case <-protocol.cancelCalled:
				if !consumed {
					t.Fatal("abort without a consumed tool result")
				}
			default:
				if consumed {
					t.Fatal("no abort after tool result consumed")
				}
			}
			killed := false
			for _, tr := range trace.snapshot() {
				if tr.Kind == "kill" {
					killed = true
				}
			}
			if killed == consumed {
				t.Fatalf("killed=%t consumed=%t", killed, consumed)
			}
		})
	}
}
