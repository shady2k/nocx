package skill

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// located is one file of one discovered skill, after every containment rule
// has been applied to it: which root holds the skill, which relative path was
// asked for, and the directory entry that path is joined onto.
type located struct {
	skill discovered
	// entry is the DIRECTORY name under the root, which is not always the
	// skill's name: discovery takes the name from the frontmatter and the
	// path from the entry it found. The checked path and the read path have
	// to be the same path, so both come from here.
	entry string
	// path is slash-separated and relative to the skill's own directory.
	path string
}

// locate resolves name by root precedence and settles whether relPath names a
// file inside that skill's directory. It is one function rather than one per
// caller because containment is a single answer: the assistant's Read and the
// person's File reach the same bytes under the same rules, and a second check
// would agree with this one everywhere anybody looked and disagree on the one
// case nobody tried. An empty relPath means the skill's own SKILL.md, which
// is what a caller asks for when it wants the body.
func locate(roots []Root, name, relPath string) (located, error) {
	var found *discovered
	for _, candidate := range discoverDetailed(roots, false) {
		if candidate.Name == name {
			copy := candidate
			found = &copy
			break
		}
	}
	if found == nil {
		return located{}, fmt.Errorf("skill %q was not found", name)
	}

	clean, err := cleanRelativePath(relPath)
	if err != nil {
		return located{}, err
	}
	filePath := clean
	if filePath == "" {
		filePath = "SKILL.md"
	}

	at := located{skill: *found, entry: found.BaseDir, path: path.Clean(filePath)}
	if found.root.Dir == "" {
		// An embedded root has no symlinks and no directory to escape from:
		// fs.FS rejects a path with a .. element outright, and the lexical
		// clean above has already refused one. There is nothing further to
		// check, and inventing a check here would be a second answer.
		return at, nil
	}

	at.entry = filepath.Base(found.BaseDir)
	base, evalErr := filepath.EvalSymlinks(found.BaseDir)
	if evalErr != nil {
		return located{}, fmt.Errorf("skill %q base: %w", name, evalErr)
	}
	target := filepath.Join(found.BaseDir, filepath.FromSlash(filePath))
	evaluated, evalErr := filepath.EvalSymlinks(target)
	if evalErr != nil {
		return located{}, fmt.Errorf("skill %q path %q: %w", name, relPath, evalErr)
	}
	if !within(base, evaluated) {
		return located{}, fmt.Errorf("skill %q path %q escapes its directory", name, relPath)
	}
	info, statErr := os.Stat(evaluated)
	if statErr != nil {
		return located{}, fmt.Errorf("skill %q path %q: %w", name, relPath, statErr)
	}
	if !info.Mode().IsRegular() {
		return located{}, fmt.Errorf("skill %q path %q is not a regular file", name, relPath)
	}
	return at, nil
}

// Read resolves name using root precedence and returns either the skill body
// or a verbatim file within that skill directory. Every returned byte carries
// the provenance of the root that supplied it.
func Read(roots []Root, name, relPath string) (Content, error) {
	at, err := locate(roots, name, relPath)
	if err != nil {
		return Content{}, err
	}
	data, err := readRootFile(at.skill.root, at.entry, at.path, MaxReadBytes)
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
	return Content{
		Bytes:      data,
		Provenance: at.skill.Provenance,
		Path:       at.path,
		Changed:    at.skill.Status == StatusChanged,
	}, nil
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
