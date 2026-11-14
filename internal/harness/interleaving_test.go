package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type interleaveProtocol struct {
	mode    string
	decoded chan struct{}
}

func (p *interleaveProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	// Every response ID comes from bytes actually read by the agent, not Encode.
	script := `while read -r id name; do
 case "$name" in
 setup) echo "response $id"; echo "response $id"; echo 'response unmatched';;
 prompt-end) echo delta; echo end;;
 prompt-stale) echo end; echo end;;
 prompt-failure) echo failure;;
 prompt-hold) echo delta;;
 prompt-exit) exit 7;;
 prompt-flood) while :; do echo delta; done;;
 abort) echo end;;
 esac
 done`
	if p.mode == "blocked" {
		script = `exec sleep 30`
	}
	return Launch{Argv: []string{"/bin/sh", "-c", script}}, nil
}
func (p *interleaveProtocol) NewCodec(SessionInfo) Codec { return p }
func (p *interleaveProtocol) Setup() []Step {
	if p.mode == "blocked" {
		return nil
	}
	return []Step{{Command: Command{Name: "setup"}, Check: func(payload any) error {
		if payload != "ok" {
			return fmt.Errorf("setup payload: %v", payload)
		}
		return nil
	}}}
}

func (p *interleaveProtocol) BeginTurn(TurnSpec) []Step {
	if p.mode == "blocked" {
		return []Step{{Command: Command{Name: "blocked", Payload: strings.Repeat("x", 1<<20)}, Prompt: true}, {Command: Command{Name: "forbidden"}}}
	}
	return []Step{{Command: Command{Name: "prompt-" + p.mode}, Prompt: true}}
}
func (p *interleaveProtocol) CancelSequence() []Command { return []Command{{Name: "abort"}} }
func (p *interleaveProtocol) Encode(id string, c Command) ([]byte, error) {
	if c.Name == "blocked" {
		payload, ok := c.Payload.(string)
		if !ok {
			return nil, errors.New("invalid payload")
		}
		return []byte(id + " " + payload + "\n"), nil
	}
	return []byte(id + " " + c.Name + "\n"), nil
}

func (p *interleaveProtocol) Decode(line []byte) (Decoded, error) {
	select {
	case p.decoded <- struct{}{}:
	default:
	}
	s := string(line)
	if strings.HasPrefix(s, "response ") {
		return Decoded{Response: &Response{ID: strings.TrimPrefix(s, "response "), Payload: "ok"}}, nil
	}
	switch s {
	case "end":
		return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: "test"}}}}, nil
	case "failure":
		return Decoded{Events: []Event{{Kind: EventFailure, Failure: &Failure{Reason: "decoded failure"}}}}, nil
	default:
		return Decoded{Events: []Event{{Kind: EventAnswerDelta, Text: s}}}, nil
	}
}

type wireTrace struct {
	mu      sync.Mutex
	entries []Transition
	changed chan struct{}
}

func (w *wireTrace) observe(tr Transition) {
	tr.Data = bytes.Clone(tr.Data)
	w.mu.Lock()
	w.entries = append(w.entries, tr)
	w.mu.Unlock()
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

func (w *wireTrace) snapshot() []Transition {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Transition(nil), w.entries...)
}

func (w *wireTrace) await(ctx context.Context, kind string) bool {
	for {
		for _, tr := range w.snapshot() {
			if tr.Kind == kind {
				return true
			}
		}
		select {
		case <-w.changed:
		case <-ctx.Done():
			return false
		}
	}
}

func checkWire(t *testing.T, seed int, entries []Transition) {
	t.Helper()
	// Keep incomplete frames between syscall chunks. An incomplete frame may only
	// be abandoned by killing that generation, never completed with abort bytes.
	wire := map[uint64][]byte{}
	cancelled := map[uint64]bool{}
	for _, tr := range entries {
		switch tr.Kind {
		case "cancel":
			cancelled[tr.Generation] = true
		case "write":
			if cancelled[tr.Generation] {
				if len(wire[tr.Generation]) != 0 {
					t.Fatalf("seed %d wrote after cancelling partial frame", seed)
				}
				if !bytes.Contains(tr.Data, []byte("abort")) {
					t.Fatalf("seed %d work after cancel: %q", seed, tr.Data)
				}
			}
			wire[tr.Generation] = append(wire[tr.Generation], tr.Data...)
			for {
				i := bytes.IndexByte(wire[tr.Generation], '\n')
				if i < 0 {
					break
				}
				frame := wire[tr.Generation][:i]
				wire[tr.Generation] = wire[tr.Generation][i+1:]
				if bytes.Contains(frame, []byte("forbidden")) {
					t.Fatalf("seed %d wrote queued command after blocked frame", seed)
				}
			}
		}
	}
}

func assertTerminal(t *testing.T, ctx context.Context, seed int, turn Turn) {
	t.Helper()
	out, err := turn.Wait(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("seed %d Wait bound", seed)
	}
	count := 0
	for ev := range turn.Events() {
		if count > 0 {
			t.Fatalf("seed %d event after terminal", seed)
		}
		if ev.Kind == EventEnd {
			count++
			if err != nil || ev.End.Model != out.Model {
				t.Fatalf("seed %d Wait disagrees: %v", seed, err)
			}
		}
		if ev.Kind == EventFailure {
			count++
			if ev.Failure.Reason == "cancelled" {
				if !errors.Is(err, ErrCancelled) {
					t.Fatalf("seed %d cancellation mismatch %v", seed, err)
				}
			} else if err == nil || err.Error() != ev.Failure.Reason {
				t.Fatalf("seed %d failure mismatch %v", seed, err)
			}
		}
	}
	if count != 1 {
		t.Fatalf("seed %d terminal count %d", seed, count)
	}
}

func TestRandomizedInterleaving(t *testing.T) {
	before := runtime.NumGoroutine()
	for seed := range 2000 {
		random := rand.New(rand.NewSource(int64(seed))) //nolint:gosec // reproducible schedule
		mode := []string{"end", "failure", "hold", "stale", "exit", "blocked", "flood"}[random.Intn(5)]
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		p := &interleaveProtocol{mode: mode, decoded: make(chan struct{}, 1)}
		trace := &wireTrace{changed: make(chan struct{}, 1)}
		base := Base{ShutdownTimeout: 10 * time.Millisecond, Observe: trace.observe}
		if mode == "hold" && random.Intn(2) == 0 {
			base.Tools = func(context.Context, SessionInfo) (ToolSession, ToolBridge, error) {
				tools := &pendingToolSession{began: make(chan struct{}), ending: make(chan struct{}, 4), release: make(chan struct{})}
				return tools, ToolBridge{}, nil
			}
		}
		m := NewSessionManager(base, p)
		for turnIndex := range 2 {
			turn, err := m.Run(ctx, TurnSpec{ConversationID: "seed"})
			if err != nil {
				t.Fatalf("seed %d turn %d Run: %v", seed, turnIndex, err)
			}
			if mode == "hold" || mode == "blocked" || mode == "flood" || random.Intn(3) == 0 {
				if turnIndex == 0 && random.Intn(2) == 0 && !trace.await(ctx, "prompt-written") {
					t.Fatalf("seed %d prompt not written", seed)
				}
				if err = turn.Cancel(ctx); err != nil {
					t.Fatalf("seed %d Cancel: %v", seed, err)
				}
			}
			out, waitErr := turn.Wait(ctx)
			if waitErr == nil && out.Model == "test" {
				owned, ok := turn.(*actorTurn)
				if !ok {
					t.Fatal("expected actor turn")
				}
				prompt := false
				for _, tr := range trace.snapshot() {
					if tr.Kind == "prompt-written" && tr.Turn == owned.number {
						prompt = true
					}
				}
				if !prompt {
					t.Fatalf("seed %d turn %d stale end before prompt", seed, turnIndex)
				}
			}
			if errors.Is(waitErr, context.DeadlineExceeded) {
				t.Fatalf("seed %d turn %d mode %s bound trace=%+v", seed, turnIndex, mode, trace.snapshot())
			}
			assertTerminal(t, ctx, seed, turn)
		}
		if err := m.Close(ctx); err != nil {
			t.Fatalf("seed %d Close: %v", seed, err)
		}
		entries := trace.snapshot()
		checkWire(t, seed, entries)
		decisions := map[uint64]string{}
		for _, tr := range entries {
			if tr.Kind == "cancel" && decisions[tr.Turn] == "" {
				decisions[tr.Turn] = "cancel"
			}
			if tr.Kind == "terminal-taken" && decisions[tr.Turn] == "" {
				decisions[tr.Turn] = string(tr.Data)
			}
		}
		// The selected terminal is observable separately from publication so
		// endpoint settlement cannot hide cancellation/end precedence.
		for _, tr := range entries {
			if tr.Kind == "terminal-taken" && decisions[tr.Turn] == "cancel" && string(tr.Data) != string(EventFailure) {
				t.Fatalf("seed %d cancel lost precedence", seed)
			}
		}
		// A run cancelled during startup may have no wire. Otherwise there must be
		// actual write observations; Encode-only instrumentation cannot satisfy this.
		wrote, prompt := false, false
		for _, tr := range entries {
			wrote = wrote || tr.Kind == "write"
			prompt = prompt || tr.Kind == "prompt-written"
		}
		if prompt && !wrote {
			t.Fatalf("seed %d prompt without syscall bytes", seed)
		}
		stop()
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutine leak: before %d after %d", before, after)
	}
}

func TestWaitUsesPublishedTerminal(t *testing.T) {
	tkn := &actorTurn{session: newSession(Base{}, shellProtocol{}), events: make(chan Event, 65), done: make(chan struct{})}
	publish(tkn, Event{Kind: EventEnd, End: &End{Model: "published"}})
	if err := tkn.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := tkn.Wait(context.Background())
	if err != nil || out.Model != "published" {
		t.Fatalf("Wait rewrote terminal: %+v %v", out, err)
	}
}

func TestStalledConsumerCancellation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	p := &interleaveProtocol{mode: "flood", decoded: make(chan struct{}, 1)}
	m := NewSessionManager(Base{ShutdownTimeout: 5 * time.Millisecond}, p)
	defer func() { _ = m.Close(ctx) }()
	turn, err := m.Run(ctx, TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	// Observe the bounded delivery queue full, not a duration.
	owned, ok := turn.(*actorTurn)
	if !ok {
		t.Fatal("expected actor turn")
	}
	for len(owned.events) < 64 {
		select {
		case <-ctx.Done():
			t.Fatal("queue did not fill")
		default:
			runtime.Gosched()
		}
	}
	if err = turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = turn.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
	terminals := 0
	for ev := range turn.Events() {
		if terminals > 0 {
			t.Fatal("event after terminal")
		}
		if ev.Kind == EventFailure {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals=%d", terminals)
	}
}
