package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

type registrationTestOpener struct {
	helperOpenTestOpener
	sequence uint64
}

func (o *registrationTestOpener) RegisterReconnect(_ context.Context, _ session.Config, seq uint64) error {
	o.sequence = seq
	return nil
}

func TestPaneReconnect_DTOConformsToContract(t *testing.T) {
	raw, err := json.Marshal(paneReconnectResult{helperOpenPane, true})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "pane.reconnect.schema.json"), raw, "registered")
}

func TestPaneReconnect_OverTheWireConformsToContract(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	helper := &registrationTestOpener{}
	ws.opener.helper = helper
	ws.resolver.set(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}})
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	ws.bindPane(helperOpenPane, wc, newConnState(), openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "p", Cols: 80, Rows: 24})
	payload := PaneConnection{Generation: 1, PaneID: helperOpenPane, ConnectionEpoch: 1, ConnectionSequence: 7, Helper: PaneConnectionHelper{State: "serving", Action: "none"}, Reconnect: &PaneConnectionReconnect{EndsShells: 1}}
	ws.PublishPaneConnection(payload)
	resp := vaultCall(t, conn, "pane.reconnect", map[string]any{"paneId": helperOpenPane, "connectionEpoch": 1}, 1)
	if resp.Error != nil {
		t.Fatalf("registration: %+v", resp.Error)
	}
	validateJSON(t, loadSchema(t, "pane.reconnect.schema.json"), resp.Result, "wire registration")
	if helper.sequence != 7 {
		t.Fatalf("claimed sequence=%d, want 7", helper.sequence)
	}
}

type closingPaneTestOpener struct {
	helperOpenTestOpener
	entered, resume chan struct{}
}

func (o *closingPaneTestOpener) OpenHosted(ctx context.Context, cfg session.Config, claim string) (HostedSessionOpen, bool, error) {
	close(o.entered)
	<-o.resume
	// An external spawn may have completed even after the pane was removed.
	return o.helperOpenTestOpener.OpenHosted(ctx, cfg, claim)
}

func TestPaneClosedWhileItsOpenCompletesLeavesNoSession(t *testing.T) {
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
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "open", "params": map[string]any{"kind": "ssh", "profileId": "p", "paneId": helperOpenPane, "cols": 80, "rows": 24}})
	if err := conn.WriteMessage(1, req); err != nil {
		t.Fatal(err)
	}
	<-helper.entered
	ws.closeSessionsForPanes(map[string]struct{}{helperOpenPane: {}})
	close(helper.resume)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var resp struct {
			ID int `json:"id"`
		}
		if err = json.Unmarshal(raw, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.ID == 1 {
			break
		}
	}
	if got := ws.registry.List(); len(got) != 0 {
		t.Fatalf("closed pane retained %d sessions", len(got))
	}
	if ws.paneRecordOf(helperOpenPane) != nil {
		t.Fatal("closed pane was rebound")
	}
}
