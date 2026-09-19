package workers

// The worktree ask and the worktree facts (nocx-xn63t.1.2).
//
// A spawn may be asked to create the checkout its pane will live in. The
// record's whole job in that is to CARRY two things and decide nothing: the
// ask travels to the spawner as asked, and the resolved facts the spawner
// answers are stamped onto the participant at MarkLive — the moment the
// record accepts a checkout whose existence until then belonged to the
// spawn's own compensation. A launcher that never enrols must therefore
// leave a record that names no checkout, because the compensation removes
// the checkout and a record that kept naming it would outlive its own undo.

import (
	"context"
	"testing"
)

// registerAsk is register() with a worktree ask, for the tests that are
// about the ask and not about the six steps around it.
func (h *harness) registerAsk(ctx context.Context, ask *WorktreeAsk) (Participant, error) {
	reg, err := h.reg.Register(ctx, RegisterRequest{
		Group:              testGroup,
		CoordinatorSession: coordSession,
		Role:               RoleWorker,
		Task:               "read AGENTS.md and report",
		Environment:        "env-local",
		Worktree:           ask,
	})
	return reg.Participant, err
}

// Criterion: the ask reaches the spawner AS ASKED — the branch required, the
// base optional — because resolving it is the spawner's (the git seam's)
// business and a record that normalized it would be a second owner of the
// question.
func TestARegistrationCarriesTheWorktreeAskToTheSpawner(t *testing.T) {
	h := newHarness(t)
	var seen *WorktreeAsk
	h.spawn.before = func(req SpawnRequest) {
		seen = req.Worktree
	}
	made := &WorktreeAsk{Branch: "feat/worker-worktree", Base: "abc123"}
	p, err := h.registerAsk(context.Background(), made)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if seen == nil {
		t.Fatal("the spawner was asked for no worktree")
	}
	if seen.Branch != made.Branch || seen.Base != made.Base {
		t.Fatalf("spawner saw ask %+v, want %+v unchanged", *seen, *made)
	}
	if p.State != StateLive {
		t.Fatalf("state = %s, want live", p.State)
	}
}

// Criterion: the facts the spawner answered are stamped onto the record at
// MarkLive — the returned participant and the store's row agree, because one
// is read back from the other.
func TestARegistrationStampsTheWorktreeTheSpawnerMade(t *testing.T) {
	h := newHarness(t)
	h.spawn.wt = Worktree{Path: "/wt/nocx-feat", Branch: "feat/worker-worktree", Base: "4f2a1c9"}

	p, err := h.registerAsk(context.Background(), &WorktreeAsk{Branch: "feat/worker-worktree"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	want := h.spawn.wt
	if p.Worktree != want {
		t.Fatalf("returned participant worktree = %+v, want %+v", p.Worktree, want)
	}
	row, ok := h.store.read(t, p.ID)
	if !ok {
		t.Fatalf("no record for %q", p.ID)
	}
	if row.Worktree != want {
		t.Fatalf("stored worktree = %+v, want %+v", row.Worktree, want)
	}
	if row.State != StateLive {
		t.Fatalf("state = %s, want live", row.State)
	}
}

// Criterion: a spawn that asked for no worktree stamps none — the zero
// Worktree is the "shares its coordinator's checkout" case, and nothing in
// the record may invent one.
func TestARegistrationWithoutAWorktreeStampsNone(t *testing.T) {
	h := newHarness(t)
	var sawAsk bool
	h.spawn.before = func(req SpawnRequest) {
		sawAsk = req.Worktree != nil
	}

	p, err := h.register(context.Background())
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if sawAsk {
		t.Fatal("the spawner was handed a worktree ask nobody made")
	}
	if p.Worktree != (Worktree{}) {
		t.Fatalf("worktree = %+v, want zero", p.Worktree)
	}
	row, ok := h.store.read(t, p.ID)
	if !ok {
		t.Fatalf("no record for %q", p.ID)
	}
	if row.Worktree != (Worktree{}) {
		t.Fatalf("stored worktree = %+v, want zero", row.Worktree)
	}
}

// Criterion: the stamp never lands without liveness. A launcher whose
// enrolment never arrives is compensated away — Kill removes what the spawn
// built — and the record must not keep naming a checkout the compensation
// removed: it terminalizes with the zero Worktree.
func TestAFailedEnrolmentLeavesTheWorktreeUnstamped(t *testing.T) {
	h := newHarness(t)
	h.enrol.never = true
	h.spawn.wt = Worktree{Path: "/wt/nocx-feat", Branch: "feat/worker-worktree", Base: "4f2a1c9"}

	p, err := h.registerAsk(context.Background(), &WorktreeAsk{Branch: "feat/worker-worktree"})
	if err == nil {
		t.Fatal("an enrolment that never arrived registered anyway")
	}
	if p.Worktree != (Worktree{}) {
		t.Fatalf("a compensated record names a checkout: %+v", p.Worktree)
	}
	row, ok := h.store.read(t, p.ID)
	if !ok {
		t.Fatalf("no record for %q", p.ID)
	}
	if row.Worktree != (Worktree{}) {
		t.Fatalf("stored worktree = %+v, want zero after compensation", row.Worktree)
	}
	if !row.State.Terminal() {
		t.Fatalf("state = %s, want terminal", row.State)
	}
	if !h.spawn.wasKilled() {
		t.Fatal("the spawn was not killed, so nothing removed the checkout it built")
	}
}
