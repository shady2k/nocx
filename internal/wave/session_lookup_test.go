package wave

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A worker calling in from its own process knows its SESSION and nothing
// else — the participant id is backend-owned (A9) and never travels to the
// agent. So the record has to answer "which participant is this session",
// and it is the record's question rather than the caller's: an answer built
// by scanning HeldBy from outside would need a coordinator session it does
// not have.
func TestParticipantBySessionNamesTheWorkerRunningInIt(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.EnsureWave(ctx, ID("coordinator-session"), "coordinator-session"); err != nil {
		t.Fatalf("ensure wave: %v", err)
	}
	p := Participant{
		ID:           ParticipantID("worker-1"),
		Wave:         ID("coordinator-session"),
		Role:         RoleWorker,
		State:        StatePrepared,
		Task:         "read your own mail",
		RegisteredAt: time.Unix(1, 0),
	}
	if err := store.CommitPrepared(ctx, p); err != nil {
		t.Fatalf("commit prepared: %v", err)
	}
	live := Liveness{BackendInstance: "instance-1", SessionID: "worker-session", Epoch: 1}
	if err := store.MarkLive(ctx, p.ID, live); err != nil {
		t.Fatalf("mark live: %v", err)
	}

	got, err := store.ParticipantBySession(ctx, "worker-session")
	if err != nil {
		t.Fatalf("participant by session: %v", err)
	}
	if got.ID != p.ID {
		t.Fatalf("participant by session = %q, want %q", got.ID, p.ID)
	}
}

// A session that is nobody's participant is REFUSED, not answered with a zero
// participant: an empty ParticipantID would name mailbox "" and hand the
// caller a mailbox that belongs to no one.
func TestParticipantBySessionRefusesASessionThatIsNobody(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.ParticipantBySession(context.Background(), "not-a-worker")
	if !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("unknown session error = %v, want ErrNoSuchParticipant", err)
	}
}

// The empty session never matches. A participant that has not been marked
// live has no session id at all, and a lookup of "" must not return it —
// that would hand a prepared worker's mailbox to any caller whose session
// the authorizer failed to establish.
func TestParticipantBySessionNeverMatchesTheEmptySession(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.EnsureWave(ctx, ID("coordinator-session"), "coordinator-session"); err != nil {
		t.Fatalf("ensure wave: %v", err)
	}
	if err := store.CommitPrepared(ctx, Participant{
		ID: ParticipantID("worker-1"), Wave: ID("coordinator-session"),
		Role: RoleWorker, State: StatePrepared, RegisteredAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatalf("commit prepared: %v", err)
	}
	if _, err := store.ParticipantBySession(ctx, ""); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("empty session error = %v, want ErrNoSuchParticipant", err)
	}
}

// A session id is reused across incarnations of one session, so two records
// can carry it — the terminal one from a previous epoch and the live one.
// The live participant is the answer; returning the dead one would hand the
// caller a mailbox nothing will ever write to again.
func TestParticipantBySessionPrefersTheLiveIncarnation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.EnsureWave(ctx, ID("coordinator-session"), "coordinator-session"); err != nil {
		t.Fatalf("ensure wave: %v", err)
	}
	for _, seed := range []struct {
		id    ParticipantID
		at    time.Time
		epoch uint64
		dead  bool
	}{
		{id: "worker-old", at: time.Unix(1, 0), epoch: 1, dead: true},
		{id: "worker-new", at: time.Unix(2, 0), epoch: 2},
	} {
		if err := store.CommitPrepared(ctx, Participant{
			ID: seed.id, Wave: ID("coordinator-session"), Role: RoleWorker,
			State: StatePrepared, RegisteredAt: seed.at,
		}); err != nil {
			t.Fatalf("commit prepared %s: %v", seed.id, err)
		}
		if err := store.MarkLive(ctx, seed.id, Liveness{
			BackendInstance: "instance-1", SessionID: "worker-session", Epoch: seed.epoch,
		}); err != nil {
			t.Fatalf("mark live %s: %v", seed.id, err)
		}
		if seed.dead {
			if err := store.Terminalize(ctx, seed.id, StateAbandoned); err != nil {
				t.Fatalf("terminalize %s: %v", seed.id, err)
			}
		}
	}
	got, err := store.ParticipantBySession(ctx, "worker-session")
	if err != nil {
		t.Fatalf("participant by session: %v", err)
	}
	if got.ID != ParticipantID("worker-new") {
		t.Fatalf("participant by session = %q, want the live worker-new", got.ID)
	}
}
