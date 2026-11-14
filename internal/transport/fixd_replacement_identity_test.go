package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

type fixdHostedOpener struct {
	reg         *session.Reg
	driver      *panedriver.Driver
	calls       atomic.Int32
	replacement *exitEndableChannel
}

func (o *fixdHostedOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	n := o.calls.Add(1)
	id := session.ID("55555555555555555555555555555559")
	channel := newExitEndableChannel()
	if n == 2 {
		id = "66666666666666666666666666666669"
		channel = o.replacement
	}
	if n > 2 {
		id = "77777777777777777777777777777779"
	}
	g := o.driver.Bind(cfg.PaneID, "conn-1")
	BindPaneConnection(ctx, g)
	s, err := o.reg.Adopt(ctx, cfg, id, channel)
	if err == nil {
		o.driver.AdoptSession(cfg.PaneID, "conn-1", string(id), "instance", g)
	}
	return HostedSessionOpen{Session: s, Host: cfg.Host, Account: "alice", Generation: "fixd"}, true, err
}

func TestQueuedReplacementCannotCloseNewIndependentShell(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	driver := panedriver.New("instance", logger)
	helper := &fixdHostedOpener{reg: reg, driver: driver, replacement: newExitEndableChannel()}
	ws := NewWSServer(logger, reg, WithPaneConnectionPolicy(driver), WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}}), WithHelperSessionOpener(helper))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := soleWSConn(t, ws)
	state := newConnState()
	params := openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	if _, err := h.openInto(ctx, wc, state, params, func(openResult) {}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	result := make(chan error, 1)
	driver.SetAction(func(actionCtx context.Context, pane string, expectedBinding uint64) error {
		close(entered)
		<-release
		err := ws.ReconnectPane(actionCtx, pane, expectedBinding)
		result <- err
		return err
	})
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 1, State: proto.ConnectionEnded, Cause: "lost-link"})
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 2, State: proto.ConnectionConnected})
	waittest.WaitFor(t, "queued action launched", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	opened, err := h.openInto(ctx, wc, state, params, func(openResult) {})
	if err != nil {
		t.Fatal(err)
	}
	var newer session.Session
	for _, s := range reg.List() {
		if string(s.ID()) == opened.SessionID {
			newer = s
		}
	}
	if newer == nil {
		t.Fatal("independent open did not adopt a shell")
	}
	unblock()
	waittest.WaitFor(t, "queued action finished", func() bool { return len(result) == 1 })
	err = <-result
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("stale action refused: %v", err)
	}
	closed, ended := helper.replacement.state()
	if closed || ended {
		t.Fatal("queued old replacement closed newer independent shell")
	}
	if helper.calls.Load() != 2 {
		t.Fatal("queued old replacement opened another shell")
	}
	if _, err := newer.Write([]byte("new shell remains usable")); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	retained := state.sessions[newer.ID()]
	state.mu.Unlock()
	if retained != newer {
		t.Fatal("queued action erased newer pane ownership")
	}
}
