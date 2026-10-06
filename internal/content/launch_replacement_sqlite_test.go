package content

import (
	"context"
	"errors"
	"testing"
)

func TestRelaunchKeepsPreviousHeadUntilItsBindingCommits(t *testing.T) {
	store, source := launchStoreFixture(t)
	ctx := context.Background()
	firstIntent := enforceLaunchIntent(t, source)
	first, err := store.Launches().Prepare(ctx, firstIntent)
	if err != nil {
		t.Fatal(err)
	}
	firstCandidate := HelperIdentity{Generation: source.Generation, SessionID: "first-candidate"}
	firstBinding := Session{ID: firstCandidate.SessionID, WorkspaceID: firstIntent.WorkspaceID, PaneID: firstIntent.PaneID, Generation: firstCandidate.Generation, LifecycleApplied: new(uint64)}
	first, err = store.Launches().Commit(ctx, LaunchCommit{LaunchID: first.ID, ExpectedSource: source, SourceCwd: "/work", Candidate: firstCandidate, Binding: firstBinding})
	if err != nil {
		t.Fatal(err)
	}
	var pane, generation string
	if err = store.db.QueryRowContext(ctx, `SELECT json_extract(payload,'$.pane'),json_extract(payload,'$.generation') FROM sessions WHERE id=?`, firstCandidate.SessionID).Scan(&pane, &generation); err != nil {
		t.Fatal(err)
	}
	if pane != firstIntent.PaneID || generation != firstCandidate.Generation {
		t.Fatalf("committed binding pane=%q generation=%q", pane, generation)
	}

	secondIntent := LaunchPrepare{ID: "confirmed-remove", PaneID: firstIntent.PaneID, WorkspaceID: firstIntent.WorkspaceID, Source: firstCandidate, TargetGeneration: "helper-generation-2", ExpectedHeadID: first.ID, StandardRevision: 1, Mode: LaunchOff}
	second, err := store.Launches().Prepare(ctx, secondIntent)
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.Launches().Head(ctx, firstIntent.PaneID)
	if err != nil || head.ID != first.ID || head.State != LaunchActive || head.GrantID == nil {
		t.Fatalf("preparation replaced prior head: %+v, %v", head, err)
	}
	if second.TargetGeneration != secondIntent.TargetGeneration || second.Helper != nil || second.GrantID != nil {
		t.Fatalf("preparing removal lost its target or minted authority: %+v", second)
	}
	secondCandidate := HelperIdentity{Generation: secondIntent.TargetGeneration, SessionID: "second-candidate"}
	secondBinding := Session{ID: secondCandidate.SessionID, WorkspaceID: secondIntent.WorkspaceID, PaneID: secondIntent.PaneID, Generation: secondCandidate.Generation, LifecycleApplied: new(uint64)}
	commit := LaunchCommit{LaunchID: second.ID, ExpectedSource: firstCandidate, SourceCwd: "/work", ExpectedHeadID: first.ID, Candidate: secondCandidate, Binding: secondBinding}
	if _, err = store.Launches().Commit(ctx, commit); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Launches().Commit(ctx, commit); err != nil {
		t.Fatalf("lost-response replay: %v", err)
	}
	head, err = store.Launches().Head(ctx, firstIntent.PaneID)
	if err != nil || head.ID != second.ID || head.Mode != LaunchOff || head.GrantID != nil || head.Helper == nil || *head.Helper != secondCandidate {
		t.Fatalf("committed removal head: %+v, %v", head, err)
	}
	previous, err := store.Launches().GetLaunch(ctx, first.ID)
	if err != nil || previous.State != LaunchEnded || previous.GrantID == nil || *previous.GrantID != *first.GrantID || previous.PolicyDigest != first.PolicyDigest {
		t.Fatalf("prior launch changed authority: %+v, %v", previous, err)
	}
	retirement, err := store.Launches().Retirement(ctx, firstCandidate)
	if err != nil || !retirement.ClosePending || retirement.OperationID != second.ID {
		t.Fatalf("source retirement: %+v, %v", retirement, err)
	}
	if err = store.Launches().CompleteRetirement(ctx, RetirementConfirmation{Identity: firstCandidate, Resolution: RetirementClosed}); err != nil {
		t.Fatal(err)
	}
	retirement, err = store.Launches().Retirement(ctx, firstCandidate)
	if err != nil || retirement.ClosePending {
		t.Fatalf("retired incarnation lost its completed marker: %+v, %v", retirement, err)
	}
}

func TestLaunchBindingConflictRollsBackSelectionAndRetirement(t *testing.T) {
	store, source := launchStoreFixture(t)
	ctx := context.Background()
	intent := enforceLaunchIntent(t, source)
	launch, err := store.Launches().Prepare(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	candidate := HelperIdentity{Generation: source.Generation, SessionID: "occupied-session"}
	if err = store.Ledger().CreateSession(ctx, Session{ID: candidate.SessionID, WorkspaceID: intent.WorkspaceID, PaneID: "another-pane", Generation: candidate.Generation}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Launches().Commit(ctx, LaunchCommit{LaunchID: launch.ID, ExpectedSource: source, SourceCwd: "/work", Candidate: candidate, Binding: Session{ID: candidate.SessionID, WorkspaceID: intent.WorkspaceID, PaneID: intent.PaneID, Generation: candidate.Generation, LifecycleApplied: new(uint64)}})
	if err == nil {
		t.Fatal("occupied binding selected a candidate")
	}
	if _, err = store.Launches().Head(ctx, intent.PaneID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed binding wrote head: %v", err)
	}
	if _, err = store.Launches().Retirement(ctx, source); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed binding retired source: %v", err)
	}
	remaining, err := store.Launches().GetLaunch(ctx, launch.ID)
	if err != nil || remaining.State != LaunchPreparing || remaining.Helper != nil {
		t.Fatalf("failed binding changed operation: %+v, %v", remaining, err)
	}
}

func TestPaneClosureRetiresCommittedProcessAndPreservesUncommittedClaim(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "preparing"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			store, source := launchStoreFixture(t)
			ctx := context.Background()
			intent := enforceLaunchIntent(t, source)
			launch, err := store.Launches().Prepare(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			candidate := HelperIdentity{Generation: source.Generation, SessionID: "closing-candidate"}
			if committed {
				_, err = store.Launches().Commit(ctx, LaunchCommit{LaunchID: launch.ID, ExpectedSource: source, SourceCwd: "/work", Candidate: candidate, Binding: Session{ID: candidate.SessionID, WorkspaceID: intent.WorkspaceID, PaneID: intent.PaneID, Generation: candidate.Generation, LifecycleApplied: new(uint64)}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = store.db.ExecContext(ctx, `UPDATE panes SET closed_at=1 WHERE id=?`, intent.PaneID); err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.ExecContext(ctx, `DELETE FROM panes WHERE id=?`, intent.PaneID); err != nil {
				t.Fatal(err)
			}
			remaining, err := store.Launches().GetLaunch(ctx, launch.ID)
			if err != nil || remaining.TargetGeneration != source.Generation {
				t.Fatalf("closure erased recovery claim: %+v, %v", remaining, err)
			}
			if committed {
				retirement, retErr := store.Launches().Retirement(ctx, candidate)
				if retErr != nil || !retirement.ClosePending || remaining.State != LaunchEnded {
					t.Fatalf("closure lost process retirement: %+v %+v %v", remaining, retirement, retErr)
				}
			} else if remaining.State != LaunchPreparing || remaining.Helper != nil {
				t.Fatalf("closure changed uncommitted claim: %+v", remaining)
			}
			if _, err = store.Launches().Head(ctx, intent.PaneID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("closed pane retained selectable head: %v", err)
			}
		})
	}
}

func TestLaunchSelectionRejectsCwdOrWorkspaceDefaultsChangedDuringNativeStartup(t *testing.T) {
	for _, change := range []string{"cwd", "workspace-profile"} {
		t.Run(change, func(t *testing.T) {
			store, source := launchStoreFixture(t)
			ctx := context.Background()
			intent := enforceLaunchIntent(t, source)
			launch, err := store.Launches().Prepare(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			if change == "cwd" {
				_, err = store.Layout().SetPaneCwd(ctx, intent.PaneID, "/different-work")
			} else {
				_, err = store.Layout().UpdateWorkspaceProfile(ctx, intent.WorkspaceID, 0, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			candidate := HelperIdentity{Generation: source.Generation, SessionID: "stale-prepared-candidate"}
			_, err = store.Launches().Commit(ctx, LaunchCommit{LaunchID: launch.ID, ExpectedSource: source, SourceCwd: "/work", Candidate: candidate, Binding: Session{ID: candidate.SessionID, WorkspaceID: intent.WorkspaceID, PaneID: intent.PaneID, Generation: candidate.Generation, LifecycleApplied: new(uint64)}})
			if !errors.Is(err, ErrLaunchConflict) {
				t.Fatalf("stale native selection error=%v", err)
			}
			if _, err = store.Launches().Head(ctx, intent.PaneID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("stale candidate became selected: %v", err)
			}
			if _, err = store.Launches().Retirement(ctx, source); !errors.Is(err, ErrNotFound) {
				t.Fatalf("stale candidate retired source: %v", err)
			}
			var bindings int
			if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE id=?`, candidate.SessionID).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if bindings != 0 {
				t.Fatal("stale candidate acquired a durable foreground binding")
			}
		})
	}
}
