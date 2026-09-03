package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Snapshot is the durable portion of the skill library. Builtin skills are
// intentionally absent: they come from the binary and are restored by the
// destination build. Files retain their relative paths so references and
// other files in each skill directory survive a profile move unchanged.
//
// Installed skills travel too (owner decision, 2026-09-03; design §11,
// nocx-qja4m.8). What that accepts, written here because it is the part that
// is easy to miss: the approval digest lives in skills.json, which travels as
// Settings, and it is computed over relative content — so a restored
// installed skill arrives `approved`, replaying on the new machine an
// approval the person gave once for one set of bytes. It is the same
// trade-off already taken for managed, which a model drafts and which travels
// approved today.
type Snapshot struct {
	Authored  []SnapshotTree  `json:"authored"`
	Managed   []SnapshotTree  `json:"managed"`
	Installed []SnapshotTree  `json:"installed"`
	Settings  json.RawMessage `json:"settings,omitempty"`
}

type SnapshotTree struct {
	Name  string         `json:"name"`
	Files []SnapshotFile `json:"files"`
}

type SnapshotFile struct {
	Path  string `json:"path"`
	Bytes string `json:"bytes"`
}

// TreeCount is how many skill directories a snapshot carries, and it is the
// one owner of that sum. Every caller that reports a skill count — the create
// summary, the restore result and the restore preview — asked it separately,
// so a fourth root meant three places to remember; they now ask here.
func (s Snapshot) TreeCount() int {
	return len(s.Authored) + len(s.Managed) + len(s.Installed)
}

// treesFor maps a provenance onto the field of this snapshot that carries it,
// and is the single owner of which roots a backup carries at all. Both
// directions read it, so a root cannot be walked by one and ignored by the
// other — which is exactly the defect it replaces: Snapshot's `root.Dir != ""`
// guard preceded the provenance switch, so the installed root was walked and
// its result discarded, and an unreadable file under it failed a whole backup
// for contents that never reached the snapshot.
//
// Builtin is deliberately absent, so it reports false: those bytes come from
// the binary and the destination build restores them.
func (s *Snapshot) treesFor(provenance Provenance) (*[]SnapshotTree, bool) {
	switch provenance {
	case ProvenanceAuthored:
		return &s.Authored, true
	case ProvenanceManaged:
		return &s.Managed, true
	case ProvenanceInstalled:
		return &s.Installed, true
	}
	return nil, false
}

// Snapshot returns the authored, managed and installed skill trees plus
// skills.json. Builtins never enter the snapshot.
func (s *Store) Snapshot() (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errUnavailable
	}
	var out Snapshot
	for _, root := range s.roots {
		into, carried := out.treesFor(root.Provenance)
		if !carried || root.Dir == "" {
			continue
		}
		trees, err := snapshotRoot(root.Dir)
		if err != nil {
			return Snapshot{}, fmt.Errorf("snapshot %s skills: %w", root.Provenance, err)
		}
		*into = append(*into, trees...)
	}
	if s.docStore != nil {
		var raw json.RawMessage
		found, err := s.docStore.Read(DocumentName, &raw)
		if err != nil {
			return Snapshot{}, fmt.Errorf("snapshot %s: %w", DocumentName, err)
		}
		if found {
			out.Settings = append(json.RawMessage(nil), raw...)
		}
	}
	return out, nil
}

// RestoreSnapshot atomically writes each file in the authored, managed and
// installed trees and then restores skills.json. The snapshot's interval
// begins when a file is renamed into place and ends after the settings
// document is written; callers journal the prior snapshot before entering it.
//
// A field the snapshot does not carry — an `installed` written by no build
// that existed before the decision above — decodes as nil, and a nil tree list
// writes nothing. Restore never deletes, so a snapshot silent about a root
// leaves that root exactly as it found it.
func (s *Store) RestoreSnapshot(snapshot Snapshot) error {
	if s == nil {
		return errUnavailable
	}
	for _, root := range s.roots {
		trees, carried := snapshot.treesFor(root.Provenance)
		if !carried {
			continue
		}
		if root.Dir == "" {
			return fmt.Errorf("%s skill root is unavailable", root.Provenance)
		}
		if err := ensureRoot(root.Dir); err != nil {
			return fmt.Errorf("restore %s skill root: %w", root.Provenance, err)
		}
		for _, tree := range *trees {
			if err := restoreTree(s, root.Dir, tree); err != nil {
				return fmt.Errorf("restore %s skill %q: %w", root.Provenance, tree.Name, err)
			}
		}
	}
	if s.docStore == nil {
		if len(snapshot.Settings) != 0 {
			return errors.New("restore skills: settings document is unavailable")
		}
		return nil
	}
	if len(snapshot.Settings) == 0 {
		if err := s.docStore.Delete(DocumentName); err != nil {
			return fmt.Errorf("restore %s: delete document: %w", DocumentName, err)
		}
		return nil
	}
	var document struct {
		SchemaVersion int      `json:"schemaVersion"`
		Disabled      []string `json:"disabled"`
	}
	if err := json.Unmarshal(snapshot.Settings, &document); err != nil {
		return fmt.Errorf("restore %s: %w", DocumentName, err)
	}
	if document.SchemaVersion == 0 {
		return fmt.Errorf("restore %s: missing schemaVersion", DocumentName)
	}
	if err := s.docStore.Write(DocumentName, snapshot.Settings); err != nil {
		return fmt.Errorf("restore %s: %w", DocumentName, err)
	}
	return nil
}

func snapshotRoot(root string) ([]SnapshotTree, error) {
	entries, err := os.ReadDir(root) //nolint:gosec // root is configured by the composition root
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	trees := make([]SnapshotTree, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		treeRoot := filepath.Join(root, entry.Name())
		var tree SnapshotTree
		tree.Name = entry.Name()
		err := filepath.WalkDir(treeRoot, func(path string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if item.Type()&os.ModeSymlink != 0 {
				if item.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if item.IsDir() {
				return nil
			}
			if !item.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(treeRoot, path)
			if err != nil {
				return err
			}
			bytes, err := os.ReadFile(path) //nolint:gosec // path is beneath a configured skill root
			if err != nil {
				return err
			}
			tree.Files = append(tree.Files, SnapshotFile{Path: filepath.ToSlash(rel), Bytes: string(bytes)})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %q: %w", entry.Name(), err)
		}
		sort.Slice(tree.Files, func(i, j int) bool { return tree.Files[i].Path < tree.Files[j].Path })
		trees = append(trees, tree)
	}
	sort.Slice(trees, func(i, j int) bool { return trees[i].Name < trees[j].Name })
	return trees, nil
}

func ensureRoot(root string) error {
	if err := os.MkdirAll(root, managedSkillDirMode); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("root is not a directory")
	}
	return nil
}

func restoreTree(s *Store, root string, tree SnapshotTree) error {
	if tree.Name == "" || filepath.Base(tree.Name) != tree.Name || tree.Name == "." || tree.Name == ".." {
		return errors.New("invalid skill directory name")
	}
	base := filepath.Join(root, tree.Name)
	if err := rejectSymlinkAncestors(root, base); err != nil {
		return err
	}
	if err := s.fs.MkdirAll(base, managedSkillDirMode); err != nil {
		return err
	}
	for _, file := range tree.Files {
		if err := restoreFile(s, base, file); err != nil {
			return err
		}
	}
	return nil
}

func restoreFile(s *Store, base string, file SnapshotFile) error {
	if file.Path == "" || filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path || file.Path == "." || strings.HasPrefix(filepath.ToSlash(file.Path), "../") {
		return fmt.Errorf("invalid relative path %q", file.Path)
	}
	path := filepath.Join(base, filepath.FromSlash(file.Path))
	rel, err := filepath.Rel(base, path)
	if err != nil || rel != filepath.FromSlash(file.Path) {
		return fmt.Errorf("path escapes skill directory: %q", file.Path)
	}
	dir := filepath.Dir(path)
	if err = rejectSymlinkAncestors(base, dir); err != nil {
		return err
	}
	if err = s.fs.MkdirAll(dir, managedSkillDirMode); err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target %q is a symlink", file.Path)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	tmp := path + ".tmp-" + strconv.FormatUint(s.seq.Add(1), 10)
	opened, err := s.fs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, managedSkillFileMode)
	if err != nil {
		return err
	}
	if _, err := opened.Write([]byte(file.Bytes)); err != nil {
		_ = opened.Close()
		_ = s.fs.Remove(tmp)
		return err
	}
	if err := opened.Close(); err != nil {
		_ = s.fs.Remove(tmp)
		return err
	}
	if err := s.fs.Sync(tmp); err != nil {
		_ = s.fs.Remove(tmp)
		return err
	}
	if err := s.fs.Rename(tmp, path); err != nil {
		_ = s.fs.Remove(tmp)
		return err
	}
	if err := s.fs.Sync(dir); err != nil {
		return err
	}
	return nil
}

func rejectSymlinkAncestors(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes skill root: %q", path)
	}
	current := rootAbs
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symlink", part)
		}
	}
	return nil
}
