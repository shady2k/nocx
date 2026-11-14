package harness

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Gate the forwarding select itself: no sleeps or scheduler timing decide when
// the full queue gains space relative to cancellation.
func TestBaseTerminalPublicationCancellation(t *testing.T) {
	for _, kind := range []EventKind{EventEnd, EventFailure} {
		for _, order := range []string{"publish-first", "cancel-first", "both-ready"} {
			t.Run(string(kind)+"/"+order, func(t *testing.T) {
				stdinR, stdinW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stdinR.Close() }()
				defer func() { _ = stdinW.Close() }()
				stdoutR, stdoutW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stdoutR.Close() }()
				defer func() { _ = stdoutW.Close() }()
				p := &Process{
					stdin: stdinW, stdout: stdoutR, reaped: true,
					events: make(chan Event, 2), stopped: make(chan struct{}), done: make(chan struct{}),
				}
				p.events <- Event{Kind: EventAnswerDelta, Text: "first"}
				p.events <- Event{Kind: EventAnswerDelta, Text: "second"}
				usage := &Usage{InputTokens: 17, OutputTokens: 23}
				ev := Event{Kind: kind, End: &End{Model: "published-model", Usage: usage}, Failure: &Failure{Reason: "published failure"}}
				entered, release := make(chan struct{}), make(chan struct{})
				published := make(chan bool, 1)
				go func() { close(entered); <-release; published <- p.forward(ev) }()
				<-entered
				var sent bool
				switch order {
				case "publish-first":
					<-p.events
					close(release)
					sent = <-published
					if !sent {
						t.Fatal("publication lost before cancellation")
					}
					_ = p.Cancel(context.Background())
				case "cancel-first":
					_ = p.Cancel(context.Background())
					close(release)
					sent = <-published
					if sent {
						t.Fatal("publication succeeded with full queue")
					}
				case "both-ready":
					_ = p.Cancel(context.Background())
					<-p.events
					close(release) // Both select arms are ready before forward starts.
					sent = <-published
					if sent {
						t.Fatal("publication succeeded after cancellation before entry")
					}
				}
				var terminal *Event
				if sent {
					terminal = &ev
				}
				p.finish(terminal, "stdout closed by cancellation")
				var last Event
				terminals := 0
				for event := range p.Events() {
					last = event
					if event.Kind == EventEnd || event.Kind == EventFailure {
						terminals++
					}
				}
				if terminals != 1 {
					t.Fatalf("terminal count=%d", terminals)
				}
				outcome, waitErr := p.Wait(context.Background())
				if sent && kind == EventEnd {
					if last.Kind != EventEnd || waitErr != nil || outcome.Model != ev.End.Model || outcome.Usage != usage {
						t.Fatalf("published End: last=%+v outcome=%+v error=%v", last, outcome, waitErr)
					}
				} else {
					reason := "cancelled"
					if sent {
						reason = ev.Failure.Reason
					}
					if last.Kind != EventFailure || last.Failure.Reason != reason || waitErr == nil || waitErr.Error() != reason && !errors.Is(waitErr, ErrCancelled) {
						t.Fatalf("published Failure: last=%+v error=%v want=%q", last, waitErr, reason)
					}
					if errors.Is(waitErr, ErrCancelled) != (reason == "cancelled") {
						t.Fatalf("cancellation error=%v reason=%q", waitErr, reason)
					}
				}
			})
		}
	}
}

func TestBasePublishedTerminalCleanupFailure(t *testing.T) {
	for _, kind := range []EventKind{EventEnd, EventFailure} {
		p := &Process{events: make(chan Event, 1), done: make(chan struct{}), cleanupErr: errors.New("cleanup failed")}
		ev := Event{Kind: kind, End: &End{Model: "test"}, Failure: &Failure{Reason: "provider failed"}}
		if !p.forward(ev) {
			t.Fatal("terminal was not published")
		}
		p.finish(&ev, "")
		outcome, err := p.Wait(context.Background())
		if kind == EventEnd && (err != nil || outcome.Model != "test") || kind == EventFailure && (err == nil || err.Error() != "provider failed") {
			t.Fatalf("kind=%s outcome=%+v error=%v", kind, outcome, err)
		}
		if len(p.events) != 1 {
			t.Fatal("cleanup appended a second terminal")
		}
	}
}
