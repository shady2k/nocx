//go:build nocx_local_ssh

package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/transport"
)

func TestPaneConnection_OverTheWireConformsToContract(t *testing.T) {
	srv := startPasswordSFTPSSHServer(t, t.TempDir())
	proxy := sshtest.NewFaultProxy(t, srv.addr)
	srv.addr = proxy.Addr()
	driver := panedriver.New("wire-instance", nil)
	journey := startPaneJourneyWithSecrets(t, srv, &askRecorder{value: openPasswordFixturePassword}, func(source string, ev proto.ConnectionStateEvent) { driver.ApplyFrom(source, ev) })
	opener := journey.opener
	opener.paneConns = driver
	opener.reconnectPolicy = func() string { return "ask" }
	// The existing protocol peer has the same event callback the production
	// local-helper connection installs. The subscription is installed by open.
	ws := transport.NewWSServer(log.NewSlogAdapter(opener.log), opener.registry,
		transport.WithHelperSessionOpener(opener),
		transport.WithProfileResolver(liveHostedCwdResolver{host: srv.addr, config: &ssh.ConnectConfig{
			User: filesFixtureUser, AuthMode: "password", Secrets: &askRecorder{value: openPasswordFixturePassword}, SecretID: filesFixtureSecret,
			AuthorizedEndpoint: srv.addr, IntegrationMode: "raw", Shell: "bash",
		}}), transport.WithPaneConnectionPolicy(paneConnectionOwner{Driver: driver, opener: opener}))
	driver.SetPublisher(func(s panedriver.Snapshot) { ws.PublishPaneConnection(paneConnectionWire(s)) })
	driver.SetAction(func(ctx context.Context, pane string, binding uint64) error {
		return ws.ReconnectPane(ctx, pane, binding)
	})
	if err := ws.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := dialTraceWSServer(t, ws)
	const pane = "0190d3a0-1c2b-7def-8abc-0123456789ab"
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "open", "params": map[string]any{"kind": "ssh", "profileId": "wire", "paneId": pane, "cols": 80, "rows": 24}})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatal(err)
	}
	schema := loadAppContractSchema(t, "pane.connection.schema.json")
	seenOpening, seenSession := false, false
	var oldSession string
	for !seenSession {
		raw := waitForNotification(t, conn, "pane.connection")
		validateAppContract(t, schema, raw, "real helper connection")
		var got transport.PaneConnection
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.PaneID != pane || got.ConnectionID == "" {
			t.Fatalf("missing binding: %s", raw)
		}
		seenOpening = seenOpening || got.Transport.State == "opening"
		seenSession = got.Transport.State == "connected" && got.SessionID != ""
		if seenSession {
			oldSession = got.SessionID
		}
	}
	if !seenOpening {
		t.Fatal("real open did not publish its opening stages")
	}
	// A real reset reaches the helper supervisor, its subscription and the
	// pane offer. Pressing the wire action must create a different shell.
	proxy.Reset()
	proxy.HoldBanner()
	var epoch uint64
	for epoch == 0 {
		raw := waitForNotification(t, conn, "pane.connection")
		validateAppContract(t, schema, raw, "network loss")
		var got transport.PaneConnection
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Transport.State == "ended" && got.Reconnect != nil {
			epoch = got.ConnectionEpoch
		}
	}
	reconnect, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "pane.reconnect", "params": map[string]any{"paneId": pane, "connectionEpoch": epoch}})
	if err := conn.WriteMessage(websocket.TextMessage, reconnect); err != nil {
		t.Fatal(err)
	}
	// A host which accepts TCP but never answers SSH must not hold the RPC
	// open. Keep its version banner behind a gate until registration is read.
	registrationSchema := loadAppContractSchema(t, "pane.reconnect.schema.json")
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("registration blocked on SSH banner: %v", err)
		}
		var envelope struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.ID != 2 {
			var notification struct {
				Method string                   `json:"method"`
				Params transport.PaneConnection `json:"params"`
			}
			if err := json.Unmarshal(raw, &notification); err == nil && notification.Method == "pane.connection" && notification.Params.SessionID != "" && notification.Params.SessionID != oldSession {
				t.Fatalf("new shell %s arrived while SSH banner was held", notification.Params.SessionID)
			}
			continue
		}
		if len(envelope.Error) > 0 {
			t.Fatalf("register: %s", envelope.Error)
		}
		validateAppContract(t, registrationSchema, envelope.Result, "real registration while banner held")
		break
	}
	proxy.Release()
	for {
		raw := waitForNotification(t, conn, "pane.connection")
		validateAppContract(t, schema, raw, "reconnect return")
		var got transport.PaneConnection
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Transport.State == "connected" && got.SessionID != "" && got.SessionID != oldSession {
			return
		}
	}
}

// waitForNotification reads until one notification of method arrives.
func waitForNotification(t *testing.T, conn *websocket.Conn, method string) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Method != method {
			continue
		}
		return envelope.Params
	}
}
