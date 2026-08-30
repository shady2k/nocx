package apiimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/pathname"
)

// ArchiveImportResult is the report for one document written from a Postman
// archive. Unsupported features stay attached to the document that lost them.
type ArchiveImportResult struct {
	Kind        ArchiveDocumentKind
	Name        string
	Unsupported []Unsupported
}

// ImportPostmanArchive reads and fans out a Postman archive into one
// collection-shaped folder per document below dest. Every document is
// preflighted before the first write, so malformed members, duplicate names,
// and occupied targets cannot leave a partial archive import behind.
func ImportPostmanArchive(ctx context.Context, fsys FS, dest string, r io.Reader, route apicoll.Route, refs ArchiveSecretRefs) ([]ArchiveImportResult, error) {
	dest = strings.TrimRight(filepath.Clean(dest), string(filepath.Separator))
	if dest == "" || dest == "." || dest == ".." || dest == string(filepath.Separator) {
		return nil, fmt.Errorf("apiimport: %q is not a usable archive destination", dest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	documents, err := ReadPostmanArchive(r)
	if err != nil {
		return nil, err
	}
	destExisted, err := ValidatePostmanArchiveDestination(fsys, dest, documents)
	if err != nil {
		return nil, err
	}

	if err := fsys.MkdirAll(dest, collectionDirMode); err != nil {
		return nil, fmt.Errorf("apiimport: create archive destination: %w", err)
	}
	arrived := make([]string, 0, len(documents))
	complete := false
	defer func() {
		if complete {
			return
		}
		for _, path := range arrived {
			_ = fsys.RemoveAll(path)
		}
		if !destExisted {
			_ = fsys.RemoveAll(dest)
		}
	}()

	results := make([]ArchiveImportResult, 0, len(documents))
	for _, doc := range documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		child := filepath.Join(dest, doc.Name)
		unsupported, err := ImportInto(ctx, fsys, child, bytes.NewReader(doc.Document), route, refs[doc.Path])
		if err != nil {
			return nil, fmt.Errorf("apiimport: import %s %q: %w", doc.Kind, doc.Name, err)
		}
		arrived = append(arrived, child)
		results = append(results, ArchiveImportResult{
			Kind:        doc.Kind,
			Name:        doc.Name,
			Unsupported: unsupported,
		})
	}
	complete = true
	return results, nil
}

// ValidatePostmanArchiveDestination applies the archive writer's complete
// destination preflight without creating or writing anything. The bool says
// whether dest already existed, which the writer needs for rollback.
func ValidatePostmanArchiveDestination(fsys FS, dest string, documents []ArchiveDocument) (bool, error) {
	destExisted := false
	if info, statErr := fsys.Lstat(dest); statErr == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("apiimport: archive destination %q is not a directory", dest)
		}
		destExisted = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("apiimport: check archive destination: %w", statErr)
	}

	seen := make(map[string]struct{}, len(documents))
	for _, doc := range documents {
		if err := pathname.CheckComponent(doc.Name); err != nil {
			return false, fmt.Errorf("apiimport: archive document name %q is invalid: %w", doc.Name, err)
		}
		key := strings.ToLower(doc.Name)
		if _, exists := seen[key]; exists {
			return false, fmt.Errorf("apiimport: archive contains duplicate document name %q", doc.Name)
		}
		seen[key] = struct{}{}
		if _, statErr := fsys.Lstat(filepath.Join(dest, doc.Name)); statErr == nil {
			return false, fmt.Errorf("apiimport: archive destination for %q already exists", doc.Name)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, fmt.Errorf("apiimport: check archive destination for %q: %w", doc.Name, statErr)
		}
	}
	return destExisted, nil
}

// ArchiveSecretRefs maps an archive MEMBER PATH to the references minted for
// that document's secret variables.
//
// Keyed by path rather than by name because two documents in one archive may
// declare the same variable name carrying different values, and a map keyed
// by name would hand the second document the first one's record.
type ArchiveSecretRefs map[string]SecretRefs
