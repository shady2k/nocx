package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
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
//
// THE FAR ARM ALONE, and deliberately: this is the route that has always
// worked, asserted on its own so that the local arm added beside it
// (helper_inventory_local_test.go, nocx-s8mfn) cannot quietly replace it. The
// daemon the entry comes from is farHelperStand, which both tests share.
func TestHelperSessionInventoryIsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	ctx := context.Background()
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	spawned := farHelperStand(t, a, coresession.ID("inventory-session"), "build.example.com", "deploy", "generation-a")

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
	if entry.HostSessionID.Session != spawned {
		t.Fatalf("host session id = %q, want spawned %q", entry.HostSessionID.Session, spawned)
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
