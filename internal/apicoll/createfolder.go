package apicoll

// Making a folder inside a collection, which is the half §6.2 already
// describes and nothing could reach.
//
// A collection is a folder and it may contain folders. The Postman importer
// WRITES them, so a collection that arrived from an export has structure;
// a collection built inside nocx had none, because there was no
// folder-creation call site anywhere — two grammars for one concept, and
// the one a person could reach was the poorer.
//
// # A NAME and a PARENT, never a path
//
// The grammar is api.collections.create's, extended by exactly one
// parameter. The NAME is a single component and is refused if it is
// anything else; the PARENT is an EXISTING folder, addressed the way every
// other thing inside a collection is addressed — the backend-minted handle
// plus a path relative to it (§13.1). "" is the collection root.
//
// Why not one relative path, `a/b/c`, with the folders made along the way.
// Two reasons, and the first is the rule: a name that may contain a
// separator is a name that has to be sanitised or refused as a path, and
// §13.1's whole property is that the caller names a component and the
// package derives the location. Second, MkdirAll is a create that succeeds
// for a request nobody made: `reports/janury/daily` typed with the month
// misspelled would silently mint the misspelling, and there would be no
// moment at which the caller could be told the parent is not there. So
// nesting is REPEATED CALLS — one folder per call, each naming a parent
// that already exists — and the answer to "the parent is not there" is a
// refusal by name rather than a folder tree nobody asked for.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FolderCreated is the folder that was just made and the collection it now
// sits in.
//
// The collection rides along for the reason Created carries a handle
// (create.go): the caller's next move is to draw the tree with the new
// folder in it, and a caller that had to List afterwards would be reading
// the folder a second time at a second moment. One call, one answer, and
// the tree cannot be drawn from a listing taken before the folder existed.
type FolderCreated struct {
	// RelPath is where the folder is, relative to the handle — the same
	// spelling every later call names it by, and the value the caller
	// passes back as the parent of the next one.
	RelPath    string
	Collection Collection
}

// CreateFolder makes ONE folder inside an open collection.
//
// The interval, both ends named: the folder exists on disk from the moment
// Mkdir returns until somebody deletes it, and it is in every Collection
// this service answers from that same moment — the listing that comes back
// here is read AFTER the directory is made, so there is no state in which
// the call has succeeded and the folder is not in the answer.
//
// An existing name is refused rather than adopted, and the refusal is
// Mkdir's own EEXIST rather than a Lstat before it: a check-then-create
// would report "free" and then write into a folder somebody's git pull had
// landed in between. That is the same rule the import follows for its
// destination and the same one NewDefaultCollection follows for a
// collection.
func (s *service) CreateFolder(h HandleID, parentRelPath, name string) (FolderCreated, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return FolderCreated{}, err
	}
	if err = validateComponentName(name, ErrInvalidFolderName, "folder"); err != nil {
		return FolderCreated{}, err
	}
	if err = validateFolderPath(parentRelPath); err != nil {
		return FolderCreated{}, err
	}
	if parentRelPath == "" && name == environmentsDirName {
		return FolderCreated{}, fmt.Errorf(
			"%w: %q is where the collection keeps its environments, not its requests (§6.2)",
			ErrInvalidFolderName, environmentsDirName)
	}

	// The parent must be there already. resolveWithin is what says no
	// component of it is a symlink — the guarantee every read and write on
	// this surface has, and it matters more here: a `dir -> /home/you/.ssh`
	// would otherwise make a folder in somebody's home directory.
	parentFull := hd.root
	rel := name
	if parentRelPath != "" {
		if parentFull, err = resolveWithin(hd.root, parentRelPath); err != nil {
			return FolderCreated{}, err
		}
		fi, statErr := os.Lstat(parentFull)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			return FolderCreated{}, fmt.Errorf("%w: %q", ErrFolderNotFound, parentRelPath)
		case statErr != nil:
			return FolderCreated{}, fmt.Errorf("apicoll: stat folder %q: %w", parentRelPath, statErr)
		case !fi.IsDir():
			return FolderCreated{}, fmt.Errorf("%w: %q is not a folder", ErrFolderNotFound, parentRelPath)
		}
		rel = parentRelPath + "/" + name
	}

	// 0700, and Mkdir rather than MkdirAll: exactly one folder, in a
	// collection whose root already has this posture (default.go).
	if err = os.Mkdir(filepath.Join(parentFull, name), 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return FolderCreated{}, fmt.Errorf("%w: %q", ErrFolderExists, rel)
		}
		return FolderCreated{}, fmt.Errorf("apicoll: create folder %q: %w", rel, err)
	}

	m, err := readManifest(hd.root)
	if err != nil {
		return FolderCreated{}, err
	}
	coll, err := readCollection(hd, m)
	if err != nil {
		return FolderCreated{}, err
	}
	return FolderCreated{RelPath: rel, Collection: coll}, nil
}
