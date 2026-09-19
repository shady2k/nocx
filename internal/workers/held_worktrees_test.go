package workers

// HeldWorktrees — which checkouts the record's non-terminal participants
// hold, across every session (nocx-xn63t.1.4).

import (
	"context"
	"testing"
)

// Criterion: the answer carries only the worktrees of participants that are
// neither terminal nor worktree-less, and it answers ACROSS sessions — the
// leftovers question is about a repository, and a checkout of one session's
// worker is held even while another session's coordinator asks what is left
// over. An answer that offered a live worker's checkout up as abandoned
// would be the one mistake the answer must never make.
func TestHeldWorktreesAnswersNonTerminalParticipantsAcrossSessions(t *testing.T) {
	ctx := context.Background()
	s := newSeededStore(t)

	held := newParticipant("p-held")
	if err := s.CommitPrepared(ctx, held); err != nil {
		t.Fatalf("commit: %v", err)
	}
	live := Worktree{Path: "/data/worktrees/nocx-1a2b3c4d/feat-one", Branch: "feat/one", Base: "4f2a1c9b"}
	if err := s.MarkLive(ctx, held.ID, testLiveness(), live); err != nil {
		t.Fatalf("mark live: %v", err)
	}

	// A second worker, holding its own checkout, whose coordinator session is
	// a different one — the cross-session half of the criterion.
	other := newParticipant("p-other")
	other.Group = ID("other-worker")
	if err := s.EnsureGroup(ctx, other.Group, "sess-other-coordinator"); err != nil {
		t.Fatalf("ensure other worker: %v", err)
	}
	if err := s.CommitPrepared(ctx, other); err != nil {
		t.Fatalf("commit other: %v", err)
	}
	otherTree := Worktree{Path: "/data/worktrees/nocx-1a2b3c4d/feat-two", Branch: "feat/two", Base: "4f2a1c9b"}
	if err := s.MarkLive(ctx, other.ID, testLiveness(), otherTree); err != nil {
		t.Fatalf("mark other live: %v", err)
	}

	// A prepared participant with no worktree stamped yet (the worktree is
	// accepted at MarkLive): contributes nothing, because a checkout whose
	// spawn has not gone live may still be removed by its compensation.
	plain := newParticipant("p-plain")
	if err := s.CommitPrepared(ctx, plain); err != nil {
		t.Fatalf("commit plain: %v", err)
	}

	got, err := s.HeldWorktrees(ctx)
	if err != nil {
		t.Fatalf("held worktrees: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("held worktrees = %+v, want exactly the two held checkouts", got)
	}
	paths := map[string]bool{}
	for _, wt := range got {
		paths[wt.Path] = true
	}
	if !paths[live.Path] || !paths[otherTree.Path] {
		t.Fatalf("held worktrees = %+v, want %q and %q", got, live.Path, otherTree.Path)
	}

	// And the ended worker's checkout stops being held the moment the record
	// knows it ended: a terminal participant holds nothing.
	if _, exitErr := s.RecordExit(ctx, held.ID, Exit{Cause: "exited", Code: 0}); exitErr != nil {
		t.Fatalf("exit: %v", exitErr)
	}
	got, err = s.HeldWorktrees(ctx)
	if err != nil {
		t.Fatalf("held worktrees after the exit: %v", err)
	}
	if len(got) != 1 || got[0].Path != otherTree.Path {
		t.Fatalf("held worktrees = %+v, want only %q", got, otherTree.Path)
	}
}

// Criterion: a record with nothing non-terminal answers empty and no error —
// the ordinary shape of a backend that just started.
func TestHeldWorktreesAnswersEmptyWhenNothingIsHeld(t *testing.T) {
	s := newSeededStore(t)
	got, err := s.HeldWorktrees(context.Background())
	if err != nil {
		t.Fatalf("held worktrees: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("held worktrees = %+v, want empty", got)
	}
}
