package app

import (
	"context"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestHostedCandidateRemainsPrivateUntilIdempotentPublish(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	source := fakeArtifacts{payload: syntheticPayload}
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), source.hash())
	app := bootLocalAppOn(t, source)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opener := app.localHelper
	client, generation, err := opener.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := session.Config{Kind: session.KindLocal, Cwd: "/", Cols: 80, Rows: 24}
	spawn := hostedSpawn{
		client: client, registry: opener.registry, lifecycle: opener.kernel,
		loss: opener.lifecycleLoss, publishScreen: opener.publishScreen,
		blockRows: opener.blockRows, environmentEntries: opener.environmentEntries,
		cursors: opener.lifecycleCursors, stopping: opener.lifecycleStopping, log: opener.log,
	}
	candidate, err := spawn.openCandidate(ctx, cfg, func(ctx context.Context, life *proto.LifecycleLaunch) (helperclient.SessionEntry, error) {
		return client.Spawn(ctx, proto.SpawnParams{Cwd: cfg.Cwd, Cols: cfg.Cols, Rows: cfg.Rows, Lifecycle: life})
	})
	if err != nil {
		t.Fatalf("open private candidate: %v", err)
	}
	sid := session.ID(candidate.entry.HostSessionID.Session)
	if _, err = app.Session.Get(sid); err == nil {
		t.Fatal("private candidate entered the session registry before publication")
	}
	if opener.holds(sid) {
		t.Fatal("private candidate entered helper-held public routing before publication")
	}

	result, err := candidate.Publish(ctx)
	if err != nil {
		t.Fatalf("publish candidate: %v", err)
	}
	open, err := opener.hostedOpenResult(cfg, generation, result)
	if err != nil {
		t.Fatalf("publish local projections: %v", err)
	}
	unpublished, err := spawn.openCandidate(ctx, cfg, func(ctx context.Context, life *proto.LifecycleLaunch) (helperclient.SessionEntry, error) {
		return client.Spawn(ctx, proto.SpawnParams{Cwd: cfg.Cwd, Cols: cfg.Cols, Rows: cfg.Rows, Lifecycle: life})
	})
	if err != nil {
		t.Fatalf("open abortable candidate: %v", err)
	}
	abortedID := session.ID(unpublished.entry.HostSessionID.Session)
	for range 2 {
		if err = unpublished.Abort(ctx); err != nil {
			t.Fatalf("abort candidate: %v", err)
		}
	}
	if _, err = app.Session.Get(abortedID); err == nil {
		t.Fatal("aborted candidate entered the session registry")
	}
	if opener.holds(abortedID) {
		t.Fatal("aborted candidate entered helper-held routing")
	}
	entries, err := client.Sessions(ctx)
	if err != nil {
		t.Fatalf("list helper sessions after abort: %v", err)
	}
	if len(entries) != 1 || entries[0].HostSessionID.Session != string(sid) {
		t.Fatalf("helper sessions after abort=%+v, want only the published session", entries)
	}
	if open.Session.ID() != sid {
		t.Fatalf("published session id=%s, candidate id=%s", open.Session.ID(), sid)
	}
	if _, err = app.Session.Get(sid); err != nil {
		t.Fatalf("published candidate is absent from registry: %v", err)
	}
	if !opener.holds(sid) {
		t.Fatal("published candidate was not entered into helper-held routing")
	}
	again, err := candidate.Publish(ctx)
	if err != nil || again.Session.ID() != sid {
		t.Fatalf("repeated publish returned session=%v err=%v", again.Session, err)
	}
}
