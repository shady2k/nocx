package harness

import (
	"context"
	"errors"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type interleaveProtocol struct {
	seed    int
	mu      sync.Mutex
	names   []string
	decoded chan struct{}
}

func (p *interleaveProtocol) Start(context.Context, TurnSpec) (Launch, error) {
	script := "while read x; do case \"$x\" in prompt) echo delta; echo end;; abort) echo end;; esac; done"
	switch p.seed % 5 {
	case 1:
		script = "while read x; do echo delta; done" // ignores abort
	case 2:
		script = "while :; do echo delta; done" // fills stdout
	case 3:
		script = "read x; exit 7"
	case 4:
		script = "while read x; do echo response; echo response; echo end; done"
	}
	return Launch{Argv: []string{"/bin/sh", "-c", script}}, nil
}

func (p *interleaveProtocol) Commands(TurnSpec) []Command {
	if p.seed%7 == 0 {
		return []Command{{Name: "blocked", Payload: strings.Repeat("x", 1<<20)}, {Name: "work-after-block"}}
	}
	return []Command{{Name: "prompt"}}
}

func (p *interleaveProtocol) Cancel() []Command {
	p.mu.Lock()
	p.names = append(p.names, "cancel-taken")
	p.mu.Unlock()
	return []Command{{Name: "abort"}}
}

func (p *interleaveProtocol) Encode(_ string, c Command) ([]byte, error) {
	p.mu.Lock()
	p.names = append(p.names, c.Name)
	p.mu.Unlock()
	if c.Name == "blocked" {
		payload, ok := c.Payload.(string)
		if !ok {
			return nil, errors.New("invalid test payload")
		}
		return []byte(payload), nil
	}
	return []byte(c.Name + "\n"), nil
}

func (p *interleaveProtocol) Decode(line []byte) (Decoded, error) {
	select {
	case p.decoded <- struct{}{}:
	default:
	}
	switch string(line) {
	case "end":
		p.mu.Lock()
		p.names = append(p.names, "end-taken")
		p.mu.Unlock()
		return Decoded{Events: []Event{{Kind: EventEnd, End: &End{Model: "test"}}}}, nil
	case "response":
		return Decoded{Response: &Response{ID: "stale-duplicate"}}, nil
	default:
		return Decoded{Events: []Event{{Kind: EventAnswerDelta, Text: "delta"}}}, nil
	}
}

func TestRandomizedInterleaving(t *testing.T) {
	before := runtime.NumGoroutine()
	for seed := range 2000 {
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		p := &interleaveProtocol{seed: seed, decoded: make(chan struct{}, 1)}
		m := NewSessionManager(Base{ShutdownTimeout: time.Millisecond * 5}, p)
		turn, err := m.Run(ctx, TurnSpec{ConversationID: "seed"})
		if err != nil {
			t.Fatal(err)
		}
		random := rand.New(rand.NewSource(int64(seed))) //nolint:gosec // reproducible schedule
		if seed%7 != 0 && seed%5 != 3 && random.Intn(2) == 0 {
			select {
			case <-p.decoded:
			case <-ctx.Done():
				t.Fatalf("seed %d no stdout", seed)
			}
		}
		if seed%5 == 0 && seed%7 != 0 && random.Intn(2) == 0 {
			// Establish end-first by publication, rather than by elapsed time.
			if _, err = turn.Wait(ctx); err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
		}
		if err = turn.Cancel(ctx); err != nil {
			t.Fatalf("seed %d Cancel: %v", seed, err)
		}
		outcome, waitErr := turn.Wait(ctx)
		if errors.Is(waitErr, context.DeadlineExceeded) {
			t.Fatalf("seed %d Wait bound", seed)
		}
		var last Event
		terminals := 0
		for ev := range turn.Events() {
			if terminals != 0 {
				t.Fatalf("seed %d event after terminal", seed)
			}
			last = ev
			if ev.Kind == EventEnd || ev.Kind == EventFailure {
				terminals++
			}
		}
		if terminals != 1 {
			t.Fatalf("seed %d terminals %d", seed, terminals)
		}
		if last.Kind == EventEnd {
			if waitErr != nil || outcome.Model != last.End.Model {
				t.Fatalf("seed %d Wait disagrees", seed)
			}
		} else {
			expected := last.Failure.Reason
			if expected == "cancelled" {
				if !errors.Is(waitErr, ErrCancelled) {
					t.Fatalf("seed %d Wait cancellation mismatch", seed)
				}
			} else if waitErr == nil || waitErr.Error() != expected {
				t.Fatalf("seed %d Wait failure mismatch", seed)
			}
		}
		if err = m.Close(ctx); err != nil {
			t.Fatalf("seed %d Close: %v", seed, err)
		}
		p.mu.Lock()
		cancelTaken := false
		firstDecision := ""
		for _, name := range p.names {
			if firstDecision == "" && (name == "cancel-taken" || name == "end-taken") {
				firstDecision = name
			}
			if name == "cancel-taken" {
				cancelTaken = true
			} else if cancelTaken && name != "abort" && name != "end-taken" {
				t.Fatalf("seed %d work after abort: %v", seed, p.names)
			}
		}
		if firstDecision == "cancel-taken" && !errors.Is(waitErr, ErrCancelled) {
			t.Fatalf("seed %d cancel-first outcome: %v", seed, waitErr)
		}
		if firstDecision == "end-taken" && last.Kind != EventEnd {
			t.Fatalf("seed %d end-first outcome: %+v", seed, last)
		}
		p.mu.Unlock()
		stop()
	}
	// Every session Close joins its pipe readers and reaper; no per-turn watcher.
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
	p := &interleaveProtocol{seed: 2, decoded: make(chan struct{}, 1)}
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
