package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperproto "github.com/shady2k/nocx/internal/helper/proto"
	helper "github.com/shady2k/nocx/internal/helper/session"
	coresession "github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestHelperSessionInventoryWithoutActiveHelperIsUnavailable(t *testing.T) {
	reg := &helperRegistry{hosts: make(map[coresession.ID]*hostHelper)}
	_, err := (&helperSessionInventories{registry: reg}).Sessions(context.Background())
	if err == nil {
		t.Fatal("inventory without an active helper returned an empty answer")
	}
}

// The helper session spawned below travels through the real helper client,
// the composition-root registry, and the app's real WebSocket. A transport
// harness with an injected inventory would prove only the last adapter.
func TestHelperSessionInventoryIsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	ctx := context.Background()
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := helper.New(helper.Options{
		Generation: "generation-a",
		Spawner:    helper.NewLocalSpawner(logger, helper.Shell{Path: "/bin/sh", Args: []string{"-i"}}),
		Inspector:  helper.NewInspector(),
		Log:        logger,
		Limits:     helper.DefaultLimits(),
	})
	serverConn, clientConn := net.Pipe()
	host := helperhost.New(serverConn, serverConn, "generation-a", "instance-a", logger)
	host.Register(svc)
	release := svc.Bind(host)
	serveDone := make(chan error, 1)
	go func() { serveDone <- host.Serve(ctx) }()

	c, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(clientConn),
		ExpectHash:  "generation-a",
		SentinelTTL: time.Second,
		Log:         logger,
	})
	if err != nil {
		release()
		svc.Close()
		_ = serverConn.Close()
		t.Fatalf("helper Dial: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		svc.Close()
		release()
		if err := <-serveDone; err != nil {
			t.Errorf("helper host: %v", err)
		}
	})

	var spawned helperproto.SpawnResult
	if err := c.Call(ctx, helperproto.ServiceSession, helperproto.OpSpawn,
		helperproto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	f := &sessionFactory{
		reg: a.helperRegistry, sid: coresession.ID("inventory-session"),
		host: "build.example.com", account: "deploy", expectHash: "generation-a",
	}
	a.helperRegistry.mu.Lock()
	a.helperRegistry.hosts[f.sid] = &hostHelper{f: f, client: c}
	a.helperRegistry.mu.Unlock()

	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { a.Shutdown(ctx) })
	conn := dialAppWS(t, a)
	t.Cleanup(func() { _ = conn.Close() })

	resp := callAppWS(t, conn, "sessions.inventory", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("sessions.inventory: %+v", resp.Error)
	}
	var result struct {
		Sessions []client.SessionEntry `json:"sessions"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode sessions.inventory: %v", err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one spawned session", result.Sessions)
	}
	entry := result.Sessions[0]
	if entry.HostSessionID.Session != spawned.Entry.Session.Session {
		t.Fatalf("host session id = %q, want spawned %q", entry.HostSessionID.Session, spawned.Entry.Session.Session)
	}
	if entry.HostSessionID.Generation != "generation-a" {
		t.Fatalf("generation = %q, want generation-a", entry.HostSessionID.Generation)
	}
	if entry.Observed == nil {
		t.Fatal("observed is nil for spawned helper session")
	}
	if entry.Observed.Unavailable == nil {
		t.Fatal("observed.unavailable is nil; the explicit observation shape was lost")
	}
}
