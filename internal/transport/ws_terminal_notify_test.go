package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/outbound"
	"github.com/shady2k/nocx/internal/waittest"
)

// Done is consulted only when ordered admission must wait for room. This
// witness lets the test release the writer after admission really blocks.
type notifyWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *notifyWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func saturatedBlockSubscriber(t *testing.T) (*lifecycleTestEnv, session.ID, *blockingSocket, *outbound.Conn) {
	t.Helper()
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	e.ws.RegisterLifecycleLane("saturated-completion", sid)
	sock := &blockingSocket{released: make(chan struct{}), writeStarted: make(chan struct{})}
	out := outbound.New(sock, outbound.Config{QueueDepth: 1})
	t.Cleanup(func() { out.Close() })
	t.Cleanup(func() {
		select {
		case <-sock.released:
		default:
			close(sock.released)
		}
	})
	e.ws.getRx(sid).setSubscriber(&wsConn{out: out}, newConnState())
	if err := out.TryEnqueue(websocket.TextMessage, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	<-sock.writeStarted
	if err := out.TryEnqueue(websocket.TextMessage, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	return e, sid, sock, out
}

func awaitNotifyProducer(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(wantWithin):
		t.Fatal(what)
	}
}

// Every command in a pasted burst must start, complete, close its stored
// rows and receive its history receipt, even when the writer first stops.
// One producer models the authenticated lane's sequential commit callbacks;
// the independent outbound writer must drain it without a renderer ACK.
func TestTerminalBlockNotificationsSurviveRefreshableSaturation(t *testing.T) {
	e, sid, sock, out := saturatedBlockSubscriber(t)
	ctx := &notifyWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	done := make(chan struct{})
	const commands = 500
	go func() {
		defer close(done)
		for i := range commands {
			attempt := fmt.Sprintf("command-%d", i)
			for _, state := range []string{lifecyclepub.AttemptOpen, lifecyclepub.AttemptCompleted} {
				e.ws.PublishLifecycle(ctx, lifecyclepub.Fact{
					Lane: "saturated-completion", Lifecycle: lifecyclepub.LifecycleNative,
					Attempt: &lifecyclepub.Attempt{ID: attempt, State: state},
				})
			}
			e.ws.notifyBlockSubscriber(ctx, sid, "block.closed", blockClosedParams{EntryID: attempt})
			e.ws.publishHistoryRecorded(ctx, lifecyclepub.Fact{Lane: "saturated-completion"}, historyRecordedData{SessionID: sid, AttemptID: attempt})
		}
	}()
	awaitNotifyProducer(t, ctx.waiting, "notification did not wait on the full outbound queue")
	close(sock.released)
	awaitNotifyProducer(t, done, "notification producer did not finish after outbound capacity returned")
	waittest.WaitFor(t, "all pasted command notifications written", func() bool {
		sock.mu.Lock()
		defer sock.mu.Unlock()
		return len(sock.frames) >= 2+4*commands
	})
	sock.mu.Lock()
	frames := append([]outbound.Frame(nil), sock.frames...)
	sock.mu.Unlock()
	if len(frames) != 2+4*commands {
		t.Fatalf("frames = %d, want %d", len(frames), 2+4*commands)
	}
	for i := range commands {
		attempt := fmt.Sprintf("command-%d", i)
		for j, method := range []string{"lifecycle.changed", "lifecycle.changed", "block.closed", "history.recorded"} {
			frame := frames[2+4*i+j]
			params, ok := notificationParams(frame.Data, method)
			if !ok {
				t.Fatalf("command %d frame %d: want %s, got %s", i, j, method, frame.Data)
			}
			if j < 2 {
				var got lifecycleChangedParams
				if err := json.Unmarshal(params, &got); err != nil {
					t.Fatal(err)
				}
				state := lifecyclepub.AttemptOpen
				if j == 1 {
					state = lifecyclepub.AttemptCompleted
				}
				if got.Attempt == nil || got.Attempt.ID != attempt || got.Attempt.State != state {
					t.Fatalf("command %d: fact = %+v", i, got)
				}
			} else if j == 2 {
				var got blockClosedParams
				if err := json.Unmarshal(params, &got); err != nil {
					t.Fatal(err)
				}
				if got.EntryID != attempt {
					t.Fatalf("close = %q, want %q", got.EntryID, attempt)
				}
			} else {
				var got historyRecordedParams
				if err := json.Unmarshal(params, &got); err != nil {
					t.Fatal(err)
				}
				if got.AttemptID != attempt {
					t.Fatalf("receipt = %q, want %q", got.AttemptID, attempt)
				}
			}
		}
	}
	if out.StallCount() != 0 {
		t.Fatal("ordered notification raised outbound.stalled")
	}
}

func TestTerminalBlockNotificationsReleaseOnTeardown(t *testing.T) {
	for _, method := range []string{"lifecycle.changed", "block.closed", "history.recorded"} {
		for _, teardown := range []string{"cancel", "close"} {
			t.Run(method+"/"+teardown, func(t *testing.T) {
				e, sid, _, out := saturatedBlockSubscriber(t)
				base, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &notifyWaitContext{Context: base, waiting: make(chan struct{})}
				done := make(chan struct{})
				go func() {
					defer close(done)
					switch method {
					case "lifecycle.changed":
						e.ws.PublishLifecycle(ctx, lifecyclepub.Fact{Lane: "saturated-completion", Lifecycle: lifecyclepub.LifecycleNative})
					case "block.closed":
						e.ws.notifyBlockSubscriber(ctx, sid, method, blockClosedParams{EntryID: "command"})
					case "history.recorded":
						e.ws.publishHistoryRecorded(ctx, lifecyclepub.Fact{Lane: "saturated-completion"}, historyRecordedData{SessionID: sid, AttemptID: "command"})
					}
				}()
				awaitNotifyProducer(t, ctx.waiting, "notification did not wait on full outbound queue")
				if teardown == "cancel" {
					cancel()
				} else {
					out.Close()
				}
				awaitNotifyProducer(t, done, "notification producer remained blocked after teardown")
			})
		}
	}
}
