package skill

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Read resolves name using root precedence and returns either the skill body
// or a verbatim file within that skill directory. Every returned byte carries
// the provenance of the root that supplied it.
func Read(roots []Root, name, relPath string) (Content, error) {
	var found *discovered
	for _, candidate := range discoverDetailed(roots) {
		if candidate.Name == name {
			copy := candidate
			found = &copy
			break
		}
	}
	if found == nil {
		return Content{}, fmt.Errorf("skill %q was not found", name)
	}

	clean, err := cleanRelativePath(relPath)
	if err != nil {
		return Content{}, err
	}
	filePath := clean
	if filePath == "" {
		filePath = "SKILL.md"
	}

	var data []byte
	if found.root.Dir != "" {
		base, evalErr := filepath.EvalSymlinks(found.BaseDir)
		if evalErr != nil {
			return Content{}, fmt.Errorf("skill %q base: %w", name, evalErr)
		}
		target := filepath.Join(found.BaseDir, filepath.FromSlash(filePath))
		evaluated, evalErr := filepath.EvalSymlinks(target)
		if evalErr != nil {
			return Content{}, fmt.Errorf("skill %q path %q: %w", name, relPath, evalErr)
		}
		if !within(base, evaluated) {
			return Content{}, fmt.Errorf("skill %q path %q escapes its directory", name, relPath)
		}
		info, statErr := os.Stat(evaluated)
		if statErr != nil {
			return Content{}, fmt.Errorf("skill %q path %q: %w", name, relPath, statErr)
		}
		if !info.Mode().IsRegular() {
			return Content{}, fmt.Errorf("skill %q path %q is not a regular file", name, relPath)
		}
		data, err = readRootFile(found.root, name, filePath, MaxReadBytes)
	} else {
		data, err = readRootFile(found.root, name, path.Clean(filePath), MaxReadBytes)
	}
	if err != nil {
		return Content{}, fmt.Errorf("skill %q path %q: %w", name, relPath, err)
	}
	if relPath == "" {
		_, offset, ok := parseFrontmatter(data)
		if !ok {
			return Content{}, fmt.Errorf("skill %q has invalid frontmatter", name)
		}
		data = data[offset:]
	}
	return Content{Bytes: data, Provenance: found.Provenance, Path: filePath}, nil
}

func cleanRelativePath(relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", errors.New("skill path must be relative")
	}
	if relPath == "" {
		return "", nil
	}
	clean := filepath.Clean(relPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("skill path escapes its directory")
	}
	if clean == "." {
		return "", errors.New("skill path must name a file")
	}
	return filepath.ToSlash(clean), nil
}

func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
