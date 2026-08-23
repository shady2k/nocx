package apicoll

// The environments half of the collection folder. `environments/` sits
// beside the requests (§6.2) and each file answers two questions at once:
// WHERE (baseUrl and the rest of the plain values) and HOW TO REACH IT (the
// route). They are one record on purpose — §6.5's third consequence is that
// a production request cannot accidentally go out around its bastion,
// because the address and the route cannot drift from each other.

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/storage"
)

// ErrNotAnEnvironmentPath — the path is inside the collection but does not
// name an environment file: it is not `environments/<name>.json`, it is
// nested below that, or it is not a regular file. Distinct from
// ErrNotARequestPath because the two surfaces are not each other's back
// door: reading a request through ReadEnvironment, or an environment
// through ReadRequest, is a caller that has lost track of what it holds.
var ErrNotAnEnvironmentPath = errors.New("apicoll: path does not name an environment file")

// ErrEnvironmentNotFound — nothing at that path inside the collection.
var ErrEnvironmentNotFound = errors.New("apicoll: no such environment in the collection")

// ErrMalformedEnvironment — the file exists and is not an environment: bad
// JSON, a field the format does not declare, or a route that does not say
// how to get there. Named, so one bad file is a bad file rather than a
// collection whose environments will not list.
var ErrMalformedEnvironment = errors.New("apicoll: environment file is malformed")

// EnvironmentRef is one environment file: where it is inside the
// collection, and what it says. The path is carried because it is how every
// later call names it — the same handle-plus-relative-path rule the request
// surface obeys (§13.1).
type EnvironmentRef struct {
	RelPath     string      `json:"relPath"`
	Environment Environment `json:"environment"`
}

// EnvironmentReader is the environments half of the folder surface.
//
// It is a second interface rather than two more methods on Service because
// they are two concerns with two audiences: a caller resolving a request to
// send needs the environment and never writes one, and Service's own
// property — Open is the ONLY entry point that takes a root — is asserted
// against Service's method set. Both are implemented by one type, so there
// is one handle table and one root re-validation, which is the part that
// must not be duplicated.
type EnvironmentReader interface {
	// ListEnvironments reads every environment in the folder. Malformed
	// files come back NAMED alongside the good ones rather than as an
	// error, for the same reason the request listing does it: one bad file
	// must not hide the rest.
	ListEnvironments(h HandleID) ([]EnvironmentRef, []MalformedRef, error)

	// ReadEnvironment reads one, addressed by its path relative to the
	// handle.
	ReadEnvironment(h HandleID, relPath string) (Environment, error)
}

// Collections is the whole collection surface. NewService remains the
// narrower constructor for a caller that only reads requests.
type Collections interface {
	Service
	EnvironmentReader
	EnvironmentWriter
	Creator
	Starter
}

// NewCollections returns the whole surface — requests, environments and the
// minting of a new collection — backed by one handle table.
//
// p decides where a collection created with no place named goes (§6.1); the
// location is derived from it INSIDE this package, so no caller names a path
// in order to get a collection. A nil p is a service that cannot create one,
// and says so by name (ErrNoDefaultLocation).
func NewCollections(p storage.Paths) Collections {
	s := newService()
	s.paths = p
	return s
}

var (
	_ EnvironmentReader = (*service)(nil)
	_ Collections       = (*service)(nil)
)

// ListEnvironments walks `environments/` and reads each `.json` in it.
//
// os.ReadDir reports directory entries rather than stat-ing through them,
// so a symlinked environment file arrives here AS a symlink and is named as
// malformed instead of being followed — the same rule ReadEnvironment
// applies, reached by a different route. It has to hold here too: a listing
// that opened a planted symlink would have read the file before anybody
// clicked anything.
func (s *service) ListEnvironments(h HandleID) ([]EnvironmentRef, []MalformedRef, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(hd.root, environmentsDirName)

	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// A collection with no environments is a collection. A Postman
		// export without one is ordinary.
		return []EnvironmentRef{}, []MalformedRef{}, nil
	case err != nil:
		return nil, nil, fmt.Errorf("apicoll: list environments in %s: %w", dir, err)
	}

	envs := []EnvironmentRef{}
	bad := []MalformedRef{}
	for _, e := range entries {
		rel := environmentsDirName + "/" + e.Name()
		if e.IsDir() {
			// Environments are not nested. A directory here is not an
			// environment and is not walked into.
			continue
		}
		if !strings.HasSuffix(e.Name(), requestExt) {
			continue
		}
		if !e.Type().IsRegular() {
			bad = append(bad, MalformedRef{
				RelPath: rel,
				Reason:  "not a regular file; symlinks are not followed",
			})
			continue
		}
		env, err := readEnvironmentFile(filepath.Join(dir, e.Name()))
		if err != nil {
			bad = append(bad, MalformedRef{RelPath: rel, Reason: err.Error()})
			continue
		}
		envs = append(envs, EnvironmentRef{RelPath: rel, Environment: env})
	}

	// os.ReadDir is already sorted by name, but the order is asserted
	// rather than inherited: two runs of one collection must compare.
	sort.SliceStable(envs, func(i, j int) bool { return envs[i].RelPath < envs[j].RelPath })
	sort.SliceStable(bad, func(i, j int) bool { return bad[i].RelPath < bad[j].RelPath })
	return envs, bad, nil
}

// ReadEnvironment reads one environment file, without following a symlink
// anywhere along the path.
func (s *service) ReadEnvironment(h HandleID, relPath string) (Environment, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return Environment{}, err
	}
	if err = validateEnvironmentPath(relPath); err != nil {
		return Environment{}, err
	}
	full, err := resolveWithin(hd.root, relPath)
	if err != nil {
		return Environment{}, err
	}

	fi, err := os.Lstat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Environment{}, fmt.Errorf("%w: %q", ErrEnvironmentNotFound, relPath)
	case err != nil:
		return Environment{}, fmt.Errorf("apicoll: stat environment %q: %w", relPath, err)
	case !fi.Mode().IsRegular():
		return Environment{}, fmt.Errorf("%w: %q is not a regular file", ErrNotAnEnvironmentPath, relPath)
	}

	env, err := readEnvironmentFile(full)
	if err != nil {
		return Environment{}, fmt.Errorf("%w: %q: %w", ErrMalformedEnvironment, relPath, err)
	}
	return env, nil
}

// validateEnvironmentPath decides whether relPath may name an environment
// file at all, before any part of the filesystem is touched. It reuses the
// "outside the collection" half rather than restating it — one owner for
// that question (path.go) — and adds only what is specific here: the file
// lives directly under `environments/` and nowhere else.
//
// Nothing is cleaned. `environments/./dev.json` is refused rather than
// quietly rewritten, for the reason path.go already gives: a caller that
// meant dev.json can say dev.json.
func validateEnvironmentPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("%w: the path is empty", ErrNotAnEnvironmentPath)
	}
	if err := checkInsideCollection(relPath); err != nil {
		return err
	}
	if relPath != filepath.Clean(relPath) {
		return fmt.Errorf("%w: %q is not already clean; it is refused rather than rewritten",
			ErrPathOutsideCollection, relPath)
	}
	dir, file := path.Split(filepath.ToSlash(relPath))
	if strings.TrimSuffix(dir, "/") != environmentsDirName {
		return fmt.Errorf("%w: %q is not directly under %s/", ErrNotAnEnvironmentPath, relPath, environmentsDirName)
	}
	if !strings.HasSuffix(file, requestExt) || file == requestExt {
		return fmt.Errorf("%w: %q is not a %s file", ErrNotAnEnvironmentPath, relPath, requestExt)
	}
	// An environment file is shared in the same pull request as everything
	// else in the folder, so it is held to the same portable-name rule
	// (path.go, and internal/pathname behind it): `environments/con.json` is
	// a file a colleague on Windows cannot check out either.
	return checkPortablePath(relPath, ErrNotAnEnvironmentPath)
}

// readEnvironmentFile reads and validates one file. It takes an already
// resolved absolute path: deciding WHICH file may be read is the caller's,
// and this is the part that decides whether what is in it is an
// environment.
//
// There is no version probe here, and that is deliberate rather than
// missing: the MANIFEST owns the collection's schema version (version.go),
// and readManifest refuses a folder from a newer build before any
// environment or request file is opened. A second version field here would
// be a second answer to one question.
func readEnvironmentFile(full string) (Environment, error) {
	raw, err := os.ReadFile(full) //nolint:gosec // full is validated to be inside the collection
	if err != nil {
		return Environment{}, err
	}
	var env Environment
	// Strict: a field the format does not declare is refused. That is the
	// FORMAT half of §8 — a hostile file has no field in which to spell an
	// identifier for stored credential material, so the attack is
	// unspellable rather than guarded.
	if err := decodeStrict(raw, &env); err != nil {
		return Environment{}, err
	}
	if err := validateRoute(env.Route); err != nil {
		return Environment{}, err
	}
	return env, nil
}

// validateRoute refuses a route that does not say how to get there.
//
// This is §6.5's third consequence, which is also its reason: a production
// request cannot accidentally go out around the bastion. An environment
// whose route is missing, unknown, or names a connection without naming
// WHICH connection is REFUSED — never quietly treated as direct, because a
// silent downgrade to direct is exactly the send the route exists to
// prevent. A file arriving in a pull request with `"route":{}` would
// otherwise send the reader's production request out of their own
// interface.
func validateRoute(r Route) error {
	switch r.Kind {
	case RouteDirect:
		if r.ProfileID != "" {
			return fmt.Errorf("route is %q but names profile %q; a direct route goes out from this machine and names no connection",
				RouteDirect, r.ProfileID)
		}
		return nil
	case RouteConnection:
		if r.ProfileID == "" {
			return fmt.Errorf("route is %q but names no connection; a request routed through a connection must say which one",
				RouteConnection)
		}
		return nil
	case "":
		return fmt.Errorf("the environment declares no route; it is refused rather than treated as %q, "+
			"because a request the user routed through a connection must never quietly go out of this machine", RouteDirect)
	default:
		return fmt.Errorf("unknown route kind %q", r.Kind)
	}
}

// EnvironmentWriter writes one environment file back.
//
// A fifth interface beside Service, EnvironmentReader, Creator and Starter,
// for the reason each of the others gives: Service's property — Open is the
// ONLY entry point that accepts a root — is asserted against Service's
// method set, and this takes no root either.
//
// It is separate from EnvironmentReader rather than folded into it because
// the two have different audiences: the sender reads and never writes, and a
// reader handed a writer is a reader that could.
type EnvironmentWriter interface {
	// WriteEnvironment writes one environment file atomically, and never
	// through a symlink. The path is validated exactly as ReadEnvironment
	// validates it — `environments/<file>.json`, directly under the
	// directory, nothing cleaned — so a caller that may not read a file may
	// not write it either.
	//
	// A path that does not exist yet is how an environment is CREATED:
	// there is no separate create, because a write to a name nothing
	// occupies is the whole of it and a second method would be a second
	// answer to "how does an environment come to exist".
	WriteEnvironment(h HandleID, relPath string, env Environment) error
}

var _ EnvironmentWriter = (*service)(nil)

// WriteEnvironment implements EnvironmentWriter.
//
// The route is validated before anything is written, by the SAME predicate
// that guards a file being read (validateRoute). A collection is shared
// through git, so a file this build writes is a file some other build reads:
// writing a route this package would refuse to read back would put a folder
// on disk that only its author can open.
func (s *service) WriteEnvironment(h HandleID, relPath string, env Environment) error {
	hd, err := s.resolve(h)
	if err != nil {
		return err
	}
	if err = validateEnvironmentPath(relPath); err != nil {
		return err
	}
	if _, err = resolveWithin(hd.root, relPath); err != nil {
		return err
	}
	if err = validateRoute(env.Route); err != nil {
		return err
	}
	// The atomic write, the mode bits and the refusal to rename over a
	// symlink at the target are storage.DocumentStore's — the existing
	// answer, not a second one. WriteRequest says the same thing one file
	// over, and for the same reason.
	if err = s.docStoreFor(hd.root).Write(relPath, env); err != nil {
		return fmt.Errorf("apicoll: write environment %q: %w", relPath, err)
	}
	return nil
}
