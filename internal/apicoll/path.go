package apicoll

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/pathname"
)

// environmentsDirName sits beside the requests and is not one of them (§6.2).
const environmentsDirName = "environments"

// requestExt is the extension a request file has. JSON, and only JSON —
// `contracts/` already holds JSON Schemas, so the format costs no hand-written
// parser and no second road to types (§6.2).
const requestExt = ".json"

// validateRequestPath decides whether relPath may name a request file at
// all, before any part of the filesystem is touched. Three questions, and the
// ORDER is part of the answer:
//
//  1. Does it point outside the collection — absolute, or containing `..`?
//     ErrPathOutsideCollection. This is first because it is first for the
//     caller too: `../../id_ed25519` names no request file either, and
//     answering "that is not a .json" to an attempt to read an SSH key would
//     be technically true and useless.
//  2. Does it name a request file — a `.json` that is neither the manifest
//     nor an environment? ErrNotARequestPath.
//  3. Is it spelled canonically? ErrPathOutsideCollection.
//  4. Is every component of it a name every platform we ship to can take,
//     and is the path bounded? ErrNotARequestPath. Last, because it is the
//     mildest of the four: the caller named a request file, it just named
//     one that cannot exist on a colleague's machine.
//
// Nothing is cleaned. `./req.json` and `sub/../req.json` both denote req.json
// after a filepath.Clean and both are refused, because a caller that meant
// req.json can say req.json, and a caller that did not mean it must not be
// quietly given it.
func validateRequestPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("%w: the path is empty", ErrNotARequestPath)
	}
	if err := checkInsideCollection(relPath); err != nil {
		return err
	}
	if !strings.HasSuffix(relPath, requestExt) {
		return fmt.Errorf("%w: %q is not a %s file", ErrNotARequestPath, relPath, requestExt)
	}
	if relPath == ManifestName {
		return fmt.Errorf("%w: %q is the collection manifest", ErrNotARequestPath, relPath)
	}
	if strings.HasPrefix(relPath, environmentsDirName+"/") {
		return fmt.Errorf("%w: %q is an environment, not a request", ErrNotARequestPath, relPath)
	}
	if relPath != filepath.Clean(relPath) {
		return fmt.Errorf("%w: %q is not already clean; it is refused rather than rewritten",
			ErrPathOutsideCollection, relPath)
	}
	return checkPortablePath(relPath, ErrNotARequestPath)
}

// checkPortablePath asks the one owner of "is this name usable as a path
// component on every platform we ship to" (internal/pathname) about relPath
// as a whole: every component, how deep it goes, how long it is in total.
//
// It is a WRAPPER and not a rule. The rule is one rule with one owner —
// apiimport MINTS through the same package, so a name the importer produces
// is a name this package accepts, by construction rather than by both being
// kept in step. What belongs here is only the sentinel, because the sentence
// a surface shows differs by what the caller was naming.
//
// A collection that already holds such a name still LISTS it, and the
// refusal names it: the remedy is to rename the file, which is the same
// thing the colleague on Windows needs done before they can check the folder
// out at all.
func checkPortablePath(relPath string, sentinel error) error {
	if err := pathname.CheckRelPath(relPath); err != nil {
		return fmt.Errorf("%w: %q: %s", sentinel, relPath, err)
	}
	return nil
}

// checkInsideCollection is the half that answers "outside", and only that.
func checkInsideCollection(relPath string) error {
	if strings.ContainsRune(relPath, 0) {
		return fmt.Errorf("%w: the path contains a NUL byte", ErrPathOutsideCollection)
	}
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("%w: %q is absolute; every path after Open is relative to the handle",
			ErrPathOutsideCollection, relPath)
	}
	for _, el := range strings.Split(relPath, string(filepath.Separator)) {
		if el == ".." {
			return fmt.Errorf("%w: %q climbs out of the collection", ErrPathOutsideCollection, relPath)
		}
	}
	return nil
}

// resolveWithin turns a validated relative path into an absolute one, and is
// the second half of the guarantee: no component of it may be a symlink.
//
// Why per component and not only the leaf. `internal/storage/document.go`
// refuses to write over a symlink at the target, which is the right guard for
// a document whose directory the app owns. A collection folder's directories
// come from a pull request too, so `dir -> /home/you/.ssh` with a write to
// `dir/planted.json` would slip past a leaf-only check. Both halves are
// needed; this one is the collection's, and the store's own guard still runs
// underneath it.
//
// Together with validateRelPath — relative, clean, no `..` — a path whose
// every component is a real directory or a real file is under root both
// lexically and physically, which is what "inside the collection" has to mean
// when the folder is hostile.
//
// A component that does not exist ends the walk: nothing below a path that is
// not there can exist either, and WriteRequest legitimately names a directory
// it is about to create.
func resolveWithin(root, rel string) (string, error) {
	cur := root
	for _, el := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, el)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("apicoll: stat %s: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %q passes through a symlink, which is not followed", ErrPathOutsideCollection, rel)
		}
	}
	return filepath.Join(root, rel), nil
}

// validateFolderPath decides whether relPath may name a FOLDER inside the
// collection — the parent a new folder is created in. "" is the collection
// root itself, which is where a folder created with no parent named goes.
//
// It is validateRequestPath's sibling and shares its first half
// (checkInsideCollection) rather than restating it: "outside the
// collection" is one question with one owner. What differs is the second
// half — a folder is not a `.json`, so the suffix rule cannot apply, and
// each COMPONENT is instead held to the same rule a new folder's name is
// (validateComponentName). That is what keeps `.git`, `..` and an
// over-long segment out by the same sentence in both directions.
//
// Nothing is cleaned, for validateRequestPath's reason: a caller that meant
// `a/b` can say `a/b`, and one that said `a/./b` must not be quietly given
// it.
func validateFolderPath(relPath string) error {
	if relPath == "" {
		return nil
	}
	if err := checkInsideCollection(relPath); err != nil {
		return err
	}
	if relPath != filepath.Clean(relPath) {
		return fmt.Errorf("%w: %q is not already clean; it is refused rather than rewritten",
			ErrPathOutsideCollection, relPath)
	}
	if err := checkPortablePath(relPath, ErrInvalidFolderName); err != nil {
		return err
	}
	// Every component has already been held to the portable-name rule by the
	// line above. This loop is what only a COLLECTION knows: no dotfiles, and
	// `environments` at the top is taken (§6.2).
	for i, el := range strings.Split(relPath, "/") {
		if err := validateComponentName(el, ErrInvalidFolderName, "folder"); err != nil {
			return err
		}
		if i == 0 && el == environmentsDirName {
			return fmt.Errorf("%w: %q is where the collection keeps its environments, not its requests (§6.2)",
				ErrInvalidFolderName, environmentsDirName)
		}
	}
	return nil
}
