package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

func TestCorruptDisabledDocumentFailsClosed(t *testing.T) {
	configDir := t.TempDir()
	authored := filepath.Join(configDir, "skills")
	writeExistingSkill(t, authored, "deploy", "name: deploy\ndescription: d", "body")
	if err := os.WriteFile(filepath.Join(configDir, DocumentName), []byte(`{"schemaVersion":1,"disabled":[`), 0o600); err != nil {
		t.Fatalf("write corrupt document: %v", err)
	}

	source := NewStore(OSFileSystem{}, []Root{
		{Dir: authored, Provenance: ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: ProvenanceManaged},
	}, storage.NewDocumentStore(configDir))

	if got := source.Index(); len(got) != 0 {
		t.Fatalf("want no skills on a corrupt document, got %d", len(got))
	}
	if _, err := source.Read("deploy", ""); err == nil {
		t.Fatal("want skills.read to refuse a skill when the disabled document is corrupt")
	}
}

func TestSetEnabledPersistsAndHidesSkill(t *testing.T) {
	configDir := t.TempDir()
	authored := filepath.Join(configDir, "skills")
	writeExistingSkill(t, authored, "deploy", "name: deploy\ndescription: d", "body")
	roots := []Root{
		{Dir: authored, Provenance: ProvenanceAuthored},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: ProvenanceManaged},
	}
	docStore := storage.NewDocumentStore(configDir)
	source := NewStore(OSFileSystem{}, roots, docStore)

	if err := source.SetEnabled("deploy", false); err != nil {
		t.Fatalf("disable skill: %v", err)
	}
	list, err := source.List()
	if err != nil {
		t.Fatalf("list disabled skill: %v", err)
	}
	if len(list.Skills) != 1 || list.Skills[0].Enabled {
		t.Fatalf("list = %+v, want one disabled skill", list.Skills)
	}
	if got := source.Index(); len(got) != 0 {
		t.Fatalf("index = %+v, want no disabled skills", got)
	}
	if _, readErr := source.Read("deploy", ""); readErr == nil {
		t.Fatal("want skills.read to refuse a disabled skill")
	}

	fresh := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	if got := fresh.Index(); len(got) != 0 {
		t.Fatalf("fresh index = %+v, want no disabled skills", got)
	}
	freshList, err := fresh.List()
	if err != nil {
		t.Fatalf("fresh list: %v", err)
	}
	if len(freshList.Skills) != 1 || freshList.Skills[0].Enabled {
		t.Fatalf("fresh list = %+v, want one disabled skill", freshList.Skills)
	}
}

func TestApprovedDigestCoversEveryFileAndIsDeterministic(t *testing.T) {
	configDir := t.TempDir()
	managed := filepath.Join(configDir, "managed-skills")
	store := NewStore(OSFileSystem{}, []Root{{Dir: managed, Provenance: ProvenanceManaged}}, storage.NewDocumentStore(configDir))
	if err := store.Create("deploy", "deploy", "body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(managed, "deploy", "references"), 0o700); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	for _, file := range []struct {
		name string
		body string
	}{
		{name: "z.md", body: "last"},
		{name: "a.md", body: "first"},
	} {
		if err := os.WriteFile(filepath.Join(managed, "deploy", "references", file.name), []byte(file.body), 0o600); err != nil {
			t.Fatalf("write %s: %v", file.name, err)
		}
	}
	first, err := hashSkillDirectory(filepath.Join(managed, "deploy"))
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second, err := hashSkillDirectory(filepath.Join(managed, "deploy"))
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first != second {
		t.Fatalf("hash changed without content change: %q != %q", first, second)
	}
	if got := store.Index(); len(got) != 0 {
		t.Fatalf("index = %+v, want reference files to invalidate approval", got)
	}
}

// approvedDigest reads back what Approve actually wrote. Asserting on the
// document rather than on a nil error is the point: Approve's whole job is to
// record the digest, so "it returned no error" is not evidence it did.
func approvedDigest(t *testing.T, configDir, name string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(configDir, DocumentName)) //nolint:gosec // test path inside t.TempDir
	if os.IsNotExist(err) {
		// No document at all is the strongest form of "nothing recorded": a
		// refused Approve must not create one.
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", DocumentName, err)
	}
	var doc struct {
		Digests map[string]string `json:"digests"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", DocumentName, err)
	}
	digest, found := doc.Digests[name]
	return digest, found
}

// TestApproveRecordsAnInstalledSkillWhileTheManagedRootIsASymlink is the whole
// bead (nocx-ablu5). Approving an INSTALLED skill wrote nothing into the
// managed root and never has, yet a guard on that root refused the call — so a
// person whose managed-skills happened to be a symlink could not approve a
// skill that lives somewhere else entirely.
func TestApproveRecordsAnInstalledSkillWhileTheManagedRootIsASymlink(t *testing.T) {
	configDir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(configDir, "managed-skills")); err != nil {
		t.Fatalf("symlink managed root: %v", err)
	}
	installed := filepath.Join(configDir, "installed-skills")
	writeExistingSkill(t, installed, "deploy", "name: deploy\ndescription: theirs", "installed body")
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	if got := listed(t, mustList(t, store), "deploy").Status; got != StatusChanged {
		t.Fatalf("status before Approve = %q, want changed", got)
	}

	if err := store.Approve("deploy"); err != nil {
		t.Fatalf("Approve an installed skill under a symlinked managed root: %v", err)
	}

	digest, recorded := approvedDigest(t, configDir, "deploy")
	if !recorded {
		t.Fatal("Approve recorded no digest for the installed skill")
	}
	want, err := hashSkillDirectory(filepath.Join(installed, "deploy"))
	if err != nil {
		t.Fatalf("hash installed skill: %v", err)
	}
	if digest != want {
		t.Fatalf("digest = %q, want the hash of the installed skill's own directory %q", digest, want)
	}
	if got := listed(t, mustList(t, NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))), "deploy").Status; got != StatusApproved {
		t.Fatalf("status after Approve = %q, want approved", got)
	}
	// The symlink target is READ-ONLY to Approve, and stays untouched.
	entries, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target = %v, want Approve to write nothing into the managed root", entries)
	}
}

func TestApproveRecordsAChangedManagedSkill(t *testing.T) {
	configDir := t.TempDir()
	managed := filepath.Join(configDir, "managed-skills")
	writeExistingSkill(t, managed, "deploy", "name: deploy\ndescription: drafted", "managed body")
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	if got := listed(t, mustList(t, store), "deploy").Status; got != StatusChanged {
		t.Fatalf("status before Approve = %q, want changed", got)
	}

	if err := store.Approve("deploy"); err != nil {
		t.Fatalf("Approve a changed managed skill: %v", err)
	}

	digest, recorded := approvedDigest(t, configDir, "deploy")
	if !recorded {
		t.Fatal("Approve recorded no digest for the managed skill")
	}
	want, err := hashSkillDirectory(filepath.Join(managed, "deploy"))
	if err != nil {
		t.Fatalf("hash managed skill: %v", err)
	}
	if digest != want {
		t.Fatalf("digest = %q, want %q", digest, want)
	}
}

// TestApproveRefusesAProvenanceWithNoApprovalToRecord keeps the precondition
// Approve actually has. Authored bytes are the person's own and builtin bytes
// are ours; neither has an approval digest to diverge from, so there is
// nothing for Approve to record and the document must stay untouched.
func TestApproveRefusesAProvenanceWithNoApprovalToRecord(t *testing.T) {
	for _, tc := range []struct{ name, skill string }{
		{name: "authored", skill: "deploy"},
		{name: "builtin", skill: "skill-authoring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "authored body")
			store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

			err := store.Approve(tc.skill)
			if err == nil {
				t.Fatalf("want Approve to refuse a %s skill", tc.name)
			}
			if !strings.Contains(err.Error(), "not a changed managed or installed skill") {
				t.Fatalf("refusal = %q, want it to name the provenance precondition", err)
			}
			if _, recorded := approvedDigest(t, configDir, tc.skill); recorded {
				t.Fatalf("Approve recorded a digest for a %s skill", tc.name)
			}
		})
	}
}

func TestApproveRefusesAnUnchangedSkill(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "deploy", "name: deploy\ndescription: theirs", "installed body")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	if err := store.Approve("deploy"); err != nil {
		t.Fatalf("first Approve: %v", err)
	}
	first, recorded := approvedDigest(t, configDir, "deploy")
	if !recorded {
		t.Fatal("first Approve recorded no digest")
	}

	err := store.Approve("deploy")
	if err == nil {
		t.Fatal("want Approve to refuse a skill that is already approved")
	}
	if !strings.Contains(err.Error(), "not changed") {
		t.Fatalf("refusal = %q, want it to say the skill is not changed", err)
	}
	if second, _ := approvedDigest(t, configDir, "deploy"); second != first {
		t.Fatalf("digest moved on a refused Approve: %q -> %q", first, second)
	}
}

func TestApproveRefusesANameThatMatchesNothing(t *testing.T) {
	configDir := t.TempDir()
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

	err := store.Approve("absent")
	if err == nil {
		t.Fatal("want Approve to refuse a name no root holds")
	}
	if !strings.Contains(err.Error(), "not a changed managed or installed skill") {
		t.Fatalf("refusal = %q, want it to name the precondition", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, DocumentName)); !os.IsNotExist(err) {
		t.Fatalf("a refused Approve wrote %s: %v", DocumentName, err)
	}
}

// mustList is List with its error already handled, so the assertions above
// read as the state they are about.
func mustList(t *testing.T, store *Store) ListResult {
	t.Helper()
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("List document error: %s", result.DocumentError)
	}
	return result
}
