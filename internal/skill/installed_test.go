package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// installedRoots is the production root order (internal/app/app.go): authored,
// builtin, managed, installed — installed LAST, because precedence is slice
// order and nothing downloaded may shadow what the person wrote or what we
// ship.
func installedRoots(t *testing.T, configDir string) []Root {
	t.Helper()
	return []Root{
		{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored},
		{FS: builtinFSForTest(), Provenance: ProvenanceBuiltin},
		{Dir: filepath.Join(configDir, "managed-skills"), Provenance: ProvenanceManaged},
		{Dir: filepath.Join(configDir, "installed-skills"), Provenance: ProvenanceInstalled},
	}
}

// listed is the one discovered skill under test, so an assertion says what it
// is about rather than depending on how many skills the builtin root ships.
func listed(t *testing.T, result ListResult, name string) ListedSkill {
	t.Helper()
	for _, item := range result.Skills {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("List() = %+v, want a skill named %q", result.Skills, name)
	return ListedSkill{}
}

func indexed(index []Skill, name string) (Skill, bool) {
	for _, item := range index {
		if item.Name == name {
			return item, true
		}
	}
	return Skill{}, false
}

func TestInstalledSkillIsDroppedWhenAnAuthoredSkillHoldsTheName(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "authored body")
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "deploy", "name: deploy\ndescription: theirs", "installed body")

	got := Discover(installedRoots(t, configDir))

	deploys := 0
	for _, found := range got {
		if found.Name != "deploy" {
			continue
		}
		deploys++
		if found.Provenance != ProvenanceAuthored {
			t.Fatalf("provenance = %q, want authored: a downloaded skill must never shadow one you wrote", found.Provenance)
		}
	}
	if deploys != 1 {
		t.Fatalf("Discover() = %+v, want exactly one skill named deploy", got)
	}
}

func TestAnAuthoredOrBuiltinSkillIsNeverDroppedByAnInstalledOne(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "authored body")
	installed := filepath.Join(configDir, "installed-skills")
	writeExistingSkill(t, installed, "deploy", "name: deploy\ndescription: theirs", "installed body")
	writeExistingSkill(t, installed, "skill-authoring", "name: skill-authoring\ndescription: theirs", "installed body")

	roots := installedRoots(t, configDir)
	for _, found := range Discover(roots) {
		if found.Provenance == ProvenanceInstalled {
			t.Fatalf("skill %q surfaced as installed; the authored and builtin roots hold both names", found.Name)
		}
	}

	authored, err := Read(roots, "deploy", "")
	if err != nil {
		t.Fatalf("read deploy: %v", err)
	}
	if strings.TrimSpace(string(authored.Bytes)) != "authored body" {
		t.Fatalf("deploy body = %q, want the authored bytes", authored.Bytes)
	}
	if authored.Provenance != ProvenanceAuthored {
		t.Fatalf("deploy provenance = %q, want authored", authored.Provenance)
	}
	shipped, err := Read(roots, "skill-authoring", "")
	if err != nil {
		t.Fatalf("read skill-authoring: %v", err)
	}
	if shipped.Provenance != ProvenanceBuiltin {
		t.Fatalf("skill-authoring provenance = %q, want builtin", shipped.Provenance)
	}
}

func TestInstalledSkillWithNoRecordedDigestIsChangedAndLeavesTheIndex(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "deploy", "name: deploy\ndescription: theirs", "installed body")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	deploy := listed(t, result, "deploy")
	if deploy.Provenance != ProvenanceInstalled {
		t.Fatalf("provenance = %q, want installed", deploy.Provenance)
	}
	if deploy.Status != StatusChanged {
		t.Fatalf("status = %q, want changed: an installed skill with no recorded digest fails closed", deploy.Status)
	}
	if _, found := indexed(store.Index(), "deploy"); found {
		t.Fatalf("Index() = %+v, want a changed installed skill kept out of the prompt", store.Index())
	}
	// Turned on, so the ONLY thing this asserts is the digest — an installed
	// skill also arrives inert (nocx-0bsa4.2), and a Read of one that is off
	// refuses before it can report anything about its bytes.
	if enableErr := store.SetEnabled("deploy", true); enableErr != nil {
		t.Fatalf("SetEnabled: %v", enableErr)
	}
	content, err := store.Read("deploy", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !content.Changed {
		t.Fatal("Read reported an unrecorded installed skill as unchanged")
	}
}

func TestApproveRecordsAnInstalledSkillSoItReachesTheIndexAndReadsClean(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "deploy", "name: deploy\ndescription: theirs", "installed body")
	roots := installedRoots(t, configDir)
	store := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))

	if err := store.Approve("deploy"); err != nil {
		t.Fatalf("Approve an installed skill: %v", err)
	}

	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := listed(t, result, "deploy").Status; got != StatusApproved {
		t.Fatalf("status = %q, want approved after Approve", got)
	}
	// Approve records the bytes; it does not turn the skill on, and it must
	// not — the two are different acts and an installed skill is in play only
	// once the person says so.
	if _, found := indexed(store.Index(), "deploy"); found {
		t.Fatalf("Index() = %+v, want Approve to record bytes without putting the skill in play", store.Index())
	}
	if enableErr := store.SetEnabled("deploy", true); enableErr != nil {
		t.Fatalf("SetEnabled: %v", enableErr)
	}
	index := store.Index()
	entry, found := indexed(index, "deploy")
	if !found || entry.Provenance != ProvenanceInstalled {
		t.Fatalf("Index() = %+v, want the approved installed skill in the prompt index", index)
	}
	content, err := store.Read("deploy", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if content.Changed {
		t.Fatal("an approved installed skill still reads as changed")
	}
	if strings.TrimSpace(string(content.Bytes)) != "installed body" {
		t.Fatalf("body = %q, want the installed bytes", content.Bytes)
	}

	// The interval closes only when the bytes move again.
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "deploy", "name: deploy\ndescription: theirs", "edited body")
	fresh := NewStore(OSFileSystem{}, roots, storage.NewDocumentStore(configDir))
	relisted, err := fresh.List()
	if err != nil {
		t.Fatalf("List after edit: %v", err)
	}
	if got := listed(t, relisted, "deploy").Status; got != StatusChanged {
		t.Fatalf("status = %q, want changed after the bytes moved", got)
	}
}

func TestRemoveDeletesAnInstalledSkillAndClearsItsDigest(t *testing.T) {
	configDir := t.TempDir()
	installed := filepath.Join(configDir, "installed-skills")
	writeExistingSkill(t, installed, "deploy", "name: deploy\ndescription: theirs", "installed body")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	if err := store.Approve("deploy"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if err := store.Remove("deploy"); err != nil {
		t.Fatalf("Remove an installed skill: %v", err)
	}

	if _, err := os.Stat(filepath.Join(installed, "deploy", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("SKILL.md still on disk after Remove: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(configDir, DocumentName)) //nolint:gosec // test path inside t.TempDir
	if err != nil {
		t.Fatalf("read %s: %v", DocumentName, err)
	}
	var doc struct {
		Digests map[string]string `json:"digests"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", DocumentName, err)
	}
	if _, kept := doc.Digests["deploy"]; kept {
		t.Fatalf("digests = %v, want the removed installed skill's digest cleared", doc.Digests)
	}
}

func TestRemoveStillRefusesABuiltinSkill(t *testing.T) {
	configDir := t.TempDir()
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

	err := store.Remove("skill-authoring")
	if err == nil {
		t.Fatal("want a builtin skill refused by Remove")
	}
	if !strings.Contains(err.Error(), "builtin") {
		t.Fatalf("refusal = %q, want it to name the builtin provenance", err)
	}
}

func TestAssistantWritesRefuseANameHeldByAnInstalledSkill(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "deploy", "name: deploy\ndescription: theirs", "installed body")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"create", func() error { return store.Create("deploy", "d", "b") }},
		{"update", func() error { return store.Update("deploy", "d", "b") }},
		{"delete", func() error { return store.Delete("deploy") }},
	} {
		err := tc.call()
		if err == nil {
			t.Fatalf("%s: want the installed name refused", tc.name)
		}
		if !strings.Contains(err.Error(), "installed") {
			t.Fatalf("%s refusal = %q, want it to name the holder's provenance", tc.name, err)
		}
		if !strings.Contains(err.Error(), "deploy") {
			t.Fatalf("%s refusal = %q, want it to name the skill", tc.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(configDir, "managed-skills", "deploy", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("a refused write reached the managed root: %v", err)
	}
}
