package transport

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

type reconnectEndBarrier struct {
	session.Registry
	old     session.ID
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *reconnectEndBarrier) EndSession(id session.ID) error {
	if id == r.old {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	return r.Registry.EndSession(id)
}

func TestOldReconnectTeardownCannotEraseReopenedPaneLifetime(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const oldID = session.ID("11111111111111111111111111111111")
	const newID = session.ID("22222222222222222222222222222222")
	barrier := &reconnectEndBarrier{Registry: reg, old: oldID, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()
	ws := NewWSServer(logger, barrier)
	ws.lifetimeCtx = ctx
	ws.opener = &sessionOpener{}
	wc := &wsConn{}
	oldState := newConnState()
	params := openParams{PaneID: "reused-pane"}
	ws.bindPane(params.PaneID, wc, oldState, params)
	oldChannel := newExitEndableChannel()
	old, err := reg.Adopt(ctx, session.Config{Kind: session.KindRemote, PaneID: params.PaneID}, oldID, oldChannel)
	if err != nil {
		t.Fatal(err)
	}
	oldState.add(old)
	result := make(chan error, 1)
	go func() { result <- ws.ReconnectPane(context.Background(), params.PaneID) }()
	waittest.WaitFor(t, "old shell teardown entered", func() bool {
		select {
		case <-barrier.entered:
			return true
		default:
			return false
		}
	})

	// Reuse both the renderer id and its connection state, as a layout close and
	// reopen on the same WebSocket does, while the old external close is parked.
	ws.unbindPane(params.PaneID)
	ws.bindPane(params.PaneID, wc, oldState, params)
	current := ws.paneRecordOf(params.PaneID)
	newChannel := newExitEndableChannel()
	fresh, err := reg.Adopt(ctx, session.Config{Kind: session.KindRemote, PaneID: params.PaneID}, newID, newChannel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.EndSession(newID) }()
	oldState.add(fresh)
	release()
	waittest.WaitFor(t, "old reconnect canceled", func() bool { return len(result) == 1 })
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("old reconnect result = %v, want canceled", err)
	}
	if ws.paneRecordOf(params.PaneID) != current || current.ctx.Err() != nil {
		t.Fatal("old reconnect replaced or canceled new pane binding")
	}
	oldState.mu.Lock()
	retained := oldState.sessions[newID]
	oldState.mu.Unlock()
	if retained != fresh {
		t.Fatal("old reconnect erased new lifetime's session ownership")
	}
	closed, ended := newChannel.state()
	if closed || ended {
		t.Fatal("old reconnect closed new lifetime's shell")
	}
}

func TestCanceledReconnectCannotReplaceNewPaneBinding(t *testing.T) {
	_, logger := logtest.New(t)
	ws := NewWSServer(logger, session.New(logger, nil))
	ws.lifetimeCtx = context.Background()
	wc := &wsConn{}
	state := newConnState()
	params := openParams{PaneID: "reused-pane"}
	oldCtx, cancel := context.WithCancel(context.Background())
	ws.bindPaneForOpen(oldCtx, params.PaneID, wc, state, params)
	ws.unbindPane(params.PaneID)
	cancel()
	current := ws.bindPaneForOpen(context.Background(), params.PaneID, wc, state, params)
	if current == nil {
		t.Fatal("new lifetime did not bind")
	}
	if stale := ws.bindPaneForOpen(oldCtx, params.PaneID, wc, state, params); stale != nil {
		t.Fatal("canceled old reconnect acquired binding")
	}
	if ws.paneRecordOf(params.PaneID) != current || current.ctx.Err() != nil {
		t.Fatal("canceled admission replaced or canceled new lifetime")
	}
}
