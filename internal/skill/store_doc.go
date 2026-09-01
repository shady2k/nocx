package skill

import (
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
}

// ListedSkill is the settings page's complete view of a discovered skill.
type ListedSkill struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Provenance  Provenance `json:"provenance"`
	Path        string     `json:"path"`
	Enabled     bool       `json:"enabled"`
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
	}
	return s
}

// NewStoreWithDocumentStore wires the skill settings document through the
// same DocumentStore as the rest of the config family.
func NewStoreWithDocumentStore(fsys FileSystem, roots []Root, docStore storage.DocumentStore) *Store {
	return newStore(fsys, roots, docStore)
}

func (s *Store) loadDisabled() (map[string]struct{}, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	return s.loadDisabledLocked()
}

func (s *Store) loadDisabledLocked() (map[string]struct{}, error) {
	disabled := make(map[string]struct{})
	if s.docStore == nil {
		s.docFailure = nil
		return disabled, nil
	}
	var raw json.RawMessage
	found, err := s.docStore.Read(DocumentName, &raw)
	if err != nil {
		s.docFailure = fmt.Errorf("read %s: %w", DocumentName, err)
		return nil, s.docFailure
	}
	if !found {
		s.docFailure = nil
		return disabled, nil
	}
	var probe struct {
		SchemaVersion storage.SchemaVersion `json:"schemaVersion"`
	}
	if parseErr := json.Unmarshal(raw, &probe); parseErr != nil {
		s.docFailure = fmt.Errorf("parse %s: %w", DocumentName, parseErr)
		return nil, s.docFailure
	}
	migrated, err := Module.Migrate(raw, probe.SchemaVersion)
	if err != nil {
		s.docFailure = fmt.Errorf("read %s: %w", DocumentName, err)
		return nil, s.docFailure
	}
	var d document
	if err := json.Unmarshal(migrated, &d); err != nil {
		s.docFailure = fmt.Errorf("parse %s: %w", DocumentName, err)
		return nil, s.docFailure
	}
	for _, name := range d.Disabled {
		canonical, err := normalizeName(name)
		if err != nil {
			s.docFailure = fmt.Errorf("parse %s: disabled name %q: %w", DocumentName, name, err)
			return nil, s.docFailure
		}
		if canonical != name {
			s.docFailure = fmt.Errorf("parse %s: disabled name %q is not canonical", DocumentName, name)
			return nil, s.docFailure
		}
		disabled[canonical] = struct{}{}
	}
	s.docFailure = nil
	return disabled, nil
}

func (s *Store) documentError() error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	return s.docFailure
}

// List returns every discovered skill, including disabled ones, for Settings.
// Prompt indexing and Read use discoverDetailed's exclusion mode instead.
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
	disabled, err := s.loadDisabledLocked()
	if err != nil {
		return err
	}
	if enabled {
		delete(disabled, name)
	} else {
		disabled[name] = struct{}{}
	}
	return s.writeDisabledLocked(disabled)
}

func (s *Store) writeDisabledLocked(disabled map[string]struct{}) error {
	if s.docStore == nil {
		return errors.New("skill settings document is unavailable")
	}
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := s.docStore.Write(DocumentName, document{SchemaVersion: Module.Current, Disabled: names}); err != nil {
		return fmt.Errorf("write %s: %w", DocumentName, err)
	}
	s.docFailure = nil
	return nil
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
	return nil
}
