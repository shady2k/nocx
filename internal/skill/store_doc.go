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
	"sync"

	"github.com/shady2k/nocx/internal/storage"
)

const DocumentName = "skills.json"

// Module declares the skill settings document's schema version. The document
// owns only dynamic per-skill enablement; the global feature switch remains a
// normal settings declaration.
var Module = storage.Module{Name: "skills", Current: 1}

type document struct {
	SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	Disabled      []string              `json:"disabled"`
	Digests       map[string]string     `json:"digests,omitempty"`
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
	d := document{Disabled: []string{}, Digests: map[string]string{}}
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
	return s.writeDocumentLocked(disabled, d.Digests)
}

func (s *Store) writeDocumentLocked(disabled map[string]struct{}, digests map[string]string) error {
	if s.docStore == nil {
		return errors.New("skill settings document is unavailable")
	}
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := s.docStore.Write(DocumentName, document{SchemaVersion: Module.Current, Disabled: names, Digests: digests}); err != nil {
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
	return s.writeDocumentLocked(disabled, d.Digests)
}

func (s *Store) clearApprovalDigest(name string) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	delete(d.Digests, name)
	disabled := make(map[string]struct{}, len(d.Disabled))
	for _, item := range d.Disabled {
		disabled[item] = struct{}{}
	}
	return s.writeDocumentLocked(disabled, d.Digests)
}

// Approve records the current bytes of a changed managed skill. Authored and
// builtin skills have no assistant approval digest and cannot use this path.
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
	if target == nil || target.Provenance != ProvenanceManaged {
		return fmt.Errorf("skill %q is not a changed managed skill", name)
	}
	if target.Status != StatusChanged {
		return fmt.Errorf("skill %q is not changed", name)
	}
	return s.recordApprovalDigest(name, target.BaseDir)
}

// Remove deletes the person-facing authored or managed skill. Builtins are
// embedded and therefore refuse explicitly so the page never offers a click
// that can only fail.
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
	if found.Provenance == ProvenanceManaged {
		if err := s.clearApprovalDigest(found.Name); err != nil {
			return fmt.Errorf("skill %q: clear approval: %w", found.Name, err)
		}
	}
	return nil
}
