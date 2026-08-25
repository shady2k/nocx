package apicoll

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadFolderVariables reads the reserved variable document for one existing
// folder. The folder path is relative to the opened collection; empty names
// the collection root. An absent document is the ordinary empty state.
func (s *service) ReadFolderVariables(h HandleID, relPath string) ([]Param, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return nil, err
	}
	if pathErr := validateFolderPath(relPath); pathErr != nil {
		return nil, pathErr
	}
	folder, err := resolveWithin(hd.root, relPath)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(folder)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w: %q", ErrFolderNotFound, relPath)
	case err != nil:
		return nil, fmt.Errorf("apicoll: stat folder %q: %w", relPath, err)
	case !fi.IsDir():
		return nil, fmt.Errorf("%w: %q is not a folder", ErrFolderNotFound, relPath)
	}
	file := filepath.Join(folder, folderVariablesFileName)
	variables, exists, err := readFolderVariablesFile(file, folderVariablesFileName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Param{}, nil
	}
	return variables, nil
}

// WriteFolderVariables replaces a folder's reserved variable document using
// the collection's existing atomic document store. Empty rows delete the
// document, so absence and an empty editor are one persisted state.
func (s *service) WriteFolderVariables(h HandleID, relPath string, variables []Param) ([]Param, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return nil, err
	}
	if pathErr := validateFolderPath(relPath); pathErr != nil {
		return nil, pathErr
	}
	folder, err := resolveWithin(hd.root, relPath)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(folder)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w: %q", ErrFolderNotFound, relPath)
	case err != nil:
		return nil, fmt.Errorf("apicoll: stat folder %q: %w", relPath, err)
	case !fi.IsDir():
		return nil, fmt.Errorf("%w: %q is not a folder", ErrFolderNotFound, relPath)
	}
	file := filepath.Join(folder, folderVariablesFileName)
	if _, _, err := readFolderVariablesFile(file, folderVariablesFileName); err != nil {
		return nil, err
	}
	canonical := make([]Param, len(variables))
	for i, variable := range variables {
		name := strings.TrimSpace(variable.Name)
		if !variable.Enabled && name == "" {
			canonical[i] = variable
			continue
		}
		if !validVarName(name) {
			return nil, fmt.Errorf("%w: variable %d has invalid name %q", ErrMalformedFolderVariables, i, variable.Name)
		}
		canonical[i] = variable
		canonical[i].Name = name
	}

	store := s.docStoreFor(hd.root)
	fileRel := filepath.ToSlash(filepath.Join(relPath, folderVariablesFileName))
	if len(canonical) == 0 {
		if err := store.Delete(fileRel); err != nil {
			return nil, fmt.Errorf("apicoll: delete folder variables %q: %w", relPath, err)
		}
		return []Param{}, nil
	}
	if err := store.Write(fileRel, folderVariablesFile{Variables: &canonical}); err != nil {
		return nil, fmt.Errorf("apicoll: write folder variables %q: %w", relPath, err)
	}
	return canonical, nil
}
