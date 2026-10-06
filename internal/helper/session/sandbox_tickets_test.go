package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

func sandboxCallError(t *testing.T, svc *session.Service, op string, params any) error {
	t.Helper()
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Call(context.Background(), op, encoded)
	return err
}

func TestSandboxConsumedRemoveNeverRespawnsAfterClose(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	prepared := call[proto.SandboxPrepareResult](t, svc, proto.OpSandboxPrepare, proto.SandboxPrepareParams{OperationID: "remove-1", LaunchID: "off-1", Mode: proto.SandboxOff, Cwd: t.TempDir()})
	request := proto.SandboxLaunchParams{Ticket: prepared.Ticket, OperationID: "remove-1", LaunchID: "off-1", Mode: proto.SandboxOff, Shape: proto.SandboxLaunchShape{Cols: 80, Rows: 24}}
	first := call[proto.SpawnResult](t, svc, proto.OpSandboxLaunch, request)
	call[proto.CloseSessionResult](t, svc, proto.OpCloseSession, proto.CloseSessionParams{Session: first.Entry.Session})
	repeated := call[proto.SpawnResult](t, svc, proto.OpSandboxLaunch, request)
	if repeated.Entry.Session != first.Entry.Session {
		t.Fatal("consumed removal produced a new session")
	}
	spawner.mu.Lock()
	attempts := len(spawner.reqs)
	spawner.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("removal attempted %d spawns", attempts)
	}
	inventory := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inventory.Sessions) != 0 {
		t.Fatal("closed removal became live on replay")
	}
	request.Shape.Cols++
	if err := sandboxCallError(t, svc, proto.OpSandboxLaunch, request); err == nil {
		t.Fatal("changed consumed payload accepted")
	}
	request.Ticket = "unknown-token"
	if err := sandboxCallError(t, svc, proto.OpSandboxLaunch, request); err == nil {
		t.Fatal("unknown ticket spawned")
	}
}

func TestSandboxFailedLaunchIsOneAttempt(t *testing.T) {
	spawner := newGatedSpawner()
	spawner.inner.err = errors.New("cannot fork")
	svc := newService(t, newSink(), spawner, session.Limits{})
	prepared := call[proto.SandboxPrepareResult](t, svc, proto.OpSandboxPrepare, proto.SandboxPrepareParams{OperationID: "remove-failed", LaunchID: "off-failed", Mode: proto.SandboxOff, Cwd: t.TempDir()})
	request := proto.SandboxLaunchParams{Ticket: prepared.Ticket, OperationID: "remove-failed", LaunchID: "off-failed", Mode: proto.SandboxOff}
	close(spawner.release)
	if err := sandboxCallError(t, svc, proto.OpSandboxLaunch, request); err == nil {
		t.Fatal("failed native operation reported success")
	}
	if err := sandboxCallError(t, svc, proto.OpSandboxLaunch, request); err == nil {
		t.Fatal("failed operation lost its terminal result")
	}
	if attempts := len(spawner.entered); attempts != 1 {
		t.Fatalf("failed launch attempted %d forks", attempts)
	}
}

func TestSandboxTicketExpiryCannotMintAuthority(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).UnixNano())
	spawner := &fakeSpawner{}
	svc := session.New(session.Options{Generation: "expiry", Spawner: spawner, Log: discardLog(), Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	t.Cleanup(svc.Close)
	prepared := call[proto.SandboxPrepareResult](t, svc, proto.OpSandboxPrepare, proto.SandboxPrepareParams{OperationID: "remove-expired", LaunchID: "off-expired", Mode: proto.SandboxOff, Cwd: t.TempDir()})
	clock.Add(int64(time.Minute))
	if err := sandboxCallError(t, svc, proto.OpSandboxLaunch, proto.SandboxLaunchParams{Ticket: prepared.Ticket, OperationID: "remove-expired", LaunchID: "off-expired", Mode: proto.SandboxOff}); err == nil {
		t.Fatal("expired ticket spawned")
	}
	spawner.mu.Lock()
	attempts := len(spawner.reqs)
	spawner.mu.Unlock()
	if attempts != 0 {
		t.Fatalf("expired ticket attempted %d forks", attempts)
	}
}
