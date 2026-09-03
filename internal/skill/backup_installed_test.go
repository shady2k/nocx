package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// An installed skill TRAVELS in a backup, beside authored and managed
// (owner decision, 2026-09-03; design §11, nocx-qja4m.8). Builtin still does
// not, for the reason backup.go states: it comes from the binary.
//
// What the decision accepts, so nobody rediscovers it as a surprise: the
// digest lives in skills.json, which the snapshot carries as Settings, and it
// is computed over relative content — so a restored installed skill arrives
// `approved`. That is the same trade-off already taken for managed.

// installedSkillFiles writes a skill with a nested reference file, so the
// assertions below are about a TREE rather than a single SKILL.md.
func installedSkillFiles(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "---\nname: " + name + "\ndescription: from a URL\n---\ninstalled body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "hosts.md"), []byte("prod is eu-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCarriesInstalledTreesBesideAuthoredAndManaged(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "mine", "name: mine\ndescription: written here", "authored body")
	installedSkillFiles(t, filepath.Join(configDir, "installed-skills"), "downloaded")
	installedSkillFiles(t, filepath.Join(configDir, "installed-skills"), "another")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if len(snapshot.Installed) != 2 {
		t.Fatalf("Installed = %+v, want both installed trees", snapshot.Installed)
	}
	if snapshot.Installed[0].Name != "another" || snapshot.Installed[1].Name != "downloaded" {
		t.Fatalf("Installed = %+v, want the trees sorted by name", snapshot.Installed)
	}
	files := snapshot.Installed[1].Files
	if len(files) != 2 || files[0].Path != "SKILL.md" || files[1].Path != "references/hosts.md" {
		t.Fatalf("files = %+v, want the whole tree with sorted relative paths", files)
	}
	if files[1].Bytes != "prod is eu-1\n" {
		t.Fatalf("reference bytes = %q, want the file's contents", files[1].Bytes)
	}
	// Builtin still never travels: it comes from the binary.
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "skill-authoring") {
		t.Fatalf("snapshot carried builtin skill data: %s", raw)
	}
}

// TestARestoredInstalledSkillReadsApprovedRatherThanChanged is the criterion a
// green suite could otherwise hide: the digest in skills.json and the restored
// bytes have to agree, and the only honest way to ask is through a real Store
// in the destination rather than by inspecting the snapshot's fields.
func TestARestoredInstalledSkillReadsApprovedRatherThanChanged(t *testing.T) {
	sourceConfig := t.TempDir()
	installedSkillFiles(t, filepath.Join(sourceConfig, "installed-skills"), "downloaded")
	source := NewStore(OSFileSystem{}, installedRoots(t, sourceConfig), storage.NewDocumentStore(sourceConfig))
	if err := source.Approve("downloaded"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := source.SetEnabled("downloaded", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	destinationConfig := t.TempDir()
	destination := NewStore(OSFileSystem{}, installedRoots(t, destinationConfig), storage.NewDocumentStore(destinationConfig))
	if restoreErr := destination.RestoreSnapshot(snapshot); restoreErr != nil {
		t.Fatalf("RestoreSnapshot: %v", restoreErr)
	}

	result, err := destination.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := listed(t, result, "downloaded")
	if got.Provenance != ProvenanceInstalled {
		t.Fatalf("provenance = %q, want installed", got.Provenance)
	}
	if got.Status != StatusApproved {
		t.Fatalf("status = %q, want approved: the restored bytes must match the digest that travelled with them", got.Status)
	}
	if got.Enabled {
		t.Fatal("the enabled state did not travel: the skill was disabled at the source")
	}
	body, err := os.ReadFile(filepath.Join(destinationConfig, "installed-skills", "downloaded", "references", "hosts.md")) //nolint:gosec // test-owned temp dir
	if err != nil {
		t.Fatalf("restored reference file: %v", err)
	}
	if string(body) != "prod is eu-1\n" {
		t.Fatalf("reference bytes = %q, want the whole tree restored", body)
	}
}

// TestSnapshotFromAnOlderBuildLeavesTheInstalledRootAlone: a backup written
// before installed skills travelled has no `installed` key at all. Restoring
// it must neither fail nor empty the root — restore writes trees, it never
// deletes them, and an absent field means "say nothing about installed".
func TestSnapshotFromAnOlderBuildLeavesTheInstalledRootAlone(t *testing.T) {
	older := `{"authored":[{"name":"mine","files":[{"path":"SKILL.md","bytes":"---\nname: mine\ndescription: written here\n---\nauthored body\n"}]}],` +
		`"managed":[],"settings":{"schemaVersion":2,"disabled":[],"digests":{}}}`
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(older), &snapshot); err != nil {
		t.Fatalf("decode a snapshot from an older build: %v", err)
	}
	if snapshot.Installed != nil {
		t.Fatalf("Installed = %+v, want nil for a snapshot that has no such field", snapshot.Installed)
	}

	configDir := t.TempDir()
	installedRoot := filepath.Join(configDir, "installed-skills")
	installedSkillFiles(t, installedRoot, "downloaded")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

	if err := store.RestoreSnapshot(snapshot); err != nil {
		t.Fatalf("RestoreSnapshot from an older build: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(installedRoot, "downloaded", "SKILL.md")) //nolint:gosec // test-owned temp dir
	if err != nil {
		t.Fatalf("the installed skill did not survive the restore: %v", err)
	}
	if !strings.Contains(string(body), "installed body") {
		t.Fatalf("installed SKILL.md = %q, want it untouched", body)
	}
	if _, statErr := os.Stat(filepath.Join(installedRoot, "downloaded", "references", "hosts.md")); statErr != nil {
		t.Fatalf("the installed reference file did not survive the restore: %v", statErr)
	}
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := listed(t, result, "downloaded").Provenance; got != ProvenanceInstalled {
		t.Fatalf("provenance = %q, want the installed skill still discovered", got)
	}
	if got := listed(t, result, "mine").Provenance; got != ProvenanceAuthored {
		t.Fatalf("provenance = %q, want the older snapshot's authored tree restored", got)
	}
}

// The containment checks are the reason a snapshot may be handed to a Store at
// all: a backup document is a file the person can edit or receive. Each arm
// below is one way out of the installed root, and each must be refused.
func TestRestoreSnapshotContainsInstalledSkillsWithinTheirRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prepare returns the tree to restore and a path outside the root
		// that must remain untouched.
		prepare func(t *testing.T, root, outside string) SnapshotTree
	}{
		{
			name: "an absolute path",
			prepare: func(_ *testing.T, _, outside string) SnapshotTree {
				return SnapshotTree{
					Name:  "downloaded",
					Files: []SnapshotFile{{Path: filepath.Join(outside, "escaped.md"), Bytes: "must not escape"}},
				}
			},
		},
		{
			name: "an escaping relative path",
			prepare: func(_ *testing.T, _, _ string) SnapshotTree {
				return SnapshotTree{
					Name:  "downloaded",
					Files: []SnapshotFile{{Path: "../../escaped.md", Bytes: "must not escape"}},
				}
			},
		},
		{
			name: "a symlinked target file",
			prepare: func(t *testing.T, root, outside string) SnapshotTree {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, "downloaded"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "escaped.md"), filepath.Join(root, "downloaded", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
				return SnapshotTree{
					Name:  "downloaded",
					Files: []SnapshotFile{{Path: "SKILL.md", Bytes: "must not escape"}},
				}
			},
		},
		{
			name: "a symlinked ancestor directory",
			prepare: func(t *testing.T, root, outside string) SnapshotTree {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, "downloaded"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "downloaded", "references")); err != nil {
					t.Fatal(err)
				}
				return SnapshotTree{
					Name:  "downloaded",
					Files: []SnapshotFile{{Path: "references/escaped.md", Bytes: "must not escape"}},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir, outside := t.TempDir(), t.TempDir()
			root := filepath.Join(configDir, "installed-skills")
			tree := tc.prepare(t, root, outside)
			store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

			err := store.RestoreSnapshot(Snapshot{Installed: []SnapshotTree{tree}})

			if err == nil {
				t.Fatal("want the restore refused")
			}
			if !strings.Contains(err.Error(), "installed") {
				t.Fatalf("refusal = %q, want it to name the installed root", err)
			}
			if _, statErr := os.Stat(filepath.Join(outside, "escaped.md")); !os.IsNotExist(statErr) {
				t.Fatalf("the restore escaped the installed root, stat error = %v", statErr)
			}
		})
	}
}

// TestAnUnreadableFileFailsEveryCarriedRootTheSameWay is the assertion the
// bead calls the walk-without-an-arm defect. Before installed travelled, its
// root was WALKED anyway — the `root.Dir != ""` guard preceded the provenance
// switch — so an unreadable file under installed-skills failed the whole
// backup for a root whose contents never reached the snapshot. One behaviour
// for one concept: whatever an unreadable file does under authored, it does
// under installed too, and the walk now means something because
// TestSnapshotCarriesInstalledTreesBesideAuthoredAndManaged says the bytes
// arrive.
func TestAnUnreadableFileFailsEveryCarriedRootTheSameWay(t *testing.T) {
	for _, tc := range []struct {
		provenance Provenance
		dir        string
	}{
		{provenance: ProvenanceAuthored, dir: "skills"},
		{provenance: ProvenanceManaged, dir: "managed-skills"},
		{provenance: ProvenanceInstalled, dir: "installed-skills"},
	} {
		t.Run(string(tc.provenance), func(t *testing.T) {
			configDir := t.TempDir()
			writeExistingSkill(t, filepath.Join(configDir, tc.dir), "deploy", "name: deploy\ndescription: d", "body")
			unreadable := filepath.Join(configDir, tc.dir, "deploy", "SKILL.md")
			if err := os.Chmod(unreadable, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
			store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

			_, err := store.Snapshot()

			if err == nil {
				t.Fatal("want an unreadable file to fail the snapshot")
			}
			want := "snapshot " + string(tc.provenance) + " skills: walk \"deploy\": "
			if !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("error = %q, want the prefix %q", err, want)
			}
		})
	}
}

// TestSnapshotSkipsARootItDoesNotCarry closes the other half of the same defect: a
// root the snapshot does not carry must not be read at all, so it cannot fail
// a backup either. Builtin is backed by an fs.FS with no Dir, so the check
// that matters is the one the switch now makes rather than the Dir guard.
func TestSnapshotSkipsARootItDoesNotCarry(t *testing.T) {
	configDir := t.TempDir()
	otherRoot := filepath.Join(configDir, "other-skills")
	writeExistingSkill(t, otherRoot, "deploy", "name: deploy\ndescription: d", "body")
	unreadable := filepath.Join(otherRoot, "deploy", "SKILL.md")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	roots := append(installedRoots(t, configDir), Root{Dir: otherRoot, Provenance: ProvenanceBuiltin})
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))

	if _, err := store.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v; a root the snapshot does not carry must not be walked", err)
	}
}
