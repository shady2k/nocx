package apiimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/pathname"
)

// File and directory modes. A collection is shared by committing it, not by
// loosening its permissions on the machine that holds it.
const (
	collectionFileMode = 0o600
	collectionDirMode  = 0o700

	environmentsDir = "environments"
	stagingPattern  = ".apiimport-*"
)

// ImportInto converts an import document and writes it to dest as ONE
// ATOMIC ARRIVAL (design §12.2).
//
// The document is a Postman export or a single curl line, told apart by its
// first byte. AN IMPORT NEVER FIRES A REQUEST: see TestPackageNeverExecs,
// which asserts this package cannot reach net/http or os/exec at all.
//
// The shape, and every step of it is a test with the failure injected
// through FS:
//
//  1. dest must not exist. It is REFUSED, not replaced — an import that
//     silently ate the collection somebody was working in would be a
//     data-loss bug with a success message on it.
//  2. Everything is assembled in a staging directory created INSIDE dest's
//     parent, so the rename is within one filesystem. Across filesystems a
//     rename is a copy, and a copy is not atomic.
//  3. Files and then the directories holding them are synced, deepest
//     first; without that the crash window is "the rename landed and the
//     contents did not".
//  4. One rename, then the parent is synced so the directory entry is
//     durable too.
//  5. Only then are the secret values offered to the BindWriter.
//
// The invariant, with both ends named as testing rule 3 demands:
//
//	dest DOES NOT EXIST from before the first byte is written until the
//	rename has landed AND every binding this import declares has been
//	written; from that moment it exists until the user deletes it. There is
//	no state in between in which dest exists and this import is unfinished
//	— any failure after the rename removes it again.
//
// Ordering within the binding store is apibind's (§8.2, §12.2: the vault
// value first, the binding second). This package hands over one value at a
// time and that is the whole of its part in it.
//
// route is how the document was ACQUIRED, and the environment this mints
// inherits it (§6): a collection fetched through a connection whose
// environment said `direct` is a collection where every request fails until
// the person sets by hand the thing they had already told the import. Only
// Kind and ProfileID are inherited — InsecureTLS is the environment's own
// choice (apicoll/collection.go:126) and a one-off fetch may not make it
// for every request the collection will ever send.
func ImportInto(ctx context.Context, fsys FS, b BindWriter, dest string, r io.Reader, route apicoll.Route) ([]Unsupported, error) {
	if fsys == nil || b == nil {
		return nil, errors.New("apiimport: an importer needs both a filesystem and a binding store")
	}
	dest = strings.TrimRight(filepath.Clean(dest), string(filepath.Separator))
	if dest == "" || dest == "." || dest == ".." || dest == string(filepath.Separator) {
		return nil, fmt.Errorf("apiimport: %q is not a usable collection folder", dest)
	}
	parent := filepath.Dir(dest)
	if parent == dest {
		return nil, fmt.Errorf("apiimport: %q has no parent directory to stage in", dest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res, err := parseImport(r, route)
	if err != nil {
		return nil, err
	}

	// An existing destination is refused rather than replaced, and a plain
	// file at that path is refused too: treating it as absent would put a
	// rename over somebody's file.
	if _, statErr := fsys.Lstat(dest); statErr == nil {
		return nil, fmt.Errorf("apiimport: %s already exists; an import never replaces a collection", dest)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("apiimport: check %s: %w", dest, statErr)
	}

	files, err := layout(res)
	if err != nil {
		return nil, err
	}

	staging, err := fsys.MkdirTemp(parent, stagingPattern)
	if err != nil {
		return nil, fmt.Errorf("apiimport: stage next to %s: %w", dest, err)
	}
	// Until the rename, the only thing to undo is the staging directory.
	arrived := false
	defer func() {
		if !arrived {
			_ = fsys.RemoveAll(staging)
		}
	}()

	dirs := map[string]bool{}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		full := filepath.Join(staging, filepath.FromSlash(f.relPath))
		if d := path.Dir(f.relPath); d != "." {
			if !dirs[d] {
				if err := fsys.MkdirAll(filepath.Join(staging, filepath.FromSlash(d)), collectionDirMode); err != nil {
					return nil, fmt.Errorf("apiimport: create %s: %w", d, err)
				}
				dirs[d] = true
			}
		}
		if err := fsys.WriteFile(full, f.body, collectionFileMode); err != nil {
			return nil, fmt.Errorf("apiimport: write %s: %w", f.relPath, err)
		}
		if err := fsys.Sync(full); err != nil {
			return nil, fmt.Errorf("apiimport: sync %s: %w", f.relPath, err)
		}
	}

	// Directories deepest first, then the staging root: a directory entry
	// is only durable once its own directory is.
	for _, d := range deepestFirst(dirs) {
		if err := fsys.Sync(filepath.Join(staging, filepath.FromSlash(d))); err != nil {
			return nil, fmt.Errorf("apiimport: sync %s: %w", d, err)
		}
	}
	if err := fsys.Sync(staging); err != nil {
		return nil, fmt.Errorf("apiimport: sync the staging directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := fsys.Rename(staging, dest); err != nil {
		return nil, fmt.Errorf("apiimport: move the import into place: %w", err)
	}
	arrived = true
	// From here the collection is visible, so every remaining failure has
	// to take it away again — the invariant above has no state in which
	// dest exists and the import is unfinished.
	undo := func(cause error) error {
		if rmErr := fsys.RemoveAll(dest); rmErr != nil {
			return fmt.Errorf("%w (and %s could not be removed: %v)", cause, dest, rmErr)
		}
		return cause
	}

	if err := fsys.Sync(parent); err != nil {
		return nil, undo(fmt.Errorf("apiimport: sync %s: %w", parent, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, undo(err)
	}

	for _, s := range res.Secrets {
		env := s.Environment
		if env == "" {
			env = defaultEnvName
		}
		key := apibind.Key{Collection: dest, Environment: env, Variable: s.Variable}
		if err := b.Bind(ctx, key, s.Value); err != nil {
			// The variable's NAME is safe to name; its value is what we are
			// carrying and never what we report.
			return nil, undo(fmt.Errorf("apiimport: bind %s: %w", s.Variable, err))
		}
	}
	return res.Unsupported, nil
}

// parseImport picks the entrance. A Postman export is JSON and a curl line
// is not, and one byte tells them apart — which is the whole of "two
// entrances, one converter" (§10).
func parseImport(r io.Reader, route apicoll.Route) (postmanResult, error) {
	var res postmanResult
	raw, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return res, fmt.Errorf("apiimport: read import document: %w", err)
	}
	if len(raw) > MaxDocumentBytes {
		return res, fmt.Errorf("apiimport: import document is over the %d-byte limit", MaxDocumentBytes)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return res, errors.New("apiimport: the import document is empty")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return parsePostman(bytes.NewReader(raw), route)
	}
	return curlImport(string(trimmed))
}

// curlImport wraps one curl line in the smallest collection that can hold
// it: the request, and an environment declaring whatever variables the line
// turned out to need.
func curlImport(line string) (postmanResult, error) {
	var res postmanResult
	namer := newVarNamer()
	req, offers, unsup, err := parseCurl(line, namer, credentialsToBinder)
	if err != nil {
		return res, err
	}
	alloc := newPathAllocator()
	relPath := alloc.take("", req.Name, fallbackRequest, ".json")
	req.ID = requestID(relPath)

	res.Requests = []apicoll.Request{req}
	res.Collection = apicoll.Collection{
		Name: collectionNameFor(req.URL),
		Requests: []apicoll.RequestRef{{
			RelPath: relPath,
			Name:    req.Name,
			Method:  req.Method,
		}},
	}
	res.Unsupported = unsup

	env := apicoll.Environment{Name: defaultEnvName, Route: apicoll.Route{Kind: apicoll.RouteDirect}}
	for _, o := range offers {
		o.Environment = defaultEnvName
		env.SecretVars = append(env.SecretVars, o.Variable)
		res.Secrets = append(res.Secrets, o)
	}
	// An auth field that is EXACTLY one reference — which is what the
	// importers write, and what "carries the name, not the value" means in
	// the model — needs that name declared secret too, or the send has
	// nothing to report as unresolved when the binding is absent.
	sawAuth := map[string]bool{}
	if n, ok := apicoll.ExactReference(req.Auth.Token); ok {
		sawAuth[n] = true
	}
	if n, ok := apicoll.ExactReference(req.Auth.Password); ok {
		sawAuth[n] = true
	}
	for n := range sawAuth {
		if !contains(env.SecretVars, n) {
			env.SecretVars = append(env.SecretVars, n)
		}
	}
	res.Environments = []apicoll.Environment{env}
	return res, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// collectionNameFor names a one-request collection after its host, which is
// what a person recognises in a list of folders.
func collectionNameFor(rawURL string) string {
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "Imported request"
	}
	return s
}

// plannedFile is one file the import will write, addressed the way the
// collection addresses it: a slash-separated path relative to the root.
type plannedFile struct {
	relPath string
	body    []byte
}

// layout turns the converted model into the files to write. It is a
// separate step so that the whole document is marshalled BEFORE anything
// touches the disk: a marshal error after twenty files is a partial import
// for no reason at all.
func layout(res postmanResult) ([]plannedFile, error) {
	if len(res.Requests) != len(res.Collection.Requests) {
		return nil, fmt.Errorf("apiimport: %d requests for %d references", len(res.Requests), len(res.Collection.Requests))
	}
	files := make([]plannedFile, 0, len(res.Requests)+len(res.Environments)+1)

	// The manifest is spelled by the package that reads it. apicoll owns
	// the file's name, its fields and the schemaVersion this build writes;
	// this package knows only that a collection needs one and what it is
	// called. A second serialisation here is precisely the two-owners
	// defect that shipped an import nothing could open (nocx-1qtef): this
	// package wrote `collection.json` holding an apicoll.Collection while
	// the reader looked for `nocx-collection.json` holding
	// {schemaVersion, name}, and both packages were green.
	//
	// The list of requests is deliberately NOT written anywhere: the
	// folder IS the list (§6.2), and res.Collection.Requests survives as
	// the layout's own plan for where each request file goes. A file
	// restating it would be a second answer to "what is in this
	// collection", and the one that goes stale.
	body, err := apicoll.MarshalManifest(res.Collection.Name)
	if err != nil {
		return nil, err
	}
	files = append(files, plannedFile{apicoll.ManifestName, body})

	for i, req := range res.Requests {
		rel := res.Collection.Requests[i].RelPath
		if err := safeRelPath(rel); err != nil {
			return nil, err
		}
		body, err := marshal(req)
		if err != nil {
			return nil, err
		}
		files = append(files, plannedFile{rel, body})
	}

	envAlloc := newPathAllocator()
	for _, env := range res.Environments {
		rel := envAlloc.take(environmentsDir, env.Name, defaultEnvName, ".json")
		if err := safeRelPath(rel); err != nil {
			return nil, err
		}
		body, err := marshal(env)
		if err != nil {
			return nil, err
		}
		files = append(files, plannedFile{rel, body})
	}
	return files, nil
}

// safeRelPath is the belt to slug's braces. Every path here was minted by
// pathAllocator and cannot contain a traversal, so this can only fail if
// that stops being true — which is exactly when a check is worth having,
// because §13.1's rule is that a path out of a collection is refused rather
// than clamped.
//
// It asks pathname the same question apicoll asks it, which makes this the
// place where "what the importer mints is what the store accepts" is checked
// at RUN time rather than only in a test: an import that would write a path
// the store would then refuse fails here, whole, before a single file lands.
func safeRelPath(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return fmt.Errorf("apiimport: refusing the path %q", rel)
	}
	if path.Clean(rel) != rel {
		return fmt.Errorf("apiimport: refusing the path %q", rel)
	}
	if err := pathname.CheckRelPath(rel); err != nil {
		return fmt.Errorf("apiimport: refusing the path %q: %w", rel, err)
	}
	return nil
}

// marshal writes indented JSON with a trailing newline: these files are
// meant to be reviewed in a pull request (§6.1), and a one-line file has no
// diff worth reading.
func marshal(v any) ([]byte, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("apiimport: marshal: %w", err)
	}
	return append(body, '\n'), nil
}

// deepestFirst orders the directories so a child is synced before its
// parent.
func deepestFirst(dirs map[string]bool) []string {
	out := make([]string, 0, len(dirs))
	for d := range dirs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := strings.Count(out[i], "/"), strings.Count(out[j], "/")
		if di != dj {
			return di > dj
		}
		return out[i] < out[j]
	})
	return out
}

// ---- the real filesystem ----

// NewOSFS returns the FS that ImportInto uses in the product. It is the
// only place in this package that touches a disk, which is what leaves
// every failure path above reachable from a test.
func NewOSFS() FS { return osFS{} }

type osFS struct{}

func (osFS) MkdirTemp(dir, pattern string) (string, error) { return os.MkdirTemp(dir, pattern) }

func (osFS) MkdirAll(p string, perm os.FileMode) error { return os.MkdirAll(p, perm) }

func (osFS) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(name) }

// WriteFile refuses to write through a symlink. internal/storage's document
// store already refuses exactly this (document.go), and §13.1 says why:
// writing never follows a link, or a collection out of a pull request
// chooses which file gets overwritten.
//
// gosec reads the variable path here as file inclusion. Taking a path IS
// what this type is for — it is the injectable seam FS exists to be — so
// the answer is not to hide the parameter but to say where the path comes
// from: ImportInto builds every one as filepath.Join(staging, seg…) where
// staging came from MkdirTemp and each seg was minted by slug, which cannot
// produce "", "." or ".." and cannot produce a separator. safeRelPath
// re-checks that in layout, before the disk is touched at all. And O_EXCL
// is the second half: an existing file or a symlink is refused rather than
// followed, so even a path that got past all of the above cannot pick the
// file that gets overwritten.
func (osFS) WriteFile(name string, b []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, perm) //nolint:gosec // the path is minted by slug and re-checked by safeRelPath; O_EXCL refuses a symlink
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Sync fsyncs a file or a directory. A directory needs it for the same
// reason a file does: the entry is not durable until it is.
//
// The same G304 as WriteFile above, and narrower: Sync only ever reopens a
// path ImportInto has just created itself, and it reads nothing — the
// handle exists to be fsynced and closed.
func (osFS) Sync(name string) error {
	f, err := os.Open(name) //nolint:gosec // reopens a path this package has just created, and reads none of it
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (osFS) RemoveAll(p string) error { return os.RemoveAll(p) }
