package apicoll

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Open resolves the folder the user chose, refuses it if it is not a
// collection, and mints the handle every later call names. This is the ONLY
// method that takes a root (§13.1).
func (s *service) Open(root string) (HandleID, Collection, error) {
	if !filepath.IsAbs(root) {
		return "", Collection{}, fmt.Errorf("%w: %q is not an absolute path", ErrPathOutsideCollection, root)
	}

	// Resolve the root's symlinks ONCE, here. The user may legitimately have
	// chosen a symlinked folder — they named it, so it is theirs — and from
	// this point on the resolved path is the collection's identity and no
	// symlink inside it is ever followed.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", Collection{}, fmt.Errorf("apicoll: open collection %s: %w", root, err)
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		return "", Collection{}, fmt.Errorf("apicoll: open collection %s: %w", root, err)
	}
	if !fi.IsDir() {
		return "", Collection{}, fmt.Errorf("apicoll: %s is not a directory; a collection is a folder", root)
	}

	m, err := readManifest(resolved)
	if err != nil {
		return "", Collection{}, err
	}

	id, err := s.newID()
	if err != nil {
		return "", Collection{}, err
	}
	hd := &handle{root: resolved, namedAs: filepath.Clean(root), openedAs: fi}

	coll, err := readCollection(hd, m)
	if err != nil {
		return "", Collection{}, err
	}

	// Registered last: a folder that could not be listed hands back no
	// handle, so there is no id in the table that names a collection the
	// caller was never given.
	s.mu.Lock()
	s.handles[id] = hd
	s.mu.Unlock()
	return id, coll, nil
}

// List re-reads the folder. Contents are never cached: the folder is shared
// through git, so a pull, a branch switch or the user's own editor changes it
// underneath us, and a cache would be a second copy of a truth that already
// has an owner — the files.
func (s *service) List(h HandleID) (Collection, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return Collection{}, err
	}
	m, err := readManifest(hd.root)
	if err != nil {
		return Collection{}, err
	}
	return readCollection(hd, m)
}

// ReadRequest reads one request file, without following a symlink anywhere
// along the path.
func (s *service) ReadRequest(h HandleID, relPath string) (Request, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return Request{}, err
	}
	if err = validateRequestPath(relPath); err != nil {
		return Request{}, err
	}
	full, err := resolveWithin(hd.root, relPath)
	if err != nil {
		return Request{}, err
	}

	fi, err := os.Lstat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Request{}, fmt.Errorf("%w: %q", ErrRequestNotFound, relPath)
	case err != nil:
		return Request{}, fmt.Errorf("apicoll: stat request %q: %w", relPath, err)
	case !fi.Mode().IsRegular():
		return Request{}, fmt.Errorf("%w: %q is not a regular file", ErrNotARequestPath, relPath)
	}

	raw, err := os.ReadFile(full) //nolint:gosec // full is validated to be inside the collection
	if err != nil {
		return Request{}, fmt.Errorf("apicoll: read request %q: %w", relPath, err)
	}
	var r Request
	if err = decodeStrict(raw, &r); err != nil {
		return Request{}, fmt.Errorf("%w: %q: %w", ErrMalformedRequest, relPath, err)
	}
	return r, nil
}

// WriteRequest writes one request file atomically, and never through a
// symlink.
//
// The request is written as given. The one normalisation the round trip is
// stated against — an empty slice becomes nil — is `omitempty`'s, not this
// package's: JSON has no way to keep the two apart, so the canonical form is
// nil and the encoder already produces it. A canonical() helper here was
// written and then deleted, because it changed nothing that the struct tags
// were not already doing and a second answer to one question is the thing
// AGENTS.md is most against.
// The atomic write, the mode bits and the refusal to rename over a
// symlink at the target are storage.DocumentStore's — the existing answer
// (internal/storage/document.go:159), not a second one. What this package
// adds is the part a document store cannot know: which paths a hostile folder
// may name at all.
func (s *service) WriteRequest(h HandleID, relPath string, r Request) error {
	hd, err := s.resolve(h)
	if err != nil {
		return err
	}
	if err := validateRequestPath(relPath); err != nil {
		return err
	}
	if _, err := resolveWithin(hd.root, relPath); err != nil {
		return err
	}
	if err := s.docStoreFor(hd.root).Write(relPath, r); err != nil {
		return fmt.Errorf("apicoll: write request %q: %w", relPath, err)
	}
	return nil
}

// DeleteRequest removes one request file, and never follows a symlink to do
// it.
//
// The same path rules as every read (validateRequestPath, resolveWithin): a
// caller that may not READ a file may not delete it either, which is the
// property §13.1 is made of — and it matters more here, because a delete
// that followed a symlink out of the collection would remove somebody's file
// from somewhere they never opened.
//
// os.Remove, not RemoveAll: this deletes a FILE. A directory that happens to
// be named `x.json` is refused rather than emptied.
func (s *service) DeleteRequest(h HandleID, relPath string) error {
	hd, err := s.resolve(h)
	if err != nil {
		return err
	}
	if err = validateRequestPath(relPath); err != nil {
		return err
	}
	full, err := resolveWithin(hd.root, relPath)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %q", ErrRequestNotFound, relPath)
	case err != nil:
		return fmt.Errorf("apicoll: stat request %q: %w", relPath, err)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%w: %q is not a regular file", ErrNotARequestPath, relPath)
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("apicoll: delete request %q: %w", relPath, err)
	}
	return nil
}

// readCollection walks the folder and builds the listing. A decode failure is
// collected, never fatal: one bad file must not hide a collection.
func readCollection(hd *handle, m manifest) (Collection, error) {
	refs, bad, err := listRequests(hd.root)
	if err != nil {
		return Collection{}, err
	}
	return Collection{Name: m.Name, Requests: refs, Malformed: bad}, nil
}

// listRequests walks the folder for request files.
//
// filepath.WalkDir reads directory entries rather than stat-ing through them,
// so a symlinked request file arrives here AS a symlink and is named as
// malformed instead of being followed. That is the same rule ReadRequest
// applies, reached by a different route, and it has to hold here too: a
// listing that opened `steal.json` would have read the file before anybody
// clicked anything.
func listRequests(root string) ([]RequestRef, []MalformedRef, error) {
	var refs []RequestRef
	var bad []MalformedRef

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if walkErr != nil {
			// An unreadable subdirectory is one unreadable subdirectory, not
			// a collection that will not list.
			if rel != "." {
				bad = append(bad, MalformedRef{RelPath: rel, Reason: walkErr.Error()})
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			switch {
			case rel == ".":
				return nil
			case rel == environmentsDirName:
				// environments/ sits beside the requests (§6.2).
				return fs.SkipDir
			case strings.HasPrefix(d.Name(), "."):
				// .git and friends. A collection is shared through git, so
				// its own metadata is inside it and is not the user's data.
				return fs.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(rel, requestExt) || rel == ManifestName {
			return nil
		}
		if !d.Type().IsRegular() {
			bad = append(bad, MalformedRef{
				RelPath: rel,
				Reason:  "not a regular file; symlinks are not followed",
			})
			return nil
		}

		raw, err := os.ReadFile(p) //nolint:gosec // p came from walking the collection root
		if err != nil {
			bad = append(bad, MalformedRef{RelPath: rel, Reason: err.Error()})
			return nil
		}
		var r Request
		if err := decodeStrict(raw, &r); err != nil {
			bad = append(bad, MalformedRef{RelPath: rel, Reason: err.Error()})
			return nil
		}
		refs = append(refs, RequestRef{RelPath: rel, Name: r.Name, Method: r.Method})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("apicoll: list collection %s: %w", root, err)
	}

	// WalkDir is lexical, so the order is already deterministic; the empty
	// slices are explicit because a nil one marshals as null and a listing
	// that says null has told the renderer something different from "none".
	if refs == nil {
		refs = []RequestRef{}
	}
	if bad == nil {
		bad = []MalformedRef{}
	}
	return refs, bad, nil
}
