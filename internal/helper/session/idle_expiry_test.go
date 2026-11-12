package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

func TestLiveSessionAbsenceDeadline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		days     *int
		duration time.Duration
	}{
		{"default", nil, 72 * time.Hour}, {"configured", intPointer(1), 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock(time.Now())
			spawner := &fakeSpawner{}
			svc := newServiceWithClock(t, newSink(), spawner, session.Limits{}, clock.Now)
			call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{SessionIdleDays: tc.days})
			clock.Advance(tc.duration - time.Nanosecond)
			assertIdleInventory(t, svc, 1)
			clock.Advance(time.Nanosecond)
			assertIdleInventory(t, svc, 0)
			spawner.last().mu.Lock()
			closed := spawner.last().closed
			spawner.last().mu.Unlock()
			if !closed {
				t.Fatal("expired live process was not closed")
			}
		})
	}
}

func intPointer(v int) *int { return &v }
func assertIdleInventory(t *testing.T, svc *session.Service, want int) {
	t.Helper()
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != want {
		t.Fatalf("inventory has %d sessions, want %d", len(inv.Sessions), want)
	}
}

func TestReattachmentProtectsAndLastDetachRestartsAbsence(t *testing.T) {
	clock := newFakeClock(time.Now())
	sink := newSink()
	spawner := &fakeSpawner{}
	svc := newServiceWithClock(t, sink, spawner, session.Limits{}, clock.Now)
	spawned := spawnOne(t, svc)
	clock.Advance(71*time.Hour + 59*time.Minute)
	attached := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{Session: spawned.Session, Subscriber: proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")})
	clock.Advance(200 * time.Hour)
	assertIdleInventory(t, svc, 1)
	call[proto.DetachResult](t, svc, proto.OpDetach, proto.DetachParams{Attachment: attached.Attachment})
	clock.Advance(72*time.Hour - time.Nanosecond)
	assertIdleInventory(t, svc, 1)
	clock.Advance(time.Nanosecond)
	assertIdleInventory(t, svc, 0)
}

func TestZeroDisablesLiveSessionAbsenceExpiry(t *testing.T) {
	clock := newFakeClock(time.Now())
	svc := newServiceWithClock(t, newSink(), &fakeSpawner{}, session.Limits{}, clock.Now)
	call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{SessionIdleDays: intPointer(0)})
	clock.Advance(1000 * 24 * time.Hour)
	assertIdleInventory(t, svc, 1)
}

func TestCloseFailureDoesNotReportSessionEnded(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newServiceWithClock(t, newSink(), spawner, session.Limits{}, time.Now)
	entry := spawnOne(t, svc)
	spawner.last().mu.Lock()
	spawner.last().sigErr = errors.New("hangup refused")
	spawner.last().mu.Unlock()
	_, err := svc.Call(callCtx(), proto.OpCloseSession, mustJSON(t, proto.CloseSessionParams{Session: entry.Session}))
	if err == nil {
		t.Fatal("failed hangup reported successful termination")
	}
	assertIdleInventory(t, svc, 1)

	spawner.last().mu.Lock()
	spawner.last().sigErr = nil
	spawner.last().mu.Unlock()
	call[proto.CloseSessionResult](t, svc, proto.OpCloseSession, proto.CloseSessionParams{Session: entry.Session})
	assertIdleInventory(t, svc, 0)
}

func TestProcessCloseFailureRetainsRecordUntilRetry(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newServiceWithClock(t, newSink(), spawner, session.Limits{}, time.Now)
	entry := spawnOne(t, svc)
	spawner.last().mu.Lock()
	spawner.last().closeErr = errors.New("close failed")
	spawner.last().mu.Unlock()
	_, err := svc.Call(callCtx(), proto.OpCloseSession, mustJSON(t, proto.CloseSessionParams{Session: entry.Session}))
	if err == nil {
		t.Fatal("failed process close reported success")
	}
	assertIdleInventory(t, svc, 1)
	spawner.last().mu.Lock()
	spawner.last().closeErr = nil
	spawner.last().mu.Unlock()
	call[proto.CloseSessionResult](t, svc, proto.OpCloseSession, proto.CloseSessionParams{Session: entry.Session})
	assertIdleInventory(t, svc, 0)
}

func TestConnectionReleaseStartsClockOnlyAfterLastAttachment(t *testing.T) {
	clock := newFakeClock(time.Now())
	first := newSink()
	second := newSink()
	svc := newServiceWithClock(t, first, &fakeSpawner{}, session.Limits{}, clock.Now)
	entry := spawnOne(t, svc)
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{Session: entry.Session, Subscriber: proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")})
	release := bindTo(svc, second)
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{Session: entry.Session, Subscriber: proto.SubscriberID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")})
	clock.Advance(100 * time.Hour)
	release()
	clock.Advance(100 * time.Hour)
	assertIdleInventory(t, svc, 1)
	// Releasing the first connection removes the final attachment.
	// Bind returns an independent release; the original cleanup also uses it.
	svc.Bind(first)()
	clock.Advance(72*time.Hour - time.Nanosecond)
	assertIdleInventory(t, svc, 1)
	clock.Advance(time.Nanosecond)
	assertIdleInventory(t, svc, 0)
}

func TestZeroAlsoDisablesExitedSessionAbsenceExpiry(t *testing.T) {
	clock := newFakeClock(time.Now())
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newServiceWithClock(t, sink, spawner, session.Limits{}, clock.Now)
	call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{SessionIdleDays: intPointer(0)})
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 1)
	clock.Advance(1000 * 24 * time.Hour)
	assertIdleInventory(t, svc, 1)
}

func TestNegativeSessionIdleDaysRefusedBeforeSpawn(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newServiceWithClock(t, newSink(), spawner, session.Limits{}, time.Now)
	_, err := svc.Call(callCtx(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{SessionIdleDays: intPointer(-1)}))
	if err == nil {
		t.Fatal("negative idle days accepted")
	}
	if len(spawner.procs) != 0 {
		t.Fatal("invalid lifetime spawned a process")
	}
}
