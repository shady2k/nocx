package apicoll

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/pathname"
	"github.com/shady2k/nocx/internal/storage"
)

// DefaultCollectionsDirName is the folder under the app's data directory that
// holds collections the user did not choose a place for.
const DefaultCollectionsDirName = "collections"

// NewDefaultCollection creates an empty collection in the default location
// and returns its root, ready for Open.
//
// # Where a new collection goes, and why
//
// `<Paths.DataDir()>/collections/<name>`. Decided here rather than left open
// (design §15 q1, which offered a fixed directory or a remembered preference
// in the manner of Bruno's ~/Documents/bruno).
//
// Three reasons, in order of weight:
//
//  1. §6.1 already says it: "A new collection with no answer given goes to a
//     default folder under the app directory, so 'just make one' works
//     without a decision." A remembered preference is a different feature —
//     it is the user choosing a place, which they can already do by choosing
//     a folder — and it would put a second answer beside this one for the
//     question "where do collections live".
//  2. The data directory, not the config directory. ConfigDir holds
//     human-recoverable configuration documents this app owns and rewrites
//     whole; a collection is the user's content, and it is content the user
//     is expected to take away, put under git and share. DataDir is where
//     content already lives (ADR-0011, content.db).
//  3. No caller names a path in order to get one. The location is derived
//     from storage.Paths INSIDE this package, which is what keeps the §13.1
//     property true through the creation path as well: Open is still the only
//     entry point that accepts a root, and the root it is given here is one
//     this package built.
//
// The build tag decides which app directory that is, so a dev stand and the
// installed app never write each other's collections (internal/storage/appdir.go).
func NewDefaultCollection(p storage.Paths, name string) (string, error) {
	if err := validateCollectionName(name); err != nil {
		return "", err
	}
	root := filepath.Join(p.DataDir(), DefaultCollectionsDirName, name)

	// Refuse rather than write a fresh manifest over somebody's collection.
	// Lstat, so a symlink sitting at that name counts as occupied.
	if _, err := os.Lstat(root); err == nil {
		return "", fmt.Errorf("%w: %s", ErrCollectionExists, root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("apicoll: create collection %s: %w", root, err)
	}

	// 0700 is the posture the profile directory already uses; git carries no
	// mode bit but the execute one, so this costs the folder nothing when it
	// is shared and keeps it private while it is not.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("apicoll: create collection %s: %w", root, err)
	}
	if err := storage.NewDocumentStore(root).Write(ManifestName, newManifest(name)); err != nil {
		return "", fmt.Errorf("apicoll: write manifest for %s: %w", root, err)
	}
	return root, nil
}

// validateCollectionName refuses anything that is not a single folder name.
// The name reaches a path, so it is exactly as hostile as a path (§13.1) —
// and the answer is the same one: refuse, never sanitise. A name quietly
// stripped of its slashes creates a folder the user did not ask for under a
// name they did not choose.
func validateCollectionName(name string) error {
	return validateComponentName(name, ErrInvalidCollectionName, "collection")
}

// validateComponentName is that rule, once, for every name this package
// turns into one path segment: the collection folder here, and a folder
// inside a collection (createfolder.go).
//
// It is parameterised by the sentinel and the noun rather than copied,
// because the two callers differ only in what a surface must SAY. The rule
// itself — a single component, no separator, no leading dot, nothing that
// denotes a directory, bounded, and nothing a platform we ship to refuses —
// has one owner, and a second copy of it would agree with this one
// everywhere anybody looked and disagree on the day somebody widened only
// one.
//
// TWO rules meet here and only one of them is this package's. "Is this one
// usable path component anywhere" belongs to internal/pathname, which the
// importer mints through as well, so the two sides cannot drift. "No
// dotfiles" is a COLLECTION's rule and stays here: `.git` is a perfectly
// portable name, and the reason it is refused is that a hidden folder in a
// collection is one a reviewer never sees in the diff.
func validateComponentName(name string, sentinel error, what string) error {
	switch {
	case name == "":
		// pathname refuses this too and owns the rule; the branch is here
		// only to name which noun is missing, which it cannot know.
		return fmt.Errorf("%w: a %s needs a name", sentinel, what)
	case strings.HasPrefix(name, ".") && name != "." && name != "..":
		return fmt.Errorf("%w: %q starts with a dot", sentinel, name)
	}
	if err := pathname.CheckComponent(name); err != nil {
		return fmt.Errorf("%w: %s", sentinel, err)
	}
	return nil
}
