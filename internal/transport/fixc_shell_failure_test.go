package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/waittest"
)

// A retry must reach the actual hosted-session adoption, not merely acknowledge
// the button. Registration is a forbidden operation while the socket is healthy.
type fixcHostedOpener struct {
	reg    *session.Reg
	id     session.ID
	driver *panedriver.Driver
}

func (o *fixcHostedOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	generation := o.driver.Bind(cfg.PaneID, "conn-1")
	BindPaneConnection(ctx, generation)
	s, err := o.reg.Adopt(ctx, cfg, o.id, &helperOpenTestChannel{done: make(chan struct{})})
	if err == nil {
		o.driver.AdoptSession(cfg.PaneID, "conn-1", string(s.ID()), "instance-1", generation)
	}
	return HostedSessionOpen{Session: s, Host: cfg.Host, Account: "alice", Generation: "fixc"}, true, err
}

func (*fixcHostedOpener) RegisterReconnect(context.Context, session.Config, uint64) error {
	return errors.New("healthy shared connection must not be reconnected to retry a shell")
}

type fixcFailureFrame struct {
	PaneID    string `json:"paneId"`
	Epoch     uint64 `json:"connectionEpoch"`
	SessionID string `json:"sessionId"`
	Transport struct {
		State string `json:"state"`
	} `json:"transport"`
	Failure *struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	} `json:"shellFailure"`
	Offer *struct {
		EndsShells int `json:"endsShells"`
	} `json:"reconnect"`
}

func TestReplacementShellFailureRetainsActionableRecoveryOnHealthyTransport(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	const replacementID = session.ID("11111111111111111111111111111119")
	const sharedID = session.ID("22222222222222222222222222222229")
	var sealed atomic.Bool
	sealed.Store(true)
	driver := panedriver.New("instance-1", logger)
	ws := NewWSServer(logger, reg, WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		if sealed.Load() {
			return "", nil, vault.ErrVaultSealed
		}
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}}), WithHelperSessionOpener(&fixcHostedOpener{reg: reg, id: replacementID, driver: driver}), WithPaneConnectionPolicy(driver))
	driver.SetAction(func(ctx context.Context, pane string, binding uint64) error {
		return ws.ReconnectPane(ctx, pane, binding)
	})
	driver.SetPublisher(func(s panedriver.Snapshot) {
		shells := s.Transport.PlainShells
		p := PaneConnection{PaneID: s.PaneID, ConnectionID: s.ConnectionID, ConnectionEpoch: s.ConnectionEpoch, InstanceID: s.InstanceID, SessionID: s.SessionID, Transport: PaneConnectionTransport{State: s.Transport.State, StateMs: s.Transport.StateMs, PlainShells: &shells}, Attempt: PaneConnectionAttempt{Shown: "none", Candidate: "none"}, ObservedAt: "2026-11-14T11:00:00Z"}
		if s.Reconnect != nil {
			p.Reconnect = &PaneConnectionReconnect{EndsShells: s.Reconnect.EndsShells}
		}
		if s.ShellFailure != nil {
			p.ShellFailure = &PaneShellFailure{Code: s.ShellFailure.Code, Message: s.ShellFailure.Message, Data: s.ShellFailure.Data}
		}
		ws.PublishPaneConnection(p)
	})
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := soleWSConn(t, ws)
	state := newConnState()
	ws.bindPane(helperOpenPane, wc, state, openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24})
	sharedChannel := newExitEndableChannel()
	shared, err := reg.Adopt(ctx, session.Config{Kind: session.KindRemote, PaneID: "other-pane"}, sharedID, sharedChannel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.EndSession(sharedID) }()
	state.add(shared)
	initialBinding := driver.Bind(helperOpenPane, "conn-1")
	ws.paneRecordOf(helperOpenPane).bindingGeneration = initialBinding
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 1, State: proto.ConnectionConnected})
	_ = readNotification(t, conn, "pane.connection", wantWithin)

	// This is the real post-return shell action, not a constructed failure DTO.
	if err := ws.ReconnectPane(ctx, helperOpenPane, initialBinding); err == nil {
		t.Fatal("sealed vault unexpectedly opened shell")
	}
	readFailure := func() fixcFailureFrame {
		t.Helper()
		raw := readNotification(t, conn, "pane.connection", wantWithin)
		validateJSON(t, loadSchema(t, "pane.connection.schema.json"), raw, "replacement shell failure")
		var f fixcFailureFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		if f.Transport.State != "connected" || f.Failure == nil || f.Offer == nil {
			t.Fatalf("shell failure is not actionable on healthy transport: %s", raw)
		}
		if f.Failure.Code != -32001 || f.Failure.Data["reason"] != "vault-sealed" || f.Failure.Message == "" {
			t.Fatalf("vault unlock refusal lost: %s", raw)
		}
		if f.Offer.EndsShells != 0 {
			t.Fatalf("shell retry advertises disconnect: %s", raw)
		}
		return f
	}
	failed := readFailure()
	response := vaultCall(t, conn, "pane.reconnect", map[string]any{"paneId": helperOpenPane, "connectionEpoch": failed.Epoch}, 401)
	if response.Error != nil && response.Error.Code != -32001 {
		t.Fatalf("shell retry used supervisor instead of open path: %+v", response.Error)
	}
	failed = readFailure()
	if failed.Epoch <= 1 {
		t.Fatal("failure did not advance pane observation")
	}
	// A retained shell error does not turn a genuinely lost socket into a
	// shell-only retry: the real transport observation owns that choice.
	sealed.Store(false)
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 2, State: proto.ConnectionEnded, Cause: "lost-link"})
	rawLost := readNotification(t, conn, "pane.connection", wantWithin)
	var lost fixcFailureFrame
	if err := json.Unmarshal(rawLost, &lost); err != nil {
		t.Fatal(err)
	}
	response = vaultCall(t, conn, "pane.reconnect", map[string]any{"paneId": helperOpenPane, "connectionEpoch": lost.Epoch}, 403)
	if response.Error == nil || !strings.Contains(response.Error.Message, "healthy shared connection must not be reconnected") {
		t.Fatalf("genuine transport loss bypassed supervisor: %+v", response)
	}
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 3, State: proto.ConnectionConnected})
	failed = readFailure()
	response = vaultCall(t, conn, "pane.reconnect", map[string]any{"paneId": helperOpenPane, "connectionEpoch": failed.Epoch}, 402)
	if response.Error != nil {
		t.Fatalf("recovery refused: %+v", response.Error)
	}
	for {
		raw := readNotification(t, conn, "pane.connection", wantWithin)
		var success fixcFailureFrame
		if err := json.Unmarshal(raw, &success); err != nil {
			t.Fatal(err)
		}
		if success.SessionID == string(replacementID) {
			validateJSON(t, loadSchema(t, "pane.connection.schema.json"), raw, "shell recovery success")
			if success.Failure != nil || success.Offer != nil || success.Transport.State != "connected" {
				t.Fatalf("successful adoption retained failure or rewrote transport: %s", raw)
			}
			break
		}
	}
	waittest.WaitFor(t, "replacement shell adopted", func() bool {
		for _, s := range reg.List() {
			if s.ID() == replacementID {
				return true
			}
		}
		return false
	})
	if _, err := shared.Write([]byte("still usable")); err != nil {
		t.Fatalf("shared healthy shell stopped working: %v", err)
	}
	closed, ended := sharedChannel.state()
	if closed || ended {
		t.Fatal("shell recovery closed another pane on healthy connection")
	}
	state.mu.Lock()
	retained := state.sessions[replacementID]
	state.mu.Unlock()
	if retained == nil || retained.PaneID() != helperOpenPane {
		t.Fatal("opened replacement never became pane-owned session")
	}
}

func TestOldReplacementFailureCannotPoisonReusedPane(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	driver := panedriver.New("instance", logger)
	ws := NewWSServer(logger, reg, WithPaneConnectionPolicy(driver), WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		close(entered)
		<-release
		return "", nil, vault.ErrVaultSealed
	}}))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	wc := &wsConn{}
	params := openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
	ws.bindPane(helperOpenPane, wc, newConnState(), params)
	initialBinding := driver.Bind(helperOpenPane, "conn-1")
	ws.paneRecordOf(helperOpenPane).bindingGeneration = initialBinding
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 1, State: proto.ConnectionConnected})
	result := make(chan error, 1)
	go func() { result <- ws.ReconnectPane(ctx, helperOpenPane, initialBinding) }()
	waittest.WaitFor(t, "old replacement reached resolver", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	ws.unbindPane(helperOpenPane)
	ws.bindPane(helperOpenPane, wc, newConnState(), params)
	generation := driver.Bind(helperOpenPane, "conn-1")
	driver.AdoptSession(helperOpenPane, "conn-1", "new-shell", "instance", generation)
	unblock()
	waittest.WaitFor(t, "old replacement finished", func() bool { return len(result) == 1 })
	<-result
	// A fresh positive observation after the old action finished catches a stale
	// failure stored silently as well as one published immediately.
	observed := make(chan panedriver.Snapshot, 1)
	driver.SetPublisher(func(s panedriver.Snapshot) { observed <- s })
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 2, State: proto.ConnectionConnected})
	snap := <-observed
	if snap.SessionID != "new-shell" || snap.ShellFailure != nil || snap.Reconnect != nil {
		t.Fatalf("old failure poisoned new pane: %+v", snap)
	}
}

func TestFailedFirstOpenRecoversToFirstOwnedShell(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	driver := panedriver.New("instance", logger)
	const firstID = session.ID("33333333333333333333333333333339")
	ws := NewWSServer(logger, reg, WithPaneConnectionPolicy(driver), WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}}), WithHelperSessionOpener(&fixcHostedOpener{reg: reg, id: firstID, driver: driver}))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	state := newConnState()
	ws.bindPane(helperOpenPane, soleWSConn(t, ws), state, openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24})
	initialBinding := driver.Bind(helperOpenPane, "conn-1")
	ws.paneRecordOf(helperOpenPane).bindingGeneration = initialBinding
	result := make(chan error, 1)
	driver.SetAction(func(ctx context.Context, pane string, binding uint64) error {
		err := ws.ReconnectPane(ctx, pane, binding)
		result <- err
		return err
	})
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 1, State: proto.ConnectionFailed, Cause: "auth-rejected"})
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 2, State: proto.ConnectionOpening, Stage: "tcp"})
	driver.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "conn-1", Seq: 3, State: proto.ConnectionConnected})
	waittest.WaitFor(t, "first recovered shell owned by pane", func() bool { state.mu.Lock(); defer state.mu.Unlock(); return state.sessions[firstID] != nil })
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	sessions := reg.List()
	if len(sessions) != 1 || sessions[0].ID() != firstID || sessions[0].PaneID() != helperOpenPane {
		t.Fatalf("first recovery did not adopt exactly one shell: %v", sessions)
	}
	if _, err := sessions[0].Write([]byte("first input")); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentOpenCannotEnterBusyReconnectLifetime(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	ws := NewWSServer(logger, reg)
	ws.lifetimeCtx = ctx
	wc := &wsConn{}
	state := newConnState()
	params := openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
	ws.bindPane(helperOpenPane, wc, state, params)
	current := ws.paneRecordOf(helperOpenPane)
	const id = session.ID("44444444444444444444444444444449")
	channel := newExitEndableChannel()
	shell, err := reg.Adopt(ctx, session.Config{Kind: session.KindRemote, PaneID: helperOpenPane}, id, channel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.EndSession(id) }()
	state.add(shell)
	ws.paneMu.Lock()
	current.busy = true
	ws.paneMu.Unlock()
	competing := params
	competing.ProfileID = "different-host"
	rec, err := ws.bindPaneForOpen(ctx, helperOpenPane, wc, newConnState(), competing)
	if !errors.Is(err, errPaneReconnectBusy) || rec != nil {
		t.Fatalf("independent open admitted during reconnect: rec=%v err=%v", rec, err)
	}
	if ws.paneRecordOf(helperOpenPane) != current || current.ctx.Err() != nil || current.params.ProfileID != "saved" {
		t.Fatal("refused open mutated existing reconnect lifetime")
	}
	state.mu.Lock()
	retained := state.sessions[id]
	state.mu.Unlock()
	closed, ended := channel.state()
	if retained != shell || closed || ended {
		t.Fatal("refused open damaged existing shell")
	}
}
