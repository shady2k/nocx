package apicoll

// The built-in collection: the folder a stand has before anybody makes one.
//
// §6.1 gives the app two doors — open a folder the user chose, and make one
// for a user who has not chosen — and both of them are ASKS. So the state a
// person meets on a fresh stand is an empty tree with two buttons, and the
// first thing the product asks them to do is administration: name a folder,
// or find one. Nothing in the panel can be pressed until they have.
//
// This is the third door and it is nobody's ask: on the first listing of a
// session the built-in collection is opened, and created the one time it is
// not there yet. It is an ordinary collection in the ordinary place —
// `<DataDir>/collections/Playground`, the same folder Create writes into —
// so it can be edited, committed, moved or deleted like any other, and the
// panel holds no second notion of "a collection that is special".
//
// It carries two requests and one environment rather than being empty,
// because an empty collection is the same blank surface one folder deeper.
// The two are chosen to show the panel working end to end without an
// account, a key or a local server: `/zen` answers a line of text, and
// `/rate_limit` answers JSON, both unauthenticated, and both reach their
// host through `{{baseUrl}}` — so the environment picker has something to
// govern from the first second.
//
// Deleted on purpose stays deleted for the session: the ensure runs once per
// process (internal/capability/api.go), so a person who closes it is not
// arguing with the panel. It returns on the next start, which is what makes
// it built-in rather than a one-time seeding nobody can get back.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StarterName is the built-in collection's name, and — since a collection
// folder is named after it — the folder's name too.
const StarterName = "Playground"

// Starter opens the built-in collection.
//
// A fourth interface beside Service, EnvironmentReader and Creator, for the
// reason create.go already gives: Service's property — Open is the ONLY
// entry point that accepts a root — is asserted against Service's method
// set, and this one accepts no root at all.
type Starter interface {
	// EnsureStarter opens the built-in collection, creating and seeding it
	// the first time. It is idempotent: an existing folder is opened as it
	// stands and never re-seeded, so an edited or emptied Playground is the
	// user's, not a template this package keeps restoring.
	EnsureStarter() (Created, error)
}

var _ Starter = (*service)(nil)

// EnsureStarter implements Starter.
//
// The interval, both ends named: from the first call in a process until the
// folder stops being the directory it was opened on, the handle returned
// here addresses the built-in collection. The folder itself outlives every
// handle — it is a folder on disk, and only the user deletes it.
func (s *service) EnsureStarter() (Created, error) {
	if s.paths == nil {
		return Created{}, ErrNoDefaultLocation
	}
	root := filepath.Join(s.paths.DataDir(), DefaultCollectionsDirName, StarterName)

	// Lstat, so a symlink sitting at that name counts as present: this
	// package never follows one, and Open would refuse it by name. Creating
	// over it is the one thing that must not happen.
	_, err := os.Lstat(root)
	switch {
	case err == nil:
		op, openErr := s.Open(root)
		if openErr != nil {
			return Created{Root: root}, openErr
		}
		return Created{Root: root, Handle: op.Handle, Collection: op.Collection}, nil
	case !errors.Is(err, os.ErrNotExist):
		return Created{Root: root}, fmt.Errorf("apicoll: open the built-in collection %s: %w", root, err)
	}

	made, err := s.Create(StarterName)
	if err != nil {
		return Created{Root: root}, err
	}
	if err = s.seedStarter(made.Handle); err != nil {
		// The folder exists and is a working, empty collection — Create's
		// own partial-failure posture (create.go), for the same reason:
		// what is on disk works, and deleting it to report that the seeding
		// did not would be the worse answer.
		return made, err
	}
	// Re-read, so what comes back names the files that were just written
	// rather than the empty folder Create opened.
	coll, err := s.List(made.Handle)
	if err != nil {
		return made, err
	}
	made.Collection = coll
	return made, nil
}

// seedStarter writes the two requests and the environment.
//
// Through this service's own writers, not through a second spelling of the
// format: WriteRequest validates the path and writes atomically, and the
// environment goes through the same document store — so a seeded file is
// byte-for-byte a file the user could have written, and there is one owner
// of what a request file looks like.
func (s *service) seedStarter(h HandleID) error {
	hd, err := s.resolve(h)
	if err != nil {
		return err
	}
	env := Environment{
		Name:   "GitHub",
		Values: map[string]string{"baseUrl": "https://api.github.com"},
		Route:  Route{Kind: RouteDirect},
	}
	if err = s.docStoreFor(hd.root).Write(environmentsDirName+"/github.json", env); err != nil {
		return fmt.Errorf("apicoll: seed the built-in collection's environment: %w", err)
	}
	for _, seed := range starterRequests() {
		if err = s.WriteRequest(h, seed.relPath, seed.request); err != nil {
			return fmt.Errorf("apicoll: seed %s in the built-in collection: %w", seed.relPath, err)
		}
	}
	return nil
}

type starterSeed struct {
	relPath string
	request Request
}

// starterRequests is the built-in collection's contents, spelled once.
//
// The ids are stable strings rather than minted ones: two stands seeding the
// same two requests should produce the same two files, so a Playground
// committed from one machine and pulled onto another is not a diff.
func starterRequests() []starterSeed {
	return []starterSeed{
		{
			relPath: "zen.json",
			request: Request{
				ID:     "starter-zen",
				Name:   "Zen",
				Method: "GET",
				URL:    "{{baseUrl}}/zen",
				Body:   Body{Kind: BodyNone},
				Auth:   Auth{Kind: AuthNone},
			},
		},
		{
			relPath: "rate-limit.json",
			request: Request{
				ID:     "starter-rate-limit",
				Name:   "Rate limit",
				Method: "GET",
				URL:    "{{baseUrl}}/rate_limit",
				Headers: []Header{
					{Name: "Accept", Value: "application/vnd.github+json", Enabled: true},
				},
				Body: Body{Kind: BodyNone},
				Auth: Auth{Kind: AuthNone},
			},
		},
	}
}
