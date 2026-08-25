package apicoll

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// folderVariablesFileName is deliberately hidden and reserved. A visible
// `variables.json` would be a valid request file and could collide with a
// collection someone already committed; `.variables.json` cannot be mistaken
// for a request by the listing, and its name tells a reviewer exactly what it
// carries. Alternatives are rejected by choosing a name outside the request
// file namespace rather than by teaching every request caller another case.
const folderVariablesFileName = ".variables.json"

type folderVariablesFile struct {
	Variables *[]Param `json:"variables"`
}

// readFolderVariablesFile reads one reserved file without creating it. The
// bool distinguishes an absent file (ordinary inheritance) from a file that
// exists but cannot be trusted. Every present file is strict JSON because a
// typo in a shared collection must not silently change a send.
func readFolderVariablesFile(full, rel string) ([]Param, bool, error) {
	fi, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("%w: %q: %v", ErrMalformedFolderVariables, rel, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, true, fmt.Errorf("%w: %q is not a regular file", ErrMalformedFolderVariables, rel)
	}
	raw, err := os.ReadFile(full) //nolint:gosec // full is derived beneath the opened root
	if err != nil {
		return nil, true, fmt.Errorf("%w: read %q: %v", ErrMalformedFolderVariables, rel, err)
	}
	var file folderVariablesFile
	if err := decodeStrict(raw, &file); err != nil {
		return nil, true, fmt.Errorf("%w: %q: %v", ErrMalformedFolderVariables, rel, err)
	}
	if file.Variables == nil {
		return nil, true, fmt.Errorf("%w: %q: missing variables list", ErrMalformedFolderVariables, rel)
	}
	variables := *file.Variables
	if variables == nil {
		return nil, true, fmt.Errorf("%w: %q: variables must be an array", ErrMalformedFolderVariables, rel)
	}
	for i, variable := range variables {
		name := strings.TrimSpace(variable.Name)
		if !variable.Enabled && name == "" {
			continue
		}
		if !validVarName(name) {
			return nil, true, fmt.Errorf("%w: %q: variable %d has invalid name %q", ErrMalformedFolderVariables, rel, i, variable.Name)
		}
	}
	return variables, true, nil
}

// folderVariablesFor returns rows nearest-first for a request path. That
// order is load-bearing: RequestLookup's first-row-wins rule then makes the
// nearest folder answer before its parents, while the request's own rows
// remain ahead of all inherited rows.
func folderVariablesFor(root, requestRelPath string) ([]Param, []string, error) {
	dir := filepath.ToSlash(filepath.Dir(requestRelPath))
	var inherited []Param
	var sources []string
	for {
		full := filepath.Join(root, filepath.FromSlash(dir), folderVariablesFileName)
		rel := filepath.ToSlash(filepath.Join(dir, folderVariablesFileName))
		if dir == "." {
			rel = folderVariablesFileName
		}
		variables, exists, err := readFolderVariablesFile(full, rel)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			inherited = append(inherited, variables...)
			for range variables {
				sources = append(sources, folderVariableFolder(rel))
			}
		}
		if dir == "." {
			break
		}
		dir = filepath.ToSlash(filepath.Dir(dir))
	}
	return inherited, sources, nil
}

func attachFolderVariables(root, requestRelPath string, r Request) (Request, error) {
	inherited, sources, err := folderVariablesFor(root, requestRelPath)
	if err != nil {
		return Request{}, err
	}
	r.folderVariables = inherited
	r.folderVariableSources = sources
	return r, nil
}

func folderVariableFolder(rel string) string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return ""
	}
	return dir
}
