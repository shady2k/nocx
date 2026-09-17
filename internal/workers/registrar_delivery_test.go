package workers

// What became of the task travels out with the registration (nocx-f545a.3),
// and only out: it is never written into the participant's record, because the
// reason a task was not typed is a screen reading and AD-6 forbids a screen
// reading from assigning status to a participant (ADR-0064 §4).

import (
	"context"
	"testing"
)

// deliveringSpawner is the harness's spawner, wrapped so the launcher it
// returns says what became of its task.
type deliveringSpawner struct {
	inner    *fakeSpawner
	delivery TaskDelivery
}

func (d deliveringSpawner) Spawn(ctx context.Context, req SpawnRequest) (Spawned, error) {
	sp, err := d.inner.Spawn(ctx, req)
	if err != nil {
		return nil, err
	}
	return deliveringSpawned{Spawned: sp, delivery: d.delivery}, nil
}

type deliveringSpawned struct {
	Spawned
	delivery TaskDelivery
}

func (d deliveringSpawned) TaskDelivery() TaskDelivery { return d.delivery }

func TestARegistrationCarriesWhatBecameOfTheTask(t *testing.T) {
	h := newHarness(t)
	want := TaskDelivery{WaitingOn: "permission_choice"}
	h.reg.spawn = deliveringSpawner{inner: h.spawn, delivery: want}

	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "read AGENTS.md and report", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.State != StateLive {
		t.Fatalf("state = %q, want live: a task waiting on a question does not stop a worker existing", reg.State)
	}
	if reg.Delivery != want {
		t.Fatalf("delivery = %+v, want %+v", reg.Delivery, want)
	}

	// And the record holds none of it: what was stored is the participant, and
	// reading it back is reading the participant, whatever the spawn reported.
	stored, ok := h.store.read(t, reg.ID)
	if !ok {
		t.Fatalf("participant %q was not stored", reg.ID)
	}
	if stored.State != StateLive || stored.Task != "read AGENTS.md and report" {
		t.Fatalf("stored participant = %+v", stored)
	}
}

// A launcher that attempted no delivery says nothing, and nothing is invented
// for it: the zero TaskDelivery, not "typed".
func TestALauncherThatAttemptedNoDeliveryReportsNone(t *testing.T) {
	h := newHarness(t)
	reg, err := h.reg.Register(context.Background(), RegisterRequest{
		Group: testGroup, CoordinatorSession: coordSession,
		Role: RoleWorker, Task: "read AGENTS.md and report", Command: "claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.Delivery != (TaskDelivery{}) {
		t.Fatalf("delivery = %+v, want none reported", reg.Delivery)
	}
}
