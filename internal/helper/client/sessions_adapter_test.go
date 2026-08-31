package client_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestSessionsMapsTheRealHelperResultIntoCoordinatorDTO(t *testing.T) {
	c := hostedSessions(t)
	var spawned proto.SpawnResult
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn,
		proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	entries, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Sessions returned %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.HostSessionID.Session != spawned.Entry.Session.Session {
		t.Fatalf("session id = %q, want %q", entry.HostSessionID.Session, spawned.Entry.Session.Session)
	}
	if entry.HostSessionID.Generation != string(spawned.Entry.Session.Generation) {
		t.Fatalf("generation = %q, want %q", entry.HostSessionID.Generation, spawned.Entry.Session.Generation)
	}
	if entry.Launch.Cwd != "/" {
		t.Fatalf("launch cwd = %q, want /", entry.Launch.Cwd)
	}
	if entry.Observed == nil {
		t.Fatal("observed is nil for a real helper inventory entry")
	}
	if entry.Observed.Unavailable == nil {
		t.Fatal("observed.unavailable is nil; coordinator DTO must preserve the explicit empty/list distinction")
	}
}
