package apicoll

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
