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

func TestSandboxRollbackCancelsUnusedLaunchWithoutForking(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	prepared := call[proto.SandboxPrepareResult](t, svc, proto.OpSandboxPrepare, proto.SandboxPrepareParams{OperationID: "unused-operation", LaunchID: "unused-launch", Mode: proto.SandboxOff, Cwd: t.TempDir()})
	result := call[proto.SandboxDiscardResult](t, svc, proto.OpSandboxDiscard, proto.SandboxDiscardParams{OperationID: prepared.OperationID, LaunchID: prepared.LaunchID})
	if result.Entry != nil {
		t.Fatalf("unused rollback returned a process: %+v", result.Entry)
	}
	if err := sandboxCallError(t, svc, proto.OpSandboxLaunch, proto.SandboxLaunchParams{Ticket: prepared.Ticket, OperationID: prepared.OperationID, LaunchID: prepared.LaunchID, Mode: proto.SandboxOff}); err == nil {
		t.Fatal("rolled-back ticket spawned")
	}
	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	if len(spawner.reqs) != 0 {
		t.Fatalf("rollback forked %d processes", len(spawner.reqs))
	}
}

func TestSandboxRollbackCancellationNeverReportsAnInflightLaunchAbsent(t *testing.T) {
	spawner := newGatedSpawner()
	svc := newService(t, newSink(), spawner, session.Limits{})
	prepared := call[proto.SandboxPrepareResult](t, svc, proto.OpSandboxPrepare, proto.SandboxPrepareParams{OperationID: "inflight-operation", LaunchID: "inflight-launch", Mode: proto.SandboxOff, Cwd: t.TempDir()})
	launchParams, err := json.Marshal(proto.SandboxLaunchParams{Ticket: prepared.Ticket, OperationID: prepared.OperationID, LaunchID: prepared.LaunchID, Mode: proto.SandboxOff})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		value any
		err   error
	}
	launched := make(chan result, 1)
	go func() {
		value, launchErr := svc.Call(context.Background(), proto.OpSandboxLaunch, launchParams)
		launched <- result{value, launchErr}
	}()
	<-spawner.entered
	rollbackParams, err := json.Marshal(proto.SandboxDiscardParams{OperationID: prepared.OperationID, LaunchID: prepared.LaunchID})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = svc.Call(cancelled, proto.OpSandboxDiscard, rollbackParams); !errors.Is(err, context.Canceled) {
		t.Fatalf("inflight rollback reported absence instead of uncertainty: %v", err)
	}
	close(spawner.release)
	outcome := <-launched
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	spawn, ok := outcome.value.(proto.SpawnResult)
	if !ok {
		t.Fatalf("native launch returned %T instead of a spawn result", outcome.value)
	}
	rollback := call[proto.SandboxDiscardResult](t, svc, proto.OpSandboxDiscard, proto.SandboxDiscardParams{OperationID: prepared.OperationID, LaunchID: prepared.LaunchID})
	if rollback.Entry == nil || rollback.Entry.Session != spawn.Entry.Session {
		t.Fatalf("rollback lost the consumed candidate: %+v", rollback)
	}
	if err = sandboxCallError(t, svc, proto.OpSandboxDiscard, proto.SandboxDiscardParams{OperationID: prepared.OperationID, LaunchID: "another-launch"}); err == nil {
		t.Fatal("rollback accepted mismatched correlation")
	}
	spawner.inner.mu.Lock()
	defer spawner.inner.mu.Unlock()
	if len(spawner.inner.reqs) != 1 {
		t.Fatalf("rollback changed the one-attempt launch: %d forks", len(spawner.inner.reqs))
	}
}
