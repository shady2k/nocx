package apicoll

// Minting a collection, which is the other half of §6.1: the app opens a
// folder the user chose, AND it makes one for a user who has not chosen.
//
// NewDefaultCollection (default.go) already decided WHERE a new collection
// goes and refuses to write over one that is there. This file is what a
// caller reaches: it puts the environments directory beside the manifest and
// then OPENS the folder, so a create hands back exactly what an open hands
// back and the caller has one thing to do afterwards rather than two.
//
// The root is still accepted in one place only (§13.1). Create takes a NAME
// — a single folder name, never a path — derives the root inside this
// package from storage.Paths, and hands it to this service's own Open. No
// caller names a path in order to get a collection.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoDefaultLocation — this service was built without an app directory, so
// it cannot decide where a new collection goes. NewService is deliberately
// that service: it reads folders the user chose and mints none, and a
// refusal by name is what keeps that honest rather than a nil dereference in
// the filesystem.
var ErrNoDefaultLocation = errors.New("apicoll: this collection service has no app directory, so it cannot decide where a new collection goes")

// Created is a collection that has just been minted AND opened: where it
// went, the handle that addresses it, and what is in it (nothing).
//
// The handle is here rather than left for a follow-up Open because the two
// would otherwise be a race with the user's own next action: a renderer that
// created a collection and then opened it by path would be naming a root a
// second time, which is the one thing §13.1 forbids.
type Created struct {
	Root       string
	Handle     HandleID
	Collection Collection
}

// Creator mints an empty collection in the default location.
//
// It is a third interface beside Service and EnvironmentReader, implemented
// by the same type, for the reason environment.go already gives: Service's
// own property — Open is the ONLY entry point that takes a root — is
// asserted against Service's method set, and a Create sitting in it would
// have to be read past every time somebody checked that property. One handle
// table, one root re-validation, three audiences.
type Creator interface {
	// Create mints an empty collection called name and opens it. A name
	// that is not a single folder name is refused (§13.1); an existing
	// folder at that name is refused rather than merged, because writing a
	// fresh manifest over somebody's collection is data loss wearing the
	// word "create".
	Create(name string) (Created, error)
	// DefaultRoot is WHERE a collection created with no place named goes —
	// the directory that holds them, not a collection inside it.
	//
	// It exists so a surface can SHOW a person where their collection is
	// about to land. The import ask names its destination as an absolute
	// path and had no default at all, while Create next door takes a name
	// and puts the folder here: two doors to one concept, and the one that
	// asks more is the one somebody arriving from Postman meets. This is
	// the half that was missing — the location was derived inside this
	// package and never told to anybody.
	//
	// "" when this service was built without an app directory, which is the
	// state Create names by ErrNoDefaultLocation. A surface that gets ""
	// offers no default and the person types a path, which is exactly what
	// they do today; it is not a degrade to report, because nothing was
	// promised.
	//
	// It answers a location rather than accepting one: §13.1's property is
	// that Open is the only entry point that takes a root, and this hands
	// one OUT, for a field the person can then rewrite.
	DefaultRoot() string
}

var _ Creator = (*service)(nil)

// Create implements Creator.
//
// The interval, both ends named: the folder exists from the moment
// NewDefaultCollection returns until somebody deletes it, and the handle
// returned beside it addresses that folder from here until the folder stops
// being the directory it was opened on (resolve, handle.go).
//
// The partial failure is worth stating rather than leaving to be discovered.
// If the environments directory or the open fails, the folder has already
// been created with its manifest and IS LEFT IN PLACE. That is deliberate:
// an absent `environments/` lists as no environments rather than as a
// failure, so what is on disk is a working collection, and deleting a thing
// that works in order to report a thing that did not would be the worse
// answer. What the user sees is the error, and the folder waiting under the
// name they chose — which the next Create with that name refuses by name.
// DefaultRoot implements Creator.
func (s *service) DefaultRoot() string {
	if s.paths == nil {
		return ""
	}
	return filepath.Join(s.paths.DataDir(), DefaultCollectionsDirName)
}

func (s *service) Create(name string) (Created, error) {
	if s.paths == nil {
		return Created{}, ErrNoDefaultLocation
	}
	root, err := NewDefaultCollection(s.paths, name)
	if err != nil {
		return Created{}, err
	}
	// 0700 matches the collection root NewDefaultCollection just made: git
	// carries no mode bit but the execute one, so this costs the folder
	// nothing when it is shared and keeps it private while it is not.
	if err = os.Mkdir(filepath.Join(root, environmentsDirName), 0o700); err != nil {
		return Created{}, fmt.Errorf("apicoll: create %s/ in the new collection %s: %w", environmentsDirName, root, err)
	}
	// A folder that did not exist a moment ago cannot be one somebody
	// already has open, so Opened.AlreadyOpen is false here by construction
	// and Created has no field for it: the question only has an answer worth
	// carrying where a caller names a folder that was already there.
	op, err := s.Open(root)
	if err != nil {
		return Created{}, err
	}
	return Created{Root: root, Handle: op.Handle, Collection: op.Collection}, nil
}
