package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
)

const DocumentName = "skills.json"

// skillsSchemaVersion is this module's current document version. It is a
// named constant rather than a literal in Module because the migration rung
// below has to stamp the same number, and a func referencing Module inside
// Module's own initializer is an initialization cycle.
const skillsSchemaVersion storage.SchemaVersion = 2

// Module declares the skill settings document's schema version. The document
// owns dynamic per-skill enablement, the digest recorded when the person
// approved a skill's bytes, and — since version 2 — where an installed skill
// came from; the global feature switch remains a normal settings declaration.
var Module = storage.Module{
	Name:    "skills",
	Current: skillsSchemaVersion,
	Migrations: []storage.Migration{
		{From: 1, To: 2, Up: migrateToSources},
	},
}

// migrateToSources carries a version 1 document forward. Version 2 IS version
// 1, plus an optional `sources` map, so there is nothing to convert: a
// document written before a skill could be installed simply has no sources,
// and an absent map reads as an empty one.
//
// The rung still has to exist, and this is the whole reason it does.
// storage.Module.Migrate refuses a stored version it has no migration FROM, so
// without a rung every skills.json already on disk would fail the entire list
// closed the moment Current moved — it is the version bump that needs
// carrying, not the shape. Restamping keeps the in-memory document honest
// about which shape the reader just applied.
//
// A version 0 document — one with no schemaVersion at all — is deliberately
// still refused, exactly as it was at version 1: there is no rung from 0, and
// inventing one would mean guessing at a shape nothing ever wrote.
func migrateToSources(data []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("document is not a JSON object")
	}
	fields["schemaVersion"] = json.RawMessage(strconv.Itoa(int(skillsSchemaVersion)))
	return json.Marshal(fields)
}

type document struct {
	SchemaVersion storage.SchemaVersion  `json:"schemaVersion"`
	Disabled      []string               `json:"disabled"`
	Digests       map[string]string      `json:"digests,omitempty"`
	Sources       map[string]skillSource `json:"sources,omitempty"`
}

// skillSource records where an installed skill came from, keyed by skill name
// like Digests. It exists so an update can re-run the install against the URL
// the person actually installed from; without it an update could only search
// for the name somewhere else, and skill names are not namespaced across
// sources, so a same-named skill elsewhere would silently reassign provenance.
//
// It is NEVER read as provenance. Provenance is the root (skill.go) and this
// row is content in a file anything able to write skills.json could write — so
// a source entry for a name the authored root holds changes nothing at all
// about that skill, and there is a test for exactly that direction.
type skillSource struct {
	URL         string `json:"url"`
	InstalledAt string `json:"installedAt"`
}

// ListedSkill is the settings page's complete view of a discovered skill.
type ListedSkill struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Provenance  Provenance `json:"provenance"`
	Path        string     `json:"path"`
	Enabled     bool       `json:"enabled"`
	Status      Status     `json:"status"`
}

// ListResult is returned by the settings-page RPC. A document failure is a
// visible result rather than an empty successful list, and its path tells the
// person which file needs repair.
type ListResult struct {
	Skills        []ListedSkill `json:"skills"`
	DocumentPath  string        `json:"documentPath"`
	DocumentError string        `json:"documentError,omitempty"`
}

func newStore(fsys FileSystem, roots []Root, docStore storage.DocumentStore) *Store {
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	copyRoots := append([]Root(nil), roots...)
	var managedDir string
	for _, root := range copyRoots {
		if root.Provenance == ProvenanceManaged && root.Dir != "" {
			managedDir = root.Dir
			break
		}
	}
	if docStore == nil {
		for _, dir := range FilesystemRoots(copyRoots) {
			docStore = storage.NewDocumentStore(filepath.Dir(dir))
			break
		}
	}
	s := &Store{
		fs: fsys, roots: copyRoots, managedDir: managedDir,
		docStore: docStore, locks: make(map[string]*sync.Mutex),
	}
	for i := range s.roots {
		s.roots[i].disabled = s.loadDisabled
		s.roots[i].digests = s.loadApprovedDigests
	}
	return s
}

func (s *Store) loadDisabled() (map[string]struct{}, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, err
	}
	disabled := make(map[string]struct{}, len(d.Disabled))
	for _, name := range d.Disabled {
		disabled[name] = struct{}{}
	}
	return disabled, nil
}

func (s *Store) loadApprovedDigests() (map[string]string, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, err
	}
	return d.Digests, nil
}

func (s *Store) loadDocumentLocked() (document, error) {
	d := document{Disabled: []string{}, Digests: map[string]string{}, Sources: map[string]skillSource{}}
	if s.docStore == nil {
		s.docFailure = nil
		return d, nil
	}
	var raw json.RawMessage
	found, err := s.docStore.Read(DocumentName, &raw)
	if err != nil {
		s.docFailure = fmt.Errorf("read %s: %w", DocumentName, err)
		return document{}, s.docFailure
	}
	if !found {
		s.docFailure = nil
		return d, nil
	}
	var probe struct {
		SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	}
	if parseErr := json.Unmarshal(raw, &probe); parseErr != nil {
		s.docFailure = fmt.Errorf("parse %s: %w", DocumentName, parseErr)
		return document{}, s.docFailure
	}
	migrated, err := Module.Migrate(raw, probe.SchemaVersion)
	if err != nil {
		s.docFailure = fmt.Errorf("read %s: %w", DocumentName, err)
		return document{}, s.docFailure
	}
	if err := json.Unmarshal(migrated, &d); err != nil {
		s.docFailure = fmt.Errorf("parse %s: %w", DocumentName, err)
		return document{}, s.docFailure
	}
	if d.Digests == nil {
		d.Digests = map[string]string{}
	}
	if d.Sources == nil {
		d.Sources = map[string]skillSource{}
	}
	for _, name := range d.Disabled {
		canonical, err := normalizeName(name)
		if err != nil {
			s.docFailure = fmt.Errorf("parse %s: disabled name %q: %w", DocumentName, name, err)
			return document{}, s.docFailure
		}
		if canonical != name {
			s.docFailure = fmt.Errorf("parse %s: disabled name %q is not canonical", DocumentName, name)
			return document{}, s.docFailure
		}
	}
	for name, digest := range d.Digests {
		canonical, err := normalizeName(name)
		if err != nil || canonical != name {
			s.docFailure = fmt.Errorf("parse %s: digest name %q is not canonical", DocumentName, name)
			return document{}, s.docFailure
		}
		if len(digest) != sha256HexSize || !isHexDigest(digest) {
			s.docFailure = fmt.Errorf("parse %s: digest for %q is invalid", DocumentName, name)
			return document{}, s.docFailure
		}
	}
	// Read exactly as strictly as the digests above, and for the same reason:
	// a row nobody can act on is a defect in the file, not a row to skip. A
	// source that silently vanished would leave a skill listed as installed
	// with no way to update it, which is the state this map exists to prevent.
	for name, source := range d.Sources {
		canonical, err := normalizeName(name)
		if err != nil || canonical != name {
			s.docFailure = fmt.Errorf("parse %s: source name %q is not canonical", DocumentName, name)
			return document{}, s.docFailure
		}
		// profile.ValidateBaseURL is the repo's one owner of "is this string
		// a fetchable http(s) address at all" — shape, not policy, with the
		// address rules enforced at dial time. internal/transport already
		// crosses a module boundary to reuse it rather than derive the answer
		// a second time. Its rejection of userinfo matters here too:
		// credentials must never come to rest in a plaintext document.
		if err := profile.ValidateBaseURL(source.URL); err != nil {
			s.docFailure = fmt.Errorf("parse %s: source url for %q is invalid: %w", DocumentName, name, err)
			return document{}, s.docFailure
		}
		if _, err := time.Parse(time.RFC3339, source.InstalledAt); err != nil {
			s.docFailure = fmt.Errorf("parse %s: source installedAt for %q is not an RFC3339 time", DocumentName, name)
			return document{}, s.docFailure
		}
	}
	s.docFailure = nil
	return d, nil
}

const sha256HexSize = sha256.Size * 2

func isHexDigest(value string) bool {
	var decoded [sha256.Size]byte
	_, err := hex.Decode(decoded[:], []byte(value))
	return err == nil
}

func (s *Store) documentError() error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	return s.docFailure
}

// List returns every discovered skill, including disabled and changed ones,
// for Settings. Prompt indexing and Read use discoverDetailed's exclusion mode.
func (s *Store) List() (ListResult, error) {
	if s == nil {
		return ListResult{}, errUnavailable
	}
	detailed := discoverDetailed(s.roots, true)
	result := ListResult{
		Skills:       make([]ListedSkill, 0, len(detailed)),
		DocumentPath: s.DocumentPath(),
	}
	if err := s.documentError(); err != nil {
		result.DocumentError = err.Error()
		return result, nil
	}
	for _, found := range detailed {
		result.Skills = append(result.Skills, ListedSkill{
			Name:        found.Name,
			Description: found.Description,
			Provenance:  found.Provenance,
			Path:        filepath.Join(found.BaseDir, "SKILL.md"),
			Enabled:     found.Enabled,
			Status:      found.Status,
		})
	}
	return result, nil
}

// DocumentPath is the actual skills.json path used by the store.
func (s *Store) DocumentPath() string {
	if s == nil {
		return DocumentName
	}
	for _, dir := range FilesystemRoots(s.roots) {
		return filepath.Join(filepath.Dir(dir), DocumentName)
	}
	return DocumentName
}

func (s *Store) SetEnabled(name string, enabled bool) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	listed, err := s.List()
	if err != nil {
		return err
	}
	if listed.DocumentError != "" {
		return errors.New(listed.DocumentError)
	}
	found := false
	for _, item := range listed.Skills {
		if item.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("skill %q was not found", name)
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	disabled := make(map[string]struct{}, len(d.Disabled))
	for _, item := range d.Disabled {
		disabled[item] = struct{}{}
	}
	if enabled {
		delete(disabled, name)
	} else {
		disabled[name] = struct{}{}
	}
	return s.writeDocumentLocked(d, disabled)
}

// writeDocumentLocked persists the document that was loaded, with disabled
// replacing its Disabled list. It takes the whole loaded value rather than a
// parameter per map ON PURPOSE: every mutation rebuilds the file from what it
// read, so a field a writer forgets to carry is a field the next toggle
// silently deletes. Passing the document through means a new field is carried
// by construction instead of by three call sites remembering it.
func (s *Store) writeDocumentLocked(d document, disabled map[string]struct{}) error {
	if s.docStore == nil {
		return errors.New("skill settings document is unavailable")
	}
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	d.SchemaVersion = Module.Current
	d.Disabled = names
	if err := s.docStore.Write(DocumentName, d); err != nil {
		return fmt.Errorf("write %s: %w", DocumentName, err)
	}
	s.docFailure = nil
	return nil
}

func (s *Store) recordApprovalDigest(name, dir string) error {
	digest, err := hashSkillDirectory(dir)
	if err != nil {
		return fmt.Errorf("skill %q: hash: %w", name, err)
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	if d.Digests == nil {
		d.Digests = make(map[string]string)
	}
	d.Digests[name] = digest
	disabled := make(map[string]struct{}, len(d.Disabled))
	for _, item := range d.Disabled {
		disabled[item] = struct{}{}
	}
	return s.writeDocumentLocked(d, disabled)
}

// clearApprovalDigest forgets everything the document records about one skill:
// the digest AND the source, together, because they are one record of a skill
// the person did not write. A source outliving its skill is a row for a name
// that is not on disk, which the next install of that name would read as its
// own history.
func (s *Store) clearApprovalDigest(name string) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	delete(d.Digests, name)
	delete(d.Sources, name)
	disabled := make(map[string]struct{}, len(d.Disabled))
	for _, item := range d.Disabled {
		disabled[item] = struct{}{}
	}
	return s.writeDocumentLocked(d, disabled)
}

// Approve records the current bytes of a changed managed or installed skill.
// Authored and builtin skills have no approval digest to diverge from and
// cannot use this path.
func (s *Store) Approve(name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()
	if rootErr := s.requireManagedRoot(); rootErr != nil {
		return rootErr
	}
	var target *discovered
	for _, found := range discoverDetailed(s.roots, true) {
		if found.Name == name {
			copy := found
			target = &copy
			break
		}
	}
	if target == nil || !target.Provenance.digested() {
		return fmt.Errorf("skill %q is not a changed managed or installed skill", name)
	}
	if target.Status != StatusChanged {
		return fmt.Errorf("skill %q is not changed", name)
	}
	return s.recordApprovalDigest(name, target.BaseDir)
}

// Remove deletes the person-facing authored, managed or installed skill.
// Builtins are embedded and therefore refuse explicitly so the page never
// offers a click that can only fail.
func (s *Store) Remove(name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	listed, err := s.List()
	if err != nil {
		return err
	}
	if listed.DocumentError != "" {
		return errors.New(listed.DocumentError)
	}
	var target *discovered
	for _, found := range discoverDetailed(s.roots, true) {
		if found.Name == name {
			copy := found
			target = &copy
			break
		}
	}
	if target == nil {
		return fmt.Errorf("skill %q was not found", name)
	}
	if target.Provenance == ProvenanceBuiltin {
		return fmt.Errorf("builtin skill %q cannot be deleted", name)
	}
	unlock, err := s.lockName(name)
	if err != nil {
		return err
	}
	defer unlock()
	return s.removeDiscovered(*target)
}

func (s *Store) removeDiscovered(found discovered) error {
	if found.root.Dir == "" {
		return fmt.Errorf("skill %q has no removable filesystem path", found.Name)
	}
	root, err := filepath.Abs(found.root.Dir)
	if err != nil {
		return fmt.Errorf("skill %q: root: %w", found.Name, err)
	}
	dir, err := filepath.Abs(found.BaseDir)
	if err != nil {
		return fmt.Errorf("skill %q: directory: %w", found.Name, err)
	}
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("skill %q: root: %w", found.Name, err)
	}
	dirEval, err := filepath.EvalSymlinks(dir)
	if err != nil || !within(rootEval, dirEval) {
		return fmt.Errorf("skill %q: directory escapes its root", found.Name)
	}
	target := filepath.Join(dir, "SKILL.md")
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("skill %q: inspect: %w", found.Name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || linkCount(info) > 1 {
		return fmt.Errorf("skill %q: SKILL.md is not a removable regular file", found.Name)
	}
	if err := s.fs.Remove(target); err != nil {
		return fmt.Errorf("skill %q: delete: %w", found.Name, err)
	}
	if err := s.fs.Sync(dir); err != nil {
		return fmt.Errorf("skill %q: sync directory after delete: %w", found.Name, err)
	}
	if found.Provenance.digested() {
		if err := s.clearApprovalDigest(found.Name); err != nil {
			return fmt.Errorf("skill %q: clear approval: %w", found.Name, err)
		}
	}
	return nil
}
