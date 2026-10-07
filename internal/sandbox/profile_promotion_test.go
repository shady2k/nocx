package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func TestPromoteDirectoryCapturesBothClocksBeforeCopyingInheritance(t *testing.T) {
	ctx := context.Background()
	store := &restoreTestWorkspaceStore{profiles: map[string]WorkspaceProfile{"named": {WorkspaceID: "named", Revision: 4}}}
	repo := NewProfileRepository(&profileDocStore{}, StandardDocumentName, store)
	if _, err := repo.UpdateStandard(0, true, ProfileRoots{ReadOnlyDirs: []string{"/initial"}, ReadWriteDirs: []string{"/work"}}); err != nil {
		t.Fatal(err)
	}
	latest, err := repo.UpdateStandard(1, true, ProfileRoots{ReadOnlyDirs: []string{"/latest"}, ReadWriteDirs: []string{"/work"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, conflictErr := repo.PromoteDirectory(ctx, "named", 1, 4, ReadOnly, "/observed"); !errors.Is(conflictErr, ErrProfileConflict) {
		t.Fatalf("stale inherited standard accepted: %v", conflictErr)
	}
	unchanged, err := repo.GetWorkspaceProfile(ctx, "named")
	if err != nil || unchanged.Revision != 4 || unchanged.Override != nil {
		t.Fatalf("conflict materialized an override: %+v %v", unchanged, err)
	}
	standard, workspace, err := repo.PromoteDirectory(ctx, "named", 2, 4, ReadOnly, "/observed")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(standard, latest) || workspace.Revision != 5 || workspace.Override == nil || !reflect.DeepEqual(workspace.Override.ReadOnlyDirs, []string{"/latest", "/observed"}) || !reflect.DeepEqual(workspace.Override.ReadWriteDirs, []string{"/work"}) {
		t.Fatalf("incorrect future named scope: %+v %+v", standard, workspace)
	}
	if _, _, conflictErr := repo.PromoteDirectory(ctx, "named", 2, 4, ReadWrite, "/another"); !errors.Is(conflictErr, ErrProfileConflict) {
		t.Fatalf("stale workspace clock accepted: %v", conflictErr)
	}
	persisted, err := repo.GetStandard()
	if err != nil || !reflect.DeepEqual(persisted, latest) {
		t.Fatalf("named promotion mutated standard: %+v %v", persisted, err)
	}
}

func TestPromoteDirectoryUpgradesExactROWithoutConflictingRootClasses(t *testing.T) {
	repo := NewProfileRepository(&profileDocStore{}, StandardDocumentName, nil)
	if _, err := repo.UpdateStandard(0, true, ProfileRoots{ReadOnlyDirs: []string{"/observed"}, ReadWriteDirs: []string{}}); err != nil {
		t.Fatal(err)
	}
	standard, _, err := repo.PromoteDirectory(context.Background(), DefaultWorkspaceID, 1, 0, ReadWrite, "/observed")
	if err != nil {
		t.Fatal(err)
	}
	if standard.Revision != 2 || !standard.Enabled || len(standard.ReadOnlyDirs) != 0 || !reflect.DeepEqual(standard.ReadWriteDirs, []string{"/observed"}) {
		t.Fatalf("RO upgrade produced conflicting authority: %+v", standard)
	}
	alreadyCovered, _, err := repo.PromoteDirectory(context.Background(), DefaultWorkspaceID, 2, 0, ReadOnly, "/observed/subdirectory")
	if err != nil || !reflect.DeepEqual(alreadyCovered, standard) {
		t.Fatalf("covered future permission was expanded again: %+v %v", alreadyCovered, err)
	}
}

func TestPromoteDirectoryAtRootLimitRefusesWithoutAdvancingClock(t *testing.T) {
	repo := NewProfileRepository(&profileDocStore{}, StandardDocumentName, nil)
	roots := ProfileRoots{ReadOnlyDirs: make([]string, MaxProfileRoots), ReadWriteDirs: []string{}}
	for index := range roots.ReadOnlyDirs {
		roots.ReadOnlyDirs[index] = "/root-" + strconv.Itoa(index)
	}
	before, err := repo.UpdateStandard(0, true, roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, promotionErr := repo.PromoteDirectory(context.Background(), DefaultWorkspaceID, 1, 0, ReadOnly, "/new-observed"); promotionErr == nil {
		t.Fatal("promotion exceeded profile root bound")
	}
	after, err := repo.GetStandard()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refused promotion changed configuration: %+v %v", after, err)
	}
}
