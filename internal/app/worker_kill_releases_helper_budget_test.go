package app

// nocx-isjh4, gap 1 (coordinator review): worker lifecycle is exactly where
// orchestration opens and closes panes by the dozen, and it never released a
// helper session's window budget — Kill and workerCloser.Close both called
// sessionCloser.Close (detach-only). Both call sites already know the EXACT
// session they own (s.sess / p.Liveness.SessionID), so no pane-discovery is
// needed the way the layout handler's closeSessionsForPanes needs one: this
// test drives Kill through a real, in-process helper and asserts its budget
// is released.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
	"github.com/shady2k/nocx/internal/workspace"
)

func TestKillingAWorkerReleasesItsHelperSessionsWindowBudget(t *testing.T) {
	ctx := context.Background()
	logger := log.NewSlogAdapter(discardLogger(t))
	dir := t.TempDir()
	const gen = "2222222222222222bbbbbbbbbbbbbbbb"

	// The real, in-process daemon this worker's pane will be spawned on —
	// the same shape session_reconcile_local_test.go's fakeLocalEndpoint
	// uses everywhere else in this package, so this test drives the real
	// helper/session.Service rather than a double of it.
	ep := startFakeLocalEndpoint(t, dir, gen)

	reg := session.New(logger, nil) // no local ptf: every open reaches the helper
	opener := &localHelperOpener{
		log:      discardLogger(t),
		registry: reg,
		dir:      dir,
		installed: helperlocal.Installed{
			Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
		},
	}
	tp := transport.NewWSServer(logger, reg, transport.WithHelperSessionOpener(opener))
	t.Cleanup(func() { _ = tp.Stop(context.Background()) })

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enrol := newWorkerEnrolments(logger, reg)
	spawner := &workerSpawner{
		layout: db.Layout(), opener: tp, sessions: reg,
		enrolments: enrol, workspace: string(workspace.Default), log: logger,
	}

	spawned, err := spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "participant-under-test", Group: "group-under-test", Command: "/bin/true",
	})
	if err != nil {
		t.Fatalf("spawning a worker through the real helper: %v", err)
	}

	if used := ep.svc.WindowBytesInUse(); used == 0 {
		t.Fatal("the worker's pane opened but the helper reports no window budget committed")
	}

	if err := spawned.Kill(ctx); err != nil {
		t.Fatalf("killing the worker: %v", err)
	}

	if used := ep.svc.WindowBytesInUse(); used != 0 {
		t.Fatalf("window bytes in use = %d after killing the worker, want 0 — its helper session was not released", used)
	}
}
