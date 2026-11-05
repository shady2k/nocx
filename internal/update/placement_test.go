package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReconcileUsesStateBesideReadOnlyTarget(t *testing.T) {
	for _, mode := range []os.FileMode{0o555, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			targetDir := filepath.Join(root, "target")
			state := filepath.Join(root, "state")
			for _, dir := range []string{targetDir, state} {
				if err := os.Mkdir(dir, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(targetDir, "nocx.AppImage")
			makeMinimalAppImage(t, target)
			if err := os.Chmod(targetDir, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(targetDir, 0o750) }) //nolint:gosec // Restore traversal of this private test directory for cleanup.
			u := NewUpdater(UpdaterConfig{Platform: newFakePlatform(), InstallPath: target, StateDir: state})
			if err := u.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(state, ".nocx-update.lock")); err != nil {
				t.Fatalf("state lock: %v", err)
			}
			entries, err := os.ReadDir(targetDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "nocx.AppImage" {
				t.Fatalf("target contents: %v", entries)
			}
		})
	}
}

type refusingPlacementPlatform struct {
	Platform
	refusal error
}

func (p refusingPlacementPlatform) Preflight(context.Context, string) error { return p.refusal }

func placementTree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestApplyRefusalLeavesAllDirectoriesUntouched(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	targetDir := filepath.Join(root, "target")
	for _, dir := range []string{state, targetDir} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(targetDir, "nocx.AppImage")
	makeMinimalAppImage(t, target)
	before := placementTree(t, root)
	refusal := errors.New("installation refused")
	u := NewUpdater(UpdaterConfig{Platform: refusingPlacementPlatform{newFakePlatform(), refusal}, InstallPath: target, StateDir: state, CurrentVersion: "0.1.0"})
	err := u.Apply(context.Background(), &UpdateInfo{Version: "0.2.0"})
	if !errors.Is(err, refusal) {
		t.Fatalf("Apply = %v", err)
	}
	if after := placementTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("before %v; after %v", before, after)
	}
}

func TestInstallTargetSelection(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstWritable bool
		want          string
	}{
		{"system", true, "/Applications/nocx.app"}, {"home fallback", false, "/home/person/Applications/nocx.app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probed []string
			got, err := ResolveInstallTarget("darwin", "/AppTranslocation/random/nocx.app/Contents/MacOS/nocx", "", "/home/person", func(dir string) error {
				probed = append(probed, dir)
				if dir == "/Applications" && !tc.firstWritable {
					return os.ErrPermission
				}
				return nil
			})
			if err != nil || got != tc.want {
				t.Fatalf("target %q, error %v", got, err)
			}
			if tc.firstWritable && len(probed) != 1 || !tc.firstWritable && len(probed) != 2 {
				t.Fatalf("probed %v", probed)
			}
		})
	}
	t.Run("neither writable", func(t *testing.T) {
		_, err := ResolveInstallTarget("darwin", "/Downloads/nocx.app/Contents/MacOS/nocx", "", "/home/person", func(string) error { return os.ErrPermission })
		if err == nil {
			t.Fatal("expected refusal")
		}
	})
}

func TestInstallTargetCreatesHomeApplications(t *testing.T) {
	home := t.TempDir()
	target, err := ResolveInstallTarget("darwin", "/AppTranslocation/nocx.app/Contents/MacOS/nocx", "", home, func(dir string) error {
		if dir == "/Applications" {
			return os.ErrPermission
		}
		return ProbeInstallDirectory(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(home, "Applications", "nocx.app") {
		t.Fatalf("target = %q", target)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 0 {
		t.Fatalf("created Applications directory contains probe debris: %v, %v", entries, err)
	}
}

func TestRecoveryUsesJournalTargetAfterSelectionChanges(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		t.Run(fmt.Sprint(finalize), func(t *testing.T) {
			install, jp := pendingUpdate(t, "0.1.0", "0.2.0")
			selected := filepath.Join(t.TempDir(), "different.AppImage")
			makeMinimalAppImage(t, selected)
			before, err := os.ReadFile(selected) //nolint:gosec // selected is inside t.TempDir.
			if err != nil {
				t.Fatal(err)
			}
			u := NewUpdater(UpdaterConfig{InstallPath: selected, StateDir: filepath.Dir(jp), CurrentVersion: "0.2.0", Platform: NewPlatform(), Coordinator: &stubProbe{build: CoordinatorBuild{Version: "0.2.0"}}})
			if finalize {
				err = u.ReportHealthy(context.Background())
				if _, statErr := os.Stat(backupPath(install)); statErr != nil {
					t.Fatalf("journal target backup: %v", statErr)
				}
			} else {
				err = u.Reconcile(context.Background())
				rec, readErr := readJournal(jp)
				if readErr != nil || rec == nil || rec.LaunchAttempts != 2 {
					t.Fatalf("journal target was not reconciled: %+v, %v", rec, readErr)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(selected) //nolint:gosec // selected is inside t.TempDir.
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("new selection changed: %q, %v", after, err)
			}
		})
	}
}

func TestFirstInstallInterruptedBeforeRename(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprint(staged), func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "nocx.AppImage")
			jp := testJournalPath(t, target)
			rec := &journalRecord{TxID: "first", InstallPath: target, FromVersion: "0.1.0", ToVersion: "0.2.0"}
			extract := extractionDir(target, rec.TxID)
			if err := os.MkdirAll(extract, 0o750); err != nil {
				t.Fatal(err)
			}
			if staged {
				makeMinimalAppImage(t, swapPath(target))
				var err error
				rec.NewBundleID, err = statBundleID(swapPath(target))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := writeJournal(jp, rec); err != nil {
				t.Fatal(err)
			}
			u := NewUpdater(UpdaterConfig{Platform: newFakePlatform(), InstallPath: target, StateDir: filepath.Dir(jp)})
			if err := u.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{target, swapPath(target), extract, jp} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("interrupted first install left %s: %v", path, err)
				}
			}
		})
	}
}
