package transport

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
)

func TestWSServerStopDrainsSessionWorkAndRejectsNewWork(t *testing.T) {
	ws, db, stop := newAgentWSServer(t)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	sub := ws.methods["session.intent"].submission
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	if rej := sub.TrySubmit(context.Background(), control.Task{Key: "session-A", Run: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
	}}); rej != nil {
		t.Fatalf("submit running task: %+v", rej)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted task never started")
	}
	queued := make(chan error, 1)
	if rej := sub.TrySubmit(context.Background(), control.Task{Key: "session-A", Run: func(ctx context.Context) {
		queued <- ctx.Err()
	}}); rej != nil {
		t.Fatalf("submit queued task: %+v", rej)
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- ws.Stop(context.Background()) }()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel admitted session work")
	}
	if rej := sub.TrySubmit(context.Background(), control.Task{Key: "session-B", Run: func(context.Context) { t.Error("post-Stop work ran") }}); rej == nil {
		t.Fatal("Stop admitted new session work")
	}
	select {
	case err := <-stopResult:
		t.Fatalf("Stop returned before admitted work drained: %v", err)
	default:
	}
	close(release)
	released = true
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after admitted work drained")
	}
	select {
	case err := <-queued:
		if err != context.Canceled {
			t.Fatalf("queued task context error = %v, want canceled", err)
		}
	default:
		t.Fatal("Stop discarded queued admitted session work")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("content db close: %v", err)
	}
	stopped = true
}
