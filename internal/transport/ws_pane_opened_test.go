package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestPaneOpened_OverTheWireConformsToContract(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	driver := panedriver.New("instance", logger)
	first := newExitEndableChannel()
	helper := &fixdHostedOpener{reg: reg, driver: driver, replacement: first}
	ws := NewWSServer(logger, reg, WithPaneConnectionPolicy(driver), WithHelperSessionOpener(helper), WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}}))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := soleWSConn(t, ws)
	state := newConnState()
	params := openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24, DesiredMode: "raw"}
	ws.bindPane(helperOpenPane, wc, state, params)
	generation := driver.Bind(helperOpenPane, "conn-1")
	ws.paneRecordOf(helperOpenPane).bindingGeneration = generation
	var previous paneOpened
	for i := 0; i < 2; i++ {
		if err := ws.ReconnectPane(ctx, helperOpenPane, generation); err != nil {
			t.Fatal(err)
		}
		raw := readNotification(t, conn, "pane.opened", wantWithin)
		validateJSON(t, loadSchema(t, "pane.opened.schema.json"), raw, "replacement open")
		var opened paneOpened
		if err := json.Unmarshal(raw, &opened); err != nil {
			t.Fatal(err)
		}
		if opened.Generation <= generation || opened.Open.DesiredMode != "auto" || !opened.Open.AwaitsIntegration || opened.Open.WorkspaceID == "" {
			t.Fatalf("replacement facts: %+v", opened)
		}
		state.mu.Lock()
		owned := state.sessions[session.ID(opened.Open.SessionID)]
		state.mu.Unlock()
		if owned == nil {
			t.Fatal("replacement has no socket owner")
		}
		rx := ws.getRx(owned.ID())
		subscriber, _ := rx.getSubscriber()
		if subscriber != wc {
			t.Fatal("replacement subscriber missing")
		}
		// The integrated replacement's first session fact follows its open
		// answer on the same socket; nothing needs to claim the session.
		integration := readNotification(t, conn, "session.integrationChanged", wantWithin)
		var fact struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(integration, &fact); err != nil {
			t.Fatal(err)
		}
		if fact.SessionID != opened.Open.SessionID {
			t.Fatalf("integration belongs to %q", fact.SessionID)
		}
		if i == 1 {
			for _, s := range reg.List() {
				if string(s.ID()) == previous.Open.SessionID {
					t.Fatal("older replacement retained")
				}
			}
		}
		previous = opened
		generation = opened.Generation
	}
}

func TestPaneOpened_DTOConformsToContract(t *testing.T) {
	raw, err := json.Marshal(paneOpened{PaneID: helperOpenPane, Generation: 2, Open: openResult{SessionID: "0123456789abcdef0123456789abcdef", InstanceID: "instance", SessionEpoch: 1, WorkspaceID: "workspace", Cwd: "~", DesiredMode: "raw", EffectiveSize: sizeResult{Cols: 80, Rows: 24}}})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "pane.opened.schema.json"), raw, "replacement DTO")
}

func TestPaneClosedWhileReplacementOpensLeavesNoSession(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	reg, ok := ws.registry.(*session.Reg)
	if !ok {
		t.Fatal("test server registry is not a session.Reg")
	}
	helper := &closingPaneTestOpener{helperOpenTestOpener: helperOpenTestOpener{reg: reg, id: session.ID("0123456789abcdef0123456789abcdef")}, entered: make(chan struct{}), resume: make(chan struct{})}
	ws.opener.helper = helper
	ws.resolver.set(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}})
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	ws.bindPane(helperOpenPane, soleWSConn(t, ws), newConnState(), openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "p", Cols: 80, Rows: 24})
	ws.paneRecordOf(helperOpenPane).bindingGeneration = 1
	result := make(chan error, 1)
	go func() { result <- ws.ReconnectPane(context.Background(), helperOpenPane, 1) }()
	<-helper.entered
	ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
	close(helper.resume)
	if err := <-result; err == nil {
		t.Fatal("closed pane replacement succeeded")
	}
	if sessions := reg.List(); len(sessions) != 0 {
		t.Fatalf("closed pane retained %d sessions", len(sessions))
	}
}

func TestSocketGoneWhileReplacementOpensLeavesNoSession(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	reg, ok := ws.registry.(*session.Reg)
	if !ok {
		t.Fatal("test server registry is not a session.Reg")
	}
	helper := &closingPaneTestOpener{helperOpenTestOpener: helperOpenTestOpener{reg: reg, id: session.ID("0123456789abcdef0123456789abcdef")}, entered: make(chan struct{}), resume: make(chan struct{})}
	ws.opener.helper = helper
	ws.resolver.set(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}})
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	ws.bindPane(helperOpenPane, wc, newConnState(), openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "p", Cols: 80, Rows: 24})
	ws.paneRecordOf(helperOpenPane).bindingGeneration = 1
	result := make(chan error, 1)
	go func() { result <- ws.ReconnectPane(context.Background(), helperOpenPane, 1) }()
	<-helper.entered
	_ = conn.Close()
	// The socket teardown's own pane cancellation precedes completion.
	waittest.WaitFor(t, "pane removed with socket", func() bool { return ws.paneRecordOf(helperOpenPane) == nil })
	close(helper.resume)
	if err := <-result; err == nil {
		t.Fatal("disconnected replacement succeeded")
	}
	if sessions := reg.List(); len(sessions) != 0 {
		t.Fatalf("disconnected pane retained %d sessions", len(sessions))
	}
}

func TestReplacementNotificationRefusalClosesSession(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := session.New(logger, nil)
	ws := NewWSServer(logger, reg, WithHelperSessionOpener(&fixcHostedOpener{reg: reg, id: session.ID("0123456789abcdef0123456789abcdef"), driver: panedriver.New("instance", logger)}), WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}}))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := soleWSConn(t, ws)
	wc.out.Close()
	waittest.WaitFor(t, "closed socket unregistered", func() bool { ws.connsMu.Lock(); defer ws.connsMu.Unlock(); return len(ws.conns) == 0 })
	// Preserve a pane record to exercise enqueue refusal rather than lifetime
	// cancellation, which is covered separately by the socket-close test.
	ws.bindPane(helperOpenPane, wc, newConnState(), openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "p", Cols: 80, Rows: 24})
	ws.paneRecordOf(helperOpenPane).bindingGeneration = 1
	if err := ws.ReconnectPane(ctx, helperOpenPane, 1); err == nil {
		t.Fatal("closed queue accepted replacement")
	}
	if len(reg.List()) != 0 {
		t.Fatal("refused replacement left a session")
	}
}
