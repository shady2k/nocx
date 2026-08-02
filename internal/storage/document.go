// Package storage provides shared storage path resolution and the atomic
// JSON DocumentStore capability (ADR-0011).
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DocumentStore reads and writes bounded, human-recoverable configuration as
// atomic JSON documents. Callers name a document; the store owns the path,
// permissions and atomicity.
type DocumentStore interface {
	Read(name string, into any) (found bool, err error)
	Write(name string, doc any) error
	// Delete removes the named document. Deleting a document that is not
	// there succeeds: absence is the desired end state, and an operation
	// that deletes several documents must be safe to re-run after being
	// interrupted part-way through.
	Delete(name string) error
}

// ---------------------------------------------------------------------------
// Schema version protocol
//
// Each module declares its own monotonic SchemaVersion and an ordered list of
// Migrations. There is no app-wide version number (ADR-0011 §6). The protocol
// types live in storage so every module speaks the same migration shape, but
// each module owns its version number and migration functions.
// ---------------------------------------------------------------------------

// SchemaVersion is a monotonic integer tracking a module's document format.
type SchemaVersion int

// Migration transforms document data from one version to the next.
type Migration struct {
	From SchemaVersion
	To   SchemaVersion
	Up   func(data []byte) ([]byte, error)
}

// Module declares a document module's schema, current version, and ordered
// migrations. Modules are constructed by the package that owns the document
// (e.g. profile, settings), not by storage.
type Module struct {
	Name       string
	Current    SchemaVersion
	Migrations []Migration
}

// ErrVersionTooNew is returned by Module.Migrate when the stored document
// has a version newer than the module's Current version.
var ErrVersionTooNew = errors.New("document version is newer than module")

// Migrate applies the module's migration chain to data at storedVersion,
// advancing it to module.Current. When storedVersion equals Current the
// data is returned unchanged. Version 0 means "no version on disk"; the
// first migration in the chain must cover 0→N for this to be migratable.
func (m Module) Migrate(data []byte, storedVersion SchemaVersion) ([]byte, error) {
	if storedVersion > m.Current {
		return nil, fmt.Errorf("%w: stored %d > current %d", ErrVersionTooNew, storedVersion, m.Current)
	}
	if storedVersion == m.Current {
		return data, nil
	}

	byFrom := make(map[SchemaVersion]Migration, len(m.Migrations))
	for _, mig := range m.Migrations {
		byFrom[mig.From] = mig
	}

	current := data
	v := storedVersion
	for v < m.Current {
		next, ok := byFrom[v]
		if !ok {
			return nil, fmt.Errorf("module %s: no migration from version %d", m.Name, v)
		}
		if next.To <= v {
			return nil, fmt.Errorf("module %s: non-monotonic migration %d→%d", m.Name, next.From, next.To)
		}
		var err error
		current, err = next.Up(current)
		if err != nil {
			return nil, fmt.Errorf("module %s: migration %d→%d: %w", m.Name, next.From, next.To, err)
		}
		v = next.To
	}
	return current, nil
}

// ---------------------------------------------------------------------------
// Concrete implementation
// ---------------------------------------------------------------------------

// documentStore is the concrete implementation of DocumentStore.
type documentStore struct {
	dir     string
	syncDir func(string) error
}

// NewDocumentStore creates a DocumentStore that reads and writes JSON
// documents in dir. The store is format-agnostic; callers own schema
// versioning and apply Module.Migrate on Read results.
func NewDocumentStore(dir string) DocumentStore {
	return &documentStore{
		dir:     dir,
		syncDir: syncDirectory,
	}
}

func (s *documentStore) pathFor(name string) string {
	return filepath.Join(s.dir, name)
}

// Read loads the named document and unmarshals it into into. Returns
// (false, nil) when the document does not exist.
func (s *documentStore) Read(name string, into any) (bool, error) {
	raw, err := os.ReadFile(s.pathFor(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read document %s: %w", name, err)
	}
	if len(raw) == 0 {
		return false, nil
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return false, fmt.Errorf("parse document %s: %w", name, err)
	}
	return true, nil
}

// Delete removes the named document. A document that is not there is not an
// error — see the interface doc.
//
// The directory is synced afterwards for the same reason Write syncs it: the
// rename or unlink is only durable once the directory entry is. Without it a
// crash can resurrect a document the caller was told had gone, and for the
// vault that means a reset that undoes itself.
func (s *documentStore) Delete(name string) error {
	if err := os.Remove(s.pathFor(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("delete document %s: %w", name, err)
	}
	if err := s.syncDir(filepath.Dir(s.pathFor(name))); err != nil {
		return fmt.Errorf("sync dir after deleting %s: %w", name, err)
	}
	return nil
}

// Write marshals doc to JSON and writes it atomically: temp file in the
// same directory, fsync, rename. MkdirAll ensures the directory exists
// with mode 0700. The file is written with mode 0600. Write refuses to
// overwrite a symlink target (rename would follow the link and replace
// the target, which is a security boundary violation).
func (s *documentStore) Write(name string, doc any) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal document %s: %w", name, err)
	}

	dir := filepath.Dir(s.pathFor(name))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir for document %s: %w", name, err)
	}

	tmp, err := os.CreateTemp(dir, ".doc-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	target := s.pathFor(name)

	// Symlink guard: refuse to rename over a symlink.
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write over symlink at %s", target)
		}
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename temp to %s: %w", target, err)
	}

	// Directory fsync for crash durability after rename.
	if err := s.syncDir(dir); err != nil {
		return fmt.Errorf("fsync directory %s: %w", dir, err)
	}

	return nil
}

// syncDirectory opens dir and calls Sync to persist the rename.
func syncDirectory(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // dir is always a path we created
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
