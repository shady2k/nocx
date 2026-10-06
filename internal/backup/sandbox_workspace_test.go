package backup_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/storage"
)

func sandboxWorkspaceBackupService(t *testing.T, ids ...string) (*backup.Service, *sandbox.ProfileRepository) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path: filepath.Join(t.TempDir(), "content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range ids {
		if _, err := db.Layout().CreateWorkspace(context.Background(),
			content.Workspace{ID: id, Name: id},
			content.Tab{ID: "tab-" + id, WorkspaceID: id, Layout: content.LayoutRow},
			content.Pane{ID: "pane-" + id, TabID: "tab-" + id, Kind: content.PaneLocal, Cwd: "/workspace", SizeShare: 1},
		); err != nil {
			t.Fatal(err)
		}
	}
	profiles := sandbox.NewProfileRepository(storage.NewDocumentStore(t.TempDir()), sandbox.StandardDocumentName, db.Layout())
	_, connections, settings, journal, _ := newFakeService()
	return backup.NewService(connections, settings, journal, nil, nil, nil, profiles), profiles
}

func TestSandboxBackupMergeKeepsUnlistedWorkspaceAndReplaceResetsIt(t *testing.T) {
	ctx := context.Background()
	for _, strategy := range []backup.RestoreStrategy{backup.RestoreMerge, backup.RestoreReplace} {
		t.Run(string(strategy), func(t *testing.T) {
			source, sourceProfiles := sandboxWorkspaceBackupService(t, "workspace-alpha")
			imported := sandbox.ProfileRoots{ReadOnlyDirs: []string{"/imported-read"}, ReadWriteDirs: []string{}}
			if _, err := sourceProfiles.UpdateStandard(0, true, imported); err != nil {
				t.Fatal(err)
			}
			if _, err := sourceProfiles.UpdateWorkspaceProfile(ctx, "workspace-alpha", 0, &imported); err != nil {
				t.Fatal(err)
			}
			created, err := source.Create()
			if err != nil {
				t.Fatal(err)
			}
			destination, profiles := sandboxWorkspaceBackupService(t, "workspace-alpha", "workspace-beta")
			local := sandbox.ProfileRoots{ReadOnlyDirs: []string{}, ReadWriteDirs: []string{"/local-write"}}
			for _, id := range []string{"workspace-alpha", "workspace-beta"} {
				if _, err = profiles.UpdateWorkspaceProfile(ctx, id, 0, &local); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := destination.Preview(created.Contents, strategy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = destination.Restore(created.Contents, strategy, preview.PreviewToken); err != nil {
				t.Fatal(err)
			}
			alpha, err := profiles.GetWorkspaceProfile(ctx, "workspace-alpha")
			if err != nil || alpha.Revision != 2 || alpha.Override == nil || !reflect.DeepEqual(*alpha.Override, imported) {
				t.Fatalf("listed workspace did not restore with its local clock: %#v, %v", alpha, err)
			}
			beta, err := profiles.GetWorkspaceProfile(ctx, "workspace-beta")
			if err != nil {
				t.Fatal(err)
			}
			if strategy == backup.RestoreMerge {
				if beta.Revision != 1 || beta.Override == nil || !reflect.DeepEqual(*beta.Override, local) {
					t.Fatalf("merge replaced an unlisted workspace: %#v", beta)
				}
			} else if beta.Revision != 2 || beta.Override != nil {
				t.Fatalf("replace did not reset unlisted workspace to inheritance: %#v", beta)
			}
		})
	}
}
