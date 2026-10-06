package content

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
)

func launchStoreFixture(t *testing.T) (*sqliteContent, HelperIdentity) {
	t.Helper()
	db := openStore(t, filepath.Join(t.TempDir(), "content.db"))
	store, ok := db.(*sqliteContent)
	if !ok {
		t.Fatalf("unexpected content store implementation: %T", db)
	}
	ctx := context.Background()
	if _, err := db.Layout().CreateWorkspace(ctx, Workspace{ID: "ws-launch", Name: "sandbox"}, Tab{ID: "tab-launch", WorkspaceID: "ws-launch", Layout: LayoutRow}, Pane{ID: "pane-launch", TabID: "tab-launch", Cwd: "/work", Kind: PaneLocal, SizeShare: 1}); err != nil {
		t.Fatal(err)
	}
	source := HelperIdentity{Host: "local-host", Account: "local-account", Generation: "helper-generation-1", SessionID: "session-source"}
	if err := db.Ledger().CreateSession(ctx, Session{ID: source.SessionID, WorkspaceID: "ws-launch", Host: source.Host, Account: source.Account, Generation: source.Generation, PaneID: "pane-launch"}); err != nil {
		t.Fatal(err)
	}
	return store, source
}

func enforceLaunchIntent(t *testing.T, source HelperIdentity) LaunchPrepare {
	t.Helper()
	policy := sandbox.Policy{Version: sandbox.PolicyVersion, Backend: sandbox.LinuxLandlock, BackendVersion: 9, WorkspaceID: "ws-launch", WorkspaceRoot: "/work", StandardRevision: 1, WorkspaceRevision: 0, Shell: "/bin/sh", Runner: "/app/nocx-sandbox-runner", Roots: []sandbox.Root{}}
	_, digest, err := sandbox.EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	return LaunchPrepare{ID: "launch-operation-1", PaneID: "pane-launch", WorkspaceID: "ws-launch", Source: source, ExpectedHeadID: "", StandardRevision: 1, WorkspaceRevision: 0, Mode: LaunchEnforce, Policy: &policy, PolicyDigest: digest, PolicyVersion: sandbox.PolicyVersion}
}

func TestLaunchPrepareCommitGrantAndRetirementAreAtomic(t *testing.T) {
	store, source := launchStoreFixture(t)
	ctx := context.Background()
	intent := enforceLaunchIntent(t, source)
	first, err := store.Launches().Prepare(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Launches().Prepare(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || replay.State != LaunchPreparing {
		t.Fatalf("prepare replay=%+v", replay)
	}
	if _, err = store.Launches().Prepare(ctx, LaunchPrepare{ID: "second-operation", PaneID: intent.PaneID, WorkspaceID: intent.WorkspaceID, Source: source, StandardRevision: 1, WorkspaceRevision: 0, Mode: LaunchOff}); !errors.Is(err, ErrLaunchConflict) {
		t.Fatalf("second preparing operation error=%v", err)
	}
	if first.GrantID == nil {
		t.Fatal("enforce launch lacks its launch-subject grant")
	}
	var expiry, policy string
	if err = store.db.QueryRowContext(ctx, `SELECT COALESCE(CAST(expires_at AS TEXT),'null'),policy FROM authority_grants WHERE launch_id=? AND execution_id IS NULL`, first.ID).Scan(&expiry, &policy); err != nil {
		t.Fatal(err)
	}
	if expiry != "null" || policy == "" {
		t.Fatalf("launch grant expiry=%q policy=%q", expiry, policy)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE authority_grants SET policy='{}' WHERE launch_id=?`, first.ID); err == nil {
		t.Fatal("immutable launch grant was updated")
	}
	candidate := HelperIdentity{Host: source.Host, Account: source.Account, Generation: source.Generation, SessionID: "session-candidate"}
	committed, err := store.Launches().Commit(ctx, LaunchCommit{LaunchID: first.ID, ExpectedSource: source, ExpectedHeadID: "", Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != LaunchActive || committed.Helper == nil || *committed.Helper != candidate {
		t.Fatalf("committed launch=%+v", committed)
	}
	head, err := store.Launches().Head(ctx, intent.PaneID)
	if err != nil || head.ID != first.ID {
		t.Fatalf("head=%+v err=%v", head, err)
	}
	pending, err := store.Launches().PendingRetirements(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Identity != source || pending[0].OperationID != first.ID {
		t.Fatalf("pending retirements=%+v", pending)
	}
	if err = store.Launches().End(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	ended, err := store.Launches().GetLaunch(ctx, first.ID)
	if err != nil || ended.State != LaunchEnded {
		t.Fatalf("ended launch=%+v err=%v", ended, err)
	}
	if err = store.run(ctx, func(ctx context.Context) error {
		tx, transactionErr := store.db.BeginTx(ctx, nil)
		if transactionErr != nil {
			return transactionErr
		}
		defer func() { _ = tx.Rollback() }()
		if _, transactionErr = tx.ExecContext(ctx, `DELETE FROM panes WHERE id=?`, intent.PaneID); transactionErr != nil {
			return transactionErr
		}
		return tx.Commit()
	}); err != nil {
		t.Fatal(err)
	}
	pending, err = store.Launches().PendingRetirements(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("retirement after pane cascade=%+v err=%v", pending, err)
	}
	if err = store.Launches().CompleteRetirement(ctx, RetirementConfirmation{Identity: source, Resolution: RetirementAbsent}); err != nil {
		t.Fatal(err)
	}
	pending, err = store.Launches().PendingRetirements(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed retirement still pending: %+v err=%v", pending, err)
	}
}

func TestOffLaunchHasNoGrantAndCommitRejectsStaleSourceOrClosedPane(t *testing.T) {
	for _, test := range []struct{ name, mutation string }{{"closed-pane", `UPDATE panes SET closed_at=1 WHERE id='pane-launch'`}, {"ended-source", `UPDATE sessions SET ended_at=1 WHERE id='session-source'`}} {
		t.Run(test.name, func(t *testing.T) {
			store, source := launchStoreFixture(t)
			ctx := context.Background()
			intent := LaunchPrepare{ID: "off-operation", PaneID: "pane-launch", WorkspaceID: "ws-launch", Source: source, Mode: LaunchOff}
			launch, err := store.Launches().Prepare(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			var grants int
			if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM authority_grants WHERE launch_id=?`, launch.ID).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if grants != 0 {
				t.Fatalf("Off launch has %d grants", grants)
			}
			if _, err = store.db.ExecContext(ctx, test.mutation); err != nil {
				t.Fatal(err)
			}
			candidate := HelperIdentity{Host: source.Host, Account: source.Account, Generation: source.Generation, SessionID: "candidate-off"}
			if _, err = store.Launches().Commit(ctx, LaunchCommit{LaunchID: launch.ID, ExpectedSource: source, Candidate: candidate}); !errors.Is(err, ErrLaunchConflict) {
				t.Fatalf("stale commit error=%v", err)
			}
			got, err := store.Launches().GetLaunch(ctx, launch.ID)
			if err != nil || got.State != LaunchPreparing {
				t.Fatalf("failed commit changed launch: %+v err=%v", got, err)
			}
		})
	}
}

func TestRetirementRequiresExplicitIdentityAndResolution(t *testing.T) {
	store, _ := launchStoreFixture(t)
	ctx := context.Background()
	identity := HelperIdentity{Host: "host", Account: "account", Generation: "generation", SessionID: "retired"}
	if err := store.Launches().RecordRetirement(ctx, SessionRetirement{Identity: identity, OperationID: "retirement-op", Cause: RetirementFailedCandidate, ClosePending: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Launches().CompleteRetirement(ctx, RetirementConfirmation{Identity: identity}); !errors.Is(err, ErrInvalidLaunch) {
		t.Fatalf("unconfirmed completion error=%v", err)
	}
}
