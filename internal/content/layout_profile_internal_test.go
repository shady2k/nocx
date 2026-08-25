package content

// Workspace sandbox profiles (design 2026-08-23 §3, §4.1, §8): the durable
// default stored under the `sandboxProfile` key of workspaces.payload. These
// tests live in the internal package because preserving unrelated payload keys
// can only be asserted against raw SQL, and the one-writer serialization is a
// claim about concurrent mutation the seam does not otherwise expose.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func seedProfileWorkspace(t *testing.T, layout LayoutRepository, wsID string) {
	t.Helper()
	if _, err := layout.CreateWorkspace(context.Background(),
		Workspace{ID: wsID, Name: wsID},
		Tab{ID: "tab-" + wsID, WorkspaceID: wsID, Layout: LayoutRow},
		Pane{ID: "pane-" + wsID, TabID: "tab-" + wsID, Cwd: "/srv", Kind: PaneLocal, SizeShare: 1},
	); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
}

func rawWorkspacePayload(t *testing.T, s *sqliteContent, wsID string) map[string]json.RawMessage {
	t.Helper()
	var raw string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT payload FROM workspaces WHERE id = ?`, wsID).Scan(&raw); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return m
}

func TestWorkspaceSandboxProfileAbsentFallsBackAndSetPreservesUnrelatedKeys(t *testing.T) {
	_, s, layout := lifecycleStore(t)
	ctx := context.Background()
	seedProfileWorkspace(t, layout, "ws-1")

	if prof, err := layout.WorkspaceSandboxProfile(ctx, "ws-1"); err != nil || prof != nil {
		t.Fatalf("absent profile = %#v, %v; want nil, nil", prof, err)
	}
	// Plant an unrelated payload key that a profile write must preserve.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE workspaces SET payload = '{"notes":{"x":1}}' WHERE id = 'ws-1'`); err != nil {
		t.Fatalf("plant unrelated key: %v", err)
	}

	revision, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, WorkspaceSandboxProfile{
		SchemaVersion: 1,
		WritablePaths: []string{"/workspace"},
		ReadOnlyPaths: []string{"/reference"},
	})
	if err != nil {
		t.Fatalf("SetWorkspaceSandboxProfile: %v", err)
	}
	if revision != 1 {
		t.Fatalf("first set revision = %d, want 1", revision)
	}

	prof, err := layout.WorkspaceSandboxProfile(ctx, "ws-1")
	if err != nil || prof == nil {
		t.Fatalf("read profile = %#v, %v", prof, err)
	}
	if prof.Revision != 1 || prof.SchemaVersion != 1 ||
		len(prof.WritablePaths) != 1 || prof.WritablePaths[0] != "/workspace" ||
		len(prof.ReadOnlyPaths) != 1 || prof.ReadOnlyPaths[0] != "/reference" {
		t.Fatalf("profile = %#v", prof)
	}
	payload := rawWorkspacePayload(t, s, "ws-1")
	if _, ok := payload["notes"]; !ok {
		t.Fatalf("unrelated key dropped: %s", payload)
	}
	if _, ok := payload["sandboxProfile"]; !ok {
		t.Fatalf("sandboxProfile key missing: %s", payload)
	}
}

func TestSetWorkspaceSandboxProfileStaleRevisionRefused(t *testing.T) {
	_, _, layout := lifecycleStore(t)
	ctx := context.Background()
	seedProfileWorkspace(t, layout, "ws-1")

	if _, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, WorkspaceSandboxProfile{WritablePaths: []string{"/a"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A stale token is refused and writes nothing.
	if _, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, WorkspaceSandboxProfile{WritablePaths: []string{"/b"}}); !errors.Is(err, ErrSandboxProfileRevision) {
		t.Fatalf("stale set err = %v, want ErrSandboxProfileRevision", err)
	}
	if _, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 7, WorkspaceSandboxProfile{WritablePaths: []string{"/c"}}); !errors.Is(err, ErrSandboxProfileRevision) {
		t.Fatalf("wrong-token set err = %v, want ErrSandboxProfileRevision", err)
	}
	rev, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 1, WorkspaceSandboxProfile{WritablePaths: []string{"/b"}})
	if err != nil || rev != 2 {
		t.Fatalf("update = %d, %v; want 2, nil", rev, err)
	}
	prof, err := layout.WorkspaceSandboxProfile(ctx, "ws-1")
	if err != nil || prof == nil || prof.Revision != 2 || prof.WritablePaths[0] != "/b" {
		t.Fatalf("after update = %#v, %v", prof, err)
	}
}

func TestDeleteWorkspaceSandboxProfile(t *testing.T) {
	_, s, layout := lifecycleStore(t)
	ctx := context.Background()
	seedProfileWorkspace(t, layout, "ws-1")
	if _, err := s.db.ExecContext(ctx, `UPDATE workspaces SET payload = '{"keep":true}' WHERE id = 'ws-1'`); err != nil {
		t.Fatalf("plant unrelated key: %v", err)
	}
	if _, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, WorkspaceSandboxProfile{WritablePaths: []string{"/a"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Stale delete refused.
	if err := layout.DeleteWorkspaceSandboxProfile(ctx, "ws-1", 0); !errors.Is(err, ErrSandboxProfileRevision) {
		t.Fatalf("stale delete err = %v, want ErrSandboxProfileRevision", err)
	}
	if err := layout.DeleteWorkspaceSandboxProfile(ctx, "ws-1", 1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if prof, err := layout.WorkspaceSandboxProfile(ctx, "ws-1"); err != nil || prof != nil {
		t.Fatalf("after delete = %#v, %v; want nil, nil", prof, err)
	}
	payload := rawWorkspacePayload(t, s, "ws-1")
	if _, ok := payload["keep"]; !ok {
		t.Fatalf("unrelated key dropped on delete: %s", payload)
	}
	if _, ok := payload["sandboxProfile"]; ok {
		t.Fatalf("sandboxProfile key survived delete: %s", payload)
	}
	// A second delete on the absent profile is refused, not a no-op.
	if err := layout.DeleteWorkspaceSandboxProfile(ctx, "ws-1", 0); !errors.Is(err, ErrSandboxProfileAbsent) {
		t.Fatalf("re-delete err = %v, want ErrSandboxProfileAbsent", err)
	}
}

func TestSetAndDeleteWorkspaceSandboxProfileRefuseDefaultWorkspace(t *testing.T) {
	_, _, layout := lifecycleStore(t)
	ctx := context.Background()
	if _, err := layout.SetWorkspaceSandboxProfile(ctx, DefaultWorkspaceID, 0, WorkspaceSandboxProfile{WritablePaths: []string{"/a"}}); !errors.Is(err, ErrDefaultWorkspace) {
		t.Fatalf("set on default err = %v, want ErrDefaultWorkspace", err)
	}
	if err := layout.DeleteWorkspaceSandboxProfile(ctx, DefaultWorkspaceID, 0); !errors.Is(err, ErrDefaultWorkspace) {
		t.Fatalf("delete on default err = %v, want ErrDefaultWorkspace", err)
	}
}

func TestWorkspaceSandboxProfileUnknownWorkspace(t *testing.T) {
	_, _, layout := lifecycleStore(t)
	if _, err := layout.WorkspaceSandboxProfile(context.Background(), "ws-missing"); !errors.Is(err, ErrNoSuchWorkspace) {
		t.Fatalf("unknown workspace err = %v, want ErrNoSuchWorkspace", err)
	}
}

// Concurrent creates serialize: exactly one wins the revision-0 race, so no
// update is lost and no retry/backoff loop exists (design §8).
func TestConcurrentProfileWritesSerializeWithoutLostUpdates(t *testing.T) {
	_, _, layout := lifecycleStore(t)
	ctx := context.Background()
	seedProfileWorkspace(t, layout, "ws-1")

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			_, errs[i] = layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, WorkspaceSandboxProfile{WritablePaths: []string{"/a"}})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrSandboxProfileRevision) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1", wins)
	}
	prof, err := layout.WorkspaceSandboxProfile(ctx, "ws-1")
	if err != nil || prof == nil || prof.Revision != 1 {
		t.Fatalf("final profile = %#v, %v; want revision 1", prof, err)
	}
}

func TestSandboxGrantInsertionRechecksWorkspaceProfileAtomically(t *testing.T) {
	_, _, layout := lifecycleStore(t)
	ctx := t.Context()
	seedProfileWorkspace(t, layout, "ws-1")
	revision, err := layout.SetWorkspaceSandboxProfile(ctx, "ws-1", 0, WorkspaceSandboxProfile{
		SchemaVersion: 1, WritablePaths: []string{}, ReadOnlyPaths: []string{},
	})
	if err != nil {
		t.Fatalf("SetWorkspaceSandboxProfile: %v", err)
	}
	staleRevision := revision
	revision, err = layout.SetWorkspaceSandboxProfile(ctx, "ws-1", revision, WorkspaceSandboxProfile{
		SchemaVersion: 1, WritablePaths: []string{}, ReadOnlyPaths: []string{},
	})
	if err != nil {
		t.Fatalf("advance profile: %v", err)
	}
	grant := SandboxGrant{
		PaneID: "pane-ws-1", Version: 1, IssuedAt: 42, Workspace: "/workspace", Payload: `{}`,
	}
	if err := layout.InsertSandboxGrantIfCurrent(ctx, grant, SandboxGrantExpectation{
		WorkspaceID: "ws-1", WorkspaceProfileRevision: &staleRevision,
	}); !errors.Is(err, ErrSandboxGrantStale) {
		t.Fatalf("stale insertion error = %v, want ErrSandboxGrantStale", err)
	}
	if exists, err := layout.SandboxGrantExists(ctx, grant.PaneID); err != nil || exists {
		t.Fatalf("grant after stale insertion = %v, %v; want false, nil", exists, err)
	}
	if err := layout.InsertSandboxGrantIfCurrent(ctx, grant, SandboxGrantExpectation{
		WorkspaceID: "ws-1", WorkspaceProfileRevision: &revision,
	}); err != nil {
		t.Fatalf("current insertion: %v", err)
	}
}

func TestSandboxGrantForPane(t *testing.T) {
	_, _, layout := lifecycleStore(t)
	ctx := context.Background()
	seedProfileWorkspace(t, layout, "ws-1")

	if grant, err := layout.SandboxGrantForPane(ctx, "pane-ws-1"); err != nil || grant != nil {
		t.Fatalf("no grant = %#v, %v; want nil, nil", grant, err)
	}
	if err := layout.InsertSandboxGrant(ctx, SandboxGrant{
		PaneID: "pane-ws-1", Version: 1, IssuedAt: 42, Workspace: "/workspace", Payload: `{"realized":{"backend":"landlock"}}`,
	}); err != nil {
		t.Fatalf("InsertSandboxGrant: %v", err)
	}
	grant, err := layout.SandboxGrantForPane(ctx, "pane-ws-1")
	if err != nil || grant == nil {
		t.Fatalf("grant = %#v, %v", grant, err)
	}
	if grant.PaneID != "pane-ws-1" || grant.IssuedAt != 42 || grant.Workspace != "/workspace" {
		t.Fatalf("grant = %#v", grant)
	}
	if grant, err := layout.SandboxGrantForPane(ctx, "pane-missing"); err != nil || grant != nil {
		t.Fatalf("missing grant = %#v, %v; want nil, nil", grant, err)
	}
}
