package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// aDigest is a syntactically valid sha256 hex digest. The reader checks the
// shape of a digest, not that it matches any directory, so a constant is
// enough wherever the test is about the document rather than the bytes.
const aDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// aSourceRow is a whole source row as this build writes one: the address, the
// time, and the digest of what that address served. It deliberately carries a
// DIFFERENT digest from aDigest, because the two are different facts — one is
// what nocx adopted onto disk, the other is what the address gave — and a
// fixture that made them equal would let a test pass that confused them.
const aServedDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

const aSourceRow = `{"url":"https://example.com/skills/downloaded/SKILL.md","installedAt":"2026-09-03T12:00:00Z",` +
	`"digest":"` + aServedDigest + `"}`

// aVersionTwoSourceRow is what a document written before the digest existed
// holds. Every rung of this module is purely additive, so it still reads —
// and it reads as "nothing was recorded", never as "it did not match".
const aVersionTwoSourceRow = `{"url":"https://example.com/skills/downloaded/SKILL.md","installedAt":"2026-09-03T12:00:00Z"}`

func writeDocument(t *testing.T, configDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(configDir, DocumentName), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", DocumentName, err)
	}
}

func readDocument(t *testing.T, configDir string) struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Disabled      []string                   `json:"disabled"`
	Digests       map[string]string          `json:"digests"`
	Sources       map[string]json.RawMessage `json:"sources"`
} {
	t.Helper()
	var doc struct {
		SchemaVersion int                        `json:"schemaVersion"`
		Disabled      []string                   `json:"disabled"`
		Digests       map[string]string          `json:"digests"`
		Sources       map[string]json.RawMessage `json:"sources"`
	}
	raw, err := os.ReadFile(filepath.Join(configDir, DocumentName)) //nolint:gosec // test-owned temp dir
	if err != nil {
		t.Fatalf("read %s: %v", DocumentName, err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v (%s)", DocumentName, err, raw)
	}
	return doc
}

// TestVersionOneDocumentLoadsAndTheNextWritePersistsTheCurrentVersion asserts BOTH
// halves of the version bump, because either alone is a broken product: a v1
// document is what every existing install has on disk, and a bump with no
// migration rung fails the whole list closed rather than loading it.
func TestVersionOneDocumentLoadsAndTheNextWritePersistsTheCurrentVersion(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "body")
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "downloaded", "name: downloaded\ndescription: theirs", "body")
	writeDocument(t, configDir, `{"schemaVersion":1,"disabled":["deploy"],"digests":{"downloaded":"`+aDigest+`"}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError = %q, want a version 1 document to load", result.DocumentError)
	}
	if got := listed(t, result, "deploy"); got.Enabled {
		t.Fatal("deploy is enabled: the version 1 document's disabled list did not survive the migration")
	}

	if err := store.SetEnabled("deploy", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	persisted := readDocument(t, configDir)
	if persisted.SchemaVersion != int(skillsSchemaVersion) {
		t.Fatalf("persisted schemaVersion = %d, want %d", persisted.SchemaVersion, skillsSchemaVersion)
	}
	if persisted.Digests["downloaded"] != aDigest {
		t.Fatalf("persisted digests = %v, want the version 1 digest carried forward", persisted.Digests)
	}
}

// TestADocumentNewerThanTheModuleIsRefused pins the other end of the version
// bump: an older build must not reinterpret a shape it does not know.
func TestADocumentNewerThanTheModuleIsRefused(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "body")
	writeDocument(t, configDir, fmt.Sprintf(`{"schemaVersion":%d,"disabled":[]}`, skillsSchemaVersion+1))

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError == "" {
		t.Fatal("DocumentError is empty: a document newer than the module must be refused, not read")
	}
	if len(result.Skills) != 0 {
		t.Fatalf("Skills = %+v, want none beside a document error", result.Skills)
	}
}

// TestSourcesAreReadAsStrictlyAsDigests is the point of the bead: a sources
// map is read with exactly the strictness the digests map already gets, so a
// row nobody can act on fails the WHOLE list into DocumentError rather than
// being silently ignored — a half-read document is how a skill goes on being
// listed with a source that cannot be updated.
func TestSourcesAreReadAsStrictlyAsDigests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources string
		wantErr bool
	}{
		{name: "a canonical name and an absolute https URL", sources: `{"downloaded":` + aSourceRow + `}`},
		{name: "a non-canonical name", sources: `{"Downloaded":` + aSourceRow + `}`, wantErr: true},
		{name: "a name that is not a skill name at all", sources: `{"../escape":` + aSourceRow + `}`, wantErr: true},
		{name: "an unparseable URL", sources: `{"downloaded":{"url":"https://example.com/%zz","installedAt":"2026-09-03T12:00:00Z"}}`, wantErr: true},
		{name: "a relative URL", sources: `{"downloaded":{"url":"skills/downloaded/SKILL.md","installedAt":"2026-09-03T12:00:00Z"}}`, wantErr: true},
		{name: "an empty URL", sources: `{"downloaded":{"url":"","installedAt":"2026-09-03T12:00:00Z"}}`, wantErr: true},
		{name: "a URL with no host", sources: `{"downloaded":{"url":"https:///SKILL.md","installedAt":"2026-09-03T12:00:00Z"}}`, wantErr: true},
		{name: "a URL carrying credentials", sources: `{"downloaded":{"url":"https://user:pw@example.com/SKILL.md","installedAt":"2026-09-03T12:00:00Z"}}`, wantErr: true},
		{name: "a scheme that is not fetchable", sources: `{"downloaded":{"url":"file:///etc/passwd","installedAt":"2026-09-03T12:00:00Z"}}`, wantErr: true},
		{name: "an installedAt that is not RFC3339", sources: `{"downloaded":{"url":"https://example.com/SKILL.md","installedAt":"yesterday"}}`, wantErr: true},
		{name: "an absent installedAt", sources: `{"downloaded":{"url":"https://example.com/SKILL.md"}}`, wantErr: true},
		// The digest of what the address served (nocx-ojfuc.3). Absent is a
		// row written before the field existed and is READ, because the rung
		// that carried the document forward converted nothing; a value of the
		// wrong shape is one nothing could ever compare against, and is
		// refused exactly as a bad adopted digest is.
		{name: "an absent served digest", sources: `{"downloaded":` + aVersionTwoSourceRow + `}`},
		{
			name:    "a served digest that is not hexadecimal",
			sources: `{"downloaded":{"url":"https://example.com/SKILL.md","installedAt":"2026-09-03T12:00:00Z","digest":"` + strings.Repeat("z", 64) + `"}}`,
			wantErr: true,
		},
		{
			name:    "a served digest of the wrong length",
			sources: `{"downloaded":{"url":"https://example.com/SKILL.md","installedAt":"2026-09-03T12:00:00Z","digest":"abcdef"}}`,
			wantErr: true,
		},
		{
			name:    "a served digest carrying a prefix",
			sources: `{"downloaded":{"url":"https://example.com/SKILL.md","installedAt":"2026-09-03T12:00:00Z","digest":"sha256:` + aServedDigest + `"}}`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "downloaded", "name: downloaded\ndescription: theirs", "body")
			writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":`+tc.sources+`}`)

			store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
			result, err := store.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !tc.wantErr {
				if result.DocumentError != "" {
					t.Fatalf("DocumentError = %q, want a well-formed sources row to load", result.DocumentError)
				}
				return
			}
			if result.DocumentError == "" {
				t.Fatal("DocumentError is empty: a bad sources row must fail the whole list, never degrade to one entry ignored")
			}
			if !strings.Contains(result.DocumentError, DocumentName) {
				t.Fatalf("DocumentError = %q, want it to name %s", result.DocumentError, DocumentName)
			}
			if result.DocumentPath != filepath.Join(configDir, DocumentName) {
				t.Fatalf("DocumentPath = %q, want the file the person has to repair", result.DocumentPath)
			}
			if len(result.Skills) != 0 {
				t.Fatalf("Skills = %+v, want none beside a document error", result.Skills)
			}
		})
	}
}

// TestASourceEntryNeverChangesProvenance is the shadowing defence restated for
// the document: provenance is the root and stays the root, so a row in
// skills.json — which anything able to write the file could add — cannot make
// a skill the person wrote look downloaded, nor demand a digest it never had.
func TestASourceEntryNeverChangesProvenance(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "body")
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":{"deploy":`+aSourceRow+`}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError = %q, want none", result.DocumentError)
	}
	deploy := listed(t, result, "deploy")
	if deploy.Provenance != ProvenanceAuthored {
		t.Fatalf("provenance = %q, want authored: provenance is the root and a document row may never reassign it", deploy.Provenance)
	}
	if deploy.Status != StatusApproved {
		t.Fatalf("status = %q, want approved: an authored skill has no approval digest to diverge from", deploy.Status)
	}
	if _, ok := indexed(store.Index(), "deploy"); !ok {
		t.Fatal("deploy left the prompt index: a source row made an authored skill fail closed")
	}
}

// TestRemovingAnInstalledSkillClearsItsSourceBesideItsDigest holds the pair
// together. A source outliving the skill is a row for a name that is not
// there, which the next install of that name would read as its own history.
func TestRemovingAnInstalledSkillClearsItsSourceBesideItsDigest(t *testing.T) {
	configDir := t.TempDir()
	installed := filepath.Join(configDir, "installed-skills")
	writeExistingSkill(t, installed, "downloaded", "name: downloaded\ndescription: theirs", "body")
	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	if err := store.Approve("downloaded"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	approved := readDocument(t, configDir)
	if approved.Digests["downloaded"] == "" {
		t.Fatalf("document = %+v, want a recorded digest after Approve", approved)
	}
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{"downloaded":"`+approved.Digests["downloaded"]+`"},"sources":{"downloaded":`+aSourceRow+`}}`)

	fresh := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	if err := fresh.Remove("downloaded"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	after := readDocument(t, configDir)
	if _, still := after.Digests["downloaded"]; still {
		t.Fatalf("digests = %v, want the removed skill's digest cleared", after.Digests)
	}
	if _, still := after.Sources["downloaded"]; still {
		t.Fatalf("sources = %v, want the removed skill's source cleared beside its digest", after.Sources)
	}
}

// TestAnUnrelatedWritePreservesSources is the live half of the write path
// today. Every mutation rebuilds the whole document from what was loaded, so a
// key the writer does not carry is a key the next toggle silently deletes —
// which for a source is losing the only record of where a skill came from.
func TestAnUnrelatedWritePreservesSources(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "skills"), "deploy", "name: deploy\ndescription: mine", "body")
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "downloaded", "name: downloaded\ndescription: theirs", "body")
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":{"downloaded":`+aSourceRow+`}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	if err := store.SetEnabled("deploy", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	after := readDocument(t, configDir)
	row, still := after.Sources["downloaded"]
	if !still {
		t.Fatalf("sources = %v, want the untouched skill's source to survive an unrelated toggle", after.Sources)
	}
	var got struct {
		URL         string `json:"url"`
		InstalledAt string `json:"installedAt"`
	}
	if err := json.Unmarshal(row, &got); err != nil {
		t.Fatalf("parse persisted source: %v", err)
	}
	if got.URL != "https://example.com/skills/downloaded/SKILL.md" || got.InstalledAt != "2026-09-03T12:00:00Z" {
		t.Fatalf("persisted source = %+v, want it carried through byte for byte", got)
	}
}

// TestBackupCarriesAVersionTwoDocumentUnchanged pins the version bump against
// the one thing in the backup path that reads a version at all: the document
// travels as raw bytes in Snapshot.Settings, and RestoreSnapshot's guard only
// refuses a MISSING schemaVersion. A bump that tripped that guard would
// restore a profile with no approvals and no sources, and the guard is far
// enough from this file that nothing else would say so.
//
// Whether the installed skill TREE travels in a backup is a separate open
// decision (nocx-qja4m.8) and this test deliberately takes no position on it —
// it asserts only that the document arrives byte for byte.
func TestBackupCarriesAVersionTwoDocumentUnchanged(t *testing.T) {
	source := t.TempDir()
	writeExistingSkill(t, filepath.Join(source, "skills"), "deploy", "name: deploy\ndescription: mine", "body")
	writeDocument(t, source, `{"schemaVersion":2,"disabled":["deploy"],"digests":{},"sources":{"downloaded":`+aSourceRow+`}}`)
	store := NewStore(OSFileSystem{}, installedRoots(t, source), storage.NewDocumentStore(source))
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	destination := t.TempDir()
	restored := NewStore(OSFileSystem{}, installedRoots(t, destination), storage.NewDocumentStore(destination))
	if restoreErr := restored.RestoreSnapshot(snapshot); restoreErr != nil {
		t.Fatalf("RestoreSnapshot: %v", restoreErr)
	}
	after := readDocument(t, destination)
	if after.SchemaVersion != 2 {
		t.Fatalf("restored schemaVersion = %d, want 2", after.SchemaVersion)
	}
	if len(after.Disabled) != 1 || after.Disabled[0] != "deploy" {
		t.Fatalf("restored disabled = %v, want [deploy]", after.Disabled)
	}
	if _, ok := after.Sources["downloaded"]; !ok {
		t.Fatalf("restored sources = %v, want the source row carried through", after.Sources)
	}
	result, err := restored.List()
	if err != nil {
		t.Fatalf("List after restore: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError after restore = %q, want a restored document to load", result.DocumentError)
	}
}

// TestListCarriesTheRecordedSourceOfAnInstalledSkill is the wire half of the
// bead: the fact has been on disk since the install wrote it, and the one
// question a person asks of a downloaded skill — where did this come from —
// was answerable only by opening skills.json by hand.
func TestListCarriesTheRecordedSourceOfAnInstalledSkill(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "downloaded", "name: downloaded\ndescription: theirs", "body")
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":{"downloaded":`+aSourceRow+`}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError = %q, want none", result.DocumentError)
	}
	downloaded := listed(t, result, "downloaded")
	if downloaded.Source == nil {
		t.Fatal("Source is absent: the recorded source never left the backend, which is the whole defect")
	}
	if downloaded.Source.URL != "https://example.com/skills/downloaded/SKILL.md" {
		t.Errorf("Source.URL = %q, want the recorded address", downloaded.Source.URL)
	}
	if downloaded.Source.InstalledAt != "2026-09-03T12:00:00Z" {
		t.Errorf("Source.InstalledAt = %q, want the recorded time", downloaded.Source.InstalledAt)
	}
	if downloaded.Source.Digest != aServedDigest {
		t.Errorf("Source.Digest = %q, want the digest recorded for what the address served", downloaded.Source.Digest)
	}
}

// TestListCarriesAVersionTwoSourceWithNoRecordedDigest is the other reading of
// the same field, and the one that keeps the product honest about what it
// knows. A source row written before the digest existed still names an address
// and a time, so the row still says where the bytes came from — it simply does
// not claim to know what that address served. Absent is not empty and not zero;
// it is "nothing was recorded", and the page draws no row for it.
func TestListCarriesAVersionTwoSourceWithNoRecordedDigest(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "downloaded", "name: downloaded\ndescription: theirs", "body")
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":{"downloaded":`+aVersionTwoSourceRow+`}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError = %q, want a pre-digest source row to load", result.DocumentError)
	}
	downloaded := listed(t, result, "downloaded")
	if downloaded.Source == nil {
		t.Fatal("Source is absent: a row with no digest is still a row saying where the bytes came from")
	}
	if downloaded.Source.URL == "" || downloaded.Source.InstalledAt == "" {
		t.Errorf("Source = %+v, want the address and the time it did record", *downloaded.Source)
	}
	if downloaded.Source.Digest != "" {
		t.Errorf("Source.Digest = %q, want empty: nothing was recorded, and a value here would be invented", downloaded.Source.Digest)
	}
}

// TestAnInstalledSkillWithNoRecordedSourceIsListedWithoutOne is the case a
// person creates with `mv`: a directory put into the installed root by hand
// has no source row, and the row has to render without the field rather than
// with an empty one. The provenance is still installed — the root decides
// that — so the absence of a source is not the absence of a skill.
func TestAnInstalledSkillWithNoRecordedSourceIsListedWithoutOne(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "installed-skills"), "byhand", "name: byhand\ndescription: theirs", "body")
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":{}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError = %q, want none", result.DocumentError)
	}
	byHand := listed(t, result, "byhand")
	if byHand.Provenance != ProvenanceInstalled {
		t.Fatalf("provenance = %q, want installed: the root decides that, not the document", byHand.Provenance)
	}
	if byHand.Source != nil {
		t.Fatalf("Source = %+v, want none: nothing recorded where these bytes came from", *byHand.Source)
	}
}

// TestAManagedSkillNeverCarriesASource pins the other half of "only a
// provenance that HAS a source gets the field". The assistant writes managed
// skills, so there is no address behind them, and a row in a file anything can
// write must not put one on a managed skill's wire entry — that would make the
// field a second way to ask what a skill's provenance is, when provenance is
// the root and stays the root.
func TestAManagedSkillNeverCarriesASource(t *testing.T) {
	configDir := t.TempDir()
	writeExistingSkill(t, filepath.Join(configDir, "managed-skills"), "remembered", "name: remembered\ndescription: assistant", "body")
	writeDocument(t, configDir, `{"schemaVersion":2,"disabled":[],"digests":{},"sources":{"remembered":`+aSourceRow+`}}`)

	store := NewStore(OSFileSystem{}, installedRoots(t, configDir), storage.NewDocumentStore(configDir))
	result, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.DocumentError != "" {
		t.Fatalf("DocumentError = %q, want none", result.DocumentError)
	}
	remembered := listed(t, result, "remembered")
	if remembered.Provenance != ProvenanceManaged {
		t.Fatalf("provenance = %q, want managed", remembered.Provenance)
	}
	if remembered.Source != nil {
		t.Fatalf("Source = %+v, want none on a managed skill", *remembered.Source)
	}
}
