package harness

import (
	"context"
	"errors"
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
	return Launch{Argv: []string{"/bin/sh", "-c", "while read x; do echo end; done"}}, nil
}

func (p shellProtocol) NewCodec(SessionInfo) Codec { return p }

func (shellProtocol) Setup() []Step { return nil }

func (shellProtocol) BeginTurn(TurnSpec) []Step {
	return []Step{{Command: Command{Name: "prompt"}, Prompt: true}}
}

func (shellProtocol) CancelSequence() []Command { return []Command{{Name: "abort"}} }

func (shellProtocol) Encode(_ string, c Command) ([]byte, error) { return []byte(c.Name + "\n"), nil }

func (shellProtocol) Decode([]byte) (Decoded, error) {
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
	if !trace.await(ctx, "write") {
		t.Fatal("no partial write")
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
		s.mu.Lock()
		s.pending = false
		s.mu.Unlock()
		return ctx.Err()
	}
}
func (s *pendingToolSession) Retire(ctx context.Context) error { return s.EndTurn(ctx) }

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
	return Launch{Argv: []string{"/bin/sh", "-c", `read x < "$1"; cat >/dev/null`, "agent", p.gate}}, nil
}

func (partialDrainProtocol) NewCodec(SessionInfo) Codec {
	return &interleaveProtocol{mode: "blocked", decoded: make(chan struct{}, 1)}
}
