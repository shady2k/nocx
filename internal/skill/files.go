package skill

// What one skill is MADE OF, for the person who has to decide about it.
//
// Design §8 lands an installed skill inert so the person can open it, see
// what it carries — `scripts/` included — and turn it on themselves. That is
// the whole justification for carrying executable text at all, and it needs
// a manifest of the bytes on disk. PreviewResult.Files names the same thing
// BEFORE an install and cannot serve here: it is what a document said it
// would fetch, not what is under the directory now, and it says nothing at
// all about a skill nobody installed from a URL.
//
// WHY IT IS A METHOD OF ITS OWN rather than a field on skills.list. The list
// is drawn as one row per skill and almost every row never asks this
// question; putting an array of paths on each of them would pay a directory
// WALK per skill on every refresh — and the list refreshes after every
// toggle, delete and approve — to fill a field one open card reads. Files
// answers for one skill because one skill is what a card is about.
//
// THE CUT IS REPORTED. A directory the person moved in by hand has no bound
// at all, and a card that quietly showed the first 256 of 300 files would be
// the interface asserting a manifest it had not read — the soft degrade
// AGENTS.md names. So the result carries the cap and whether it was reached,
// and the viewer can say so beside the list.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// MaxSkillFiles bounds the paths one listing returns. It is a bound on the
// ANSWER and not on the skill: the files beyond it are still on disk, still
// backed up and still readable by path, and the result says the list was cut
// so nothing has to infer it from the count.
const MaxSkillFiles = 256

// FilesResult is one skill's manifest: which skill was resolved, where its
// bytes come from, and every file under it.
type FilesResult struct {
	// Name and Provenance are the skill as RESOLVED by root precedence, for
	// FileResult's reason: a viewer labels what it is showing rather than
	// what it asked for, and the two differ exactly when two roots hold one
	// name.
	Name       string     `json:"name"`
	Provenance Provenance `json:"provenance"`
	// Files are slash-separated and relative to the skill's own directory,
	// SKILL.md first and the rest sorted — the order skills.preview already
	// uses for the same list before an install, because the file the person
	// came for should not have to be found among its references.
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
	MaxFiles  int      `json:"maxFiles"`
}

// Files lists every file of one discovered skill. It answers for ANY
// provenance and for a skill that is switched OFF, because a skill that is
// off is precisely the one this exists for: it landed inert so the person
// could look at it first, and a listing that skipped it would make that look
// impossible. That is `locate`'s offToo, and it is passed here for the same
// reason File passes it.
func Files(roots []Root, name string) (FilesResult, error) {
	// An empty relPath resolves to SKILL.md, so this is also the check that
	// the skill has the one file every skill has. Going through locate rather
	// than searching the roots again keeps root precedence and containment
	// answered in one place, which is what read.go exists to be.
	at, err := locate(roots, name, "", true)
	if err != nil {
		return FilesResult{}, err
	}
	paths, err := skillFilePaths(at.skill.root, at.entry)
	if err != nil {
		return FilesResult{}, err
	}
	out := FilesResult{
		Name:       at.skill.Name,
		Provenance: at.skill.Provenance,
		Files:      paths,
		MaxFiles:   MaxSkillFiles,
	}
	if len(out.Files) > MaxSkillFiles {
		out.Files = out.Files[:MaxSkillFiles]
		out.Truncated = true
	}
	return out, nil
}

// skillFilePaths walks one skill's directory and returns every REGULAR file
// in it, as slash-separated paths relative to that directory, SKILL.md first
// and the rest sorted.
//
// It is the one walk over a skill directory, and Snapshot's is now this one:
// the backup walked with its own copy of these rules, so a file the backup
// carried and a file this listing named could have disagreed about symlinks
// or about what counts as a file — two answers to "what is this skill made
// of", which is the defect AD-8 is about. Snapshot needs the BYTES too, so it
// joins each path onto the directory itself; that is the only part that is
// not shared, because it is the only part that differs.
//
// Symlinks are left out rather than followed, which is the rule both callers
// already kept: a backup that followed one would copy bytes from outside the
// root into the snapshot, and a card that named one would print a path whose
// bytes live somewhere else. locate refuses to READ one for the same reason,
// so a listed symlink would be a row the viewer could only fail to open.
func skillFilePaths(root Root, entry string) ([]string, error) {
	var paths []string
	var err error
	if root.FS != nil {
		paths, err = embeddedSkillFilePaths(root.FS, entry)
	} else if root.Dir != "" {
		paths, err = directorySkillFilePaths(filepath.Join(root.Dir, entry))
	} else {
		return nil, errors.New("skill root has neither Dir nor FS")
	}
	if err != nil {
		return nil, err
	}
	sortSkillFilePaths(paths)
	return paths, nil
}

func directorySkillFilePaths(base string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(base, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if item.IsDir() || !item.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// embeddedSkillFilePaths is the same walk over the builtin root. An embed.FS
// holds no symlinks and no irregular files, so the two branches differ in
// what they have to defend against rather than in what they mean.
func embeddedSkillFilePaths(fsys fs.FS, entry string) ([]string, error) {
	var paths []string
	err := fs.WalkDir(fsys, entry, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(entry, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// sortSkillFilePaths puts SKILL.md first and sorts the rest. The document is
// what the person opened the card for; sorting it in alphabetically would put
// `references/` above it on every bundle, so the first row of every bundle's
// list would be a support file. skills.preview orders its manifest the same
// way, and the two lists describe the same skill before and after an install.
func sortSkillFilePaths(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		left, right := paths[i], paths[j]
		if left == "SKILL.md" {
			return right != "SKILL.md"
		}
		if right == "SKILL.md" {
			return false
		}
		return left < right
	})
}
